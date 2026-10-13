package mcpremote

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"system/internal/tools"
)

// RegisterAdmin mounts the management API (behind the Hub login; cookie-only,
// see auth.apiKey).
func (s *Service) RegisterAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/mcp-remote", s.adminStatus)
	mux.HandleFunc("PUT /api/mcp-remote/settings", s.adminSettings)
	mux.HandleFunc("PUT /api/mcp-remote/tools", s.adminTool)
	mux.HandleFunc("DELETE /api/mcp-remote/grants/{id}", s.adminRevoke)
	mux.HandleFunc("DELETE /api/mcp-remote/grants", s.adminRevokeAll)
	mux.HandleFunc("GET /api/mcp-remote/audit", s.adminAudit)
}

type toolDTO struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Class       Class  `json:"class"`
	Enabled     bool   `json:"enabled"`
	Default     bool   `json:"default"`
	Confirm     bool   `json:"confirm"`
	Untrusted   bool   `json:"untrusted"`
}

func (s *Service) adminStatus(w http.ResponseWriter, r *http.Request) {
	st := s.load()
	list := []toolDTO{}
	for _, sp := range tools.ListSpecs(s.db) {
		e := lookup(sp.Name)
		list = append(list, toolDTO{Name: sp.Name, Description: sp.Description, Class: e.class, Enabled: st.effective(sp.Name),
			Default: e.defaultOn, Confirm: needsConfirm(sp.Name), Untrusted: untrusted(sp.Name)})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	grants, _ := s.db.ListMCPGrants()
	mcpURL := ""
	if st.PublicURL != "" {
		mcpURL = strings.TrimRight(st.PublicURL, "/") + "/mcp"
	}
	writeJSON(w, 200, map[string]any{"enabled": st.Enabled, "publicUrl": st.PublicURL, "mcpUrl": mcpURL, "tools": list, "grants": grants})
}

func (s *Service) adminSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled   *bool   `json:"enabled"`
		PublicURL *string `json:"publicUrl"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in) != nil {
		http.Error(w, "invalid body", 400)
		return
	}
	if in.PublicURL != nil {
		v := strings.TrimRight(strings.TrimSpace(*in.PublicURL), "/")
		if v != "" {
			u, err := url.Parse(v)
			if err != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || (u.Scheme != "https" && !s.insecure) {
				http.Error(w, "l'URL publique doit être une origine HTTPS (https://hôte), sans chemin", 400)
				return
			}
		}
		_ = s.db.SetAppMeta(metaURL, v)
	}
	if in.Enabled != nil {
		if *in.Enabled && s.load().PublicURL == "" {
			http.Error(w, "renseignez d'abord l'URL publique HTTPS", 400)
			return
		}
		v := "0"
		if *in.Enabled {
			v = "1"
		}
		_ = s.db.SetAppMeta(metaEnabled, v)
	}
	s.adminStatus(w, r)
}

func (s *Service) adminTool(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in) != nil || in.Name == "" {
		http.Error(w, "invalid body", 400)
		return
	}
	if lookup(in.Name).class == ClassBlocked {
		http.Error(w, "cet outil ne peut pas être exposé à distance", 400)
		return
	}
	st := s.load()
	st.Overrides[in.Name] = in.Enabled
	b, _ := json.Marshal(st.Overrides)
	_ = s.db.SetAppMeta(metaTools, string(b))
	s.adminStatus(w, r)
}

func (s *Service) adminRevoke(w http.ResponseWriter, r *http.Request) {
	if err := s.db.RevokeMCPGrant(r.PathValue("id")); err != nil {
		http.Error(w, "connexion introuvable", 404)
		return
	}
	s.audit(r.PathValue("id"), "", "hub-ui", "revoke", "", "", "ok", "")
	s.adminStatus(w, r)
}

func (s *Service) adminRevokeAll(w http.ResponseWriter, r *http.Request) {
	n, _ := s.db.RevokeAllMCPGrants()
	s.audit("", "", "hub-ui", "revoke-all", "", "", "ok", strconv.FormatInt(n, 10)+" grants")
	s.adminStatus(w, r)
}

func (s *Service) adminAudit(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if n <= 0 || n > 500 {
		n = 100
	}
	list, _ := s.db.ListMCPAudit(n)
	writeJSON(w, 200, list)
}
