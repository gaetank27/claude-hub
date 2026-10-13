package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"system/internal/session"
	"system/internal/store"
)

// accountDTO is the safe, secret-free view of a Claude subscription for the UI.
// The config dir (a server path) and any token/key are deliberately omitted.
type accountDTO struct {
	ID             string     `json:"id"`
	Label          string     `json:"label"`
	Kind           string     `json:"kind"`   // "oauth" | "apikey"
	Status         string     `json:"status"` // "connected" | "loggedout"
	Active         bool       `json:"active"`
	OrgID          string     `json:"orgId,omitempty"`
	AccountEmail   string     `json:"accountEmail,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	TokenExpiresAt *time.Time `json:"tokenExpiresAt,omitempty"`
}

// tokenExpiryFromCreds reads the OAuth token expiry from the account's
// .credentials.json file (field claudeAiOauth.expiresAt, a Unix ms timestamp).
// Returns nil when the file is missing, unreadable or the field is absent.
func tokenExpiryFromCreds(configDir string) *time.Time {
	raw, err := os.ReadFile(filepath.Join(configDir, ".credentials.json"))
	if err != nil {
		return nil
	}
	var doc struct {
		ClaudeAiOauth struct {
			ExpiresAt int64 `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.ClaudeAiOauth.ExpiresAt == 0 {
		return nil
	}
	t := time.UnixMilli(doc.ClaudeAiOauth.ExpiresAt)
	return &t
}

func toAccountDTO(a store.ClaudeAccount) accountDTO {
	return accountDTO{
		ID: a.ID, Label: a.Label, Kind: a.Kind, Status: a.Status,
		Active: a.Active, OrgID: a.OrgID, AccountEmail: a.AccountEmail,
		CreatedAt: a.CreatedAt, TokenExpiresAt: tokenExpiryFromCreds(a.ConfigDir),
	}
}

// listAccounts returns every connected/known Claude subscription.
func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	accts, err := s.mgr.Accounts()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]accountDTO, 0, len(accts))
	for _, a := range accts {
		out = append(out, toAccountDTO(a))
	}
	writeJSON(w, http.StatusOK, out)
}

// connectAccount links the hub to a new subscription. An apikey account is
// connected and made active immediately; an oauth account is created logged-out
// and completed via the login/start + login/finish flow.
func (s *Server) connectAccount(w http.ResponseWriter, r *http.Request) {
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
	// An apikey account is usable right away → make it the active subscription so
	// "login" means "now use this one".
	if acct.Kind == store.AccountKindAPIKey {
		if err := s.mgr.SwitchAccount(acct.ID); err != nil {
			writeAccountErr(w, err)
			return
		}
		acct.Active = true
	}
	writeJSON(w, http.StatusCreated, toAccountDTO(acct))
}

// renameAccount updates an account's display label.
func (s *Server) renameAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Label string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.db.SetClaudeAccountLabel(r.PathValue("id"), req.Label); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// switchAccount flips the active subscription. New sessions and the usage meter
// follow it; in-flight sessions keep their original account.
func (s *Server) switchAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.mgr.SwitchAccount(req.ID); err != nil {
		writeAccountErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// logoutAccount disconnects a subscription: it stops that account's live
// sessions, archives its credentials and marks it logged out. An empty id targets
// the currently-active account.
func (s *Server) logoutAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	id := req.ID
	if id == "" {
		a, err := s.mgr.ActiveAccount()
		if err != nil {
			writeAccountErr(w, session.ErrNoActiveAccount)
			return
		}
		id = a.ID
	}
	if err := s.mgr.LogoutAccount(r.Context(), id); err != nil {
		writeAccountErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// startLogin launches `claude auth login` under a PTY and returns the OAuth
// authorize URL the user opens in a browser to obtain the code.
func (s *Server) startLogin(w http.ResponseWriter, r *http.Request) {
	inst, err := s.mgr.StartLogin(r.PathValue("id"))
	if err != nil {
		writeAccountErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, inst)
}

// finishLogin submits the pasted auth code, validates the account is signed in
// (via `claude auth status`), enriches its identity and makes it active.
func (s *Server) finishLogin(w http.ResponseWriter, r *http.Request) {
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

// deleteAccount removes a non-active subscription (and its config dir), stopping
// its live sessions first. Any account is deletable, including default.
func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.DeleteAccount(r.Context(), r.PathValue("id")); err != nil {
		writeAccountErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeAccountErr maps account lifecycle errors to HTTP statuses.
func writeAccountErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, err)
	case errors.Is(err, session.ErrAccountActive):
		writeErr(w, http.StatusConflict, err)
	case errors.Is(err, session.ErrAccountLoggedOut),
		errors.Is(err, session.ErrNoActiveAccount):
		writeErr(w, http.StatusBadRequest, err)
	default:
		writeErr(w, http.StatusInternalServerError, err)
	}
}
