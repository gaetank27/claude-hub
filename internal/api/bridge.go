package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"system/internal/store"
	"system/internal/tools"
)

// Proton Bridge panel (Outils › Proton Bridge): connects the Proton mail account
// to the local Bridge from a Hub form and keeps the matching IMAP connector
// instance ("Proton Bridge") up to date. The Proton password, 2FA code and
// mailbox password only transit through the request body (see tools.BridgeConnect);
// the only thing stored is the Bridge-generated IMAP password, encrypted like the
// other connector secrets. Read-only: Bridge's SMTP side is never configured.

const (
	bridgeToolName   = "Proton Bridge"
	bridgeMarkerKey  = "_bridge" // config marker of the instance managed here
	bridgeSyncBudget = 20 * time.Minute
)

// bridgeWait is the in-memory view of the wait for the first sync after a connect.
var bridgeWait = struct {
	sync.Mutex
	toolID string
	since  time.Time
	done   bool
	err    string
}{}

// bridgeTool finds the IMAP instance fed by Bridge: the one this panel created,
// else a hand-made one pointing at the Bridge loopback port (cmd/addbridge).
func (s *Server) bridgeTool() (store.Tool, map[string]string, bool) {
	all, err := s.db.ListTools()
	if err != nil {
		return store.Tool{}, nil, false
	}
	var fallback *store.Tool
	var fallbackCfg map[string]string
	for i := range all {
		t := all[i]
		if t.Type != "imap" {
			continue
		}
		cfg, _ := s.decryptToolConfig(t.ConfigEnc)
		if cfg[bridgeMarkerKey] == "1" {
			return t, cfg, true
		}
		if fallback == nil && t.Name == bridgeToolName && cfg["host"] == "127.0.0.1" {
			fallback, fallbackCfg = &all[i], cfg
		}
	}
	if fallback != nil {
		return *fallback, fallbackCfg, true
	}
	return store.Tool{}, nil, false
}

type bridgeStatusDTO struct {
	tools.BridgeProbe
	State     string `json:"state"` // not_installed | stopped | no_account | busy | awaiting | syncing | ready | error
	Account   string `json:"account,omitempty"`
	ToolID    string `json:"toolId,omitempty"`
	LastError string `json:"lastError,omitempty"`
	Assistant bool   `json:"assistantAccess"`
}

func (s *Server) bridgeStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.bridgeStatusNow())
}

func (s *Server) bridgeStatusNow() bridgeStatusDTO {
	dto := bridgeStatusDTO{BridgeProbe: tools.BridgeProbeNow()}
	t, cfg, ok := s.bridgeTool()
	if ok {
		dto.Account, dto.ToolID, dto.LastError = cfg["username"], t.ID, cfg["_bridge_error"]
		dto.Assistant = s.assistantHasTool(t.ID)
	}
	switch {
	case !dto.Installed:
		dto.State = "not_installed"
	case dto.Busy:
		dto.State = "busy"
	case dto.Pending != "":
		dto.State = "awaiting"
	case !dto.ServiceActive:
		dto.State = "stopped"
	case !ok:
		dto.State = "no_account"
	default:
		if err := tools.BridgeIMAPReady(cfg); err == nil {
			dto.State = "ready"
			dto.LastError = ""
		} else {
			bridgeWait.Lock()
			syncing := bridgeWait.toolID == t.ID && !bridgeWait.done && time.Since(bridgeWait.since) < bridgeSyncBudget
			bridgeWait.Unlock()
			dto.State = "error"
			if syncing {
				dto.State = "syncing"
			}
			dto.LastError = err.Error()
		}
	}
	return dto
}

func (s *Server) assistantHasTool(toolID string) bool {
	if s.assistant == nil || s.assistant.ProjectID() == "" {
		return false
	}
	a, err := s.db.GetProjectAccess(s.assistant.ProjectID())
	return err == nil && a.ToolAllowed(toolID)
}

// bridgeConnect drives one login step. Refusals come back as {ok:false,error,kind}
// so the form shows them inline; the secrets are not echoed anywhere.
func (s *Server) bridgeConnect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email           string `json:"email"`
		Password        string `json:"password"`
		TOTP            string `json:"totp"`
		MailboxPassword string `json:"mailboxPassword"`
		CaptchaDone     bool   `json:"captchaDone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid body"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 150*time.Second)
	defer cancel()
	res, err := tools.BridgeConnect(ctx, tools.BridgeConnectInput{
		Email: req.Email, Password: req.Password, TOTP: req.TOTP, MailboxPassword: req.MailboxPassword, CaptchaDone: req.CaptchaDone,
	})
	if err != nil {
		kind := "other"
		var be *tools.BridgeError
		if errors.As(err, &be) {
			kind = be.Kind
		}
		s.bridgeRecordError(err.Error())
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "kind": kind})
		return
	}
	if res.Status != "added" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": res.Status, "message": res.Message, "url": res.URL})
		return
	}
	id, err := s.bridgeUpsertTool(res)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "kind": "other",
			"error": "Compte ajouté dans Bridge mais instance IMAP non enregistrée : " + err.Error()})
		return
	}
	go s.bridgeWaitReady(id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "added",
		"message": "Compte ajouté dans Bridge. La première synchronisation des mails démarre ; l'état se met à jour ci-dessous."})
}

// bridgeRecordError remembers the last failure on the instance, when there is one.
func (s *Server) bridgeRecordError(msg string) {
	t, cfg, ok := s.bridgeTool()
	if !ok {
		return
	}
	cfg["_bridge_error"] = msg
	if enc, err := s.encryptToolConfig(cfg); err == nil {
		_ = s.db.UpdateTool(t.ID, t.Name, enc, t.Enabled)
	}
}

// bridgeUpsertTool creates or refreshes the "Proton Bridge" IMAP instance and
// opens it to the assistant.
func (s *Server) bridgeUpsertTool(res tools.BridgeConnectResult) (string, error) {
	cfgNew := map[string]string{
		"host": "127.0.0.1", "port": strconv.Itoa(res.IMAPPort), "username": res.Email,
		"password": res.IMAPPassword, "tls": "false", bridgeMarkerKey: "1",
	}
	enc, err := s.encryptToolConfig(cfgNew)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	t, _, ok := s.bridgeTool()
	if ok {
		if err := s.db.UpdateTool(t.ID, t.Name, enc, true); err != nil {
			return "", err
		}
	} else {
		t = store.Tool{ID: uuid.NewString(), Type: "imap", Name: bridgeToolName, Status: store.ToolDisconnected,
			Enabled: true, ConfigEnc: enc, CreatedAt: now}
		if err := s.db.CreateTool(t); err != nil {
			return "", err
		}
	}
	s.bridgeGrantAssistant(t.ID)
	return t.ID, nil
}

// bridgeGrantAssistant adds the instance to the assistant's allowed connectors
// (the assistant starts in "list" mode; "all" needs nothing).
func (s *Server) bridgeGrantAssistant(toolID string) {
	if s.assistant == nil || s.assistant.ProjectID() == "" {
		return
	}
	pid := s.assistant.ProjectID()
	a, err := s.db.GetProjectAccess(pid)
	if err != nil || a.ToolAllowed(toolID) {
		return
	}
	a.AllowedTools = append(a.AllowedTools, toolID)
	_ = s.db.SetProjectAccess(pid, a)
}

// bridgeWaitReady polls until Bridge's IMAP answers with the new credentials
// (service restart + first sync), then marks the connector connected.
func (s *Server) bridgeWaitReady(toolID string) {
	bridgeWait.Lock()
	bridgeWait.toolID, bridgeWait.since, bridgeWait.done, bridgeWait.err = toolID, time.Now(), false, ""
	bridgeWait.Unlock()
	finish := func(errMsg string) {
		bridgeWait.Lock()
		if bridgeWait.toolID == toolID {
			bridgeWait.done, bridgeWait.err = true, errMsg
		}
		bridgeWait.Unlock()
	}
	deadline := time.Now().Add(bridgeSyncBudget)
	last := ""
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)
		t, cfg, ok := s.bridgeTool()
		if !ok || t.ID != toolID {
			finish("instance supprimée")
			return
		}
		err := tools.BridgeIMAPReady(cfg)
		now := time.Now().UTC()
		if err == nil {
			delete(cfg, "_bridge_error")
			if enc, e := s.encryptToolConfig(cfg); e == nil {
				_ = s.db.UpdateTool(t.ID, t.Name, enc, t.Enabled)
			}
			_ = s.db.SetToolStatus(t.ID, store.ToolConnected, "", &now)
			finish("")
			return
		}
		last = err.Error()
		_ = s.db.SetToolStatus(t.ID, store.ToolError, last, &now)
	}
	finish(last)
}

// bridgeCancel abandons a login waiting for the user, typing nothing into Bridge.
func (s *Server) bridgeCancel(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cancelled": tools.BridgeCancel()})
}

// bridgeDisconnect removes the account from Bridge and forgets the IMAP
// instance (and so its stored password).
func (s *Server) bridgeDisconnect(w http.ResponseWriter, r *http.Request) {
	t, cfg, ok := s.bridgeTool()
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 150*time.Second)
	defer cancel()
	email := strings.TrimSpace(cfg["username"])
	if err := tools.BridgeDisconnect(ctx, email); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := s.db.DeleteTool(t.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	bridgeWait.Lock()
	bridgeWait.toolID = ""
	bridgeWait.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
