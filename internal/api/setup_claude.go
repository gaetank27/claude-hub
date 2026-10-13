package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"system/internal/store"
)

// Claude-subscription wiring for the first-boot wizard. These endpoints mirror the
// gated /api/claude/* ones but are reachable before sign-in (the wizard runs
// pre-login) and refuse once setup is finished. They live here, not in
// internal/auth, because the account service hangs off the Server (s.mgr).

// setupClaudeGuard allows the wizard's Claude endpoints only while setup is open.
func (s *Server) setupClaudeGuard(w http.ResponseWriter) bool {
	if s.auth == nil || s.auth.SetupComplete() {
		writeErr(w, http.StatusConflict, errors.New("setup already completed"))
		return false
	}
	return true
}

// setupConnectClaude links a subscription during the wizard. An apikey account is
// connected and made active at once; an oauth account is created logged-out and
// completed via setupClaudeLoginStart/Finish.
func (s *Server) setupConnectClaude(w http.ResponseWriter, r *http.Request) {
	if !s.setupClaudeGuard(w) {
		return
	}
	var req struct {
		Label  string `json:"label"`
		Kind   string `json:"kind"`
		APIKey string `json:"apiKey"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	acct, err := s.mgr.ConnectAccount(req.Label, req.Kind, req.APIKey)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if acct.Kind == store.AccountKindAPIKey {
		if err := s.mgr.SwitchAccount(acct.ID); err != nil {
			writeAccountErr(w, err)
			return
		}
		acct.Active = true
	}
	writeJSON(w, http.StatusCreated, toAccountDTO(acct))
}

// setupClaudeLoginStart launches `claude auth login` and returns the authorize URL
// the operator opens to obtain the code.
func (s *Server) setupClaudeLoginStart(w http.ResponseWriter, r *http.Request) {
	if !s.setupClaudeGuard(w) {
		return
	}
	inst, err := s.mgr.StartLogin(r.PathValue("id"))
	if err != nil {
		writeAccountErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, inst)
}

// setupClaudeLoginFinish submits the pasted code, validating + activating the
// subscription.
func (s *Server) setupClaudeLoginFinish(w http.ResponseWriter, r *http.Request) {
	if !s.setupClaudeGuard(w) {
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	acct, err := s.mgr.FinishLogin(r.PathValue("id"), req.Code)
	if err != nil {
		writeAccountErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toAccountDTO(acct))
}
