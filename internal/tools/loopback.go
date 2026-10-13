package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"system/internal/secretbox"
	"system/internal/store"
)

// toolStore is the slice of the store the loopback needs (kept minimal so tests
// could fake it).
type toolStore interface {
	ListTools() ([]store.Tool, error)
	GetProjectAccess(projectID string) (store.ProjectAccess, error)
}

// allowedFor returns the tool filter for the calling project (X-Hub-Project
// header, set by tools-mcp). No header or no policy => every tool is allowed.
func allowedFor(db toolStore, r *http.Request) func(store.Tool) bool {
	pid := r.Header.Get("X-Hub-Project")
	if pid == "" {
		return func(store.Tool) bool { return true }
	}
	acc, err := db.GetProjectAccess(pid)
	if err != nil {
		return func(store.Tool) bool { return true }
	}
	return func(t store.Tool) bool { return acc.ToolAllowed(t.ID) }
}

// RegisterLoopback mounts the tools action endpoints onto mux, sharing the same
// localhost listener and token as the RAG loopback. The tools-mcp server enumerates
// actions via GET /tools/list and invokes them via POST /tools/call. Only enabled
// tools are exposed; the action name encodes the connector (e.g. proxmox_list_vms),
// resolved to the first enabled configured tool of that connector type.
func RegisterLoopback(mux *http.ServeMux, db toolStore, box *secretbox.Box, token string) {
	gate := func(w http.ResponseWriter, r *http.Request) bool {
		if token != "" && r.Header.Get("X-Rag-Token") != token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return false
		}
		return true
	}

	mux.HandleFunc("GET /tools/list", func(w http.ResponseWriter, r *http.Request) {
		if !gate(w, r) {
			return
		}
		allowed := allowedFor(db, r)
		specs := []ToolSpec{}
		seen := map[string]bool{}
		tls, _ := db.ListTools()
		for _, t := range tls {
			if !t.Enabled || !allowed(t) {
				continue
			}
			conn, ok := Get(t.Type)
			if !ok {
				continue
			}
			for _, sp := range conn.Tools() {
				if seen[sp.Name] {
					continue
				}
				seen[sp.Name] = true
				specs = append(specs, sp)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"tools": specs})
	})

	mux.HandleFunc("POST /tools/call", func(w http.ResponseWriter, r *http.Request) {
		if !gate(w, r) {
			return
		}
		var req struct {
			Action string         `json:"action"`
			Args   map[string]any `json:"args"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		conn, tool, ok := resolveAction(db, req.Action, allowedFor(db, r))
		if !ok {
			writeCall(w, "", "no enabled tool provides action "+req.Action)
			return
		}
		cfg, err := decryptConfig(box, tool.ConfigEnc)
		if err != nil {
			writeCall(w, "", "decrypt config: "+err.Error())
			return
		}
		cfg["_tool_id"] = tool.ID // stateful connectors (e.g. WhatsApp) use this
		out, err := conn.Call(r.Context(), cfg, req.Action, req.Args)
		if err != nil {
			writeCall(w, "", err.Error())
			return
		}
		writeCall(w, out, "")
	})
}

// ListSpecs returns the actions of every enabled tool, de-duplicated by name. It
// is the in-process equivalent of GET /tools/list, for callers (the remote MCP
// endpoint) that apply their own allow-list instead of a project policy.
func ListSpecs(db toolStore) []ToolSpec {
	specs := []ToolSpec{}
	seen := map[string]bool{}
	tls, _ := db.ListTools()
	for _, t := range tls {
		if !t.Enabled {
			continue
		}
		conn, ok := Get(t.Type)
		if !ok {
			continue
		}
		for _, sp := range conn.Tools() {
			if !seen[sp.Name] {
				seen[sp.Name] = true
				specs = append(specs, sp)
			}
		}
	}
	return specs
}

// CallAction runs one action on the first enabled tool providing it (in-process
// equivalent of POST /tools/call).
func CallAction(ctx context.Context, db toolStore, box *secretbox.Box, action string, args map[string]any) (string, error) {
	conn, tool, ok := resolveAction(db, action, func(store.Tool) bool { return true })
	if !ok {
		return "", fmt.Errorf("no enabled tool provides action %s", action)
	}
	cfg, err := decryptConfig(box, tool.ConfigEnc)
	if err != nil {
		return "", fmt.Errorf("decrypt config: %w", err)
	}
	cfg["_tool_id"] = tool.ID
	return conn.Call(ctx, cfg, action, args)
}

// resolveAction finds the first enabled tool whose connector exposes action.
func resolveAction(db toolStore, action string, allowed func(store.Tool) bool) (Connector, store.Tool, bool) {
	tls, _ := db.ListTools()
	for _, t := range tls {
		if !t.Enabled || !allowed(t) {
			continue
		}
		conn, ok := Get(t.Type)
		if !ok {
			continue
		}
		for _, sp := range conn.Tools() {
			if sp.Name == action {
				return conn, t, true
			}
		}
	}
	return nil, store.Tool{}, false
}

func decryptConfig(box *secretbox.Box, enc string) (map[string]string, error) {
	cfg := map[string]string{}
	if enc == "" {
		return cfg, nil
	}
	b, err := box.Decrypt(enc)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(b, &cfg)
	return cfg, nil
}

func writeCall(w http.ResponseWriter, result, errMsg string) {
	w.Header().Set("Content-Type", "application/json")
	if errMsg != "" {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": errMsg})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}
