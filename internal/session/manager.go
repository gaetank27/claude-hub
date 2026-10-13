// Package session supervises Claude Code sessions from the backend. It launches
// each session as a decoupled transient systemd unit (see systemd.go), tracks
// derived state in the store, attaches to runner sockets, and recovers running
// sessions after a backend restart or reboot.
package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"system/internal/ask"
	"system/internal/runner"
	"system/internal/store"
	"system/internal/streamjson"
	"system/internal/vault"
)

// Claude permission modes used by the hub.
const (
	ModePlan   = "plan"              // propose a plan, gate execution behind approval
	ModeBypass = "bypassPermissions" // full autonomy, no per-tool prompts
)

// ErrProjectProtected is returned when attempting to delete a protected project
// (e.g. the permanent `system` project).
var ErrProjectProtected = errors.New("project is protected")

// ErrSessionLive is returned when an operation requires a finished session but
// the target is still running or starting (e.g. deleting it).
var ErrSessionLive = errors.New("session is still live")

func validPermissionMode(m string) bool {
	switch m {
	case "default", "acceptEdits", ModePlan, ModeBypass:
		return true
	}
	return false
}

// Config holds filesystem and binary locations the manager needs.
type Config struct {
	DataDir     string // base for journals/ and sockets/
	ProjectsDir string // parent dir for new projects when no path is given
	// PublicBaseURL is the externally-reachable origin of the hub, injected into a
	// session's infrastructure context so the model can build the /x/<name>/ URL of
	// an app it exposes via the Sandbox proxy.
	PublicBaseURL string
	RunnerBin     string // absolute path to the session-runner binary
	ClaudeBin   string // absolute path to the claude binary
	// CredentialsPath is the default account's .credentials.json; its parent dir
	// (~/.claude) is the canonical source of the shared Claude context (CLAUDE.md,
	// skills, commands, …) symlinked into every other account. See shared.go.
	CredentialsPath string
	PermissionMode  string        // claude permission mode for new sessions
	Model           string        // claude model alias/id for new sessions ("" = CLI default)
	MemoryMax       string        // default per-session cap, e.g. "4G" ("" = none)
	CPUQuota        string        // default per-session cap, e.g. "200%" ("" = none)
	MaxTurns        int           // hand-back threshold: turns (0 => supervisor default)
	Inactivity      time.Duration // hand-back threshold: idle time (0 => supervisor default)
	// MCP wiring: the manager composes a per-project claude --mcp-config at launch
	// (RAG knowledge + tools + the project's private code index). These are the
	// building blocks; empty bins disable the corresponding server.
	RagMCPBin   string
	ToolsMCPBin string
	AskMCPBin   string
	RagAddr     string // loopback host:port, e.g. 127.0.0.1:8788
	RagToken    string // shared token gating the loopback endpoints
	// VaultDir is the safe deposit box for soft-deleted projects (.tar.gz). Empty
	// disables vaulting (VaultProject/Restore/Destroy then return ErrVaultUnavailable).
	VaultDir string
	// OAuthTokenURL / OAuthClientID let the manager refresh an idle account's OAuth
	// access token from its refresh token (keeps the usage meter alive). Empty
	// OAuthTokenURL disables the refresher.
	OAuthTokenURL string
	OAuthClientID string
}

// LaunchSpec is the fully-resolved input to launch a runner unit.
type LaunchSpec struct {
	SessionID      string
	ProjectDir     string
	JournalPath    string
	SocketPath     string
	PermissionMode string
	Model          string
	RunnerBin      string
	ClaudeBin      string
	MemoryMax      string
	CPUQuota       string
	// ConfigDir isolates the session's Claude subscription via CLAUDE_CONFIG_DIR
	// (its credentials never collide with another account's). Empty => CLI default.
	ConfigDir string
	// APIKey, when set (apikey accounts), is exported as ANTHROPIC_API_KEY so the
	// CLI authenticates with it instead of the OAuth credentials in ConfigDir.
	APIKey string
	// MCPConfigPath wires the hub's MCP servers (RAG) into the session. Empty skips.
	MCPConfigPath string
	// AskViaMCP redirects user questions to the blocking mcp__ask__ask_user tool:
	// the runner disables the built-in AskUserQuestion (auto-denied in headless
	// mode) and instructs the model to use the MCP tool instead. Set only when the
	// ask server was actually wired into MCPConfigPath.
	AskViaMCP bool
	// ContextFilePath points at the generated infrastructure context appended to
	// the session's system prompt (Sandbox proxy + available tools). Empty skips.
	ContextFilePath string
	Resume          bool
}

// Manager is the backend-side session supervisor.
type Manager struct {
	db  *store.DB
	cfg Config

	mu   sync.Mutex
	sups map[string]*supervisor // autonomous sessions being watched for hand-back
	// transitioning marks sessions whose runner unit is being deliberately
	// stopped and relaunched (e.g. plan approval). Their unit is briefly absent
	// while still stored as running; the reconciler must not mistake that for a
	// dead runner. Guarded by mu.
	transitioning map[string]bool
	// deciding serializes plan decisions per session so a double-clicked approval
	// cannot run two driveApproval pivots in parallel (racing on the same unit
	// name and socket). Guarded by mu.
	deciding map[string]bool

	// usageMu guards usageCache. Kept separate from mu so cost computation (a
	// read-heavy, polled path) never contends with launch/stop/reconcile.
	usageMu    sync.Mutex
	usageCache map[string]usageEntry

	// setMu guards rt, the runtime-editable settings overlay (Configuration UI).
	// New sessions/supervisors read these; in-flight ones keep their values.
	setMu sync.Mutex
	rt    runtimeSettings

	// loginMu guards logins: the in-flight `claude auth login` PTY sessions, one
	// per account, awaiting the auth code the user pastes back from the browser.
	loginMu sync.Mutex
	logins  map[string]*pendingLogin

	// vault is the deleted-projects safe deposit box; nil when VaultDir is unset.
	vault *vault.Store

	// asks brokers blocking ask_user questions: the ask-mcp loopback registers a
	// pending question here and the API answer endpoint resolves it.
	asks *ask.Broker

	// autoNamed records sessions already submitted for Haiku auto-naming so only
	// the first user message triggers it (see autoname.go).
	autoNamed sync.Map
}

func NewManager(db *store.DB, cfg Config) (*Manager, error) {
	for _, d := range []string{cfg.journalDir(), cfg.socketDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	m := &Manager{
		db:            db,
		cfg:           cfg,
		sups:          make(map[string]*supervisor),
		transitioning: make(map[string]bool),
		deciding:      make(map[string]bool),
		usageCache:    make(map[string]usageEntry),
		rt:            runtimeSettingsFromConfig(cfg),
		logins:        make(map[string]*pendingLogin),
		asks:          ask.New(),
	}
	if cfg.VaultDir != "" {
		v, err := vault.New(cfg.VaultDir)
		if err != nil {
			return nil, err
		}
		m.vault = v
	}
	return m, nil
}

// Asks exposes the ask_user broker so the loopback (ask-mcp side) and the API
// (browser answer side) can resolve the same pending questions.
func (m *Manager) Asks() *ask.Broker { return m.asks }

// beginTransition / endTransition bracket a deliberate stop+relaunch of a
// session's unit so the reconciler skips it while the unit is momentarily gone.
func (m *Manager) beginTransition(sessionID string) {
	m.mu.Lock()
	m.transitioning[sessionID] = true
	m.mu.Unlock()
}

func (m *Manager) endTransition(sessionID string) {
	m.mu.Lock()
	delete(m.transitioning, sessionID)
	m.mu.Unlock()
}

// superviseAutonomous starts (idempotently) a hand-back supervisor for a session.
func (m *Manager) superviseAutonomous(s store.Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sups[s.ID]; ok {
		return
	}
	set := m.settings()
	maxTurns := set.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultMaxTurns
	}
	inactivity := set.Inactivity
	if inactivity <= 0 {
		inactivity = defaultInactivity
	}
	ctx, cancel := context.WithCancel(context.Background())
	sessID := s.ID
	sup := &supervisor{
		maxTurns:   maxTurns,
		inactivity: inactivity,
		cancel:     cancel,
		// Persist the PR URL claude reports so the UI can link to it. Best-effort:
		// the URL is re-detected on every journal replay, so a transient DB error
		// is simply retried on the next reconnect.
		onPRURL: func(url string) { _ = m.db.SetSessionPrURL(sessID, url) },
	}
	m.sups[s.ID] = sup
	sockPath := s.SocketPath
	go sup.run(ctx, func() (net.Conn, error) { return net.Dial("unix", sockPath) })
}

// PlanBranch returns the isolation branch an autonomous session executes on, or
// "" for a non-autonomous session. Derived from the id, never stored.
func (m *Manager) PlanBranch(sessionID string) string {
	if !m.IsAutonomous(sessionID) {
		return ""
	}
	return planBranch(sessionID)
}

func (m *Manager) stopSupervisor(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sup, ok := m.sups[sessionID]; ok {
		sup.cancel()
		delete(m.sups, sessionID)
	}
}

// Attention returns the hand-back state of a session, or a zero (not-needed)
// value if it is not an autonomous session under supervision.
func (m *Manager) Attention(sessionID string) Attention {
	m.mu.Lock()
	sup, ok := m.sups[sessionID]
	m.mu.Unlock()
	if !ok {
		return Attention{}
	}
	return sup.snapshot()
}

// IsAutonomous reports whether a session is supervised as autonomous.
func (m *Manager) IsAutonomous(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.sups[sessionID]
	return ok
}

func (c Config) journalDir() string { return filepath.Join(c.DataDir, "journals") }
func (c Config) socketDir() string  { return filepath.Join(c.DataDir, "sockets") }

func (c Config) journalPath(id string) string {
	return filepath.Join(c.journalDir(), id+".jsonl")
}
func (c Config) socketPath(id string) string {
	return filepath.Join(c.socketDir(), id+".sock")
}

// CreateProject registers a new project, creating its working directory and
// initializing a git repo in it. An empty dir derives the directory as
// <ProjectsDir>/<slug>, so the UI need not ask for a path; a non-empty dir lets
// the caller point at an existing checkout. Without creating the directory, a
// project pointing at a missing path would fail later, opaquely, when the
// session runner cannot chdir into it. category is an optional grouping label.
func (m *Manager) CreateProject(ctx context.Context, name, dir, category string) (store.Project, error) {
	slug := m.uniqueSlug(name)
	if strings.TrimSpace(dir) == "" {
		dir = filepath.Join(m.cfg.ProjectsDir, slug)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return store.Project{}, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return store.Project{}, fmt.Errorf("create project dir %s: %w", abs, err)
	}
	// A versioned project enables the plan/PR workflow; init is a no-op if abs is
	// already a repo (e.g. an existing checkout the user pointed at).
	if err := gitInit(ctx, abs); err != nil {
		return store.Project{}, err
	}
	p := store.Project{
		ID:        uuid.NewString(),
		Name:      name,
		Slug:      slug,
		Dir:       abs,
		Category:  strings.TrimSpace(category),
		CreatedAt: time.Now().UTC(),
	}
	if err := m.db.CreateProject(p); err != nil {
		return store.Project{}, err
	}
	return p, nil
}

// UpdateProjectMeta sets a project's starred flag, category and free-text comment
// (user metadata).
func (m *Manager) UpdateProjectMeta(id string, starred bool, category, comment string) error {
	if _, err := m.db.GetProject(id); err != nil {
		return err
	}
	return m.db.UpdateProjectMeta(id, starred, strings.TrimSpace(category), strings.TrimSpace(comment))
}

// LaunchSession starts a new Claude Code session for a project and blocks until
// the runner's socket is ready (so the caller can attach immediately). An empty
// permissionMode falls back to the configured default; pass ModePlan to start an
// autonomous session that gates execution behind plan approval. comment overrides
// the auto-name ("Session #N") when non-empty; source tags the origin ("scheduler").
func (m *Manager) LaunchSession(ctx context.Context, projectID, permissionMode, model, comment, source string) (store.Session, error) {
	proj, err := m.db.GetProject(projectID)
	if err != nil {
		return store.Session{}, err
	}

	mode := permissionMode
	if mode == "" {
		mode = m.settings().PermissionMode
	}
	if !validPermissionMode(mode) {
		return store.Session{}, fmt.Errorf("invalid permission mode %q", mode)
	}

	// Resolve the model for this session: an explicit per-session override wins,
	// else the project's assigned model, else the global runtime-settings model.
	// The resolved value is stamped on the session so a resume reattaches on the
	// same model regardless of later default changes.
	mdl := model
	if mdl == "" {
		mdl = proj.Model
	}
	if mdl == "" {
		mdl = m.settings().Model
	}

	// Stamp the session with the project's subscription (its assigned account, or
	// the default one when unset) so its consumption is attributed to the right
	// account. No account resolves => nothing to bill work against; refuse rather
	// than silently run on the CLI's ambient credentials.
	acct, ok := m.accountForProject(proj)
	if !ok {
		return store.Session{}, ErrNoActiveAccount
	}

	// Auto-name: "Session #N" where N is 1-based count within the project.
	// An explicit comment (from the scheduler) overrides the auto-name.
	name := comment
	if name == "" {
		name = "Session #1"
		if existing, err := m.db.ListSessionsByProject(projectID); err == nil {
			name = fmt.Sprintf("Session #%d", len(existing)+1)
		}
	}

	id := uuid.NewString()
	s := store.Session{
		ID:              id,
		ProjectID:       proj.ID,
		JournalPath:     m.cfg.journalPath(id),
		SocketPath:      m.cfg.socketPath(id),
		State:           store.StateStarting,
		PermissionMode:  mode,
		CreatedAt:       time.Now().UTC(),
		ClaudeAccountID: acct.ID,
		Model:           mdl,
		Comment:         name,
		Source:          source,
	}
	if err := m.db.CreateSession(s); err != nil {
		return store.Session{}, err
	}

	if err := m.launch(ctx, proj, s, false); err != nil {
		m.db.SetSessionState(id, store.StateError)
		return store.Session{}, err
	}
	if err := m.waitForSocket(s.SocketPath, 10*time.Second); err != nil {
		m.db.SetSessionState(id, store.StateError)
		return store.Session{}, fmt.Errorf("runner did not come up: %w", err)
	}
	if err := m.db.SetSessionState(id, store.StateRunning); err != nil {
		return store.Session{}, err
	}
	s.State = store.StateRunning
	// Autonomous sessions (started in plan mode) are watched for hand-back triggers.
	if mode == ModePlan {
		m.superviseAutonomous(s)
	}
	return s, nil
}

func (m *Manager) launch(ctx context.Context, proj store.Project, s store.Session, resume bool) error {
	set := m.settings()
	mcpPath, askViaMCP := m.mcpConfigForSession(proj.ID, s.ID)
	// Regenerate the infrastructure context on every launch (fresh and resume) so it
	// always reflects the currently enabled tools. Best-effort: a write failure just
	// means the session starts without the awareness payload.
	contextPath := m.writeContextFile(proj, s.ID)
	spec := LaunchSpec{
		SessionID:       s.ID,
		ProjectDir:      proj.Dir,
		JournalPath:     s.JournalPath,
		SocketPath:      s.SocketPath,
		PermissionMode:  s.PermissionMode,
		Model:           sessionModel(s, set),
		RunnerBin:       m.cfg.RunnerBin,
		ClaudeBin:       m.cfg.ClaudeBin,
		MemoryMax:       set.MemoryMax,
		CPUQuota:        set.CPUQuota,
		MCPConfigPath:   mcpPath,
		AskViaMCP:       askViaMCP,
		ContextFilePath: contextPath,
		Resume:          resume,
	}
	// Resolve the session's subscription: the one it was stamped with (so a resume
	// reattaches under its original account), falling back to the active account.
	if acct, ok := m.accountForSession(s); ok {
		spec.ConfigDir = acct.ConfigDir
		if acct.Kind == store.AccountKindAPIKey {
			spec.APIKey = m.readAPIKey(acct)
		}
	}
	return launchRunnerUnit(ctx, spec)
}

// sessionModel is the model a session runs on: the one stamped at launch, falling
// back to the current global default for sessions created before the per-session
// model column existed (stamped model is "").
func sessionModel(s store.Session, set runtimeSettings) string {
	if s.Model != "" {
		return s.Model
	}
	return set.Model
}

// Attach opens a client connection to a session's runner socket. The caller
// reads the journal replay followed by the live stream, and may write
// stream-json user input back.
func (m *Manager) Attach(sessionID string) (net.Conn, error) {
	s, err := m.db.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	return net.Dial("unix", s.SocketPath)
}

// StopSession terminates the runner unit and marks the session stopped.
func (m *Manager) StopSession(ctx context.Context, sessionID string) error {
	m.stopSupervisor(sessionID)
	// Shield the deliberate stop from the reconciler: between stopping the unit
	// and marking the session stopped, the unit is gone while the session is
	// still stored as running — without the flag set, the reconciler would
	// reclassify this intentional stop as an unexpected death (error).
	m.beginTransition(sessionID)
	defer m.endTransition(sessionID)
	if err := stopRunnerUnit(ctx, sessionID); err != nil {
		return err
	}
	return m.db.SetSessionState(sessionID, store.StateStopped)
}

// Journal returns the raw stream-json transcript of a session, used to replay a
// finished session whose runner socket is gone (no live stream to attach to).
func (m *Manager) Journal(sessionID string) ([]byte, error) {
	s, err := m.db.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	return runner.ReadJournal(s.JournalPath)
}

// DeleteSession permanently removes a finished session: its on-disk artifacts
// (journal, socket, sidecar markers) and its DB row. It refuses a session that
// is still live (ErrSessionLive) — stop it first. Irreversible: the journal is
// the session's only record.
func (m *Manager) DeleteSession(sessionID string) error {
	s, err := m.db.GetSession(sessionID)
	if err != nil {
		return err
	}
	if s.State == store.StateRunning || s.State == store.StateStarting {
		return fmt.Errorf("%w: stop session %s before deleting", ErrSessionLive, sessionID)
	}
	m.stopSupervisor(sessionID)
	for _, p := range []string{
		s.JournalPath,
		s.SocketPath,
		runner.ExitMarkerPath(s.JournalPath),
		approvalMarkerPath(s.JournalPath),
		autoPauseMarkerPath(s.JournalPath),
	} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete %s: %w", p, err)
		}
	}
	return m.db.DeleteSession(sessionID)
}

// DeleteProject permanently removes a project and all its sessions (journals +
// DB rows). It refuses a protected project (ErrProjectProtected) and a project
// with any live session (ErrSessionLive) — stop those first. The liveness check
// runs upfront so a refusal never leaves a partially deleted project.
func (m *Manager) DeleteProject(projectID string) error {
	p, err := m.db.GetProject(projectID)
	if err != nil {
		return err
	}
	if p.Protected {
		return fmt.Errorf("%w: %s", ErrProjectProtected, projectID)
	}
	sessions, err := m.db.ListSessionsByProject(projectID)
	if err != nil {
		return err
	}
	for _, s := range sessions {
		if s.State == store.StateRunning || s.State == store.StateStarting {
			return fmt.Errorf("%w: stop session %s before deleting its project", ErrSessionLive, s.ID)
		}
	}
	for _, s := range sessions {
		if err := m.DeleteSession(s.ID); err != nil {
			return err
		}
	}
	// Clean up the project's private RAG before dropping the project row:
	// project_rag has a FK to projects(id), so leaving it triggers a FOREIGN KEY
	// constraint failure. Also drop its private collection's documents/chunks.
	if pr, err := m.db.GetProjectRag(projectID); err == nil {
		if pr.CollectionID != "" {
			if err := m.db.DeleteRagCollection(pr.CollectionID); err != nil {
				return err
			}
		}
		if err := m.db.DeleteProjectRag(projectID); err != nil {
			return err
		}
	}
	return m.db.DeleteProject(projectID)
}

// ArchiveProject soft-archives a project: it disappears from the active board but
// all its data (sessions, journals, RAG, working dir) is preserved and it can be
// restored from the Archivage page. Live sessions are stopped first — they remain
// resumable, archiving just puts the project to rest. Refuses a protected project
// (ErrProjectProtected): the permanent `system` project can't be archived.
func (m *Manager) ArchiveProject(ctx context.Context, projectID string) error {
	p, err := m.db.GetProject(projectID)
	if err != nil {
		return err
	}
	if p.Protected {
		return fmt.Errorf("%w: %s", ErrProjectProtected, projectID)
	}
	sessions, err := m.db.ListSessionsByProject(projectID)
	if err != nil {
		return err
	}
	for _, s := range sessions {
		if s.State == store.StateRunning || s.State == store.StateStarting {
			if err := m.StopSession(ctx, s.ID); err != nil {
				return fmt.Errorf("stop session %s: %w", s.ID, err)
			}
		}
	}
	// Suspend the project's private RAG so the scheduler stops (re)indexing an
	// archived project (best-effort: a missing project_rag row is harmless).
	m.setProjectRagEnabled(projectID, false)
	return m.db.SetProjectArchived(projectID, true)
}

// UnarchiveProject restores a soft-archived project to the active board and
// resumes its RAG indexing.
func (m *Manager) UnarchiveProject(projectID string) error {
	if _, err := m.db.GetProject(projectID); err != nil {
		return err
	}
	m.setProjectRagEnabled(projectID, true)
	return m.db.SetProjectArchived(projectID, false)
}

// setProjectRagEnabled flips a project's RAG enabled flag while preserving its
// other scheduling config. Best-effort: silently no-ops if the project has no
// RAG row yet.
func (m *Manager) setProjectRagEnabled(projectID string, enabled bool) {
	pr, err := m.db.GetProjectRag(projectID)
	if err != nil {
		return
	}
	_ = m.db.UpdateProjectRagConfig(projectID, enabled, pr.AutoOnChange, pr.IntervalMinutes)
}

// SendUserMessage injects a user prompt into a live session by connecting to its
// runner socket and writing one stream-json input line, then disconnecting. Used
// to drive a session programmatically (e.g. telling it to execute an approved
// plan). The runner echoes the message back on stdout (--replay-user-messages),
// so it lands in the journal without duplication.
func (m *Manager) SendUserMessage(sessionID, text string) error {
	line, err := streamjson.UserInput(text)
	if err != nil {
		return err
	}
	conn, err := m.Attach(sessionID)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return err
	}
	// Closing immediately races the runner's reader goroutine, which may not have
	// scanned our line yet (the runner closes the conn once its replay/stream
	// write fails). Hold the connection open briefly — draining and discarding the
	// replay — so the input is reliably consumed before we disconnect.
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	io.Copy(io.Discard, conn)
	return nil
}

// GetSessionState returns the current state of a session. Used by the WhatsApp
// bot to check whether a persisted session is still running after a restart.
func (m *Manager) GetSessionState(sessionID string) (string, error) {
	s, err := m.db.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	return s.State, nil
}

// SendUserBlocks injects a user prompt with optional image attachments into a
// live session. Used by the WhatsApp bot to forward received images to Claude.
func (m *Manager) SendUserBlocks(sessionID, text string, images []streamjson.ImageBlock) error {
	m.MaybeAutoName(sessionID, text)
	line, err := streamjson.UserInputBlocks(text, images, nil)
	if err != nil {
		return err
	}
	conn, err := m.Attach(sessionID)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return err
	}
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	io.Copy(io.Discard, conn)
	return nil
}

// DecidePlan resolves a plan that claude proposed via ExitPlanMode.
//
// On approval it performs the slice-2 pivot: switch the session to
// bypassPermissions durably, then stop and resume the runner so claude
// reattaches to the same session/journal under the new mode (--permission-mode
// changes are only honored at process start), and finally tell it to execute.
//
// On rejection it leaves the session in plan mode and asks claude to revise; the
// session stays live, so no restart is needed.
func (m *Manager) DecidePlan(ctx context.Context, sessionID string, approve bool) error {
	s, err := m.db.GetSession(sessionID)
	if err != nil {
		return err
	}
	if !approve {
		return m.SendUserMessage(sessionID,
			"I reject this plan. Do not execute it — wait for my revised instructions.")
	}

	// Serialize approvals per session: a double-clicked approve would otherwise
	// run two pivots in parallel, racing on the same unit name and socket. The
	// second concurrent call is rejected fast rather than blocking. Recover runs
	// before the HTTP server starts, so this only needs to cover HTTP requests.
	m.mu.Lock()
	if m.deciding[sessionID] {
		m.mu.Unlock()
		return fmt.Errorf("plan decision already in progress for session %s", sessionID)
	}
	m.deciding[sessionID] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.deciding, sessionID)
		m.mu.Unlock()
	}()

	proj, err := m.db.GetProject(s.ProjectID)
	if err != nil {
		return err
	}
	// Ensure a remote exists (and its base branch is pushed) so claude can push
	// and open a PR for the approved work. Runs BEFORE createPlanBranch so the
	// base branch — still the current HEAD here — is what gets pushed as the PR
	// target. Best-effort: missing gh/auth must not prevent execution.
	if err := ensureRemote(ctx, proj.Dir, proj.Slug); err != nil {
		log.Printf("ensure remote for session %s: %v", sessionID, err)
	}
	// Isolate the autonomous execution on a dedicated branch before it starts.
	// Best-effort: a git problem must not prevent running the approved plan.
	if err := createPlanBranch(ctx, proj.Dir, sessionID); err != nil {
		log.Printf("plan branch for session %s: %v", sessionID, err)
	}
	// Persist the intent to execute before touching the runner: the pivot below is
	// in-memory (the transitioning flag) and would be lost if the backend restarts
	// mid-pivot, stranding the resumed session idle. The marker lets Recover
	// re-drive it on the next boot. It is cleared only once claude has reacted.
	// Starts at 0 re-drives; Recover increments and bounds the cross-restart retry.
	writeApprovalAttempt(s.JournalPath, 0)
	if err := m.driveApproval(ctx, proj, s); err != nil {
		return err
	}
	clearApprovalMarker(s.JournalPath)
	return nil
}

// driveApproval performs the slice-2 pivot for an approved plan and is idempotent
// so it can be (re)run by DecidePlan or by Recover after a crash: switch the
// session to bypassPermissions durably, stop and resume the runner so claude
// reattaches under the new mode (--permission-mode is only honored at process
// start), then tell it to execute and confirm it reacted.
func (m *Manager) driveApproval(ctx context.Context, proj store.Project, s store.Session) error {
	if err := m.db.SetSessionPermissionMode(s.ID, ModeBypass); err != nil {
		return err
	}
	s.PermissionMode = ModeBypass

	// Shield the stop+relaunch from the reconciler: the unit is gone in between,
	// but the session is intentionally still running.
	m.beginTransition(s.ID)
	defer m.endTransition(s.ID)

	if err := stopRunnerUnit(ctx, s.ID); err != nil {
		return err
	}
	// The transient unit name must be free before the resume relaunch reuses it.
	if err := waitForUnitInactive(ctx, s.ID, 10*time.Second); err != nil {
		return err
	}
	if err := m.launch(ctx, proj, s, true); err != nil {
		clearApprovalMarker(s.JournalPath) // terminal: don't leave an error+marker pair
		m.db.SetSessionState(s.ID, store.StateError)
		return err
	}
	if err := m.waitForSocket(s.SocketPath, 15*time.Second); err != nil {
		clearApprovalMarker(s.JournalPath) // terminal: don't leave an error+marker pair
		m.db.SetSessionState(s.ID, store.StateError)
		return err
	}
	// waitForSocket returns as soon as the runner listens — BEFORE claude finishes
	// loading the resumed session and starts reading stdin. A message injected in
	// that window is dropped, and `claude --resume` emits nothing spontaneously to
	// signal readiness. So we send the approval and confirm claude reacted (the
	// journal grew); if not, we resend. The dropped early message is never read,
	// so a resend does not double-execute.
	const detect = 8 * time.Second
	for attempt := 0; attempt < 4; attempt++ {
		before := journalLineCount(s.JournalPath)
		if err := m.SendUserMessage(s.ID, approvalMessage); err != nil {
			return err
		}
		if waitJournalGrows(s.JournalPath, before, detect) {
			return nil
		}
	}
	return fmt.Errorf("resumed session did not react to plan approval")
}

// approvalMessage is injected once a plan is approved. Beyond "go", it instructs
// claude (running in bypassPermissions on the plan/<id> branch) to drive git
// itself: commit per step, then push and open a PR when done. The backend only
// provisions the branch+remote and later scrapes the PR URL from the journal.
const approvalMessage = "I approve this plan. Execute it now. " +
	"As you work, commit your changes on the current branch after each meaningful " +
	"step, with a clear message. When the work is complete, push the branch and " +
	"open a pull request with `gh pr create` (title and body from the plan), then " +
	"state the resulting PR URL."

// ApplyGitFallback is the backend safety net for the autonomous git workflow.
// The approval message asks claude to commit per step, push, and open a PR, but
// that is non-deterministic: if claude forgets, the work would be stranded on the
// plan branch — uncommitted, unpushed, or with no PR. After an autonomous session
// exits cleanly, the reconciler and Recover call this to finish the job.
//
// Idempotent and best-effort, matching ensureRemote/createPlanBranch: every step
// logs and continues, and re-running is a no-op once the tree is clean, the branch
// is pushed, and a PR URL is recorded — so a second reconcile tick or a boot-time
// Recover does nothing.
func (m *Manager) ApplyGitFallback(ctx context.Context, s store.Session) {
	// Re-fetch for the freshest pr_url: the supervisor may have scraped and
	// persisted a URL just before it was stopped, racing this call.
	if fresh, err := m.db.GetSession(s.ID); err == nil {
		s = fresh
	}
	proj, err := m.db.GetProject(s.ProjectID)
	if err != nil {
		log.Printf("git fallback: get project for session %s: %v", s.ID, err)
		return
	}
	if !isGitRepo(ctx, proj.Dir) {
		return
	}
	branch := planBranch(s.ID)
	// Only act when the tree is still on the plan branch. If claude switched
	// branches, committing here could land work in the wrong place — skip rather
	// than guess.
	if cur := currentBranch(ctx, proj.Dir); cur != branch {
		log.Printf("git fallback: session %s not on %s (on %q), skipping", s.ID, branch, cur)
		return
	}
	if hasUncommittedChanges(ctx, proj.Dir) {
		msg := fmt.Sprintf("chore(plan): autonomous changes for session %s", s.ID)
		if err := commitAll(ctx, proj.Dir, msg); err != nil {
			log.Printf("git fallback: commit for session %s: %v", s.ID, err)
		}
	}
	base := defaultBranch(ctx, proj.Dir)
	if base == branch {
		return // the plan branch is the default branch; nothing to target
	}
	// No commits beyond base means the session shipped nothing: don't push an
	// empty branch or open an empty PR. -1 (unknown) falls through best-effort.
	if commitsAhead(ctx, proj.Dir, base, branch) == 0 {
		return
	}
	if hasUnpushedCommits(ctx, proj.Dir, branch) {
		if err := pushBranch(ctx, proj.Dir, branch); err != nil {
			log.Printf("git fallback: push for session %s: %v", s.ID, err)
			return // can't open a PR for an unpushed branch
		}
	}
	if s.PrURL != "" {
		return // claude (or a prior fallback) already opened a PR
	}
	title := fmt.Sprintf("Autonomous changes for session %s", s.ID)
	body := "Opened by the backend git fallback: the autonomous session finished " +
		"without opening a pull request itself."
	url, err := createPR(ctx, proj.Dir, base, branch, title, body)
	if err != nil {
		// A PR may already exist for the branch (claude opened one but its URL was
		// never scraped). Recover it instead of treating this as a failure.
		if url = existingPRURL(ctx, proj.Dir, branch); url == "" {
			log.Printf("git fallback: create PR for session %s: %v", s.ID, err)
			return
		}
	}
	if err := m.db.SetSessionPrURL(s.ID, url); err != nil {
		log.Printf("git fallback: persist PR URL for session %s: %v", s.ID, err)
	}
}

// maxApprovalRedrives bounds how many times Recover re-drives a pending plan
// approval across backend restarts before giving up and marking the session
// errored. Without it, a session whose resumed runner never reacts would be
// stopped+relaunched on every boot forever — the crash-loop the reconciler also
// guards against.
const maxApprovalRedrives = 3

// approvalMarkerPath is a sidecar file recording that a plan was approved but
// claude has not yet been confirmed to react to it. Its content is the number of
// cross-restart re-drives already attempted. See writeApprovalAttempt.
func approvalMarkerPath(journalPath string) string { return journalPath + ".pending-approval" }

// writeApprovalAttempt records a pending approval and how many re-drives have
// been attempted so far (0 on the initial drive from DecidePlan).
func writeApprovalAttempt(journalPath string, n int) {
	_ = os.WriteFile(approvalMarkerPath(journalPath), []byte(strconv.Itoa(n)), 0o600)
}
func clearApprovalMarker(journalPath string) { _ = os.Remove(approvalMarkerPath(journalPath)) }

// approvalAttempts returns (n, true) when a pending-approval marker exists, where
// n is the number of re-drives already attempted. A present-but-unparseable
// marker (e.g. a torn write) reads as 0 so it keeps the full re-drive budget.
func approvalAttempts(journalPath string) (int, bool) {
	b, err := os.ReadFile(approvalMarkerPath(journalPath))
	if err != nil {
		return 0, false
	}
	n, perr := strconv.Atoi(strings.TrimSpace(string(b)))
	if perr != nil {
		n = 0
	}
	return n, true
}

func journalLineCount(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return bytes.Count(data, []byte("\n"))
}

// waitJournalGrows reports whether the journal gained lines beyond before within
// timeout — used as a proxy for "claude received our input and started reacting".
func waitJournalGrows(path string, before int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if journalLineCount(path) > before {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// Recover is called at backend startup. For each session marked running:
//   - if its unit is still active, do nothing (just reattach later on demand);
//   - otherwise the runner died (e.g. reboot) → relaunch with --resume so claude
//     continues the same session and appends to the same journal.
func (m *Manager) Recover(ctx context.Context) error {
	sessions, err := m.db.ListSessionsByState(store.StateRunning)
	if err != nil {
		return err
	}
	for _, s := range sessions {
		// A plan was approved but the backend died before claude was confirmed to
		// react: re-drive the pivot (idempotent) so the session does not stay idle.
		if n, ok := approvalAttempts(s.JournalPath); ok {
			proj, err := m.db.GetProject(s.ProjectID)
			if err != nil {
				continue
			}
			if n >= maxApprovalRedrives {
				// Give up: the resumed session has repeatedly failed to react to the
				// approval across restarts. Don't crash-loop the runner — same
				// philosophy as the reconciler. Mark error and drop the marker.
				log.Printf("recover: session %s exhausted %d approval re-drives, marking error", s.ID, n)
				clearApprovalMarker(s.JournalPath)
				m.db.SetSessionState(s.ID, store.StateError)
				continue
			}
			// Record this attempt BEFORE driving so a driveApproval that crashes the
			// backend before returning still consumes budget — otherwise the loop is
			// merely relocated, not bounded.
			writeApprovalAttempt(s.JournalPath, n+1)
			if err := m.driveApproval(ctx, proj, s); err != nil {
				log.Printf("recover: re-drive approval for session %s (attempt %d): %v", s.ID, n+1, err)
				continue // marker kept at n+1; a later boot retries until the bound
			}
			clearApprovalMarker(s.JournalPath)
			m.superviseAutonomous(s)
			continue
		}
		if !unitActive(ctx, s.ID) {
			// A clean exit recorded before the unit vanished means the session
			// finished on its own — mark it stopped rather than resuming it.
			if runner.CleanExit(s.JournalPath) {
				// A clean exit that happened while the backend was down still needs
				// the git workflow finished if claude left it incomplete. Idempotent,
				// so re-running across boots is safe.
				if isAutonomousJournal(s.JournalPath) {
					m.ApplyGitFallback(ctx, s)
				}
				m.db.SetSessionState(s.ID, store.StateStopped)
				continue
			}
			// Otherwise the runner died (e.g. reboot) → resume it.
			proj, err := m.db.GetProject(s.ProjectID)
			if err != nil {
				continue
			}
			if err := m.launch(ctx, proj, s, true); err != nil {
				m.db.SetSessionState(s.ID, store.StateError)
				continue
			}
			if err := m.waitForSocket(s.SocketPath, 15*time.Second); err != nil {
				m.db.SetSessionState(s.ID, store.StateError)
				continue
			}
		}
		// Re-arm the hand-back supervisor for autonomous sessions. Autonomy is not
		// stored: it is inferred from the journal, whose first init recorded plan
		// mode (it survives the later switch to bypassPermissions).
		if isAutonomousJournal(s.JournalPath) {
			m.superviseAutonomous(s)
		}
	}
	return nil
}

// isAutonomousJournal reports whether a session was started in plan mode (its
// defining trait of autonomy), by inspecting its journal's init event(s).
func isAutonomousJournal(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return bytes.Contains(data, []byte(`"permissionMode":"plan"`))
}

func (m *Manager) waitForSocket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			// Stat is not enough: ensure it accepts connections.
			if c, derr := net.Dial("unix", path); derr == nil {
				c.Close()
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", path)
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func (m *Manager) uniqueSlug(name string) string {
	base := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if base == "" {
		base = "project"
	}
	slug := base
	for i := 2; ; i++ {
		if _, err := m.db.GetProjectBySlug(slug); err == store.ErrNotFound {
			return slug
		}
		slug = fmt.Sprintf("%s-%d", base, i)
	}
}
