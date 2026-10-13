// Package tools is the connector framework for external integrations (Proxmox,
// Telegram, Email, …). Each connector declares the configuration fields it needs
// (for the dynamic UI form), a Test to validate credentials, and a set of actions
// exposed to Claude Code sessions through the tools-mcp server. Connectors register
// themselves in init(), so adding one is a single new file.
package tools

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// FieldType drives how the UI renders a config field.
const (
	FieldText     = "text"
	FieldPassword = "password" // secret: write-only, never returned to the client
	FieldURL      = "url"
	FieldNumber   = "number"
	FieldBool     = "bool"
)

// Field describes one configuration parameter of a connector.
type Field struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Type        string `json:"type"`
	Secret      bool   `json:"secret"`
	Required    bool   `json:"required"`
	Placeholder string `json:"placeholder,omitempty"`
}

// ArgSpec is one argument of a connector action (an MCP tool input).
type ArgSpec struct {
	Name        string `json:"name"`
	Type        string `json:"type"`            // string|integer|number|boolean|array
	Items       string `json:"items,omitempty"` // element type when Type == "array"
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// ToolSpec describes an action a connector exposes to sessions via MCP.
type ToolSpec struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Args        []ArgSpec `json:"args"`
}

// Connector is one integration type. Implementations are stateless: the caller
// passes the decrypted config map to every method.
type Connector interface {
	Type() string
	Label() string
	Fields() []Field
	Test(ctx context.Context, cfg map[string]string) error
	Tools() []ToolSpec
	Call(ctx context.Context, cfg map[string]string, action string, args map[string]any) (string, error)
}

// ChatDetector is an optional capability: a connector that can discover the chat
// to talk to from incoming traffic, for an assisted setup flow (the user need not
// look up an id by hand). Detected via type assertion in the API layer, so it does
// not bloat the base Connector interface (cf. Dashboarder).
type ChatDetector interface {
	// DetectChat polls for inbound chats newer than offset (0 = drain the backlog
	// and only report the next cursor). It is stateless: the caller carries the
	// returned NextOffset into the following poll.
	DetectChat(ctx context.Context, cfg map[string]string, offset int64) (DetectResult, error)
}

// DetectResult is one polling round of assisted chat detection.
type DetectResult struct {
	Chats      []DetectedChat `json:"chats"`
	NextOffset int64          `json:"nextOffset"`
}

// DetectedChat is one chat that messaged the connector during detection.
type DetectedChat struct {
	ChatID int64  `json:"chatId"`
	Title  string `json:"title"` // chat title, or the sender's display name
	Text   string `json:"text"`  // last message text, as a preview
}

// Dashboarder is an optional capability: a connector that exposes a live
// supervision snapshot (resource usage, inventory, …) for a tile dashboard.
// Detected via type assertion in the API layer (cf. ChatDetector).
type Dashboarder interface {
	// Dashboard returns a JSON-marshalable snapshot of the connector's state.
	Dashboard(ctx context.Context, cfg map[string]string) (any, error)
}

// MessageTester is an optional capability: a connector that can send a canned
// test message to confirm end-to-end delivery to the configured target.
type MessageTester interface {
	// TestMessage sends the test message and returns a human-readable outcome.
	TestMessage(ctx context.Context, cfg map[string]string) (string, error)
}

var registry = map[string]Connector{}

func register(c Connector) { registry[c.Type()] = c }

// Get returns the connector for a type.
func Get(t string) (Connector, bool) {
	c, ok := registry[t]
	return c, ok
}

// All returns the registered connectors, ordered by type for a stable catalog.
func All() []Connector {
	out := make([]Connector, 0, len(registry))
	for _, c := range registry {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type() < out[j].Type() })
	return out
}

// --- shared helpers for connector implementations ---

// httpClient returns a short-timeout client, optionally skipping TLS verification
// (homelab servers like Proxmox commonly use self-signed certs).
func httpClient(insecureTLS bool) *http.Client {
	c := &http.Client{Timeout: 15 * time.Second}
	if insecureTLS {
		c.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	return c
}

// missing reports the first required field that is empty, for a clear error.
func missing(cfg map[string]string, keys ...string) error {
	for _, k := range keys {
		if cfg[k] == "" {
			return fmt.Errorf("missing required field %q", k)
		}
	}
	return nil
}

// argString / argInt read MCP-call arguments (JSON numbers arrive as float64).
func argString(args map[string]any, key, def string) string {
	if v, ok := args[key].(string); ok && v != "" {
		return v
	}
	return def
}

// argStrings reads a list-of-strings argument. MCP array arguments arrive as
// []any; a lone string is tolerated as a one-element list. Empty entries are
// dropped.
func argStrings(args map[string]any, key string) []string {
	switch v := args[key].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	case string:
		if strings.TrimSpace(v) != "" {
			return []string{v}
		}
	}
	return nil
}

// argBool reads a boolean argument. MCP booleans arrive as Go bool; a string
// "true"/"1" is tolerated for clients that stringify arguments.
func argBool(args map[string]any, key string) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1"
	}
	return false
}

func argInt(args map[string]any, key string) (int, bool) {
	switch v := args[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n, true
		}
	}
	return 0, false
}
