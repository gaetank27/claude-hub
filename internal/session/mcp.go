package session

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"system/internal/mcp"
)

// mcpConfigForSession composes the claude --mcp-config file for a session of the
// given project. It always wires the global RAG (knowledge_search) and external
// Tools servers, and — when the project has a private RAG collection — injects
// PROJECT_RAG_COLLECTION into the rag-mcp env so the session also gets a
// project_code_search tool scoped to that project's own code index (strict
// isolation: a session can only search its own project). It also wires the
// blocking ask_user server, keyed to this session via ASK_SESSION_ID, so the
// model can ask the user a question that genuinely waits for an answer.
//
// One file per session is written under <DataDir>/rag (sessions of the same
// project must not share a file: ASK_SESSION_ID differs). Returns the config path
// ("" when no server can be wired) and whether the ask server was wired (which
// gates the runner's AskUserQuestion redirect).
func (m *Manager) mcpConfigForSession(projectID, sessionID string) (string, bool) {
	base := "http://" + m.cfg.RagAddr
	servers := map[string]mcp.Server{}

	if fileExists(m.cfg.RagMCPBin) {
		env := map[string]string{
			"RAG_SEARCH_URL":   base + "/search",
			"RAG_SEARCH_TOKEN": m.cfg.RagToken,
		}
		if pr, err := m.db.GetProjectRag(projectID); err == nil && pr.CollectionID != "" {
			env["PROJECT_RAG_COLLECTION"] = pr.CollectionID
		}
		// Other projects this one may search (project_code_search `project` arg).
		if acc, err := m.db.GetProjectAccess(projectID); err == nil && len(acc.AllowedProjects) > 0 {
			extra := map[string]string{}
			for _, id := range acc.AllowedProjects {
				p, perr := m.db.GetProject(id)
				pr, rerr := m.db.GetProjectRag(id)
				if perr == nil && rerr == nil && pr.CollectionID != "" {
					extra[p.Slug] = pr.CollectionID
				}
			}
			if b, err := json.Marshal(extra); err == nil && len(extra) > 0 {
				env["PROJECT_RAG_EXTRA"] = string(b)
			}
		}
		servers["knowledge"] = mcp.Server{Command: m.cfg.RagMCPBin, Env: env}
	}
	if fileExists(m.cfg.ToolsMCPBin) {
		servers["tools"] = mcp.Server{
			Command: m.cfg.ToolsMCPBin,
			Env: map[string]string{
				"TOOLS_BASE_URL":   base,
				"TOOLS_TOKEN":      m.cfg.RagToken,
				"TOOLS_PROJECT_ID": projectID,
			},
		}
	}
	askWired := false
	if fileExists(m.cfg.AskMCPBin) {
		servers["ask"] = mcp.Server{
			Command: m.cfg.AskMCPBin,
			Env: map[string]string{
				"ASK_BASE_URL":   base,
				"ASK_TOKEN":      m.cfg.RagToken,
				"ASK_SESSION_ID": sessionID,
			},
		}
		askWired = true
	}
	if len(servers) == 0 {
		return "", false
	}
	path := filepath.Join(m.cfg.DataDir, "rag", "mcp-"+sessionID+".json")
	if err := mcp.Write(path, servers); err != nil {
		log.Printf("mcp: write config for session %s: %v", sessionID, err)
		return "", false
	}
	return path, askWired
}

func fileExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}
