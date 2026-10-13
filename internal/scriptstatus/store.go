// Package scriptstatus receives status reports from external scripts (PowerShell
// monitors, cron jobs…) on a public, key-protected endpoint, keeps the latest
// report and a short history per script, detects scripts gone silent, and
// exposes the result to the personal assistant.
package scriptstatus

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"system/internal/store"
)

const (
	historyKeep = 50
	maxSummary  = 500
)

// States a script may report. "muet" is never sent by a script: it is derived.
var validStates = map[string]bool{"ok": true, "warn": true, "error": true, "running": true, "done": true}

// scriptRe is the allow-list for script names (URL segment, DB key, log field).
var scriptRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)

// ValidScript reports whether name is an acceptable script name.
func ValidScript(name string) bool { return scriptRe.MatchString(name) }

// Key is a stored API key (never contains the secret).
type Key struct {
	ID         string   `json:"id"`
	Script     string   `json:"script"`
	Label      string   `json:"label"`
	AllowedIPs []string `json:"allowedIps"`
	SilenceMin int      `json:"silenceMinutes"`
	Wake       bool     `json:"wake"`
	Enabled    bool     `json:"enabled"`
	// AllowedCommands is the allow-list of command names the Hub may queue for
	// this script (see commands.go). Empty = the script accepts no command.
	AllowedCommands []string `json:"allowedCommands"`
	// AllowImages lets the script upload screenshots (images.go). Off by default:
	// an image may show anything that was on the screen.
	AllowImages bool  `json:"allowImages"`
	CreatedAt   int64 `json:"createdAt"`
	LastUsed    int64 `json:"lastUsed"`
	hash        string
	prefixes    []netip.Prefix
}

// Report is the latest status of a script.
type Report struct {
	Script     string `json:"script"`
	State      string `json:"state"`     // as reported
	Effective  string `json:"effective"` // State, or "muet" when silent
	Summary    string `json:"summary"`
	Data       any    `json:"data"`
	SentTS     int64  `json:"sentTs,omitempty"`
	ReceivedAt int64  `json:"receivedAt"`
	AgeSeconds int64  `json:"ageSeconds"`
	SilenceMin int    `json:"silenceMinutes"`
}

// HistoryItem is one past report.
type HistoryItem struct {
	State      string `json:"state"`
	Summary    string `json:"summary"`
	Data       any    `json:"data"`
	ReceivedAt int64  `json:"receivedAt"`
}

func hashKey(k string) string {
	h := sha256.Sum256([]byte(k))
	return hex.EncodeToString(h[:])
}

// ParseIPList parses a list of IPs / CIDRs into prefixes (strict).
func ParseIPList(in []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if strings.Contains(s, "/") {
			p, err := netip.ParsePrefix(s)
			if err != nil {
				return nil, fmt.Errorf("adresse/CIDR invalide")
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("adresse IP invalide")
		}
		out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
	}
	return out, nil
}

func (k Key) ipAllowed(ip netip.Addr) bool {
	if len(k.prefixes) == 0 {
		return true
	}
	ip = ip.Unmap()
	for _, p := range k.prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func scanKey(sc interface{ Scan(...any) error }) (Key, error) {
	var k Key
	var ips, cmds string
	var wake, en, img int
	if err := sc.Scan(&k.ID, &k.Script, &k.Label, &k.hash, &ips, &k.SilenceMin, &wake, &en, &k.CreatedAt, &k.LastUsed, &cmds, &img); err != nil {
		return k, err
	}
	k.Wake, k.Enabled, k.AllowImages = wake == 1, en == 1, img == 1
	k.AllowedIPs = []string{}
	for _, s := range strings.Split(ips, ",") {
		if s = strings.TrimSpace(s); s != "" {
			k.AllowedIPs = append(k.AllowedIPs, s)
		}
	}
	k.prefixes, _ = ParseIPList(k.AllowedIPs)
	k.AllowedCommands = []string{}
	for _, c := range strings.Split(cmds, ",") {
		if c = strings.TrimSpace(c); commandRe.MatchString(c) {
			k.AllowedCommands = append(k.AllowedCommands, c)
		}
	}
	return k, nil
}

const keyCols = `id,script,label,key_hash,allowed_ips,silence_min,wake,enabled,created_at,last_used,allowed_commands,allow_images`

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func newSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "hsk_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

// RegenerateKey replaces the secret of an existing key (settings kept, old secret
// stops working at once) and returns the new secret once.
func RegenerateKey(db *store.DB, id string) (Key, string, error) {
	k, err := scanKey(db.QueryRow(`SELECT `+keyCols+` FROM script_keys WHERE id=?`, id))
	if err != nil {
		return Key{}, "", err
	}
	secret, err := newSecret()
	if err != nil {
		return Key{}, "", err
	}
	if _, err := db.Exec(`UPDATE script_keys SET key_hash=?,last_used=0 WHERE id=?`, hashKey(secret), id); err != nil {
		return Key{}, "", err
	}
	k.LastUsed = 0
	return k, secret, nil
}

// CreateKey makes a new key for script and returns the secret once.
// allowedCommands nil = the script's default list (DefaultCommands).
func CreateKey(db *store.DB, script, label string, ips []string, silenceMin int, wake bool, allowedCommands []string) (Key, string, error) {
	if !ValidScript(script) {
		return Key{}, "", errors.New("nom de script invalide (a-z, 0-9, _ -, 48 car. max)")
	}
	if silenceMin < 0 || silenceMin > 7*24*60 {
		return Key{}, "", errors.New("silenceMinutes hors limites (0 - 10080)")
	}
	pf, err := ParseIPList(ips)
	if err != nil {
		return Key{}, "", err
	}
	if len(label) > 80 {
		label = label[:80]
	}
	if allowedCommands == nil {
		allowedCommands = DefaultCommands[script]
	}
	cmds, err := NormalizeCommandList(allowedCommands)
	if err != nil {
		return Key{}, "", err
	}
	secret, err := newSecret()
	if err != nil {
		return Key{}, "", err
	}
	list := make([]string, len(pf))
	for i, p := range pf {
		if p.IsSingleIP() {
			list[i] = p.Addr().String()
		} else {
			list[i] = p.String()
		}
	}
	k := Key{ID: uuid.NewString(), Script: script, Label: label, AllowedIPs: list, SilenceMin: silenceMin,
		Wake: wake, Enabled: true, CreatedAt: time.Now().UnixMilli(), hash: hashKey(secret), AllowedCommands: cmds}
	_, err = db.Exec(`INSERT INTO script_keys(`+keyCols+`) VALUES(?,?,?,?,?,?,?,?,?,0,?,0)`,
		k.ID, k.Script, k.Label, k.hash, strings.Join(list, ","), k.SilenceMin, b2i(k.Wake), 1, k.CreatedAt, strings.Join(cmds, ","))
	return k, secret, err
}

// ListKeys returns every key (secrets are never stored).
func ListKeys(db *store.DB) ([]Key, error) {
	rows, err := db.Query(`SELECT ` + keyCols + ` FROM script_keys ORDER BY script, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Key{}
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// UpdateKey patches the mutable settings of a key; nil fields are left alone.
func UpdateKey(db *store.DB, id string, ips *[]string, silenceMin *int, wake, enabled *bool, allowedCommands *[]string) error {
	row := db.QueryRow(`SELECT `+keyCols+` FROM script_keys WHERE id=?`, id)
	k, err := scanKey(row)
	if err != nil {
		return err
	}
	if ips != nil {
		pf, err := ParseIPList(*ips)
		if err != nil {
			return err
		}
		k.AllowedIPs = k.AllowedIPs[:0]
		for _, p := range pf {
			if p.IsSingleIP() {
				k.AllowedIPs = append(k.AllowedIPs, p.Addr().String())
			} else {
				k.AllowedIPs = append(k.AllowedIPs, p.String())
			}
		}
	}
	if silenceMin != nil {
		if *silenceMin < 0 || *silenceMin > 7*24*60 {
			return errors.New("silenceMinutes hors limites (0 - 10080)")
		}
		k.SilenceMin = *silenceMin
	}
	if wake != nil {
		k.Wake = *wake
	}
	if enabled != nil {
		k.Enabled = *enabled
	}
	if allowedCommands != nil {
		cmds, err := NormalizeCommandList(*allowedCommands)
		if err != nil {
			return err
		}
		k.AllowedCommands = cmds
	}
	_, err = db.Exec(`UPDATE script_keys SET allowed_ips=?,silence_min=?,wake=?,enabled=?,allowed_commands=? WHERE id=?`,
		strings.Join(k.AllowedIPs, ","), k.SilenceMin, b2i(k.Wake), b2i(k.Enabled), strings.Join(k.AllowedCommands, ","), id)
	return err
}

// SetKeyImages grants or withdraws the right to upload images with this key.
func SetKeyImages(db *store.DB, id string, allow bool) error {
	res, err := db.Exec(`UPDATE script_keys SET allow_images=? WHERE id=?`, b2i(allow), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteKey removes a key.
func DeleteKey(db *store.DB, id string) error {
	res, err := db.Exec(`DELETE FROM script_keys WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func keysForScript(db *store.DB, script string) ([]Key, error) {
	rows, err := db.Query(`SELECT `+keyCols+` FROM script_keys WHERE script=? AND enabled=1`, script)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Key
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
