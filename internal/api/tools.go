package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"system/internal/store"
	"system/internal/tools"
	"system/internal/whatsapp"
)

// connectorDTO is a connector type in the catalog: its schema drives the dynamic
// configuration form on the frontend.
type connectorDTO struct {
	Type    string        `json:"type"`
	Label   string        `json:"label"`
	Fields  []tools.Field `json:"fields"`
	Actions []actionDTO   `json:"actions"`         // MCP actions it exposes (name + description): searchable on the Tools page
	Default string        `json:"defaultCategory"` // category id used while a tool has no explicit one
}

type actionDTO struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// listConnectors returns the catalog of available connector types.
func (s *Server) listConnectors(w http.ResponseWriter, r *http.Request) {
	all := tools.All()
	out := make([]connectorDTO, 0, len(all))
	for _, c := range all {
		actions := []actionDTO{}
		for _, sp := range c.Tools() {
			actions = append(actions, actionDTO{Name: sp.Name, Description: sp.Description})
		}
		out = append(out, connectorDTO{Type: c.Type(), Label: c.Label(), Fields: c.Fields(), Actions: actions, Default: tools.DefaultCategory(c.Type())})
	}
	writeJSON(w, http.StatusOK, out)
}

// toolDTO is a configured tool. Secret values are never included; secretsSet tells
// the form which secret fields already hold a value (so it can show a placeholder).
type toolDTO struct {
	ID           string            `json:"id"`
	Type         string            `json:"type"`
	Name         string            `json:"name"`
	Status       string            `json:"status"`
	Enabled      bool              `json:"enabled"`
	Error        string            `json:"error,omitempty"`
	Config       map[string]string `json:"config,omitempty"`     // non-secret values only
	SecretsSet   map[string]bool   `json:"secretsSet,omitempty"` // secret key -> has a value
	LastTestedAt *time.Time        `json:"lastTestedAt,omitempty"`
	CreatedAt    time.Time         `json:"createdAt"`
	Category     string            `json:"category"`     // effective category id (see /api/tool-categories)
	CategoryAuto bool              `json:"categoryAuto"` // true => not explicitly chosen (type default / "other")
}

// liveToolDTO is toToolDTO with the status of connectors whose link lives in
// memory (WhatsApp) taken from the running session: the stored status is only the
// outcome of the last manual test and would stay "error" after a re-authentication.
func (s *Server) liveToolDTO(t store.Tool) toolDTO {
	dto := toToolDTO(t)
	if t.Type == "whatsapp" && s.waMgr != nil && s.waMgr.IsConnected(t.ID) {
		dto.Status, dto.Error = store.ToolConnected, ""
	}
	return dto
}

func toToolDTO(t store.Tool) toolDTO {
	// Category is resolved against the live category list by withCategory; without
	// it the explicit choice (or the type default) is reported as-is.
	cat, auto := t.CategoryID, false
	if cat == "" {
		cat, auto = tools.DefaultCategory(t.Type), true
	}
	return toolDTO{
		ID: t.ID, Type: t.Type, Name: t.Name, Status: t.Status, Enabled: t.Enabled,
		Error: t.Error, LastTestedAt: t.LastTestedAt, CreatedAt: t.CreatedAt,
		Category: cat, CategoryAuto: auto,
	}
}

// withCategory re-resolves the category of a tool DTO against the categories that
// exist now (a deleted category falls back to the type default, then "other").
func (s *Server) withCategory(dto toolDTO, explicit string) toolDTO {
	cats, err := s.db.ListToolCategories()
	if err != nil {
		return dto
	}
	ids := map[string]bool{}
	for _, c := range cats {
		ids[c.ID] = true
	}
	dto.Category, dto.CategoryAuto = tools.EffectiveCategory(explicit, dto.Type, func(id string) bool { return ids[id] })
	return dto
}

func (s *Server) listTools(w http.ResponseWriter, r *http.Request) {
	ts, err := s.db.ListTools()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	cats, _ := s.db.ListToolCategories()
	ids := map[string]bool{}
	for _, c := range cats {
		ids[c.ID] = true
	}
	out := make([]toolDTO, 0, len(ts))
	for _, t := range ts {
		dto := s.liveToolDTO(t)
		dto.Category, dto.CategoryAuto = tools.EffectiveCategory(t.CategoryID, t.Type, func(id string) bool { return ids[id] })
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, out)
}

// getTool returns a configured tool with its non-secret config values and the set
// of secret fields that already hold a value.
func (s *Server) getTool(w http.ResponseWriter, r *http.Request) {
	t, err := s.db.GetTool(r.PathValue("id"))
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	dto := s.withCategory(s.liveToolDTO(t), t.CategoryID)
	cfg, _ := s.decryptToolConfig(t.ConfigEnc)
	dto.Config = map[string]string{}
	dto.SecretsSet = map[string]bool{}
	if conn, ok := tools.Get(t.Type); ok {
		for _, f := range conn.Fields() {
			if f.Secret {
				dto.SecretsSet[f.Key] = cfg[f.Key] != ""
			} else {
				dto.Config[f.Key] = cfg[f.Key]
			}
		}
	}
	writeJSON(w, http.StatusOK, dto)
}

// createTool registers a configured tool, encrypting its config at rest.
func (s *Server) createTool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type   string            `json:"type"`
		Name   string            `json:"name"`
		Config map[string]string `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if _, ok := tools.Get(req.Type); !ok {
		writeErr(w, http.StatusBadRequest, errors.New("unknown connector type"))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = req.Type
	}
	enc, err := s.encryptToolConfig(req.Config)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	t := store.Tool{
		ID: uuid.NewString(), Type: req.Type, Name: name, Status: store.ToolDisconnected,
		Enabled: true, ConfigEnc: enc, CreatedAt: time.Now().UTC(),
	}
	if err := s.db.CreateTool(t); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, toToolDTO(t))
}

// updateTool edits a tool's label, enabled flag and/or config. A secret field left
// empty keeps its stored value, so the form need not re-send unchanged secrets.
func (s *Server) updateTool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    *string           `json:"name"`
		Enabled *bool             `json:"enabled"`
		Config  map[string]string `json:"config"`
		// Category moves the tool to a category id; "" puts it back on automatic.
		Category *string `json:"category"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	t, err := s.db.GetTool(r.PathValue("id"))
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	cur, _ := s.decryptToolConfig(t.ConfigEnc)
	merged := mergeConfig(cur, req.Config, t.Type)
	enc, err := s.encryptToolConfig(merged)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	name := t.Name
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		name = strings.TrimSpace(*req.Name)
	}
	enabled := t.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if req.Category != nil {
		if err := s.db.SetToolCategory(t.ID, strings.TrimSpace(*req.Category)); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeErr(w, http.StatusBadRequest, errors.New("unknown category"))
				return
			}
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if err := s.db.UpdateTool(t.ID, name, enc, enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	updated, _ := s.db.GetTool(t.ID)
	writeJSON(w, http.StatusOK, s.withCategory(toToolDTO(updated), updated.CategoryID))
}

func (s *Server) deleteTool(w http.ResponseWriter, r *http.Request) {
	if _, err := s.db.GetTool(r.PathValue("id")); err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	if err := s.db.DeleteTool(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testToolConfig validates an unsaved configuration (the "test before save"
// button). It returns {ok,error} rather than an HTTP error so the form can show
// the message inline.
func (s *Server) testToolConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type   string            `json:"type"`
		Config map[string]string `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	conn, ok := tools.Get(req.Type)
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("unknown connector type"))
		return
	}
	writeTestResult(w, conn.Test(r.Context(), req.Config))
}

// detectChat drives the assisted chat-detection flow for connectors that support
// it (Telegram). It operates on an unsaved config (the token typed in the form),
// like testToolConfig, and is stateless: the frontend carries the offset between
// short-polls. Returns {ok:false,error} on failure so the form can show it inline.
func (s *Server) detectChat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type   string            `json:"type"`
		Config map[string]string `json:"config"`
		Offset int64             `json:"offset"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	conn, ok := tools.Get(req.Type)
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("unknown connector type"))
		return
	}
	detector, ok := conn.(tools.ChatDetector)
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("connector does not support chat detection"))
		return
	}
	res, err := detector.DetectChat(r.Context(), req.Config, req.Offset)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "chats": res.Chats, "nextOffset": res.NextOffset})
}

// toolDashboard returns the live supervision snapshot of a saved tool whose
// connector implements Dashboarder (Proxmox). Credentials are decrypted
// server-side and never leave the backend; an upstream failure is a 502.
func (s *Server) toolDashboard(w http.ResponseWriter, r *http.Request) {
	t, err := s.db.GetTool(r.PathValue("id"))
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	conn, ok := tools.Get(t.Type)
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("unknown connector type"))
		return
	}
	board, ok := conn.(tools.Dashboarder)
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("connector has no dashboard"))
		return
	}
	cfg, _ := s.decryptToolConfig(t.ConfigEnc)
	data, err := board.Dashboard(r.Context(), cfg)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, data)
}

// testMessage sends a canned message through a connector that supports it
// (Telegram) on an unsaved config, so the user can confirm delivery right after
// detecting the chat id, before saving.
func (s *Server) testMessage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type   string            `json:"type"`
		Config map[string]string `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	conn, ok := tools.Get(req.Type)
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("unknown connector type"))
		return
	}
	s.sendTestMessage(w, r, conn, req.Config)
}

// testMessageTool sends a canned test message for a saved tool. The stored config
// (incl. the secret token) is merged with an optional body override so the form can
// pass a freshly-detected chat_id that isn't persisted yet.
func (s *Server) testMessageTool(w http.ResponseWriter, r *http.Request) {
	t, err := s.db.GetTool(r.PathValue("id"))
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	conn, ok := tools.Get(t.Type)
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("unknown connector type"))
		return
	}
	var req struct {
		Config map[string]string `json:"config"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req) // body optional
	cur, _ := s.decryptToolConfig(t.ConfigEnc)
	s.sendTestMessage(w, r, conn, mergeConfig(cur, req.Config, t.Type))
}

// sendTestMessage runs a connector's MessageTester and writes {ok,result|error}.
func (s *Server) sendTestMessage(w http.ResponseWriter, r *http.Request, conn tools.Connector, cfg map[string]string) {
	tester, ok := conn.(tools.MessageTester)
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("connector does not support a test message"))
		return
	}
	res, err := tester.TestMessage(r.Context(), cfg)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": res})
}

// testTool tests a saved tool with its stored credentials and records the outcome.
func (s *Server) testTool(w http.ResponseWriter, r *http.Request) {
	t, err := s.db.GetTool(r.PathValue("id"))
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	conn, ok := tools.Get(t.Type)
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("unknown connector type"))
		return
	}
	cfg, _ := s.decryptToolConfig(t.ConfigEnc)
	cfg["_tool_id"] = t.ID // stateful connectors (Proton session) key on it
	testErr := conn.Test(r.Context(), cfg)
	now := time.Now().UTC()
	status, msg := store.ToolConnected, ""
	if testErr != nil {
		status, msg = store.ToolError, testErr.Error()
	}
	_ = s.db.SetToolStatus(t.ID, status, msg, &now)
	writeTestResult(w, testErr)
}

func writeTestResult(w http.ResponseWriter, err error) {
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- config crypto + merge helpers ---

func (s *Server) encryptToolConfig(cfg map[string]string) (string, error) {
	if cfg == nil {
		cfg = map[string]string{}
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return s.secrets.Encrypt(b)
}

func (s *Server) decryptToolConfig(enc string) (map[string]string, error) {
	cfg := map[string]string{}
	if enc == "" {
		return cfg, nil
	}
	b, err := s.secrets.Decrypt(enc)
	if err != nil {
		return cfg, err
	}
	_ = json.Unmarshal(b, &cfg)
	return cfg, nil
}

// mergeConfig overlays incoming values onto the current config. An empty value on
// a secret field keeps the stored secret (the form never re-sends it).
func mergeConfig(cur, incoming map[string]string, connType string) map[string]string {
	merged := map[string]string{}
	for k, v := range cur {
		merged[k] = v
	}
	secret := map[string]bool{}
	if conn, ok := tools.Get(connType); ok {
		for _, f := range conn.Fields() {
			secret[f.Key] = f.Secret
		}
	}
	for k, v := range incoming {
		if v == "" && secret[k] {
			continue // keep existing secret
		}
		merged[k] = v
	}
	return merged
}

// --- WhatsApp-specific endpoints ---

// whatsappQR returns the current pairing status (and QR code string when
// pairing is in progress) for a saved WhatsApp tool. The frontend polls this
// endpoint every few seconds to drive the QR display.
// parseAllowedNumbers splits a comma-separated phone number string into a slice.
// Empty string returns nil (all numbers allowed).
func parseAllowedNumbers(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if n := strings.TrimSpace(p); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func (s *Server) whatsappQR(w http.ResponseWriter, r *http.Request) {
	if s.waMgr == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("WhatsApp manager not running"))
		return
	}
	t, err := s.db.GetTool(r.PathValue("id"))
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	if t.Type != "whatsapp" {
		writeErr(w, http.StatusBadRequest, errors.New("not a WhatsApp tool"))
		return
	}
	cfg, _ := s.decryptToolConfig(t.ConfigEnc)
	cfg["_tool_id"] = t.ID
	botMode := cfg["bot_mode"] == "true"
	botProject := cfg["bot_project_id"]
	allowedNumbers := parseAllowedNumbers(cfg["allowed_numbers"])

	// Ensure the client is initialised (idempotent).
	if err := s.waMgr.Ensure(t.ID, botMode, botProject, allowedNumbers); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s.waMgr.QRInfo(t.ID))
}

// whatsappConnect starts a QR pairing flow for an unpaired WhatsApp tool.
func (s *Server) whatsappConnect(w http.ResponseWriter, r *http.Request) {
	if s.waMgr == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("WhatsApp manager not running"))
		return
	}
	t, err := s.db.GetTool(r.PathValue("id"))
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	if t.Type != "whatsapp" {
		writeErr(w, http.StatusBadRequest, errors.New("not a WhatsApp tool"))
		return
	}
	cfg, _ := s.decryptToolConfig(t.ConfigEnc)
	botMode := cfg["bot_mode"] == "true"
	botProject := cfg["bot_project_id"]
	allowedNumbers := parseAllowedNumbers(cfg["allowed_numbers"])

	if err := s.waMgr.Ensure(t.ID, botMode, botProject, allowedNumbers); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	// Start QR flow in background; the client polls /qr to pick up the code.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		_ = s.waMgr.StartQR(ctx, t.ID)
	}()

	writeJSON(w, http.StatusOK, map[string]string{"status": "pairing_started"})
}

// ensure whatsapp import is used through waMgr field type
var _ *whatsapp.Manager

// --- Proton-specific endpoints ---

// protonStatus reports the stored Proton session (connected, paused, last error)
// without any network call. Secrets are never returned.
func (s *Server) protonStatus(w http.ResponseWriter, r *http.Request) {
	t, err := s.db.GetTool(r.PathValue("id"))
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	if t.Type != "proton" {
		writeErr(w, http.StatusBadRequest, errors.New("not a Proton tool"))
		return
	}
	cfg, _ := s.decryptToolConfig(t.ConfigEnc)
	writeJSON(w, http.StatusOK, tools.ProtonStatusOf(cfg))
}

// protonConnect runs the one-time interactive Proton login. The password / 2FA
// code travel in the request body only; just the resulting session is stored.
// Refusals come back as {ok:false,error} so the dialog can show them inline.
func (s *Server) protonConnect(w http.ResponseWriter, r *http.Request) {
	t, err := s.db.GetTool(r.PathValue("id"))
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	if t.Type != "proton" {
		writeErr(w, http.StatusBadRequest, errors.New("not a Proton tool"))
		return
	}
	var req struct {
		Username        string `json:"username"`
		Password        string `json:"password"`
		MailboxPassword string `json:"mailboxPassword"`
		TOTP            string `json:"totp"`
		CaptchaDone     bool   `json:"captchaDone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	cfg, _ := s.decryptToolConfig(t.ConfigEnc)
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	res, err := tools.ProtonConnect(ctx, t.ID, cfg, tools.ProtonConnectInput{
		Username: req.Username, Password: req.Password, MailboxPassword: req.MailboxPassword, TOTP: req.TOTP,
		CaptchaDone: req.CaptchaDone,
	})
	now := time.Now().UTC()
	if err != nil {
		_ = s.db.SetToolStatus(t.ID, store.ToolError, err.Error(), &now)
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if res.Status == "connected" {
		_ = s.db.SetToolStatus(t.ID, store.ToolConnected, "", &now)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": res.Status, "message": res.Message, "url": res.URL, "expiresAt": res.ExpiresAt})
}

// protonCancel ends a CAPTCHA wait / half-done login: nothing is sent to Proton
// and the password held in memory is wiped.
func (s *Server) protonCancel(w http.ResponseWriter, r *http.Request) {
	t, err := s.db.GetTool(r.PathValue("id"))
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	if t.Type != "proton" {
		writeErr(w, http.StatusBadRequest, errors.New("not a Proton tool"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cancelled": tools.ProtonConnectCancel(t.ID)})
}
