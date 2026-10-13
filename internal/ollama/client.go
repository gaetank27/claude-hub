// Package ollama is a thin client over the local Ollama HTTP API for the
// supervision widget: health, installed models, loaded (in-memory) models, and
// unloading a model to free RAM.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Client talks to a local Ollama server.
type Client struct {
	host string
	http *http.Client
}

func New(host string) *Client {
	return &Client{host: host, http: &http.Client{Timeout: 8 * time.Second}}
}

// Host is the configured Ollama base URL (shown in the UI).
func (c *Client) Host() string { return c.host }

// Model is an installed model (from /api/tags).
type Model struct {
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modifiedAt"`
	Family     string    `json:"family,omitempty"`
	Params     string    `json:"params,omitempty"`
}

// Running is a model currently loaded in memory (from /api/ps).
type Running struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`     // total resident
	SizeVRAM  int64     `json:"sizeVram"` // GPU portion (0 on CPU-only)
	ExpiresAt time.Time `json:"expiresAt"`
}

// Version returns the server version and whether it is reachable.
func (c *Client) Version(ctx context.Context) (string, error) {
	var v struct {
		Version string `json:"version"`
	}
	if err := c.get(ctx, "/api/version", &v); err != nil {
		return "", err
	}
	return v.Version, nil
}

// Models lists the installed models (/api/tags).
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	var resp struct {
		Models []struct {
			Name       string    `json:"name"`
			Size       int64     `json:"size"`
			ModifiedAt time.Time `json:"modified_at"`
			Details    struct {
				Family        string `json:"family"`
				ParameterSize string `json:"parameter_size"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := c.get(ctx, "/api/tags", &resp); err != nil {
		return nil, err
	}
	out := make([]Model, 0, len(resp.Models))
	for _, m := range resp.Models {
		out = append(out, Model{
			Name: m.Name, Size: m.Size, ModifiedAt: m.ModifiedAt,
			Family: m.Details.Family, Params: m.Details.ParameterSize,
		})
	}
	return out, nil
}

// Running lists the models currently loaded in memory (/api/ps).
func (c *Client) Running(ctx context.Context) ([]Running, error) {
	var resp struct {
		Models []struct {
			Name      string    `json:"name"`
			Size      int64     `json:"size"`
			SizeVRAM  int64     `json:"size_vram"`
			ExpiresAt time.Time `json:"expires_at"`
		} `json:"models"`
	}
	if err := c.get(ctx, "/api/ps", &resp); err != nil {
		return nil, err
	}
	out := make([]Running, 0, len(resp.Models))
	for _, m := range resp.Models {
		out = append(out, Running{Name: m.Name, Size: m.Size, SizeVRAM: m.SizeVRAM, ExpiresAt: m.ExpiresAt})
	}
	return out, nil
}

// Unload evicts a model from memory by requesting keep_alive=0. Ollama has no
// dedicated unload endpoint: generation models accept it on /api/generate, while
// embedding-only models (e.g. bge-m3) reject generate, so we fall back to
// /api/embed. Either way the model is unloaded.
func (c *Client) Unload(ctx context.Context, model string) error {
	if model == "" {
		return fmt.Errorf("model is required")
	}
	genErr := c.post(ctx, "/api/generate", map[string]any{"model": model, "keep_alive": 0})
	if genErr == nil {
		return nil
	}
	if embErr := c.post(ctx, "/api/embed", map[string]any{"model": model, "input": " ", "keep_alive": 0}); embErr == nil {
		return nil
	}
	return genErr
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.host+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) post(ctx context.Context, path string, body any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.host+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ollama %s: %s", path, resp.Status)
	}
	return nil
}
