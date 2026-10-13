// Package mcpremote exposes the Hub's tools as a remote MCP server (Streamable
// HTTP) that claude.ai / the Claude mobile app can add as a custom connector.
//
// Security model: the endpoint is OFF by default; claude.ai authenticates through
// OAuth 2.1 (DCR + PKCE S256) whose consent screen demands the Hub password AND a
// fresh TOTP code; tokens are opaque, hashed at rest, short-lived and revocable;
// only whitelisted tools are served (read-only + lights by default), sensitive
// ones need an explicit per-tool opt-in AND a user_confirmed flag per call;
// every call is rate-limited and audited. See policy.go for the tool catalogue.
package mcpremote

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"system/internal/secretbox"
	"system/internal/store"
)

const (
	accessTTL  = time.Hour
	refreshTTL = 30 * 24 * time.Hour
	codeTTL    = 60 * time.Second
	auditKeep  = 90 * 24 * time.Hour
)

// Verifier checks Hub credentials (username + password + TOTP).
type Verifier interface {
	VerifyLogin(username, password, code string) (string, bool)
}

// Service is the remote MCP endpoint.
type Service struct {
	db       *store.DB
	box      *secretbox.Box
	verifier Verifier
	insecure bool // SYSTEM_MCP_ALLOW_INSECURE=1: accept plain HTTP (local tests only)

	mu    sync.Mutex
	codes map[string]authCode
	used  map[string]time.Time // TOTP replay guard

	rl     *limiter // per-grant / per-IP request budgets
	fails  *limiter // consent-screen login failures
	now    func() time.Time
	lastGC time.Time
}

func New(db *store.DB, box *secretbox.Box, v Verifier) *Service {
	return &Service{
		db: db, box: box, verifier: v,
		insecure: os.Getenv("SYSTEM_MCP_ALLOW_INSECURE") == "1",
		codes:    map[string]authCode{}, used: map[string]time.Time{},
		rl: newLimiter(), fails: newLimiter(), now: time.Now,
	}
}

// Register mounts the public routes. They sit outside /api so the Hub login
// middleware does not apply: each handler enforces its own authentication.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("/mcp", s.handleMCP)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.handlePRM)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", s.handlePRM)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.handleASMeta)
	mux.HandleFunc("POST /oauth/register", s.handleRegister)
	mux.HandleFunc("GET /oauth/authorize", s.handleAuthorize)
	mux.HandleFunc("POST /oauth/authorize", s.handleAuthorize)
	mux.HandleFunc("POST /oauth/token", s.handleToken)
}

// base returns the configured public origin ("" if unusable) after checking the
// request is for that host, over HTTPS. ok=false => the caller answers 404.
func (s *Service) base(r *http.Request) (string, bool) {
	st := s.load()
	if !st.Enabled || st.PublicURL == "" {
		return "", false
	}
	u, err := url.Parse(strings.TrimRight(st.PublicURL, "/"))
	if err != nil || u.Host == "" || u.Path != "" {
		return "", false
	}
	if u.Scheme != "https" && !(s.insecure && u.Scheme == "http") {
		return "", false
	}
	if !strings.EqualFold(r.Host, u.Host) {
		return "", false
	}
	if !s.insecure && r.TLS == nil && !strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return "", false
	}
	return u.Scheme + "://" + u.Host, true
}

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

func (s *Service) gc() {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if now.Sub(s.lastGC) < time.Minute {
		return
	}
	s.lastGC = now
	for k, c := range s.codes {
		if now.After(c.exp) {
			delete(s.codes, k)
		}
	}
	for k, t := range s.used {
		if now.Sub(t) > 2*time.Minute {
			delete(s.used, k)
		}
	}
}

// Purge is called periodically to age out audit lines and dead grants.
func (s *Service) Purge() { s.db.PurgeMCP(auditKeep) }
