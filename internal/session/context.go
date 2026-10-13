package session

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"system/internal/docker"
	"system/internal/store"
	"system/internal/tools"
)

// ContextResource is one infrastructure capability advertised to a project's
// sessions (and surfaced in the UI as a "what the session can see" badge).
type ContextResource struct {
	Key       string `json:"key"`       // stable id: "sandbox" | connector type | "s3"
	Label     string `json:"label"`     // human-facing label
	Available bool   `json:"available"` // wired/visible for this project's sessions
	Future    bool   `json:"future"`    // advertised on the roadmap, not yet active
}

// ProjectContext is the generated awareness payload for a session: a structured
// resource list (for the UI) and the rendered system-prompt text (injected via
// the runner's --append-system-prompt).
type ProjectContext struct {
	Resources []ContextResource `json:"resources"`
	Prompt    string            `json:"-"`
}

// BuildProjectContext computes, for a project, which shared Hub resources its
// sessions can use, and renders the system-prompt text that tells the model about
// them. It is the single source of truth for both the injection (Prompt) and the
// dashboard indicator (Resources), so the badge never drifts from what the model
// is actually told. The tool catalogue is derived from the enabled connectors in
// the store — the same set the tools-mcp loopback exposes — so it reflects the
// live configuration at launch time.
func (m *Manager) BuildProjectContext(proj store.Project) ProjectContext {
	resources := []ContextResource{
		// The Sandbox/Live container infrastructure plus the in-process /x/ reverse
		// proxy are platform capabilities, always available to a project.
		{Key: "sandbox", Label: "Sandbox", Available: true},
	}

	// Tool catalogue: one resource per distinct enabled connector type, in the
	// connectors' canonical order, deduped (a project may have several tools of the
	// same type configured). conn.Label()/conn.Tools() keep the advertised names
	// authoritative — no second copy of the action names to drift.
	seen := map[string]bool{}
	type enabledConn struct {
		typ     string
		label   string
		actions []string
	}
	var enabled []enabledConn
	// Honour the project's access policy: a project restricted to a tool subset
	// must only be told about (and badge) those tools, matching what the
	// tools-mcp loopback actually exposes to it.
	access, _ := m.db.GetProjectAccess(proj.ID)
	if tls, err := m.db.ListTools(); err == nil {
		for _, t := range tls {
			if !t.Enabled || !access.ToolAllowed(t.ID) || seen[t.Type] {
				continue
			}
			conn, ok := tools.Get(t.Type)
			if !ok {
				continue
			}
			seen[t.Type] = true
			actions := make([]string, 0, len(conn.Tools()))
			for _, sp := range conn.Tools() {
				actions = append(actions, sp.Name)
			}
			enabled = append(enabled, enabledConn{typ: t.Type, label: conn.Label(), actions: actions})
			resources = append(resources, ContextResource{Key: t.Type, Label: conn.Label(), Available: true})
		}
	}

	var b strings.Builder
	b.WriteString("# Claude Hub — shared infrastructure\n\n")
	b.WriteString("You are running inside a Claude Hub sub-project. The host already provides shared, " +
		"managed infrastructure. Do NOT reinstall or duplicate system services (no local Nginx, no " +
		"separate reverse proxy, no extra message/VM/mail daemon) — discover and reuse the resources below.\n\n")

	// Web exposure: the real mechanism is Docker labels + the in-process /x/ proxy,
	// NOT a per-app Nginx config.
	base := strings.TrimRight(m.GetPublicBaseURL(), "/")
	b.WriteString("## Exposing a web app (Sandbox)\n")
	b.WriteString("A global reverse proxy already routes project apps — do NOT install Nginx or your own " +
		"proxy. To make a test app reachable on the web/mobile, run it in a Docker container labelled so the " +
		"Hub proxy picks it up:\n\n")
	b.WriteString(fmt.Sprintf("- `%s=%s`\n", docker.LabelProject, proj.Slug))
	b.WriteString(fmt.Sprintf("- `%s=sandbox` (or `live`)\n", docker.LabelEnv))
	b.WriteString(fmt.Sprintf("- `%s=<your app's container port>`\n", docker.LabelWebPort))
	b.WriteString("- attach the container to the `claudehub_sandbox` network (or `claudehub_live`).\n\n")
	b.WriteString(fmt.Sprintf("The Hub sets these labels automatically when you create the container through its "+
		"container API (POST /api/docker/containers). The app is then reachable at `%s/x/<container-name>/` — "+
		"no DNS, TLS or Nginx changes needed.\n\n", base))

	// Tool catalogue: the MCP actions are already wired into this session.
	b.WriteString("## Available tools (already wired as MCP actions)\n")
	if len(enabled) == 0 {
		b.WriteString("No external connector is enabled for this Hub right now.\n\n")
	} else {
		b.WriteString("These connectors are configured in the Hub and their actions are ALREADY available to you " +
			"as MCP tools — prefer them over rebuilding your own client:\n\n")
		for _, e := range enabled {
			if len(e.actions) > 0 {
				b.WriteString(fmt.Sprintf("- **%s**: %s\n", e.label, strings.Join(e.actions, ", ")))
			} else {
				b.WriteString(fmt.Sprintf("- **%s**\n", e.label))
			}
		}
		b.WriteString("\n")
	}

	return ProjectContext{Resources: resources, Prompt: b.String()}
}

// writeContextFile renders the project's infrastructure context and writes it to
// <DataDir>/context/<sessionID>.md (one file per session, like the MCP config).
// Returns the path, or "" if it could not be written — the caller then launches
// the session without the context (best-effort, never fatal).
func (m *Manager) writeContextFile(proj store.Project, sessionID string) string {
	dir := filepath.Join(m.cfg.DataDir, "context")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("context: mkdir for session %s: %v", sessionID, err)
		return ""
	}
	path := filepath.Join(dir, sessionID+".md")
	if err := os.WriteFile(path, []byte(m.BuildProjectContext(proj).Prompt), 0o644); err != nil {
		log.Printf("context: write for session %s: %v", sessionID, err)
		return ""
	}
	return path
}
