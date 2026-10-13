// Package mcp writes the claude --mcp-config file the hub injects into every
// session. It composes several stdio MCP servers (RAG knowledge_search, external
// Tools, …) into the single file the CLI expects.
package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Server is one stdio MCP server entry: a command the CLI launches, with env.
type Server struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
}

// Write serializes the servers into the claude --mcp-config JSON at path (0600).
func Write(path string, servers map[string]Server) error {
	for name, s := range servers {
		if s.Args == nil {
			s.Args = []string{}
			servers[name] = s
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(map[string]any{"mcpServers": servers}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
