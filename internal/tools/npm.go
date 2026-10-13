package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func init() { register(npm{}) }

// npm manages Nginx Proxy Manager (NPM) via its REST API.
// Auth is stateless: a short-lived JWT is fetched at the start of each Call().
type npm struct{}

func (npm) Type() string  { return "npm" }
func (npm) Label() string { return "Nginx Proxy Manager" }

func (npm) Fields() []Field {
	return []Field{
		{Key: "base_url", Label: "URL NPM", Type: FieldURL, Required: true,
			Placeholder: "http://192.0.2.11:81"},
		{Key: "email", Label: "Email admin", Type: FieldText, Required: true,
			Placeholder: "admin@example.com"},
		{Key: "password", Label: "Mot de passe", Type: FieldPassword, Secret: true, Required: true},
	}
}

func (npm) Test(ctx context.Context, cfg map[string]string) error {
	if err := missing(cfg, "base_url", "email", "password"); err != nil {
		return err
	}
	_, err := npmToken(ctx, cfg)
	return err
}

func (npm) Tools() []ToolSpec {
	return []ToolSpec{
		{
			Name:        "npm_list_proxy_hosts",
			Description: "List all proxy hosts configured in Nginx Proxy Manager.",
			Args:        []ArgSpec{},
		},
		{
			Name:        "npm_create_proxy_host",
			Description: "Create a new proxy host in NPM (domain → upstream). Optionally request a Let's Encrypt certificate.",
			Args: []ArgSpec{
				{Name: "domain", Type: "string", Description: "Public domain name, e.g. myapp.example.com", Required: true},
				{Name: "forward_host", Type: "string", Description: "Upstream host or IP, e.g. 192.0.2.20", Required: true},
				{Name: "forward_port", Type: "integer", Description: "Upstream port, e.g. 3000", Required: true},
				{Name: "ssl", Type: "boolean", Description: "Request a Let's Encrypt certificate and force HTTPS (default false)"},
				{Name: "forward_scheme", Type: "string", Description: "Upstream scheme: http (default) or https"},
				{Name: "access_list_id", Type: "integer", Description: "ID of an NPM access list to restrict access (0 = none)"},
				{Name: "advanced_config", Type: "string", Description: "Custom nginx config block inserted in the vhost"},
			},
		},
		{
			Name:        "npm_delete_proxy_host",
			Description: "Delete a proxy host by its numeric ID.",
			Args: []ArgSpec{
				{Name: "id", Type: "integer", Description: "Proxy host ID returned by npm_list_proxy_hosts", Required: true},
			},
		},
		{
			Name:        "npm_enable_proxy_host",
			Description: "Enable (re-activate) a disabled proxy host.",
			Args: []ArgSpec{
				{Name: "id", Type: "integer", Description: "Proxy host ID", Required: true},
			},
		},
		{
			Name:        "npm_disable_proxy_host",
			Description: "Disable (pause) a proxy host without deleting it.",
			Args: []ArgSpec{
				{Name: "id", Type: "integer", Description: "Proxy host ID", Required: true},
			},
		},
		{
			Name:        "npm_list_certificates",
			Description: "List all SSL certificates stored in Nginx Proxy Manager.",
			Args:        []ArgSpec{},
		},
		{
			Name:        "npm_list_access_lists",
			Description: "List all access lists defined in Nginx Proxy Manager.",
			Args:        []ArgSpec{},
		},
	}
}

func (npm) Call(ctx context.Context, cfg map[string]string, action string, args map[string]any) (string, error) {
	token, err := npmToken(ctx, cfg)
	if err != nil {
		return "", fmt.Errorf("npm auth: %w", err)
	}

	switch action {
	case "npm_list_proxy_hosts":
		return npmListProxyHosts(ctx, cfg, token)

	case "npm_create_proxy_host":
		domain := argString(args, "domain", "")
		fwHost := argString(args, "forward_host", "")
		fwPort, _ := argInt(args, "forward_port")
		if domain == "" || fwHost == "" || fwPort == 0 {
			return "", fmt.Errorf("domain, forward_host and forward_port are required")
		}
		ssl := argBool(args, "ssl")
		scheme := argString(args, "forward_scheme", "http")
		alID := 0
		if v, ok := argInt(args, "access_list_id"); ok {
			alID = v
		}
		advanced := argString(args, "advanced_config", "")
		return npmCreateProxyHost(ctx, cfg, token, domain, fwHost, fwPort, scheme, ssl, alID, advanced)

	case "npm_delete_proxy_host":
		id, ok := argInt(args, "id")
		if !ok {
			return "", fmt.Errorf("id is required")
		}
		return npmDeleteProxyHost(ctx, cfg, token, id)

	case "npm_enable_proxy_host":
		id, ok := argInt(args, "id")
		if !ok {
			return "", fmt.Errorf("id is required")
		}
		return npmToggleProxyHost(ctx, cfg, token, id, true)

	case "npm_disable_proxy_host":
		id, ok := argInt(args, "id")
		if !ok {
			return "", fmt.Errorf("id is required")
		}
		return npmToggleProxyHost(ctx, cfg, token, id, false)

	case "npm_list_certificates":
		return npmListCerts(ctx, cfg, token)

	case "npm_list_access_lists":
		return npmListAccessLists(ctx, cfg, token)

	default:
		return "", fmt.Errorf("unknown action %q", action)
	}
}

// ---- NPM API helpers ----

func npmToken(ctx context.Context, cfg map[string]string) (string, error) {
	var resp struct {
		Token string `json:"token"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	body := map[string]string{"identity": cfg["email"], "secret": cfg["password"]}
	if err := npmDo(ctx, cfg, http.MethodPost, "/api/tokens", "", body, &resp); err != nil {
		return "", err
	}
	if resp.Error != nil {
		return "", fmt.Errorf("npm login: %s", resp.Error.Message)
	}
	if resp.Token == "" {
		return "", fmt.Errorf("npm login: empty token")
	}
	return resp.Token, nil
}

type npmProxyHost struct {
	ID             int      `json:"id"`
	DomainNames    []string `json:"domain_names"`
	ForwardHost    string   `json:"forward_host"`
	ForwardPort    int      `json:"forward_port"`
	ForwardScheme  string   `json:"forward_scheme"`
	Enabled        bool     `json:"enabled"`
	SSLForced      bool     `json:"ssl_forced"`
	CertificateID  any      `json:"certificate_id"`
	AccessListID   int      `json:"access_list_id"`
	AdvancedConfig string   `json:"advanced_config"`
	HTTP2Support   bool     `json:"http2_support"`
	BlockExploits  bool     `json:"block_exploits"`
}

func npmListProxyHosts(ctx context.Context, cfg map[string]string, token string) (string, error) {
	var hosts []npmProxyHost
	if err := npmDo(ctx, cfg, http.MethodGet, "/api/nginx/proxy-hosts", token, nil, &hosts); err != nil {
		return "", err
	}
	if len(hosts) == 0 {
		return "No proxy hosts configured.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Proxy hosts (%d):\n", len(hosts))
	for _, h := range hosts {
		status := "enabled"
		if !h.Enabled {
			status = "disabled"
		}
		ssl := ""
		if h.SSLForced {
			ssl = " [SSL]"
		}
		fmt.Fprintf(&b, "  id=%-3d %-30s → %s:%d (%s)%s\n",
			h.ID, strings.Join(h.DomainNames, ", "), h.ForwardHost, h.ForwardPort, status, ssl)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func npmCreateProxyHost(ctx context.Context, cfg map[string]string, token, domain, fwHost string, fwPort int, scheme string, ssl bool, alID int, advanced string) (string, error) {
	if scheme == "" {
		scheme = "http"
	}
	var certID any = 0
	sslForced := false
	meta := map[string]any{} // NPM's schema rejects a null meta
	if ssl {
		sslForced = true
		certID = "new" // NPM requests a Let's Encrypt cert (HTTP-01) for the domain
		meta = map[string]any{
			"letsencrypt_email": cfg["email"],
			"letsencrypt_agree": true,
			"dns_challenge":     false,
		}
	}
	payload := map[string]any{
		"domain_names":           []string{domain},
		"forward_scheme":         scheme,
		"forward_host":           fwHost,
		"forward_port":           fwPort,
		"access_list_id":         alID,
		"certificate_id":         certID,
		"ssl_forced":             sslForced,
		"http2_support":          true,
		"block_exploits":         true,
		"allow_websocket_upgrade": true,
		"advanced_config":        advanced,
		"hsts_enabled":           sslForced,
		"hsts_subdomains":        false,
		"meta":                   meta,
		"locations":              []any{},
	}
	var result npmProxyHost
	if err := npmDo(ctx, cfg, http.MethodPost, "/api/nginx/proxy-hosts", token, payload, &result); err != nil {
		return "", err
	}
	sslNote := ""
	if ssl {
		sslNote = " (Let's Encrypt certificate requested)"
		if id, _ := result.CertificateID.(float64); id == 0 {
			sslNote = " (WARNING: no certificate attached — check the NPM logs)"
		}
	}
	return fmt.Sprintf("Created proxy host id=%d: %s → %s:%d%s.", result.ID, domain, fwHost, fwPort, sslNote), nil
}

func npmDeleteProxyHost(ctx context.Context, cfg map[string]string, token string, id int) (string, error) {
	var ok bool
	if err := npmDo(ctx, cfg, http.MethodDelete, fmt.Sprintf("/api/nginx/proxy-hosts/%d", id), token, nil, &ok); err != nil {
		return "", err
	}
	return fmt.Sprintf("Deleted proxy host id=%d.", id), nil
}

func npmToggleProxyHost(ctx context.Context, cfg map[string]string, token string, id int, enable bool) (string, error) {
	action := "disable"
	if enable {
		action = "enable"
	}
	var result any
	if err := npmDo(ctx, cfg, http.MethodPost, fmt.Sprintf("/api/nginx/proxy-hosts/%d/%s", id, action), token, nil, &result); err != nil {
		return "", err
	}
	verb := "Disabled"
	if enable {
		verb = "Enabled"
	}
	return fmt.Sprintf("%s proxy host id=%d.", verb, id), nil
}

type npmCert struct {
	ID          int      `json:"id"`
	NiceName    string   `json:"nice_name"`
	Provider    string   `json:"provider"`
	DomainNames []string `json:"domain_names"`
	ExpiresOn   string   `json:"expires_on"`
}

func npmListCerts(ctx context.Context, cfg map[string]string, token string) (string, error) {
	var certs []npmCert
	if err := npmDo(ctx, cfg, http.MethodGet, "/api/nginx/certificates", token, nil, &certs); err != nil {
		return "", err
	}
	if len(certs) == 0 {
		return "No certificates stored.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Certificates (%d):\n", len(certs))
	for _, c := range certs {
		fmt.Fprintf(&b, "  id=%-3d %-30s provider=%-12s expires=%s domains=%s\n",
			c.ID, c.NiceName, c.Provider, c.ExpiresOn, strings.Join(c.DomainNames, ", "))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

type npmAccessList struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func npmListAccessLists(ctx context.Context, cfg map[string]string, token string) (string, error) {
	var lists []npmAccessList
	if err := npmDo(ctx, cfg, http.MethodGet, "/api/nginx/access-lists", token, nil, &lists); err != nil {
		return "", err
	}
	if len(lists) == 0 {
		return "No access lists defined.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Access lists (%d):\n", len(lists))
	for _, l := range lists {
		fmt.Fprintf(&b, "  id=%-3d %s\n", l.ID, l.Name)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func npmDo(ctx context.Context, cfg map[string]string, method, path, token string, in any, out any) error {
	base := strings.TrimRight(cfg["base_url"], "/")
	var bodyReader io.Reader = http.NoBody
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, bodyReader)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient(false).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("npm %s %s: %s — %s", method, path, resp.Status, strings.TrimSpace(string(body)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
