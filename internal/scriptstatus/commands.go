package scriptstatus

// Command channel: a safe way to *steer* a script (pause, resume, report-now…).
//
// A command is never code. It is a short name that must appear in the allow-list
// of the script's key (declared here at the Hub, edited in the Scripts page), plus
// a small validated JSON object of parameters. It is queued, delivered in the HTTP
// response of the script's next status report (so nothing is ever pushed to the
// script: it polls us, over the already authenticated and IP-restricted endpoint)
// and acknowledged in the report after that. What a script does with a command is
// entirely the script's own decision.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"system/internal/store"
)

const (
	maxParamsBytes   = 2 << 10 // compact size of params
	maxParamsDepth   = 3
	maxParamString   = 200
	maxParamArray    = 20
	maxAckMessage    = 200
	maxAcksPerReport = 20
	maxPerDelivery   = 10 // commands handed over per heartbeat
	maxActive        = 20 // pending+delivered per script
	maxAllowed       = 20 // names per allow-list
	cmdHistoryKeep   = 50 // finished commands kept per script

	DefaultTTL = 24 * time.Hour
	MinTTL     = time.Minute
	MaxTTL     = 7 * 24 * time.Hour

	// A delivered command whose ack has not come back after ackTimeout is handed
	// over again at the next heartbeat, up to maxDeliveries times in total.
	ackTimeout    = 10 * time.Minute
	maxDeliveries = 3
)

// Command states.
const (
	StatePending   = "pending"
	StateDelivered = "delivered"
	StateOK        = "ok"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
	StateExpired   = "expired"
)

// commandRe is the allow-list syntax for command names (and for allow-list entries).
var commandRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
var paramKeyRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}$`)

// DefaultCommands is the allow-list given to a new key of a known script when the
// caller does not specify one. Only harmless commands: the script decides what they do.
// plan-migrations / start-migrations / cancel-migrations (CloudStack storage migrations,
// the only commands that lead to writes) are deliberately NOT here: they must be added
// to the key's allow-list on purpose (Scripts page › API keys › Edit).
var DefaultCommands = map[string][]string{
	"cloudstack-migrations": {"pause", "report-now", "resume", "set-interval", "stop"},
	// Send-HubScreenshot.ps1: steer the capture loop. capture-now only brings the next
	// capture forward (the script keeps its own minimum interval and lock check).
	"screenshot": {"capture-now", "pause", "resume", "set-interval", "stop"},
}

// CmdError is a client error with its HTTP status.
type CmdError struct {
	Code int
	Msg  string
}

func (e *CmdError) Error() string { return e.Msg }

func bad(format string, a ...any) error { return &CmdError{400, fmt.Sprintf(format, a...)} }

// Command is one queued instruction (admin view).
type Command struct {
	ID          string `json:"id"`
	Script      string `json:"script"`
	Name        string `json:"name"`
	Params      any    `json:"params"`
	CreatedBy   string `json:"createdBy"`
	CreatedAt   int64  `json:"createdAt"`
	ExpiresAt   int64  `json:"expiresAt"`
	State       string `json:"state"`
	DeliveredAt int64  `json:"deliveredAt"`
	Deliveries  int    `json:"deliveries"`
	DoneAt      int64  `json:"doneAt"`
	Message     string `json:"message"`
}

// WireCommand is what the script receives: no more than it needs.
type WireCommand struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Params    any    `json:"params"`
	CreatedAt int64  `json:"createdAt"`
	ExpiresAt int64  `json:"expiresAt"`
}

// Ack is a script's acknowledgement, sent in its next status report.
type Ack struct {
	ID      string `json:"id"`
	Result  string `json:"result"` // ok | failed
	Message string `json:"message"`
}

// NormalizeCommandList validates, de-duplicates and sorts an allow-list.
func NormalizeCommandList(in []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, c := range in {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		if !commandRe.MatchString(c) {
			return nil, errors.New("nom de commande invalide (a-z, 0-9 et -, commence par une lettre, 32 car. max)")
		}
		seen[c] = true
		out = append(out, c)
	}
	if len(out) > maxAllowed {
		return nil, fmt.Errorf("trop de commandes autorisées (%d max)", maxAllowed)
	}
	sort.Strings(out)
	return out, nil
}

// AllowedCommands is the union of the allow-lists of the script's enabled keys.
func AllowedCommands(db *store.DB, script string) ([]string, error) {
	keys, err := keysForScript(db, script)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []string{}
	for _, k := range keys {
		for _, c := range k.AllowedCommands {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// validateParams checks that raw is absent/null or a small JSON object and returns
// its compact form. Strict on size, depth, key names and string content.
func validateParams(raw []byte) (string, error) {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" {
		return "{}", nil
	}
	if t[0] != '{' {
		return "", bad("params doit être un objet JSON")
	}
	var v any
	if err := json.Unmarshal([]byte(t), &v); err != nil {
		return "", bad("params : JSON invalide")
	}
	if jsonDepth([]byte(t)) > maxParamsDepth {
		return "", bad("params trop profond (%d niveaux max)", maxParamsDepth)
	}
	if err := checkParamValue(v); err != nil {
		return "", err
	}
	b, _ := json.Marshal(v)
	if len(b) > maxParamsBytes {
		return "", bad("params trop gros (%d octets max)", maxParamsBytes)
	}
	return string(b), nil
}

func checkParamValue(v any) error {
	switch x := v.(type) {
	case nil, bool, float64:
	case string:
		if len([]rune(x)) > maxParamString {
			return bad("params : texte trop long (%d car. max)", maxParamString)
		}
		for _, r := range x {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				return bad("params : caractère de contrôle interdit")
			}
		}
	case []any:
		if len(x) > maxParamArray {
			return bad("params : tableau trop long (%d max)", maxParamArray)
		}
		for _, e := range x {
			if err := checkParamValue(e); err != nil {
				return err
			}
		}
	case map[string]any:
		for k, e := range x {
			if !paramKeyRe.MatchString(k) {
				return bad("params : nom de champ invalide")
			}
			if err := checkParamValue(e); err != nil {
				return err
			}
		}
	}
	return nil
}

func parseAcks(raw json.RawMessage) ([]Ack, bool) {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" {
		return nil, true
	}
	var acks []Ack
	if err := json.Unmarshal(raw, &acks); err != nil || len(acks) > maxAcksPerReport {
		return nil, false
	}
	for i := range acks {
		if _, err := uuid.Parse(acks[i].ID); err != nil || (acks[i].Result != "ok" && acks[i].Result != "failed") {
			return nil, false
		}
		acks[i].Message = cleanText(acks[i].Message, maxAckMessage)
	}
	return acks, true
}

// SendCommand queues a command for script. by is "user" or "assistant".
// ttl <= 0 means DefaultTTL.
func (s *Service) SendCommand(script, name string, params json.RawMessage, by string, ttl time.Duration) (Command, error) {
	if !ValidScript(script) {
		return Command{}, bad("nom de script invalide")
	}
	if !commandRe.MatchString(name) {
		return Command{}, bad("nom de commande invalide (a-z, 0-9 et -, commence par une lettre, 32 car. max)")
	}
	allowed, err := AllowedCommands(s.db, script)
	if err != nil {
		return Command{}, err
	}
	ok := false
	for _, a := range allowed {
		ok = ok || a == name
	}
	if !ok {
		return Command{}, bad("commande non autorisée pour ce script (liste blanche de la clé : %s)", listOrNone(allowed))
	}
	p, err := validateParams(params)
	if err != nil {
		return Command{}, err
	}
	if err := validateCommandParams(name, p); err != nil {
		return Command{}, err
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if ttl < MinTTL || ttl > MaxTTL {
		return Command{}, bad("durée de validité hors limites (1 minute à 7 jours)")
	}
	if by != "assistant" {
		by = "user"
	}
	now := s.now().UnixMilli()
	s.cmdMu.Lock()
	defer s.cmdMu.Unlock()
	s.sweepCommands(script)
	var active int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM script_commands WHERE script=? AND state IN ('pending','delivered')`, script).Scan(&active); err != nil {
		return Command{}, err
	}
	if active >= maxActive {
		return Command{}, &CmdError{409, fmt.Sprintf("trop de commandes en cours pour ce script (%d max)", maxActive)}
	}
	c := Command{ID: uuid.NewString(), Script: script, Name: name, CreatedBy: by, CreatedAt: now,
		ExpiresAt: now + ttl.Milliseconds(), State: StatePending}
	if _, err := s.db.Exec(`INSERT INTO script_commands(id,script,name,params,created_by,created_at,expires_at,state) VALUES(?,?,?,?,?,?,?,?)`,
		c.ID, c.Script, c.Name, p, c.CreatedBy, c.CreatedAt, c.ExpiresAt, c.State); err != nil {
		return Command{}, err
	}
	_ = json.Unmarshal([]byte(p), &c.Params)
	// %q: the name is regexp-validated, but nothing user-supplied is logged raw.
	log.Printf("script-status: command queued script=%q name=%q by=%s id=%s", script, name, by, c.ID)
	return c, nil
}

func listOrNone(l []string) string {
	if len(l) == 0 {
		return "aucune"
	}
	return strings.Join(l, ", ")
}

// CancelCommand cancels a command that has not been delivered yet.
func (s *Service) CancelCommand(id string) error {
	s.cmdMu.Lock()
	defer s.cmdMu.Unlock()
	var script string
	if err := s.db.QueryRow(`SELECT script FROM script_commands WHERE id=?`, id).Scan(&script); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &CmdError{404, "commande introuvable"}
		}
		return err
	}
	s.sweepCommands(script)
	res, err := s.db.Exec(`UPDATE script_commands SET state='cancelled',done_at=?,message='annulée' WHERE id=? AND state='pending'`, s.now().UnixMilli(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return &CmdError{409, "seule une commande en attente peut être annulée (déjà livrée, terminée ou expirée)"}
	}
	return nil
}

// ListCommands returns the most recent commands (newest first) of one script, or of all when script == "".
func (s *Service) ListCommands(script string, limit int) ([]Command, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	s.cmdMu.Lock()
	defer s.cmdMu.Unlock()
	q := `SELECT id,script,name,params,created_by,created_at,expires_at,state,delivered_at,deliveries,done_at,message FROM script_commands`
	args := []any{}
	if script != "" {
		if !ValidScript(script) {
			return nil, bad("nom de script invalide")
		}
		s.sweepCommands(script)
		q += ` WHERE script=?`
		args = append(args, script)
	} else {
		s.sweepCommands("")
	}
	q += ` ORDER BY created_at DESC, rowid DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Command{}
	for rows.Next() {
		var c Command
		var p string
		if err := rows.Scan(&c.ID, &c.Script, &c.Name, &p, &c.CreatedBy, &c.CreatedAt, &c.ExpiresAt, &c.State, &c.DeliveredAt, &c.Deliveries, &c.DoneAt, &c.Message); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(p), &c.Params)
		out = append(out, c)
	}
	return out, rows.Err()
}

// sweepCommands expires overdue commands (script == "" = every script) and trims
// the history. Caller holds cmdMu.
func (s *Service) sweepCommands(script string) {
	now := s.now().UnixMilli()
	where, args := ``, []any{}
	if script != "" {
		where, args = ` AND script=?`, []any{script}
	}
	exec := func(q string, a ...any) {
		if _, err := s.db.Exec(q, a...); err != nil {
			log.Printf("script-status: sweep commands: %v", err)
		}
	}
	exec(`UPDATE script_commands SET state='expired',done_at=?,message='expirée avant exécution' WHERE state IN ('pending','delivered') AND expires_at<=?`+where,
		append([]any{now, now}, args...)...)
	exec(`UPDATE script_commands SET state='expired',done_at=?,message='non acquittée par le script après '||deliveries||' livraisons' WHERE state='delivered' AND deliveries>=? AND delivered_at<=?`+where,
		append([]any{now, maxDeliveries, now - ackTimeout.Milliseconds()}, args...)...)
	exec(`DELETE FROM script_commands WHERE state NOT IN ('pending','delivered') AND id NOT IN
		(SELECT id FROM script_commands s2 WHERE s2.script=script_commands.script AND s2.state NOT IN ('pending','delivered') ORDER BY done_at DESC, rowid DESC LIMIT ?)`+where,
		append([]any{cmdHistoryKeep}, args...)...)
}

// exchangeCommands applies the acks of an authenticated report, then returns the
// commands to deliver in the response (and marks them delivered).
func (s *Service) exchangeCommands(script string, acks []Ack) ([]WireCommand, error) {
	s.cmdMu.Lock()
	defer s.cmdMu.Unlock()
	now := s.now().UnixMilli()
	for _, a := range acks {
		state := StateOK
		if a.Result == "failed" {
			state = StateFailed
		}
		// Only this script's delivered commands: an ack can't touch anything else.
		if _, err := s.db.Exec(`UPDATE script_commands SET state=?,done_at=?,message=? WHERE id=? AND script=? AND state='delivered'`,
			state, now, a.Message, a.ID, script); err != nil {
			return nil, err
		}
	}
	s.sweepCommands(script)
	rows, err := s.db.Query(`SELECT id,name,params,created_at,expires_at FROM script_commands WHERE script=? AND
		(state='pending' OR (state='delivered' AND deliveries<? AND delivered_at<=?)) AND expires_at>? ORDER BY created_at, rowid LIMIT ?`,
		script, maxDeliveries, now-ackTimeout.Milliseconds(), now, maxPerDelivery)
	if err != nil {
		return nil, err
	}
	var out []WireCommand
	for rows.Next() {
		var c WireCommand
		var p string
		if err := rows.Scan(&c.ID, &c.Name, &p, &c.CreatedAt, &c.ExpiresAt); err != nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal([]byte(p), &c.Params)
		out = append(out, c)
	}
	rows.Close()
	// Defence in depth: never hand over a command that is no longer on the allow-list
	// (the key may have been edited since it was queued).
	allowed, err := AllowedCommands(s.db, script)
	if err != nil {
		return nil, err
	}
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	kept := out[:0]
	for _, c := range out {
		if !ok[c.Name] {
			_, _ = s.db.Exec(`UPDATE script_commands SET state='cancelled',done_at=?,message='retirée de la liste blanche' WHERE id=?`, now, c.ID)
			continue
		}
		if _, err := s.db.Exec(`UPDATE script_commands SET state='delivered',delivered_at=?,deliveries=deliveries+1 WHERE id=?`, now, c.ID); err != nil {
			return nil, err
		}
		kept = append(kept, c)
	}
	return kept, nil
}

// Per-command parameter rules for the commands that can lead to writes. Everything else
// keeps the generic validation (validateParams) only.
var (
	migVMRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
	migPoolRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._()-]{0,62}$`)
	migPlanRe = regexp.MustCompile(`^p-[0-9a-f]{12}$`)
)

// MaxMigrationVMs bounds the VM list of plan-migrations.
const MaxMigrationVMs = 20

// validateCommandParams applies strict, command-specific checks on already
// generically-validated compact params (p).
func validateCommandParams(name, p string) error {
	var m map[string]any
	_ = json.Unmarshal([]byte(p), &m)
	only := func(allowed ...string) error {
		for k := range m {
			ok := false
			for _, a := range allowed {
				ok = ok || a == k
			}
			if !ok {
				return bad("%s : paramètre inconnu %q", name, k)
			}
		}
		return nil
	}
	switch name {
	case "plan-migrations":
		if err := only("vms", "destinationPool"); err != nil {
			return err
		}
		vms, ok := m["vms"].([]any)
		if !ok || len(vms) < 1 || len(vms) > MaxMigrationVMs {
			return bad("plan-migrations : vms = liste de 1 à %d noms ou identifiants de VM", MaxMigrationVMs)
		}
		for _, v := range vms {
			s, ok := v.(string)
			if !ok || !migVMRe.MatchString(s) {
				return bad("plan-migrations : nom de VM invalide (lettres, chiffres, . _ - ; 63 car. max)")
			}
		}
		pool, ok := m["destinationPool"].(string)
		if !ok || !migPoolRe.MatchString(pool) || strings.HasSuffix(pool, " ") {
			return bad("plan-migrations : destinationPool invalide (nom ou identifiant du stockage primaire)")
		}
	case "start-migrations":
		if err := only("planId", "onError"); err != nil {
			return err
		}
		id, ok := m["planId"].(string)
		if !ok || !migPlanRe.MatchString(id) {
			return bad("start-migrations : planId invalide (identifiant d'un plan calculé par plan-migrations, ex. p-0123456789ab)")
		}
		if v, present := m["onError"]; present {
			if s, ok := v.(string); !ok || (s != "continue" && s != "stop") {
				return bad("start-migrations : onError = continue ou stop")
			}
		}
	case "cancel-migrations":
		if len(m) != 0 {
			return bad("cancel-migrations : aucun paramètre attendu")
		}
	}
	return nil
}
