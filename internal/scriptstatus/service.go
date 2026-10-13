package scriptstatus

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"system/internal/store"
)

const (
	KeyHeader   = "X-Hub-Script-Key"
	maxBody     = 64 << 10 // request body cap
	maxData     = 32 << 10 // compact size of "data"
	maxDepth    = 8
	ipRatePerMn = 120 // requests/min/IP, all outcomes
	scriptRate  = 60  // accepted+attempted requests/min/script
	failMax     = 5   // auth failures per IP …
	failWindow  = 10 * time.Minute
	banFor      = 15 * time.Minute
)

// Service owns the endpoint state: limiter, bans, fail2ban log, wake-ups.
type Service struct {
	db  *store.DB
	now func() time.Time

	logPath string
	logMu   sync.Mutex

	cmdMu sync.Mutex // serialises the command queue (commands.go)

	imgDir string     // screenshots (images.go)
	imgMu  sync.Mutex // serialises image decoding, storage and purge

	mu    sync.Mutex
	rate  map[string]*window // per IP
	srate map[string]*window // per script
	irate map[string]*window // per script, image uploads only
	fails map[string][]time.Time
	bans  map[string]time.Time

	wake func(script, state string) error // set by the backend; nil = no wake-up
}

type window struct {
	start time.Time
	n     int
}

// New creates the service; dataDir hosts the fail2ban log.
func New(db *store.DB, dataDir string) *Service {
	return &Service{db: db, now: time.Now, logPath: filepath.Join(dataDir, "script-status-auth.log"),
		imgDir: filepath.Join(dataDir, "script-images"),
		rate:   map[string]*window{}, srate: map[string]*window{}, irate: map[string]*window{}, fails: map[string][]time.Time{}, bans: map[string]time.Time{}}
}

// LogPath is the fail2ban-readable auth failure log.
func (s *Service) LogPath() string { return s.logPath }

// SetWake installs the callback used to wake the assistant.
func (s *Service) SetWake(f func(script, state string) error) { s.wake = f }

// clientIP returns the caller's address. Behind the Hub's Caddy (loopback peer)
// the last X-Forwarded-For entry is the address Caddy saw; otherwise the TCP peer.
func clientIP(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, _ := netip.ParseAddr(host)
	peer = peer.Unmap()
	if peer.IsLoopback() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if a, err := netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1])); err == nil {
				return a.Unmap()
			}
		}
	}
	return peer
}

func (s *Service) allow(m map[string]*window, key string, limit int) bool {
	now := s.now()
	w := m[key]
	if w == nil || now.Sub(w.start) >= time.Minute {
		if len(m) > 10000 { // bound memory under a flood of distinct keys
			for k, v := range m {
				if now.Sub(v.start) >= time.Minute {
					delete(m, k)
				}
			}
		}
		m[key] = &window{start: now, n: 1}
		return true
	}
	w.n++
	return w.n <= limit
}

// banned reports an active ban on ip.
func (s *Service) banned(ip string) bool {
	until, ok := s.bans[ip]
	if !ok {
		return false
	}
	if s.now().After(until) {
		delete(s.bans, ip)
		delete(s.fails, ip)
		return false
	}
	return true
}

// recordFailure notes an auth failure and bans the IP after failMax in failWindow.
func (s *Service) recordFailure(ip string) {
	now := s.now()
	kept := s.fails[ip][:0]
	for _, t := range s.fails[ip] {
		if now.Sub(t) < failWindow {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	s.fails[ip] = kept
	if len(kept) >= failMax {
		s.bans[ip] = now.Add(banFor)
	}
	if len(s.fails) > 10000 {
		for k, v := range s.fails {
			if len(v) == 0 || now.Sub(v[len(v)-1]) >= failWindow {
				delete(s.fails, k)
			}
		}
	}
}

// logLine appends one line to the fail2ban log. Every field is either parsed
// (IP) or from a fixed set / validated by regexp, so nothing attacker-controlled
// can inject a fake line.
func (s *Service) logLine(kind, ip, script, reason string) {
	line := fmt.Sprintf("%s script-status %s ip=%s script=%s reason=%s\n",
		s.now().Format("2006-01-02 15:04:05"), kind, ip, script, reason)
	s.logMu.Lock()
	defer s.logMu.Unlock()
	f, err := os.OpenFile(s.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		log.Printf("script-status: auth log: %v", err)
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// dummyHash is compared against when the script has no key, so a valid script
// name cannot be told from an unknown one by timing.
var dummyHash = hashKey("no-such-key")

// authenticate runs everything that precedes the handling of a report or of an
// image: application-level ban, per-IP rate, script name, key, IP allow-list and
// per-script rate. On refusal the response is already written (ok = false).
func (s *Service) authenticate(w http.ResponseWriter, r *http.Request) (script string, match *Key, ok bool) {
	script = r.PathValue("script")
	ipAddr := clientIP(r)
	ip := ipAddr.String()
	if !ipAddr.IsValid() {
		ip = "unknown"
	}

	s.mu.Lock()
	if s.banned(ip) {
		s.mu.Unlock()
		s.logLine("BLOCKED", ip, "-", "banned")
		w.Header().Set("Retry-After", fmt.Sprint(int(banFor.Seconds())))
		jsonErr(w, http.StatusTooManyRequests, "too many requests")
		return script, nil, false
	}
	if !s.allow(s.rate, ip, ipRatePerMn) {
		s.mu.Unlock()
		w.Header().Set("Retry-After", "60")
		jsonErr(w, http.StatusTooManyRequests, "too many requests")
		return script, nil, false
	}
	s.mu.Unlock()

	fail := func(reason string) {
		logScript := script
		if !ValidScript(script) {
			logScript = "-"
		}
		s.mu.Lock()
		s.recordFailure(ip)
		s.mu.Unlock()
		s.logLine("AUTH-FAIL", ip, logScript, reason)
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
	}

	if !ValidScript(script) {
		fail("bad-script")
		return script, nil, false
	}
	given := r.Header.Get(KeyHeader)
	if given == "" || len(given) > 200 {
		fail("no-key")
		return script, nil, false
	}
	keys, err := keysForScript(s.db, script)
	if err != nil {
		log.Printf("script-status: keys: %v", err)
		jsonErr(w, http.StatusInternalServerError, "internal error")
		return script, nil, false
	}
	gh := []byte(hashKey(given))
	for i := range keys {
		if subtle.ConstantTimeCompare(gh, []byte(keys[i].hash)) == 1 {
			match = &keys[i]
		}
	}
	if len(keys) == 0 {
		subtle.ConstantTimeCompare(gh, []byte(dummyHash))
	}
	if match == nil {
		fail("bad-key")
		return script, nil, false
	}
	if !match.ipAllowed(ipAddr) {
		fail("ip-not-allowed")
		return script, nil, false
	}

	// Authenticated from here on.
	s.mu.Lock()
	allowed := s.allow(s.srate, script, scriptRate)
	s.mu.Unlock()
	if !allowed {
		w.Header().Set("Retry-After", "60")
		jsonErr(w, http.StatusTooManyRequests, "too many requests")
		return script, nil, false
	}
	return script, match, true
}

// Receive handles POST /webhook/script-status/{script}.
func (s *Service) Receive(w http.ResponseWriter, r *http.Request) {
	script, match, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(ct), "application/json") {
		jsonErr(w, http.StatusUnsupportedMediaType, "content-type must be application/json")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		jsonErr(w, http.StatusRequestEntityTooLarge, "body too large")
		return
	}
	state, summary, data, sentTS, acks, errCode := parseReport(body)
	if errCode != "" {
		jsonErr(w, http.StatusBadRequest, errCode)
		return
	}
	if err := s.store(script, state, summary, data, sentTS); err != nil {
		log.Printf("script-status: store %s: %v", script, err)
		jsonErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	_, _ = s.db.Exec(`UPDATE script_keys SET last_used=? WHERE id=?`, s.now().UnixMilli(), match.ID)
	go s.CheckWake(script)
	// Command channel: apply the acknowledgements, then hand over what is queued.
	// A failure here never fails the status report (already stored).
	cmds, err := s.exchangeCommands(script, acks)
	if err != nil {
		log.Printf("script-status: commands %s: %v", script, err)
		cmds = nil
	}
	if cmds == nil {
		cmds = []WireCommand{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "commands": cmds})
}

// cleanText drops control and format characters (log/terminal injection) and
// bounds the length in runes.
func cleanText(in string, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range in {
		if r == '\n' || r == '\t' {
			r = ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if n++; n > max {
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// parseReport strictly validates the JSON body. The returned code is a fixed
// string (never an echo of the input), "" when valid.
func parseReport(body []byte) (state, summary, data string, sentTS int64, acks []Ack, code string) {
	var in struct {
		State   string          `json:"state"`
		Summary string          `json:"summary"`
		Data    json.RawMessage `json:"data"`
		TS      string          `json:"ts"`
		Acks    json.RawMessage `json:"acks"` // optional: acknowledgements of delivered commands
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return "", "", "", 0, nil, "invalid json"
	}
	if dec.More() {
		return "", "", "", 0, nil, "invalid json"
	}
	if _, err := dec.Token(); err != io.EOF {
		return "", "", "", 0, nil, "invalid json"
	}
	if !validStates[in.State] {
		return "", "", "", 0, nil, "invalid state"
	}
	if len([]rune(in.Summary)) > maxSummary {
		return "", "", "", 0, nil, "summary too long"
	}
	summary = cleanText(in.Summary, maxSummary)
	data = "null"
	if t := bytes.TrimSpace(in.Data); len(t) > 0 && string(t) != "null" {
		if t[0] != '{' {
			return "", "", "", 0, nil, "data must be an object"
		}
		if jsonDepth(t) > maxDepth {
			return "", "", "", 0, nil, "data too deep"
		}
		var buf bytes.Buffer
		if err := json.Compact(&buf, t); err != nil {
			return "", "", "", 0, nil, "invalid json"
		}
		if buf.Len() > maxData {
			return "", "", "", 0, nil, "data too large"
		}
		data = buf.String()
	}
	if in.TS != "" {
		t, err := time.Parse(time.RFC3339, in.TS)
		if err != nil {
			return "", "", "", 0, nil, "invalid ts"
		}
		sentTS = t.UnixMilli()
	}
	acks, ok := parseAcks(in.Acks)
	if !ok {
		return "", "", "", 0, nil, "invalid acks"
	}
	return in.State, summary, data, sentTS, acks, ""
}

// jsonDepth returns the nesting depth of already-validated JSON.
func jsonDepth(b []byte) int {
	depth, max := 0, 0
	inStr, esc := false, false
	for _, c := range b {
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{', '[':
			if depth++; depth > max {
				max = depth
			}
		case '}', ']':
			depth--
		}
	}
	return max
}

func (s *Service) store(script, state, summary, data string, sentTS int64) error {
	now := s.now().UnixMilli()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO script_status(script,state,summary,data,sent_ts,received_at,notified) VALUES(?,?,?,?,?,?,'')
		ON CONFLICT(script) DO UPDATE SET state=excluded.state,summary=excluded.summary,data=excluded.data,sent_ts=excluded.sent_ts,received_at=excluded.received_at`,
		script, state, summary, data, sentTS, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO script_history(script,state,summary,data,received_at) VALUES(?,?,?,?,?)`,
		script, state, summary, data, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM script_history WHERE script=? AND id NOT IN
		(SELECT id FROM script_history WHERE script=? ORDER BY id DESC LIMIT ?)`, script, script, historyKeep); err != nil {
		return err
	}
	return tx.Commit()
}

// silenceMin is the silence threshold of a script (largest among its enabled keys).
func silenceFor(keys []Key) (min int, wake bool) {
	for _, k := range keys {
		if k.SilenceMin > min {
			min = k.SilenceMin
		}
		if k.Wake {
			wake = true
		}
	}
	return
}

func (s *Service) build(script, state, summary, data string, sentTS, recv int64, keys []Key) Report {
	sil, _ := silenceFor(keys)
	r := Report{Script: script, State: state, Effective: state, Summary: summary, SentTS: sentTS,
		ReceivedAt: recv, SilenceMin: sil}
	r.AgeSeconds = (s.now().UnixMilli() - recv) / 1000
	if r.AgeSeconds < 0 {
		r.AgeSeconds = 0
	}
	if sil > 0 && state != "done" && r.AgeSeconds > int64(sil)*60 { // a finished ("done") script is not silent, it is over
		r.Effective = "muet"
	}
	var d any
	_ = json.Unmarshal([]byte(data), &d)
	r.Data = d
	return r
}

// Get returns the latest report of a script.
func (s *Service) Get(script string) (Report, bool, error) {
	if !ValidScript(script) {
		return Report{}, false, nil
	}
	var state, summary, data string
	var sentTS, recv int64
	err := s.db.QueryRow(`SELECT state,summary,data,sent_ts,received_at FROM script_status WHERE script=?`, script).
		Scan(&state, &summary, &data, &sentTS, &recv)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return Report{}, false, nil
		}
		return Report{}, false, err
	}
	keys, _ := keysForScript(s.db, script)
	return s.build(script, state, summary, data, sentTS, recv, keys), true, nil
}

// List returns the latest report of every script.
func (s *Service) List() ([]Report, error) {
	rows, err := s.db.Query(`SELECT script,state,summary,data,sent_ts,received_at FROM script_status ORDER BY script`)
	if err != nil {
		return nil, err
	}
	type raw struct {
		script, state, summary, data string
		sent, recv                   int64
	}
	var rs []raw
	for rows.Next() {
		var x raw
		if err := rows.Scan(&x.script, &x.state, &x.summary, &x.data, &x.sent, &x.recv); err != nil {
			rows.Close()
			return nil, err
		}
		rs = append(rs, x)
	}
	rows.Close()
	out := []Report{}
	for _, x := range rs {
		keys, _ := keysForScript(s.db, x.script)
		out = append(out, s.build(x.script, x.state, x.summary, x.data, x.sent, x.recv, keys))
	}
	return out, nil
}

// History returns the last n reports of a script, newest first.
func (s *Service) History(script string, n int) ([]HistoryItem, error) {
	if n <= 0 || n > historyKeep {
		n = historyKeep
	}
	rows, err := s.db.Query(`SELECT state,summary,data,received_at FROM script_history WHERE script=? ORDER BY id DESC LIMIT ?`, script, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryItem{}
	for rows.Next() {
		var h HistoryItem
		var data string
		if err := rows.Scan(&h.State, &h.Summary, &data, &h.ReceivedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(data), &h.Data)
		out = append(out, h)
	}
	return out, rows.Err()
}
