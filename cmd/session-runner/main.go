// Command session-runner owns a single claude process for one session. It is
// launched as a transient systemd user unit by the backend and runs decoupled
// from it: it journals the stream-json output and serves it over a Unix socket.
package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"syscall"

	"system/internal/runner"
)

func main() {
	var cfg runner.Config
	flag.StringVar(&cfg.SessionID, "session-id", "", "session UUID (also claude --session-id)")
	flag.StringVar(&cfg.ProjectDir, "project-dir", "", "claude working directory")
	flag.StringVar(&cfg.JournalPath, "journal", "", "append-only journal path")
	flag.StringVar(&cfg.SocketPath, "socket", "", "Unix socket path to serve")
	flag.StringVar(&cfg.PermissionMode, "permission-mode", "default", "claude permission mode")
	flag.StringVar(&cfg.Model, "model", "", "claude model alias/id (empty = CLI default)")
	flag.StringVar(&cfg.ClaudeBin, "claude-bin", "claude", "path to the claude binary")
	flag.StringVar(&cfg.MCPConfigPath, "mcp-config", "", "path to a claude --mcp-config file (hub MCP servers)")
	flag.BoolVar(&cfg.AskViaMCP, "ask-via-mcp", false, "disable built-in AskUserQuestion and route questions to mcp__ask__ask_user")
	flag.StringVar(&cfg.ContextFile, "context-file", "", "path to a file appended to the system prompt (Hub infrastructure context)")
	flag.BoolVar(&cfg.Resume, "resume", false, "resume an existing claude session")
	flag.Parse()

	if cfg.SessionID == "" || cfg.ProjectDir == "" || cfg.JournalPath == "" || cfg.SocketPath == "" {
		log.Fatal("session-id, project-dir, journal and socket are required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := runner.Run(ctx, cfg); err != nil {
		log.Fatalf("runner: %v", err)
	}
}
