// Package auth gates the hub. It supports two complementary mechanisms:
//
//   - Local accounts: login/password (bcrypt) followed by a mandatory TOTP 2FA
//     step. The first admin is created through the first-boot setup wizard.
//   - OIDC (optional): an Authorization-Code flow against a provider (e.g.
//     PocketID/Keycloak/Authentik), configurable from the environment or, at
//     runtime, through the wizard.
//
// Both end at the same HMAC-signed session cookie the middleware checks on every
// request. TLS is terminated by the reverse proxy in front, so this layer only
// authenticates. State/nonce and the short pending-2FA step live in signed
// cookies rather than server memory, so a flow survives a backend restart.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"system/internal/secretbox"
	"system/internal/store"
)

const (
	sessionCookie = "hub_session"
	stateCookie   = "hub_oidc_state"
	pendingCookie = "hub_2fa"
	sessionTTL    = 12 * time.Hour
	// rememberCookieTTL is how long the browser keeps a "stay signed in" cookie.
	// The session itself never expires; browsers cap cookie lifetimes at ~400
	// days, so WhoamiHandler re-issues it on each visit to keep pushing that out.
	rememberCookieTTL = 400 * 24 * time.Hour
	seenEvery         = 5 * time.Minute // granularity of a device's "last seen"
	flowTTL           = 10 * time.Minute
	pendingTTL        = 5 * time.Minute
)

// Config holds the cookie-signing key and the dependencies the local-auth and
// setup flows need.
type Config struct {
	CookieSecret []byte         // HMAC key; must be stable across restarts
	DB           *store.DB      // local users + setup state
	Secrets      *secretbox.Box // encrypts the TOTP secret at rest
	TOTPIssuer   string         // label shown in the authenticator app
}

// OIDCSettings are the parameters of the optional OIDC path. An empty Issuer
// means OIDC is disabled.
type OIDCSettings struct {
	Issuer        string
	ClientID      string
	ClientSecret  string
	RedirectURL   string
	AllowedSubs   []string
	AllowedEmails []string
}

// oidcProvider is the live, discovered OIDC client. It is rebuilt under lock
// whenever the settings change (env at boot, or the wizard at runtime).
type oidcProvider struct {
	settings OIDCSettings
	verifier *oidc.IDTokenVerifier
	oauth    oauth2.Config
}

// Authenticator guards HTTP handlers and runs the auth flows. It is always
// present (local auth cannot be turned off once an admin exists); OIDC is an
// optional add-on stored in `oidc`.
type Authenticator struct {
	cfg  Config
	mu   sync.RWMutex
	oidc *oidcProvider // nil when OIDC is not configured

	seenMu sync.Mutex
	seen   map[string]time.Time // login session id -> last last_seen_at write
}

// New builds an authenticator. It performs no network I/O — OIDC discovery is
// deferred to ConfigureOIDC so a missing/unreachable provider never blocks boot.
func New(cfg Config) *Authenticator {
	if cfg.TOTPIssuer == "" {
		cfg.TOTPIssuer = "Claude Hub"
	}
	return &Authenticator{cfg: cfg}
}

// ConfigureOIDC discovers the provider and installs (or, with an empty Issuer,
// removes) the OIDC path. The network call to the issuer's well-known endpoint
// happens here; callers treat a failure as "OIDC stays disabled".
func (a *Authenticator) ConfigureOIDC(ctx context.Context, s OIDCSettings) error {
	if s.Issuer == "" {
		a.mu.Lock()
		a.oidc = nil
		a.mu.Unlock()
		return nil
	}
	provider, err := oidc.NewProvider(ctx, s.Issuer)
	if err != nil {
		return err
	}
	op := &oidcProvider{
		settings: s,
		verifier: provider.Verifier(&oidc.Config{ClientID: s.ClientID}),
		oauth: oauth2.Config{
			ClientID:     s.ClientID,
			ClientSecret: s.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  s.RedirectURL,
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
	}
	a.mu.Lock()
	a.oidc = op
	a.mu.Unlock()
	return nil
}

// OIDCEnabled reports whether an OIDC provider is currently configured.
func (a *Authenticator) OIDCEnabled() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.oidc != nil
}

func (a *Authenticator) provider() *oidcProvider {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.oidc
}

type sessionClaims struct {
	Sub    string `json:"sub"`
	Email  string `json:"email"`
	Method string `json:"method,omitempty"` // "local" | "oidc"
	Exp    int64  `json:"exp"`
	// Forever marks a "stay signed in" session: Exp is ignored and it stays
	// valid until it is signed out or revoked.
	Forever bool `json:"fv,omitempty"`
	// Sid is the store.LoginSession row backing this cookie. The cookie is only
	// honoured while that row exists, which is what makes a device revocable.
	// Empty on cookies forged by MintSession (short-lived tooling).
	Sid string `json:"sid,omitempty"`
}

// SessionCookieName is the name of the session cookie the middleware validates.
// Exported so out-of-band tooling can install a forged session.
const SessionCookieName = sessionCookie

// MintSession forges a valid session-cookie value for the given identity, signed
// with secret. It lets out-of-band tooling (cmd/docgen) drive the authenticated
// UI without running an interactive flow. The holder of the HMAC secret is
// trusted, so keep the secret confined to the host.
func MintSession(secret []byte, sub, email string, ttl time.Duration) string {
	a := &Authenticator{cfg: Config{CookieSecret: secret}}
	return a.seal(sessionClaims{Sub: sub, Email: email, Exp: time.Now().Add(ttl).Unix()})
}

type flowState struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Exp      int64  `json:"exp"`
	Remember bool   `json:"rem,omitempty"`
}

type pendingState struct {
	UserID   string `json:"uid"`
	Exp      int64  `json:"exp"`
	Remember bool   `json:"rem,omitempty"`
}

// session extracts and validates the session cookie, returning the claims and
// whether a live (present, untampered, unexpired) session was found.
func (a *Authenticator) session(r *http.Request) (sessionClaims, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return sessionClaims{}, false
	}
	var sc sessionClaims
	if !a.open(c.Value, &sc) || !(sc.Forever || time.Now().Unix() <= sc.Exp) {
		return sessionClaims{}, false
	}
	if a.cfg.DB == nil {
		return sc, true
	}
	if sc.Sid == "" {
		// No server-side row: only short-lived cookies are accepted this way
		// (MintSession tooling, sign-ins that predate login sessions).
		return sc, !sc.Forever
	}
	if !a.cfg.DB.LoginSessionLive(sc.Sid) {
		return sessionClaims{}, false // revoked or signed out
	}
	a.touch(sc.Sid, r)
	return sc, true
}

// touch refreshes the device's "last seen" at most once per seenEvery, so the
// check on every request stays a read.
func (a *Authenticator) touch(sid string, r *http.Request) {
	a.seenMu.Lock()
	fresh := time.Since(a.seen[sid]) < seenEvery
	if !fresh {
		if a.seen == nil {
			a.seen = map[string]time.Time{}
		}
		a.seen[sid] = time.Now()
	}
	a.seenMu.Unlock()
	if !fresh {
		_ = a.cfg.DB.TouchLoginSession(sid, r.UserAgent(), clientIP(r))
	}
}

// clientIP is the caller's address. The backend listens on loopback behind the
// bundled reverse proxy, so a loopback peer's X-Forwarded-For is trusted.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if fwd, _, _ := strings.Cut(r.Header.Get("X-Forwarded-For"), ","); strings.TrimSpace(fwd) != "" {
			return strings.TrimSpace(fwd)
		}
	}
	return host
}

// setSession signs and installs the full session cookie for an authenticated
// identity, backed by a new login-session row so the device can be revoked. It
// lasts sessionTTL, or indefinitely when remember ("stay signed in") is set.
func (a *Authenticator) setSession(w http.ResponseWriter, r *http.Request, sub, email, method string, remember bool) {
	exp := time.Now().Add(sessionTTL)
	sc := sessionClaims{Sub: sub, Email: email, Method: method, Exp: exp.Unix()}
	if remember {
		exp = time.Time{}
		sc.Exp, sc.Forever = 0, true
	}
	if a.cfg.DB != nil {
		ls, err := a.cfg.DB.CreateLoginSession(sub, email, method, r.UserAgent(), clientIP(r), exp)
		if err != nil {
			http.Error(w, "session store unavailable", http.StatusInternalServerError)
			return
		}
		sc.Sid = ls.ID
	}
	a.writeSession(w, sc)
}

// writeSession (re)installs the cookie for existing claims.
func (a *Authenticator) writeSession(w http.ResponseWriter, sc sessionClaims) {
	ttl := sessionTTL
	if sc.Forever {
		ttl = rememberCookieTTL
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    a.seal(sc),
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl.Seconds()),
	})
}

// Middleware gates every request. /auth/* and /setup/* are always reachable (they
// carry the flows themselves); a valid session passes through. For everyone else,
// data (/api/*) and proxied apps (/x/*) get a 401 so the SPA can react, while the
// static SPA shell is served so it can render the login or setup screen
// client-side.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/auth/") || strings.HasPrefix(p, "/setup/") || strings.HasPrefix(p, "/webhook/") {
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := a.session(r); ok {
			next.ServeHTTP(w, r)
			return
		}
		if a.apiKey(r) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/x/") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// WhoamiHandler reports the signed-in user's identity to the SPA. The session
// cookie is HttpOnly, so the frontend cannot read it directly. Mounted under
// /auth/, it validates the cookie itself: 200 {"email"} when signed in, 401
// otherwise.
func (a *Authenticator) WhoamiHandler(w http.ResponseWriter, r *http.Request) {
	sc, ok := a.session(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if sc.Forever {
		// The SPA calls this on every load: refresh the browser-side lifetime.
		a.writeSession(w, sc)
	}
	writeJSON(w, http.StatusOK, map[string]string{"email": sc.Email})
}

// LoginHandler starts the OIDC flow: it stashes a signed state+nonce cookie and
// redirects to the provider. 404 when OIDC is not configured.
func (a *Authenticator) LoginHandler(w http.ResponseWriter, r *http.Request) {
	op := a.provider()
	if op == nil {
		http.Error(w, "OIDC not configured", http.StatusNotFound)
		return
	}
	state, err1 := randString(16)
	nonce, err2 := randString(16)
	if err1 != nil || err2 != nil {
		http.Error(w, "auth init failed", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookie,
		Value:    a.seal(flowState{State: state, Nonce: nonce, Exp: time.Now().Add(flowTTL).Unix(), Remember: r.URL.Query().Get("remember") == "1"}),
		Path:     "/auth/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(flowTTL.Seconds()),
	})
	http.Redirect(w, r, op.oauth.AuthCodeURL(state, oidc.Nonce(nonce)), http.StatusFound)
}

// oidcError redirects back to the login page with an error code the SPA turns
// into a localized message, instead of returning a raw HTTP error text. Only
// fixed codes are sent: nothing from the request is reflected into the page.
func oidcError(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, "/?oidc_error="+url.QueryEscape(code), http.StatusFound)
}

// CallbackHandler completes the OIDC flow: it checks state, exchanges the code,
// verifies the ID token and nonce, enforces the allowlist, then sets the session
// cookie and returns to the app.
func (a *Authenticator) CallbackHandler(w http.ResponseWriter, r *http.Request) {
	op := a.provider()
	if op == nil {
		http.Error(w, "OIDC not configured", http.StatusNotFound)
		return
	}
	// Provider error (e.g. user denied access).
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		code := "provider"
		if errParam == "access_denied" {
			code = "denied"
		}
		oidcError(w, r, code)
		return
	}
	c, err := r.Cookie(stateCookie)
	if err != nil {
		// No state cookie — likely a stale browser tab or a bookmarked callback URL.
		oidcError(w, r, "expired")
		return
	}
	var fs flowState
	if !a.open(c.Value, &fs) || time.Now().Unix() > fs.Exp {
		// Cookie tampered or flow took more than 10 minutes.
		oidcError(w, r, "expired")
		return
	}
	// Drop the one-shot state cookie regardless of outcome.
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Path: "/auth/", MaxAge: -1})
	if r.URL.Query().Get("state") != fs.State {
		oidcError(w, r, "state")
		return
	}
	tok, err := op.oauth.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		oidcError(w, r, "exchange")
		return
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok {
		oidcError(w, r, "token")
		return
	}
	idToken, err := op.verifier.Verify(r.Context(), rawID)
	if err != nil {
		oidcError(w, r, "token")
		return
	}
	if idToken.Nonce != fs.Nonce {
		oidcError(w, r, "state")
		return
	}
	var claims struct {
		Email string `json:"email"`
	}
	_ = idToken.Claims(&claims)
	if !allowed(op.settings, idToken.Subject, claims.Email) {
		oidcError(w, r, "forbidden")
		return
	}
	a.setSession(w, r, idToken.Subject, claims.Email, "oidc", fs.Remember)
	http.Redirect(w, r, "/", http.StatusFound)
}

// LogoutHandler ends the session (its row, so a copy of the cookie dies too) and
// clears it and any pending-2FA cookie.
func (a *Authenticator) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	if sc, ok := a.session(r); ok && sc.Sid != "" {
		_ = a.cfg.DB.DeleteLoginSession(sc.Sid)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1})
	http.SetCookie(w, &http.Cookie{Name: pendingCookie, Path: "/auth/", MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusFound)
}

// allowed enforces an OIDC allowlist. With no allowlist, any user the provider
// authenticates is accepted (the provider is the trust boundary).
func allowed(s OIDCSettings, sub, email string) bool {
	if len(s.AllowedSubs) == 0 && len(s.AllowedEmails) == 0 {
		return true
	}
	for _, x := range s.AllowedSubs {
		if x == sub {
			return true
		}
	}
	for _, e := range s.AllowedEmails {
		if strings.EqualFold(e, email) {
			return true
		}
	}
	return false
}

// seal serializes v to JSON, base64-encodes it, and appends an HMAC tag.
func (a *Authenticator) seal(v any) string {
	payload, _ := json.Marshal(v)
	b := base64.RawURLEncoding.EncodeToString(payload)
	return b + "." + a.sign(b)
}

// open verifies the HMAC tag and decodes the payload into dst. It does not check
// expiry — callers compare the Exp field themselves.
func (a *Authenticator) open(s string, dst any) bool {
	b, mac, ok := strings.Cut(s, ".")
	if !ok || !hmac.Equal([]byte(a.sign(b)), []byte(mac)) {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(b)
	if err != nil {
		return false
	}
	return json.Unmarshal(payload, dst) == nil
}

func (a *Authenticator) sign(s string) string {
	h := hmac.New(sha256.New, a.cfg.CookieSecret)
	h.Write([]byte(s))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

func randString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// writeJSON is the shared JSON responder for the auth/setup handlers.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
