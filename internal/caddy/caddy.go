// Package caddy wraps the Caddy v2 admin API (http://localhost:2019) so the
// Hub can manage domain routes and static TLS certificates without root.
package caddy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultAdmin = "http://localhost:2019"

// Client talks to the Caddy admin API.
type Client struct{ adminURL string }

// New returns a Client pointing at the given admin URL (e.g. "http://localhost:2019").
// Pass "" to use the default.
func New(adminURL string) *Client {
	if adminURL == "" {
		adminURL = defaultAdmin
	}
	return &Client{adminURL: strings.TrimRight(adminURL, "/")}
}

// GetConfig returns the live Caddy config as a raw JSON map.
func (c *Client) GetConfig(ctx context.Context) (map[string]any, error) {
	var cfg map[string]any
	if err := c.do(ctx, http.MethodGet, "/config/", nil, &cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// AddDomain inserts a reverse-proxy route for domain into srv0 (the existing
// HTTPS server) and registers a static TLS certificate loaded from certFile/keyFile.
// If the domain already exists it is replaced (idempotent).
func (c *Client) AddDomain(ctx context.Context, domain, upstreamDial, certFile, keyFile string) error {
	cfg, err := c.GetConfig(ctx)
	if err != nil {
		return fmt.Errorf("get caddy config: %w", err)
	}

	// ---- HTTP route ----
	servers := deepGet[map[string]any](cfg, "apps", "http", "servers")
	if servers == nil {
		return fmt.Errorf("caddy config: apps.http.servers not found")
	}
	srv0, ok := servers["srv0"].(map[string]any)
	if !ok {
		return fmt.Errorf("caddy config: srv0 not found")
	}

	// Remove any existing route for this domain first.
	if routes, ok := srv0["routes"].([]any); ok {
		filtered := make([]any, 0, len(routes))
		for _, r := range routes {
			if !routeMatchesDomain(r, domain) {
				filtered = append(filtered, r)
			}
		}
		srv0["routes"] = append(filtered, buildRoute(domain, upstreamDial))
	} else {
		srv0["routes"] = []any{buildRoute(domain, upstreamDial)}
	}

	// ---- TLS connection policy (SNI) ----
	tag := "hub:" + domain
	policies := toSlice(srv0["tls_connection_policies"])
	// Remove old policy for this domain.
	filtered := make([]any, 0, len(policies))
	for _, p := range policies {
		pm, ok := p.(map[string]any)
		if !ok {
			filtered = append(filtered, p)
			continue
		}
		if matchSNI, ok := pm["match"].(map[string]any); ok {
			if snis, ok := matchSNI["sni"].([]any); ok {
				for _, s := range snis {
					if s == domain {
						goto skipPolicy
					}
				}
			}
		}
		filtered = append(filtered, p)
	skipPolicy:
	}
	// Prepend domain-specific policy (must come before catch-all).
	domPolicy := map[string]any{
		"match": map[string]any{"sni": []any{domain}},
		"certificate_selection": map[string]any{
			"any_tag": []any{tag},
		},
	}
	srv0["tls_connection_policies"] = append([]any{domPolicy}, filtered...)

	// ---- Static cert (load_files) ----
	tls := deepGetOrCreate(cfg, "apps", "tls")
	certs := deepGetOrCreate(tls, "certificates")
	fileList := toSlice(certs["load_files"])
	// Remove any existing entry for this domain's tag.
	filteredFiles := make([]any, 0, len(fileList))
	for _, f := range fileList {
		if fm, ok := f.(map[string]any); ok {
			if tagsHas(fm["tags"], tag) {
				continue
			}
		}
		filteredFiles = append(filteredFiles, f)
	}
	certs["load_files"] = append(filteredFiles, map[string]any{
		"certificate": certFile,
		"key":         keyFile,
		"tags":        []any{tag},
	})

	// ---- Push full config ----
	return c.loadConfig(ctx, cfg)
}

// RemoveDomain removes the route and TLS cert for domain from the live config.
func (c *Client) RemoveDomain(ctx context.Context, domain string) error {
	cfg, err := c.GetConfig(ctx)
	if err != nil {
		return fmt.Errorf("get caddy config: %w", err)
	}
	tag := "hub:" + domain

	if srv0, ok := deepGet[map[string]any](cfg, "apps", "http", "servers")["srv0"].(map[string]any); ok {
		if routes, ok := srv0["routes"].([]any); ok {
			out := make([]any, 0, len(routes))
			for _, r := range routes {
				if !routeMatchesDomain(r, domain) {
					out = append(out, r)
				}
			}
			srv0["routes"] = out
		}
		if policies, ok := srv0["tls_connection_policies"].([]any); ok {
			out := make([]any, 0, len(policies))
			for _, p := range policies {
				pm, ok := p.(map[string]any)
				if !ok {
					out = append(out, p)
					continue
				}
				if m, ok := pm["match"].(map[string]any); ok {
					if snisHas(m["sni"], domain) {
						continue
					}
				}
				out = append(out, p)
			}
			srv0["tls_connection_policies"] = out
		}
	}

	if tls := deepGet[map[string]any](cfg, "apps", "tls"); tls != nil {
		if certs, ok := tls["certificates"].(map[string]any); ok {
			if fileList, ok := certs["load_files"].([]any); ok {
				out := make([]any, 0, len(fileList))
				for _, f := range fileList {
					if fm, ok := f.(map[string]any); ok && tagsHas(fm["tags"], tag) {
						continue
					}
					out = append(out, f)
				}
				certs["load_files"] = out
			}
		}
	}

	return c.loadConfig(ctx, cfg)
}

// loadConfig posts the full config to /load, atomically replacing the live config.
func (c *Client) loadConfig(ctx context.Context, cfg map[string]any) error {
	return c.do(ctx, http.MethodPost, "/load", cfg, nil)
}

// ---- low-level ----

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader = http.NoBody
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.adminURL+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	cl := &http.Client{Timeout: 15 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("caddy admin %s %s: %s — %s", method, path, resp.Status, strings.TrimSpace(string(b)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// ---- config builder helpers ----

func buildRoute(domain, upstreamDial string) map[string]any {
	return map[string]any{
		"match": []any{map[string]any{"host": []any{domain}}},
		"handle": []any{map[string]any{
			"handler": "subroute",
			"routes": []any{map[string]any{
				"handle": []any{
					map[string]any{
						"handler":  "encode",
						"encodings": map[string]any{"gzip": map[string]any{}, "zstd": map[string]any{}},
						"prefer":   []any{"zstd", "gzip"},
					},
					map[string]any{
						"handler":   "reverse_proxy",
						"upstreams": []any{map[string]any{"dial": upstreamDial}},
					},
				},
			}},
		}},
		"terminal": true,
	}
}

func routeMatchesDomain(r any, domain string) bool {
	rm, ok := r.(map[string]any)
	if !ok {
		return false
	}
	matches, _ := rm["match"].([]any)
	for _, m := range matches {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if snisHas(mm["host"], domain) {
			return true
		}
	}
	return false
}

func snisHas(v any, target string) bool {
	list, _ := v.([]any)
	for _, s := range list {
		if s == target {
			return true
		}
	}
	return false
}

func tagsHas(v any, tag string) bool { return snisHas(v, tag) }

func toSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

// deepGet traverses a nested map[string]any by the given keys, returning the
// value at the leaf. Returns nil if any key is missing or the value is not a map.
func deepGet[T any](m map[string]any, keys ...string) T {
	var zero T
	cur := any(m)
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return zero
		}
		cur = mm[k]
	}
	if v, ok := cur.(T); ok {
		return v
	}
	return zero
}

// deepGetOrCreate traverses/creates nested map[string]any nodes.
func deepGetOrCreate(m map[string]any, keys ...string) map[string]any {
	cur := m
	for _, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[k] = next
		}
		cur = next
	}
	return cur
}
