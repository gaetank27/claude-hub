// Package runner implements the session-runner: a standalone process that owns
// one `claude` CLI process, records its stream-json output to an append-only
// journal, and relays that stream (with replay) to clients over a Unix socket.
//
// The runner is deliberately decoupled from the web backend and from any
// database — it is launched as a transient systemd unit and survives backend
// restarts. Its journal + the claude session id are everything needed to resume.
package runner

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"system/internal/streamjson"
)

// Config parameterizes a runner invocation.
type Config struct {
	SessionID      string // also passed to `claude --session-id`
	ProjectDir     string // claude working directory
	JournalPath    string
	SocketPath     string
	PermissionMode string // "default" in slice 1
	Model          string // claude model alias/id, e.g. "opus" ("" = CLI default)
	ClaudeBin      string // absolute path to the claude binary
	Resume         bool   // true => append to existing journal and `claude --resume`
	// MCPConfigPath, when set, is passed to `claude --mcp-config` so the session
	// gets the hub's MCP servers (e.g. the RAG knowledge_search tool). Empty skips.
	MCPConfigPath string
	// AskViaMCP disables the built-in AskUserQuestion tool (auto-denied in headless
	// stream-json mode) and instructs the model to use the blocking
	// mcp__ask__ask_user tool wired via MCPConfigPath instead.
	AskViaMCP bool
	// ContextFile, when set, is a file whose contents are appended to the session's
	// system prompt so the model knows about the shared Hub infrastructure (Sandbox
	// proxy, available tools). Read best-effort: a missing/unreadable file is skipped.
	ContextFile string
}

// askSystemPrompt redirects the model from the disabled built-in AskUserQuestion
// to the hub's blocking ask_user tool. Kept terse: it is appended to every
// AskViaMCP session's system prompt.
const askSystemPrompt = "To ask the user a question or have them choose between options, you MUST use the " +
	"mcp__ask__ask_user tool and wait for their answer. The built-in AskUserQuestion tool is disabled " +
	"in this environment; do not attempt to use it. The ask_user tool blocks until the user replies, so " +
	"never assume a non-answer and never continue until you receive their choice."

// Runner wires the claude process to the journal and socket hub.
type Runner struct {
	cfg     Config
	journal *Journal
	hub     *Hub
	srv     *SocketServer

	stdinMu sync.Mutex
	stdin   io.WriteCloser
}

// Run launches the session and blocks until claude exits or ctx is cancelled.
func Run(ctx context.Context, cfg Config) error {
	journal, err := OpenJournal(cfg.JournalPath)
	if err != nil {
		return fmt.Errorf("open journal: %w", err)
	}
	defer journal.Close()

	// Drop any marker left by a previous run before recording this one's outcome.
	clearExitMarker(cfg.JournalPath)

	r := &Runner{cfg: cfg, journal: journal, hub: newHub(journal, cfg.JournalPath)}

	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--include-partial-messages",
		"--replay-user-messages",
		"--verbose",
		"--permission-mode", cfg.PermissionMode,
		"--add-dir", cfg.ProjectDir,
	}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	// Wire the hub's MCP servers (RAG knowledge_search) into the session so the
	// model can query the shared knowledge base on demand.
	if cfg.MCPConfigPath != "" {
		args = append(args, "--mcp-config", cfg.MCPConfigPath)
	}
	// Compose the appended system prompt from every fragment we have. Joined into a
	// single --append-system-prompt: we don't rely on the CLI accepting the flag
	// more than once.
	var sysFragments []string
	// The Hub infrastructure context makes the model aware of shared resources
	// (Sandbox proxy, available MCP tools) so it doesn't reinstall system services.
	if ctxPrompt := readContextFile(cfg.ContextFile); ctxPrompt != "" {
		sysFragments = append(sysFragments, ctxPrompt)
	}
	// In headless stream-json the built-in AskUserQuestion is auto-denied by the
	// CLI (no interactive answer channel), so the model would "ask" and instantly
	// continue as if declined. Disable it and point the model at the blocking
	// mcp__ask__ask_user tool (wired via --mcp-config), which waits for the user.
	if cfg.AskViaMCP {
		args = append(args, "--disallowedTools", "AskUserQuestion")
		sysFragments = append(sysFragments, askSystemPrompt)
	}
	if len(sysFragments) > 0 {
		args = append(args, "--append-system-prompt", strings.Join(sysFragments, "\n\n"))
	}
	// --session-id and --resume are mutually exclusive (the CLI rejects passing
	// both without --fork-session, which would mint a new id). On a fresh start
	// we pin the id; on resume we reattach to the same session by id.
	if cfg.Resume {
		args = append(args, "--resume", cfg.SessionID)
	} else {
		args = append(args, "--session-id", cfg.SessionID)
	}

	cmd := exec.CommandContext(ctx, cfg.ClaudeBin, args...)
	cmd.Dir = cfg.ProjectDir

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	r.stdin = stdin
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = nil // claude diagnostics go to its own logs; stdout is the protocol

	srv, err := listenSocket(cfg.SocketPath, r.hub, r.sendInput)
	if err != nil {
		return fmt.Errorf("listen socket: %w", err)
	}
	r.srv = srv
	defer srv.close()
	go srv.serve()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start claude: %w", err)
	}

	// Pump claude stdout into the journal + hub until EOF.
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // tool outputs can be large
	for sc.Scan() {
		line := sc.Bytes()
		ev := streamjson.Parse(line)
		if err := r.hub.Publish(ev.Raw, ev.IsTurnBoundary()); err != nil {
			return fmt.Errorf("publish: %w", err)
		}
	}

	// Record how claude terminated so the backend can tell a clean exit from a
	// crash after the transient unit (and its systemd exit metadata) is collected.
	waitErr := cmd.Wait()
	writeExitMarker(cfg.JournalPath, waitErr)
	return waitErr
}

// readContextFile returns the trimmed contents of the infrastructure context file,
// or "" when no path is set or the file cannot be read. Injecting the context is
// best-effort: a read failure must never prevent the session from starting.
func readContextFile(path string) string {
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// sendInput forwards a client-provided stream-json line to the claude stdin.
// The line is not journaled here: with --replay-user-messages claude echoes it
// back on stdout, where it is journaled like any other event (no duplication).
func (r *Runner) sendInput(line []byte) {
	r.stdinMu.Lock()
	defer r.stdinMu.Unlock()
	if r.stdin == nil {
		return
	}
	r.stdin.Write(append(line, '\n'))
}
