package mcpremote

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"system/internal/store"
)

type authCode struct {
	clientID, redirect, challenge, email string
	exp                                  time.Time
}

func randToken(prefix string) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func oauthErr(w http.ResponseWriter, code int, e, desc string) {
	writeJSON(w, code, map[string]string{"error": e, "error_description": desc})
}

// redirectAllowed: only Claude's hosted callbacks and loopback (Claude Code,
// tests) may ever receive an authorization code. Anything else is refused at
// registration, which is what stops a rogue DCR client from phishing a code.
func redirectAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" || u.User != nil {
		return false
	}
	switch {
	case u.Scheme == "https" && (u.Host == "claude.ai" || u.Host == "claude.com") && u.Path == "/api/mcp/auth_callback":
		return true
	case u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"):
		return true
	}
	return false
}

// redirectMatches compares to a registered URI; loopback ignores the port.
func redirectMatches(registered []string, got string) bool {
	gu, err := url.Parse(got)
	if err != nil {
		return false
	}
	for _, r := range registered {
		if r == got {
			return true
		}
		ru, err := url.Parse(r)
		if err == nil && ru.Scheme == "http" && gu.Scheme == "http" && ru.Hostname() == gu.Hostname() &&
			(ru.Hostname() == "localhost" || ru.Hostname() == "127.0.0.1") && ru.Path == gu.Path {
			return true
		}
	}
	return false
}

func (s *Service) metaBase(w http.ResponseWriter, r *http.Request) (string, bool) {
	b, ok := s.base(r)
	if !ok {
		http.NotFound(w, r)
	}
	return b, ok
}

func (s *Service) handlePRM(w http.ResponseWriter, r *http.Request) {
	b, ok := s.metaBase(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, map[string]any{
		"resource": b + "/mcp", "authorization_servers": []string{b},
		"scopes_supported": []string{"mcp"}, "bearer_methods_supported": []string{"header"},
	})
}

func (s *Service) handleASMeta(w http.ResponseWriter, r *http.Request) {
	b, ok := s.metaBase(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, map[string]any{
		"issuer": b, "authorization_endpoint": b + "/oauth/authorize", "token_endpoint": b + "/oauth/token",
		"registration_endpoint":                 b + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{"mcp"},
	})
}

// handleRegister: RFC 7591 DCR, public clients only, allowlisted redirect URIs.
func (s *Service) handleRegister(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.metaBase(w, r); !ok {
		return
	}
	if !s.rl.allow("reg:"+clientIP(r), 20, time.Hour, s.now()) {
		oauthErr(w, 429, "temporarily_unavailable", "too many registrations")
		return
	}
	var in struct {
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil || len(in.RedirectURIs) == 0 || len(in.RedirectURIs) > 5 {
		oauthErr(w, 400, "invalid_client_metadata", "redirect_uris required")
		return
	}
	for _, u := range in.RedirectURIs {
		if !redirectAllowed(u) {
			oauthErr(w, 400, "invalid_redirect_uri", "redirect URI not allowed")
			return
		}
	}
	name := strings.TrimSpace(in.ClientName)
	if name == "" {
		name = "Client MCP"
	}
	if len(name) > 60 {
		name = name[:60]
	}
	c, err := s.db.CreateMCPClient(name, in.RedirectURIs)
	if err != nil {
		oauthErr(w, 500, "server_error", "could not register")
		return
	}
	writeJSON(w, 201, map[string]any{
		"client_id": c.ID, "client_name": c.Name, "redirect_uris": c.RedirectURIs,
		"token_endpoint_auth_method": "none", "grant_types": []string{"authorization_code", "refresh_token"},
		"response_types": []string{"code"}, "client_id_issued_at": c.CreatedAt.Unix(),
	})
}

type authReq struct {
	client                          store.MCPClient
	redirect, challenge, state, res string
}

// parseAuthReq validates the authorization request (query on GET, form on POST).
// On a bad client/redirect it answers directly (never redirecting to an unvalidated URI).
func (s *Service) parseAuthReq(w http.ResponseWriter, r *http.Request, base string) (authReq, bool) {
	q := r.Form
	var a authReq
	c, err := s.db.GetMCPClient(q.Get("client_id"))
	if err != nil || !redirectMatches(c.RedirectURIs, q.Get("redirect_uri")) {
		http.Error(w, "client_id / redirect_uri invalides", http.StatusBadRequest)
		return a, false
	}
	a = authReq{client: c, redirect: q.Get("redirect_uri"), challenge: q.Get("code_challenge"), state: q.Get("state"), res: q.Get("resource")}
	fail := func(e, d string) (authReq, bool) {
		redirectBack(w, r, a.redirect, a.state, map[string]string{"error": e, "error_description": d})
		return a, false
	}
	if q.Get("response_type") != "code" {
		return fail("unsupported_response_type", "response_type=code required")
	}
	if q.Get("code_challenge_method") != "S256" || len(a.challenge) < 43 || len(a.challenge) > 128 {
		return fail("invalid_request", "PKCE S256 required")
	}
	if a.res != "" && strings.TrimRight(a.res, "/") != base+"/mcp" {
		return fail("invalid_target", "unknown resource")
	}
	return a, true
}

func redirectBack(w http.ResponseWriter, r *http.Request, redirect, state string, params map[string]string) {
	u, _ := url.Parse(redirect)
	v := u.Query()
	for k, val := range params {
		v.Set(k, val)
	}
	if state != "" {
		v.Set("state", state)
	}
	u.RawQuery = v.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Service) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	base, ok := s.metaBase(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' https://claude.ai https://claude.com http://localhost:* http://127.0.0.1:*; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	_ = r.ParseForm()
	a, ok := s.parseAuthReq(w, r, base)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		s.renderConsent(w, r, a, "")
		return
	}
	if r.Form.Get("action") != "allow" {
		redirectBack(w, r, a.redirect, a.state, map[string]string{"error": "access_denied"})
		return
	}
	ip := clientIP(r)
	now := s.now()
	if s.fails.count("ip:"+ip, 15*time.Minute, now) >= 5 || s.fails.count("all", 15*time.Minute, now) >= 15 {
		s.audit("", a.client.Name, ip, "authorize", "", "", "ratelimited", "login locked")
		w.WriteHeader(http.StatusTooManyRequests)
		s.renderConsent(w, r, a, "Trop d'échecs : réessayez dans 15 minutes.")
		return
	}
	code := strings.TrimSpace(r.Form.Get("totp"))
	email, ok := s.verifier.VerifyLogin(r.Form.Get("username"), r.Form.Get("password"), code)
	if ok {
		s.mu.Lock()
		if _, replay := s.used[email+":"+code]; replay {
			ok = false
		} else {
			s.used[email+":"+code] = now
		}
		s.mu.Unlock()
	}
	if !ok {
		s.fails.record("ip:"+ip, now)
		s.fails.record("all", now)
		s.audit("", a.client.Name, ip, "authorize", "", "", "denied", "bad credentials")
		w.WriteHeader(http.StatusUnauthorized)
		s.renderConsent(w, r, a, "Identifiants ou code invalides.")
		return
	}
	c := randToken("")
	s.mu.Lock()
	s.codes[store.HashMCPToken(c)] = authCode{clientID: a.client.ID, redirect: a.redirect, challenge: a.challenge, email: email, exp: now.Add(codeTTL)}
	s.mu.Unlock()
	s.audit("", a.client.Name, ip, "authorize", "", "", "ok", "consent granted by "+email)
	redirectBack(w, r, a.redirect, a.state, map[string]string{"code": c})
}

func (s *Service) handleToken(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.metaBase(w, r); !ok {
		return
	}
	s.gc()
	ip := clientIP(r)
	if !s.rl.allow("tok:"+ip, 30, time.Minute, s.now()) {
		oauthErr(w, 429, "temporarily_unavailable", "slow down")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		oauthErr(w, 400, "invalid_request", "form body required")
		return
	}
	now := s.now()
	issue := func(clientID, clientName, email string) {
		acc, ref := randToken("hubm_"), randToken("hubr_")
		aexp, rexp := now.Add(accessTTL), now.Add(refreshTTL)
		if _, err := s.db.CreateMCPGrant(clientID, clientName, email, store.HashMCPToken(acc), aexp, store.HashMCPToken(ref), rexp); err != nil {
			oauthErr(w, 500, "server_error", "could not issue token")
			return
		}
		writeJSON(w, 200, map[string]any{"access_token": acc, "token_type": "Bearer", "expires_in": int(accessTTL.Seconds()), "refresh_token": ref, "scope": "mcp"})
	}
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		key := store.HashMCPToken(r.Form.Get("code"))
		s.mu.Lock()
		ac, found := s.codes[key]
		delete(s.codes, key) // single use, even on failure
		s.mu.Unlock()
		if !found || now.After(ac.exp) {
			oauthErr(w, 400, "invalid_grant", "unknown or expired code")
			return
		}
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		want := base64.RawURLEncoding.EncodeToString(sum[:])
		if ac.clientID != r.Form.Get("client_id") || ac.redirect != r.Form.Get("redirect_uri") ||
			subtle.ConstantTimeCompare([]byte(want), []byte(ac.challenge)) != 1 {
			oauthErr(w, 400, "invalid_grant", "client, redirect_uri or code_verifier mismatch")
			return
		}
		c, err := s.db.GetMCPClient(ac.clientID)
		if err != nil {
			oauthErr(w, 400, "invalid_client", "unknown client")
			return
		}
		s.audit("", c.Name, ip, "token", "", "", "ok", "grant created")
		issue(c.ID, c.Name, ac.email)
	case "refresh_token":
		acc, ref := randToken("hubm_"), randToken("hubr_")
		g, err := s.db.RotateMCPRefresh(store.HashMCPToken(r.Form.Get("refresh_token")), store.HashMCPToken(acc), now.Add(accessTTL), store.HashMCPToken(ref), now.Add(refreshTTL), now)
		if err == store.ErrMCPRefreshReuse {
			s.audit("", "", ip, "token", "", "", "denied", "refresh token reuse: grant revoked")
			oauthErr(w, 400, "invalid_grant", "refresh token reuse")
			return
		}
		if err != nil || g.ClientID != r.Form.Get("client_id") {
			oauthErr(w, 400, "invalid_grant", "invalid refresh token")
			return
		}
		s.audit(g.ID, g.ClientName, ip, "token", "", "", "ok", "refreshed")
		writeJSON(w, 200, map[string]any{"access_token": acc, "token_type": "Bearer", "expires_in": int(accessTTL.Seconds()), "refresh_token": ref, "scope": "mcp"})
	default:
		oauthErr(w, 400, "unsupported_grant_type", "authorization_code or refresh_token")
	}
}

func (s *Service) renderConsent(w http.ResponseWriter, r *http.Request, a authReq, msg string) {
	host := a.redirect
	if u, err := url.Parse(a.redirect); err == nil {
		host = u.Host
	}
	var hidden strings.Builder
	for _, k := range []string{"client_id", "redirect_uri", "response_type", "code_challenge", "code_challenge_method", "state", "resource", "scope"} {
		hidden.WriteString(`<input type="hidden" name="` + k + `" value="` + html.EscapeString(r.Form.Get(k)) + `">`)
	}
	tools := s.enabledNames()
	warn := ""
	if msg != "" {
		warn = `<p class="err">` + html.EscapeString(msg) + `</p>`
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><html lang="fr"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Claude Hub — autoriser</title>
<style>body{font:16px system-ui;background:#111;color:#eee;display:grid;place-items:center;min-height:100vh;margin:0}form{background:#1c1c1e;padding:24px;border-radius:14px;max-width:420px;width:92%}
input{width:100%;box-sizing:border-box;padding:10px;margin:6px 0 12px;border-radius:8px;border:1px solid #444;background:#111;color:#eee;font-size:16px}
button{padding:10px 16px;border-radius:8px;border:0;font-size:16px;cursor:pointer}.ok{background:#d97757;color:#fff}.no{background:#333;color:#eee;margin-left:8px}
.err{color:#ff6b6b}small{color:#aaa}code{background:#000;padding:1px 4px;border-radius:4px}</style></head><body>
<form method="post" action="/oauth/authorize"><h2>Autoriser « ` + html.EscapeString(a.client.Name) + ` » ?</h2>
<p>Cette application demande l'accès à votre <b>Claude Hub</b> (mails, agenda, maison, messages…). Retour vers : <code>` + html.EscapeString(host) + `</code>.</p>
<p><small>Outils accessibles : ` + html.EscapeString(strings.Join(tools, ", ")) + `.<br>Révocable à tout moment dans le Hub (Configuration › Connecteur Claude distant).</small></p>` + warn + hidden.String() + `
<label>Identifiant<input name="username" autocomplete="username" required></label>
<label>Mot de passe<input name="password" type="password" autocomplete="current-password" required></label>
<label>Code TOTP<input name="totp" inputmode="numeric" autocomplete="one-time-code" pattern="[0-9]{6}" required></label>
<button class="ok" name="action" value="allow">Autoriser</button><button class="no" name="action" value="deny" formnovalidate>Refuser</button></form></body></html>`))
}
