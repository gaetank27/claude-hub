package tools

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func init() { register(synology{}) }

// synology talks to a Synology DiskStation Manager (DSM 7) via the Web API.
// It uses session-based auth: a SID is obtained at the start of each Call()
// and revoked via logout when done (stateless connector, no session caching).
type synology struct{}

func (synology) Type() string  { return "synology" }
func (synology) Label() string { return "Synology NAS" }

func (synology) Fields() []Field {
	return []Field{
		{Key: "base_url", Label: "URL du NAS", Type: FieldURL, Required: true,
			Placeholder: "http://synology.local:5000"},
		{Key: "username", Label: "Nom d'utilisateur", Type: FieldText, Required: true,
			Placeholder: "admin"},
		{Key: "password", Label: "Mot de passe", Type: FieldPassword, Secret: true, Required: true},
		{Key: "totp_secret", Label: "Secret TOTP (si 2FA activé)", Type: FieldPassword, Secret: true,
			Placeholder: "clé base32 affichée lors de la config 2FA dans DSM"},
		{Key: "verify_tls", Label: "Vérifier le certificat TLS", Type: FieldBool},
	}
}

func (s synology) Test(ctx context.Context, cfg map[string]string) error {
	if err := missing(cfg, "base_url", "username", "password"); err != nil {
		return err
	}
	sid, err := synLogin(ctx, cfg)
	if err != nil {
		return err
	}
	synLogout(ctx, cfg, sid)
	return nil
}

func (synology) Tools() []ToolSpec {
	return []ToolSpec{
		{Name: "synology_info",
			Description: "Get Synology NAS system information: hostname, model, DSM version, uptime."},
		{Name: "synology_list_volumes",
			Description: "List storage volumes on the Synology NAS with capacity and usage statistics."},
		{Name: "synology_list_shares",
			Description: "List shared folders configured on the Synology NAS (name, path)."},
		{Name: "synology_list_backup_tasks",
			Description: "List HyperBackup backup tasks with their current status and last successful run time."},
		{Name: "synology_run_backup_task",
			Description: "Trigger a HyperBackup task to start a backup immediately.",
			Args: []ArgSpec{
				{Name: "task_id", Type: "integer",
					Description: "ID of the HyperBackup task (from synology_list_backup_tasks).",
					Required:    true},
			}},
	}
}

func (synology) Call(ctx context.Context, cfg map[string]string, action string, args map[string]any) (string, error) {
	if err := missing(cfg, "base_url", "username", "password"); err != nil {
		return "", err
	}
	sid, err := synLogin(ctx, cfg)
	if err != nil {
		return "", err
	}
	defer synLogout(ctx, cfg, sid)

	switch action {
	case "synology_info":
		return synInfo(ctx, cfg, sid)
	case "synology_list_volumes":
		return synListVolumes(ctx, cfg, sid)
	case "synology_list_shares":
		return synListShares(ctx, cfg, sid)
	case "synology_list_backup_tasks":
		return synListBackupTasks(ctx, cfg, sid)
	case "synology_run_backup_task":
		taskID, ok := argInt(args, "task_id")
		if !ok {
			return "", fmt.Errorf("task_id is required")
		}
		return synRunBackupTask(ctx, cfg, sid, taskID)
	default:
		return "", fmt.Errorf("unknown action %q", action)
	}
}

// --- auth ---

// synLogin authenticates against the DSM API and returns a session id.
// If cfg["totp_secret"] is set, an OTP is computed from the current time and
// sent alongside the credentials so that DSM 2FA (TOTP) is handled automatically.
func synLogin(ctx context.Context, cfg map[string]string) (string, error) {
	q := url.Values{}
	q.Set("api", "SYNO.API.Auth")
	q.Set("version", "7")
	q.Set("method", "login")
	q.Set("account", cfg["username"])
	q.Set("passwd", cfg["password"])
	q.Set("format", "sid")
	if secret := strings.TrimSpace(cfg["totp_secret"]); secret != "" {
		otp, err := totpNow(secret)
		if err != nil {
			return "", fmt.Errorf("synology TOTP: %w", err)
		}
		q.Set("otp_code", otp)
		q.Set("enable_syno_token", "no")
	}

	var res struct {
		Success bool `json:"success"`
		Data    struct {
			SID string `json:"sid"`
		} `json:"data"`
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := synDoRaw(ctx, cfg, q, &res); err != nil {
		return "", err
	}
	if !res.Success {
		return "", synAuthErr(res.Error.Code)
	}
	return res.Data.SID, nil
}

func synLogout(ctx context.Context, cfg map[string]string, sid string) {
	if sid == "" {
		return
	}
	q := url.Values{}
	q.Set("api", "SYNO.API.Auth")
	q.Set("version", "7")
	q.Set("method", "logout")
	q.Set("_sid", sid)
	req, err := http.NewRequestWithContext(
		ctx, http.MethodGet,
		strings.TrimRight(cfg["base_url"], "/")+"/webapi/entry.cgi?"+q.Encode(), nil)
	if err != nil {
		return
	}
	resp, err := httpClient(cfg["verify_tls"] != "true").Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

func synAuthErr(code int) error {
	switch code {
	case 400:
		return fmt.Errorf("synology login: wrong account or password")
	case 401:
		return fmt.Errorf("synology login: account disabled")
	case 402:
		return fmt.Errorf("synology login: permission denied")
	case 403, 404, 406:
		return fmt.Errorf("synology login: two-factor authentication required (code %d)", code)
	case 407:
		return fmt.Errorf("synology login: IP blocked by the NAS")
	default:
		return fmt.Errorf("synology login failed (code %d)", code)
	}
}

// --- API helpers ---

// synCall issues a DSM API request and decodes the data envelope.
func synCall(ctx context.Context, cfg map[string]string, sid, api, method, version string, extra map[string]string) (json.RawMessage, error) {
	q := url.Values{}
	q.Set("api", api)
	q.Set("version", version)
	q.Set("method", method)
	q.Set("_sid", sid)
	for k, v := range extra {
		q.Set(k, v)
	}
	var envelope struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
		Error   struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := synDoRaw(ctx, cfg, q, &envelope); err != nil {
		return nil, err
	}
	if !envelope.Success {
		return nil, fmt.Errorf("synology %s.%s failed (code %d)", api, method, envelope.Error.Code)
	}
	return envelope.Data, nil
}

func synDoRaw(ctx context.Context, cfg map[string]string, q url.Values, out any) error {
	apiURL := strings.TrimRight(cfg["base_url"], "/") + "/webapi/entry.cgi?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient(cfg["verify_tls"] != "true").Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("synology HTTP %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// --- actions ---

func synInfo(ctx context.Context, cfg map[string]string, sid string) (string, error) {
	data, err := synCall(ctx, cfg, sid, "SYNO.DSM.Info", "getinfo", "2", nil)
	if err != nil {
		return "", err
	}
	var info struct {
		Hostname       string `json:"hostname"`
		ProductVersion string `json:"productversion"`
		BuildNumber    string `json:"buildnumber"`
		Model          string `json:"model"`
		Uptime         int64  `json:"uptime"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return "", fmt.Errorf("synology DSM info: %w", err)
	}
	days := info.Uptime / 86400
	hours := (info.Uptime % 86400) / 3600
	return fmt.Sprintf(
		"Hostname: %s\nModel: %s\nDSM: %s (build %s)\nUptime: %dd %dh",
		info.Hostname, info.Model, info.ProductVersion, info.BuildNumber, days, hours,
	), nil
}

func synListVolumes(ctx context.Context, cfg map[string]string, sid string) (string, error) {
	data, err := synCall(ctx, cfg, sid, "SYNO.Storage.CGI.Volume", "list", "1", nil)
	if err != nil {
		return "", err
	}
	var res struct {
		Volumes []struct {
			ID        string `json:"id"`
			Status    string `json:"status"`
			FreeSize  int64  `json:"free_size"`
			TotalSize int64  `json:"total_size"`
		} `json:"volumes"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return "", fmt.Errorf("synology volume list: %w", err)
	}
	if len(res.Volumes) == 0 {
		return "No volumes found.", nil
	}
	var b strings.Builder
	for _, v := range res.Volumes {
		used := v.TotalSize - v.FreeSize
		pct := 0.0
		if v.TotalSize > 0 {
			pct = float64(used) / float64(v.TotalSize) * 100
		}
		fmt.Fprintf(&b, "- %s: %.1f / %.1f GB (%.0f%%) — %s\n",
			v.ID, synGB(used), synGB(v.TotalSize), pct, v.Status)
	}
	return strings.TrimSpace(b.String()), nil
}

func synListShares(ctx context.Context, cfg map[string]string, sid string) (string, error) {
	data, err := synCall(ctx, cfg, sid, "SYNO.FileStation.List", "list_share", "2", nil)
	if err != nil {
		return "", err
	}
	var res struct {
		Shares []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"shares"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return "", fmt.Errorf("synology share list: %w", err)
	}
	if len(res.Shares) == 0 {
		return "No shared folders found.", nil
	}
	var b strings.Builder
	for _, s := range res.Shares {
		fmt.Fprintf(&b, "- %s (%s)\n", s.Name, s.Path)
	}
	return strings.TrimSpace(b.String()), nil
}

func synListBackupTasks(ctx context.Context, cfg map[string]string, sid string) (string, error) {
	data, err := synCall(ctx, cfg, sid, "SYNO.HyperBackup.Task", "list", "3",
		map[string]string{"list_type": "backup"})
	if err != nil {
		return "", err
	}
	var res struct {
		TaskList []struct {
			ID          int    `json:"id"`
			Name        string `json:"name"`
			Status      string `json:"status"`
			LastBkpTime int64  `json:"last_bkp_time"`
		} `json:"task_list"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return "", fmt.Errorf("synology backup task list: %w", err)
	}
	if len(res.TaskList) == 0 {
		return "No HyperBackup tasks found.", nil
	}
	var b strings.Builder
	for _, t := range res.TaskList {
		lastRun := "never"
		if t.LastBkpTime > 0 {
			lastRun = time.Unix(t.LastBkpTime, 0).UTC().Format("2006-01-02 15:04Z")
		}
		fmt.Fprintf(&b, "- [%d] %s — %s (last run: %s)\n", t.ID, t.Name, t.Status, lastRun)
	}
	return strings.TrimSpace(b.String()), nil
}

func synRunBackupTask(ctx context.Context, cfg map[string]string, sid string, taskID int) (string, error) {
	_, err := synCall(ctx, cfg, sid, "SYNO.HyperBackup.Task", "backup", "3",
		map[string]string{"id": fmt.Sprintf("%d", taskID)})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("HyperBackup task %d started.", taskID), nil
}

func synGB(bytes int64) float64 { return float64(bytes) / 1e9 }

// totpNow computes the current 6-digit TOTP code from a base32-encoded secret
// using the standard TOTP algorithm (RFC 6238 / HOTP RFC 4226, SHA-1, 30s step).
// No external dependency: crypto/hmac + crypto/sha1 + encoding/base32 are stdlib.
func totpNow(secret string) (string, error) {
	// Normalize: strip spaces, uppercase (some apps format the key with spaces)
	secret = strings.ToUpper(strings.ReplaceAll(secret, " ", ""))
	// Pad to a multiple of 8 if the user omitted trailing '='
	if pad := len(secret) % 8; pad != 0 {
		secret += strings.Repeat("=", 8-pad)
	}
	key, err := base32.StdEncoding.DecodeString(secret)
	if err != nil {
		return "", fmt.Errorf("invalid base32 secret: %w", err)
	}
	counter := uint64(time.Now().Unix() / 30)
	msg := make([]byte, 8)
	binary.BigEndian.PutUint64(msg, counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(msg)
	h := mac.Sum(nil)
	// Dynamic truncation (RFC 4226 §5.4)
	offset := h[len(h)-1] & 0x0f
	code := (uint32(h[offset]&0x7f)<<24 |
		uint32(h[offset+1])<<16 |
		uint32(h[offset+2])<<8 |
		uint32(h[offset+3])) % 1_000_000
	return fmt.Sprintf("%06d", code), nil
}
