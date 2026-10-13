package scriptstatus

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Admin handlers: mounted under /api/ (Hub login required), never public.

// ListKeysHandler: GET /api/script-keys.
func (s *Service) ListKeysHandler(w http.ResponseWriter, r *http.Request) {
	ks, err := ListKeys(s.db)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, 200, ks)
}

// CreateKeyHandler: POST /api/script-keys {script,label,allowedIps,silenceMinutes,wake}.
// The secret is returned once.
func (s *Service) CreateKeyHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Script          string    `json:"script"`
		Label           string    `json:"label"`
		AllowedIPs      []string  `json:"allowedIps"`
		SilenceMinutes  int       `json:"silenceMinutes"`
		Wake            *bool     `json:"wake"`
		AllowedCommands *[]string `json:"allowedCommands"`
		AllowImages     bool      `json:"allowImages"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid json"})
		return
	}
	wake := req.Wake == nil || *req.Wake
	var allowed []string // nil = default list of the script
	if req.AllowedCommands != nil {
		allowed = *req.AllowedCommands
	}
	k, secret, err := CreateKey(s.db, strings.TrimSpace(req.Script), req.Label, req.AllowedIPs, req.SilenceMinutes, wake, allowed)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if req.AllowImages {
		if err := SetKeyImages(s.db, k.ID, true); err != nil {
			writeJSON(w, 500, map[string]string{"error": "internal error"})
			return
		}
		k.AllowImages = true
	}
	writeJSON(w, 201, map[string]any{"key": k, "secret": secret})
}

// UpdateKeyHandler: PATCH /api/script-keys/{id}.
func (s *Service) UpdateKeyHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AllowedIPs      *[]string `json:"allowedIps"`
		SilenceMinutes  *int      `json:"silenceMinutes"`
		Wake            *bool     `json:"wake"`
		Enabled         *bool     `json:"enabled"`
		AllowedCommands *[]string `json:"allowedCommands"`
		AllowImages     *bool     `json:"allowImages"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid json"})
		return
	}
	err := UpdateKey(s.db, r.PathValue("id"), req.AllowedIPs, req.SilenceMinutes, req.Wake, req.Enabled, req.AllowedCommands)
	if err == nil && req.AllowImages != nil {
		err = SetKeyImages(s.db, r.PathValue("id"), *req.AllowImages)
	}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeJSON(w, 404, map[string]string{"error": "not found"})
	case err != nil:
		writeJSON(w, 400, map[string]string{"error": err.Error()})
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// RegenerateKeyHandler: POST /api/script-keys/{id}/regenerate — new secret, returned once.
func (s *Service) RegenerateKeyHandler(w http.ResponseWriter, r *http.Request) {
	k, secret, err := RegenerateKey(s.db, r.PathValue("id"))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeJSON(w, 404, map[string]string{"error": "not found"})
	case err != nil:
		writeJSON(w, 500, map[string]string{"error": "internal error"})
	default:
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, map[string]any{"key": k, "secret": secret})
	}
}

// DeleteKeyHandler: DELETE /api/script-keys/{id}.
func (s *Service) DeleteKeyHandler(w http.ResponseWriter, r *http.Request) {
	if err := DeleteKey(s.db, r.PathValue("id")); err != nil {
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// StatusListHandler: GET /api/script-status.
func (s *Service) StatusListHandler(w http.ResponseWriter, r *http.Request) {
	rs, err := s.List()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, 200, rs)
}

// StatusGetHandler: GET /api/script-status/{script} (latest + history).
func (s *Service) StatusGetHandler(w http.ResponseWriter, r *http.Request) {
	rep, ok, err := s.Get(r.PathValue("script"))
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "internal error"})
		return
	}
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return
	}
	h, _ := s.History(rep.Script, 20)
	writeJSON(w, 200, map[string]any{"status": rep, "history": h})
}

// DashboardHandler: GET /api/script-dashboard — every script's latest report plus
// its recent history (newest first) in one call, for the read-only Scripts page.
func (s *Service) DashboardHandler(w http.ResponseWriter, r *http.Request) {
	rs, err := s.List()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "internal error"})
		return
	}
	type entry struct {
		Status          Report        `json:"status"`
		History         []HistoryItem `json:"history"`
		Commands        []Command     `json:"commands"`
		AllowedCommands []string      `json:"allowedCommands"`
		Images          []ImageInfo   `json:"images"`
	}
	out := make([]entry, 0, len(rs))
	for _, rep := range rs {
		h, err := s.History(rep.Script, 30)
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": "internal error"})
			return
		}
		cmds, err := s.ListCommands(rep.Script, 15)
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": "internal error"})
			return
		}
		allowed, _ := AllowedCommands(s.db, rep.Script)
		imgs, err := s.ListImages(rep.Script)
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": "internal error"})
			return
		}
		out = append(out, entry{Status: rep, History: h, Commands: cmds, AllowedCommands: allowed, Images: imgs})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"scripts": out, "now": s.now().UnixMilli()})
}

func cmdErr(w http.ResponseWriter, err error) {
	var ce *CmdError
	if errors.As(err, &ce) {
		writeJSON(w, ce.Code, map[string]string{"error": ce.Msg})
		return
	}
	writeJSON(w, 500, map[string]string{"error": "internal error"})
}

// SendCommandHandler: POST /api/script-commands {script, name, params, ttlMinutes}.
// 400 when the name is not on the script's allow-list or the params are invalid.
func (s *Service) SendCommandHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Script     string          `json:"script"`
		Name       string          `json:"name"`
		Params     json.RawMessage `json:"params"`
		TTLMinutes int             `json:"ttlMinutes"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid json"})
		return
	}
	c, err := s.SendCommand(strings.TrimSpace(req.Script), strings.TrimSpace(req.Name), req.Params, "user", time.Duration(req.TTLMinutes)*time.Minute)
	if err != nil {
		cmdErr(w, err)
		return
	}
	writeJSON(w, 201, c)
}

// ListCommandsHandler: GET /api/script-commands?script=<name>&limit=<n>.
func (s *Service) ListCommandsHandler(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	cs, err := s.ListCommands(r.URL.Query().Get("script"), limit)
	if err != nil {
		cmdErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, cs)
}

// CancelCommandHandler: DELETE /api/script-commands/{id} (pending commands only).
func (s *Service) CancelCommandHandler(w http.ResponseWriter, r *http.Request) {
	if err := s.CancelCommand(r.PathValue("id")); err != nil {
		cmdErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
