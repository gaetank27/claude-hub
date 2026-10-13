package auth

import (
	"net/http"
	"strings"

	"system/internal/store"
)

// apiKey authenticates a machine client presenting `Authorization: Bearer hubk_…`
// and reports whether that key may make this request.
//
// An unscoped key reaches the whole API except key, signed-in device and remote-MCP
// management, which stay cookie-only so a leaked key cannot mint or revoke
// credentials. A project-scoped key
// may only launch, list and drive the sessions of its own project.
func (a *Authenticator) apiKey(r *http.Request) bool {
	if a.cfg.DB == nil {
		return false
	}
	scheme, secret, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	if !strings.EqualFold(scheme, "Bearer") || !strings.HasPrefix(secret, store.APIKeyPrefix) {
		return false
	}
	key, err := a.cfg.DB.LookupAPIKey(secret)
	if err != nil {
		return false
	}
	p := r.URL.Path
	if p == "/api/apikeys" || strings.HasPrefix(p, "/api/apikeys/") || p == "/api/cloudflare" || strings.HasPrefix(p, "/api/cloudflare/") || p == "/api/dns" || strings.HasPrefix(p, "/api/dns/") || p == "/api/mcp-remote" || strings.HasPrefix(p, "/api/mcp-remote/") || p == "/api/devices" || strings.HasPrefix(p, "/api/devices/") {
		return false
	}
	if key.ProjectID == "" {
		return true
	}
	if p == "/api/projects/"+key.ProjectID+"/sessions" {
		return true
	}
	if rest, ok := strings.CutPrefix(p, "/api/sessions/"); ok {
		id, sub, _ := strings.Cut(rest, "/")
		switch sub {
		case "", "stream", "stop", "journal":
		default:
			return false
		}
		sess, err := a.cfg.DB.GetSession(id)
		return err == nil && sess.ProjectID == key.ProjectID
	}
	return false
}
