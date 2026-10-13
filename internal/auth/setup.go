package auth

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"system/internal/store"
)

// app_meta keys owned by the setup flow.
const (
	metaSetupComplete = "setup_complete"
	metaPublicBaseURL = "public_base_url"
	metaOIDCEnabled   = "oidc_enabled"
	metaOIDCIssuer    = "oidc_issuer"
	metaOIDCClientID  = "oidc_client_id"
	metaOIDCSecret    = "oidc_client_secret"
	metaOIDCRedirect  = "oidc_redirect_url"
	metaOIDCEmails    = "oidc_allowed_emails"
)

// SetupComplete reports whether the first-boot wizard has been finished.
func (a *Authenticator) SetupComplete() bool {
	v, err := a.cfg.DB.GetAppMeta(metaSetupComplete)
	return err == nil && v == "true"
}

// SetupStatusHandler tells the SPA whether to show the wizard and which login
// methods are available. Always reachable (read-only).
func (a *Authenticator) SetupStatusHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"needsSetup":  !a.SetupComplete(),
		"oidcEnabled": a.OIDCEnabled(),
	})
}

// guardSetup rejects setup-mutating calls once the wizard is finished.
func (a *Authenticator) guardSetup(w http.ResponseWriter) bool {
	if a.SetupComplete() {
		writeErr(w, http.StatusConflict, "setup already completed")
		return false
	}
	return true
}

// SetupAdminHandler creates the first admin and returns a TOTP enrolment (QR +
// secret). It is re-runnable before the wizard is finished: a previous,
// unconfirmed attempt is discarded first.
func (a *Authenticator) SetupAdminHandler(w http.ResponseWriter, r *http.Request) {
	if !a.guardSetup(w) {
		return
	}
	var in struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if in.Username == "" {
		writeErr(w, http.StatusBadRequest, "username is required")
		return
	}
	if len(in.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	// Discard any half-finished attempt, then refuse if a confirmed admin already
	// exists (setup is mid-flight past TOTP confirmation).
	if err := a.cfg.DB.DeleteUnconfirmedUsers(); err != nil {
		writeErr(w, http.StatusInternalServerError, "reset failed")
		return
	}
	if n, err := a.cfg.DB.CountUsers(); err != nil {
		writeErr(w, http.StatusInternalServerError, "count failed")
		return
	} else if n > 0 {
		writeErr(w, http.StatusConflict, "an admin already exists; continue to 2FA confirmation")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "hash failed")
		return
	}
	enr, err := a.newEnrolment(in.Username)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "totp setup failed")
		return
	}
	if err := a.cfg.DB.CreateUser(store.User{
		ID:            uuid.NewString(),
		Username:      in.Username,
		Email:         strings.TrimSpace(in.Email),
		PasswordHash:  string(hash),
		TOTPSecretEnc: enr.encrypted,
		TOTPConfirmed: false,
		Role:          "admin",
		CreatedAt:     time.Now(),
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "create user failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"otpauthUrl": enr.uri,
		"secret":     enr.secret,
		"qrPng":      enr.qrPNG,
	})
}

// SetupTOTPConfirmHandler validates the first TOTP code, marking the enrolment
// confirmed.
func (a *Authenticator) SetupTOTPConfirmHandler(w http.ResponseWriter, r *http.Request) {
	if !a.guardSetup(w) {
		return
	}
	var in struct {
		Username string `json:"username"`
		Code     string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	u, err := a.cfg.DB.GetUserByUsername(strings.TrimSpace(in.Username))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "unknown user")
		return
	}
	valid, err := a.validateCode(u.TOTPSecretEnc, strings.TrimSpace(in.Code))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "totp check failed")
		return
	}
	if !valid {
		writeErr(w, http.StatusBadRequest, "invalid code")
		return
	}
	if err := a.cfg.DB.ConfirmUserTOTP(u.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "confirm failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// SetupOptionsHandler persists the public base URL and the optional OIDC config.
// When OIDC is enabled it discovers the provider immediately so a bad issuer is
// reported here rather than silently failing at sign-in.
func (a *Authenticator) SetupOptionsHandler(w http.ResponseWriter, r *http.Request) {
	if !a.guardSetup(w) {
		return
	}
	var in struct {
		PublicBaseURL string `json:"publicBaseUrl"`
		OIDC          struct {
			Enabled       bool   `json:"enabled"`
			Issuer        string `json:"issuer"`
			ClientID      string `json:"clientId"`
			ClientSecret  string `json:"clientSecret"`
			RedirectURL   string `json:"redirectUrl"`
			AllowedEmails string `json:"allowedEmails"` // comma-separated
		} `json:"oidc"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	_ = a.cfg.DB.SetAppMeta(metaPublicBaseURL, strings.TrimSpace(in.PublicBaseURL))

	if in.OIDC.Enabled && strings.TrimSpace(in.OIDC.Issuer) != "" {
		s := OIDCSettings{
			Issuer:        strings.TrimSpace(in.OIDC.Issuer),
			ClientID:      strings.TrimSpace(in.OIDC.ClientID),
			ClientSecret:  in.OIDC.ClientSecret,
			RedirectURL:   strings.TrimSpace(in.OIDC.RedirectURL),
			AllowedEmails: splitList(in.OIDC.AllowedEmails),
		}
		if err := a.ConfigureOIDC(r.Context(), s); err != nil {
			writeErr(w, http.StatusBadRequest, "OIDC discovery failed: "+err.Error())
			return
		}
		_ = a.cfg.DB.SetAppMeta(metaOIDCEnabled, "true")
		_ = a.cfg.DB.SetAppMeta(metaOIDCIssuer, s.Issuer)
		_ = a.cfg.DB.SetAppMeta(metaOIDCClientID, s.ClientID)
		_ = a.cfg.DB.SetAppMeta(metaOIDCSecret, s.ClientSecret)
		_ = a.cfg.DB.SetAppMeta(metaOIDCRedirect, s.RedirectURL)
		_ = a.cfg.DB.SetAppMeta(metaOIDCEmails, in.OIDC.AllowedEmails)
	} else {
		_ = a.cfg.DB.SetAppMeta(metaOIDCEnabled, "false")
		_ = a.ConfigureOIDC(r.Context(), OIDCSettings{})
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// SetupFinishHandler closes the wizard: it requires a confirmed admin, marks
// setup complete and signs the operator in so they land in the app.
func (a *Authenticator) SetupFinishHandler(w http.ResponseWriter, r *http.Request) {
	if !a.guardSetup(w) {
		return
	}
	var in struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	u, err := a.cfg.DB.GetUserByUsername(strings.TrimSpace(in.Username))
	if err != nil || !u.TOTPConfirmed {
		writeErr(w, http.StatusBadRequest, "admin not fully enrolled")
		return
	}
	if err := a.cfg.DB.SetAppMeta(metaSetupComplete, "true"); err != nil {
		writeErr(w, http.StatusInternalServerError, "finalize failed")
		return
	}
	a.setSession(w, r, u.ID, displayName(u), "local", false)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// splitList parses a comma-separated list into a trimmed, non-empty slice.
func splitList(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
