package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"system/internal/store"
	"system/internal/tools"
)

// The Cloudflare UI endpoints are cookie-only (excluded from API keys in
// auth/apikey.go). Reads return structured data; every write goes through the
// connector's single guarded path (allow_writes switch + explicit confirmation +
// audit journal) with source "ui". The API token never leaves the backend.

// cloudflareTool returns the first enabled Cloudflare connector and its
// decrypted config.
func (s *Server) cloudflareTool(w http.ResponseWriter) (store.Tool, map[string]string, bool) {
	tls, err := s.db.ListTools()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return store.Tool{}, nil, false
	}
	for _, t := range tls {
		if t.Type == "cloudflare" && t.Enabled {
			cfg, err := s.decryptToolConfig(t.ConfigEnc)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return store.Tool{}, nil, false
			}
			return t, cfg, true
		}
	}
	writeErr(w, http.StatusNotFound, errors.New("no enabled Cloudflare connector: add one in Tools"))
	return store.Tool{}, nil, false
}

func (s *Server) cloudflareStatus(w http.ResponseWriter, r *http.Request) {
	tls, _ := s.db.ListTools()
	for _, t := range tls {
		if t.Type == "cloudflare" && t.Enabled {
			cfg, _ := s.decryptToolConfig(t.ConfigEnc)
			writeJSON(w, http.StatusOK, map[string]any{"configured": cfg["api_token"] != "", "toolId": t.ID, "allowWrites": cfg["allow_writes"] == "true"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": false, "allowWrites": false})
}

func (s *Server) cloudflareRead(w http.ResponseWriter, r *http.Request) {
	_, cfg, ok := s.cloudflareTool(w)
	if !ok {
		return
	}
	args := map[string]any{"zone_id": r.URL.Query().Get("zone"), "tunnel_id": r.URL.Query().Get("tunnel")}
	data, err := tools.CloudflareRead(r.Context(), cfg, r.PathValue("kind"), args)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, data)
}

func (s *Server) cloudflareAct(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action  string         `json:"action"`
		Args    map[string]any `json:"args"`
		Confirm bool           `json:"confirm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !tools.CloudflareWriteAction(req.Action) {
		writeErr(w, http.StatusBadRequest, errors.New("not a Cloudflare write action"))
		return
	}
	if !req.Confirm {
		writeErr(w, http.StatusBadRequest, errors.New("explicit confirmation required"))
		return
	}
	_, cfg, ok := s.cloudflareTool(w)
	if !ok {
		return
	}
	cfg["_source"] = "ui"
	if req.Args == nil {
		req.Args = map[string]any{}
	}
	req.Args["user_confirmed"] = true
	conn, _ := tools.Get("cloudflare")
	out, err := conn.Call(r.Context(), cfg, req.Action, req.Args)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": out})
}

// cloudflareTunnelToken reveals the install token of a tunnel with the ready-to-run
// cloudflared commands, after explicit confirmation. Never cached by the browser.
func (s *Server) cloudflareTunnelToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Confirm bool `json:"confirm"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req)
	if !req.Confirm {
		writeErr(w, http.StatusBadRequest, errors.New("explicit confirmation required"))
		return
	}
	_, cfg, ok := s.cloudflareTool(w)
	if !ok {
		return
	}
	tok, err := tools.CloudflareTunnelToken(r.Context(), cfg, r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"token":   tok,
		"service": "sudo cloudflared service install " + tok,
		"docker":  "docker run -d --restart unless-stopped cloudflare/cloudflared:latest tunnel --no-autoupdate run --token " + tok,
	})
}

func (s *Server) cloudflareAudit(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.ListConnectorAudit("cloudflare", 100)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

// cloudflareRevoke makes the Hub forget the API token (wipes it from the encrypted
// config, switches writes off and disables the connector). The token itself stays
// valid at Cloudflare until it is deleted in the dashboard: the response says so.
func (s *Server) cloudflareRevoke(w http.ResponseWriter, r *http.Request) {
	t, cfg, ok := s.cloudflareTool(w)
	if !ok {
		return
	}
	delete(cfg, "api_token")
	cfg["allow_writes"] = "false"
	enc, err := s.encryptToolConfig(cfg)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.db.UpdateTool(t.ID, t.Name, enc, false); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.db.AddConnectorAudit(store.ConnectorAudit{Connector: "cloudflare", Action: "revoke_token", Source: "ui", Outcome: "ok", Detail: "token wiped from the Hub"})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "note": "Token removed from the Hub. Also delete it in Cloudflare (My Profile › API Tokens, or Manage Account › Account API Tokens) so it can no longer be used anywhere."})
}
