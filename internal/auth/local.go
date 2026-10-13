package auth

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"system/internal/store"
)

// displayName is what the UI shows for a local user: the email when set,
// otherwise the username.
func displayName(u store.User) string {
	if u.Email != "" {
		return u.Email
	}
	return u.Username
}

// setPending2FA installs the short-lived cookie that proves a password was
// verified and pins the user awaiting their TOTP code. It also carries the
// "stay signed in" choice made at the password step over to the TOTP step.
func (a *Authenticator) setPending2FA(w http.ResponseWriter, userID string, remember bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     pendingCookie,
		Value:    a.seal(pendingState{UserID: userID, Exp: time.Now().Add(pendingTTL).Unix(), Remember: remember}),
		Path:     "/auth/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(pendingTTL.Seconds()),
	})
}

func (a *Authenticator) pending2FA(r *http.Request) (pendingState, bool) {
	c, err := r.Cookie(pendingCookie)
	if err != nil {
		return pendingState{}, false
	}
	var ps pendingState
	if a.open(c.Value, &ps) && time.Now().Unix() <= ps.Exp {
		return ps, true
	}
	return pendingState{}, false
}

func (a *Authenticator) clearPending2FA(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: pendingCookie, Path: "/auth/", MaxAge: -1})
}

// LocalLoginHandler verifies a username/password pair. On success it does NOT
// establish a session yet: it pins the user with a pending-2FA cookie and tells
// the SPA to collect a TOTP code (totpRequired=true). The legacy case of a user
// without enrolled TOTP signs in directly.
func (a *Authenticator) LocalLoginHandler(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Remember bool   `json:"remember"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	u, err := a.cfg.DB.GetUserByUsername(strings.TrimSpace(in.Username))
	if err != nil {
		// Run a dummy hash so timing does not reveal whether the user exists.
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$invalidinvalidinvalidinvalidinvalidinvalidinvalidinv"), []byte(in.Password))
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.Password)) != nil {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if !u.TOTPConfirmed {
		a.setSession(w, r, u.ID, displayName(u), "local", in.Remember)
		writeJSON(w, http.StatusOK, map[string]bool{"totpRequired": false})
		return
	}
	a.setPending2FA(w, u.ID, in.Remember)
	writeJSON(w, http.StatusOK, map[string]bool{"totpRequired": true})
}

// TOTPVerifyHandler completes a local login: it reads the pending-2FA cookie,
// validates the submitted code against the user's secret, then mints the full
// session.
func (a *Authenticator) TOTPVerifyHandler(w http.ResponseWriter, r *http.Request) {
	ps, ok := a.pending2FA(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "no pending login")
		return
	}
	var in struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	u, err := a.cfg.DB.GetUserByID(ps.UserID)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "no pending login")
		return
	}
	valid, err := a.validateCode(u.TOTPSecretEnc, strings.TrimSpace(in.Code))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "totp check failed")
		return
	}
	if !valid {
		writeErr(w, http.StatusUnauthorized, "invalid code")
		return
	}
	a.clearPending2FA(w)
	a.setSession(w, r, u.ID, displayName(u), "local", ps.Remember)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// VerifyLogin checks a username, password AND current TOTP code, for flows that
// need fresh strong authentication without minting a Hub session (the remote MCP
// consent screen). Accounts without a confirmed TOTP are refused. It returns the
// account's display name. Callers are responsible for throttling attempts.
func (a *Authenticator) VerifyLogin(username, password, code string) (string, bool) {
	u, err := a.cfg.DB.GetUserByUsername(strings.TrimSpace(username))
	if err != nil {
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$invalidinvalidinvalidinvalidinvalidinvalidinvalidinv"), []byte(password))
		return "", false
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil || !u.TOTPConfirmed {
		return "", false
	}
	ok, err := a.validateCode(u.TOTPSecretEnc, strings.TrimSpace(code))
	if err != nil || !ok {
		return "", false
	}
	return displayName(u), true
}
