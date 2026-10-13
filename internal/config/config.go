// Package config resolves runtime configuration from the environment with sane
// defaults for the single-node Debian deployment.
package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// SystemDir is the immutable root of the permanent `system` project.
const SystemDir = "/home/claude/projects/system"

// Config holds resolved backend configuration.
type Config struct {
	ListenAddr string // host:port the backend binds to
	// RagAddr is the loopback host:port for the RAG retrieval endpoint queried by
	// the rag-mcp tool. It must stay on localhost — it is unauthenticated (gated
	// only by a per-boot token), so the public reverse proxy must never expose it.
	RagAddr     string
	DataDir     string // base for DB, journals, sockets
	DBPath      string
	ClaudeBin   string // absolute path to the claude CLI
	RunnerBin   string // absolute path to the session-runner binary
	RagMCPBin   string // absolute path to the rag-mcp binary (RAG knowledge_search tool)
	ToolsMCPBin string // absolute path to the tools-mcp binary (external integrations)
	AskMCPBin   string // absolute path to the ask-mcp binary (blocking ask_user tool)
	OllamaHost  string // local Ollama base URL (embeddings + supervision)
	// DockerHost is the Docker Engine endpoint for the Sandbox/Live environments.
	// Only the unix:// scheme (local socket) is supported.
	DockerHost string
	// PublicBaseURL is the externally-reachable origin of the hub, used to build
	// "open in browser" links to sandbox apps behind the /x/ reverse proxy.
	PublicBaseURL string
	// RRDToolBin is the rrdtool CLI the host-metrics historisation shells out to.
	RRDToolBin string
	// HostSampleSeconds is the host-metrics sampling interval (and the RRD step).
	HostSampleSeconds int
	// CredentialsPath is the claude CLI's OAuth credentials file, read to report
	// subscription usage (the top-of-page meter). Defaults to ~/.claude/.credentials.json.
	CredentialsPath string
	// OAuthTokenURL / OAuthClientID are the Claude Code OAuth token endpoint and
	// public client id, used to refresh an idle account's access token from its
	// stored refresh token (so the usage meter survives a token expiring while no
	// session is keeping it warm). Both are overridable in case the upstream
	// changes; a refresh failure is non-fatal (the meter just stays stale).
	OAuthTokenURL string
	OAuthClientID string
	// ProjectsDir is the parent folder under which a new project's working
	// directory is created when the caller does not provide an explicit path.
	// Defaults to /home/claude/projects (the sibling of the system project).
	ProjectsDir string
	// VaultDir is the persistent "safe deposit box" where deleted projects are
	// archived as .tar.gz before any on-disk destruction. It is intentionally kept
	// outside DataDir and is NEVER purged automatically — the only way to remove a
	// vault archive is the explicit "destroy" action. Defaults to
	// /home/claude/project-vault.
	VaultDir string
	// PermissionMode for new sessions. Defaults to bypassPermissions per the
	// "maximum autonomy" decision (no UI permission dialog exists yet). Slice 2
	// will front this with a plan → one-click-validate → bypass cinematic.
	PermissionMode string
	// Model alias/id forced for sessions. Defaults to "opus" (latest, most
	// capable Opus; the alias tracks new versions automatically).
	Model     string
	MemoryMax string // default per-session memory cap ("" = none)
	CPUQuota  string // default per-session CPU cap ("" = none)
	// Autonomous-mode hand-back thresholds.
	MaxTurns   int           // flag an autonomous session past this many turns
	Inactivity time.Duration // flag an autonomous session idle this long after a turn
	// AssistantAttachmentDays is how long files sent to the assistant are kept.
	AssistantAttachmentDays int
	// AssistantImagesPerHour caps the images the assistant sends (all channels).
	AssistantImagesPerHour int

	// OIDC / auth. Auth is enabled only when OIDCIssuer is set (AuthEnabled), so
	// the binary still runs auth-less in dev and matches today's behavior by
	// default. TLS is terminated by the nginx reverse proxy in front, so the
	// backend speaks plain HTTP on loopback — it must bind 127.0.0.1 (the default)
	// so nginx is the only ingress and the auth check cannot be bypassed.
	OIDCIssuer        string   // PocketID issuer URL
	OIDCClientID      string   // OIDC client registered in PocketID
	OIDCClientSecret  string   // OIDC client secret
	OIDCRedirectURL   string   // public https callback, e.g. https://hub.example.com/auth/callback
	AuthAllowedSubs   []string // allowlist of OIDC subjects ("" + emails empty => any authenticated)
	AuthAllowedEmails []string // allowlist of emails (case-insensitive)
	SessionSecret     string   // HMAC key signing the session cookie; must be stable across restarts
	// TOTPIssuer is the label shown in the operator's authenticator app for the
	// local-account 2FA enrolment (e.g. "Claude Hub").
	TOTPIssuer string
	// DisableAuth turns the whole gate off (SYSTEM_DISABLE_AUTH=1). For local dev
	// only — it serves an open hub. Production relies on the first-boot wizard
	// creating a local admin, after which auth is always on.
	DisableAuth bool
	// AllowedOrigins lists extra browser origin hosts accepted on the WebSocket
	// upgrade (host only, no scheme, e.g. "claude-hub.example.com"). The public
	// host is derived from OIDCRedirectURL in main.go; this covers any additional
	// origin. When the effective list is empty (dev, no public host), the upgrade
	// skips the origin check so a local browser still connects.
	AllowedOrigins []string
}

// AuthEnabled reports whether OIDC auth is configured. When false the backend
// serves unauthenticated (dev / pre-auth behavior).
func (c Config) AuthEnabled() bool { return c.OIDCIssuer != "" }

// Load builds the configuration. Bind defaults to loopback; set SYSTEM_LISTEN to
// the LAN IP (e.g. 192.168.1.10:8787) for VPN access — never 0.0.0.0 in prod.
func Load() Config {
	dataDir := env("SYSTEM_DATA_DIR", filepath.Join(home(), ".local", "share", "system"))
	return Config{
		ListenAddr:      env("SYSTEM_LISTEN", "127.0.0.1:8787"),
		RagAddr:         env("SYSTEM_RAG_ADDR", "127.0.0.1:8788"),
		DataDir:         dataDir,
		DBPath:          filepath.Join(dataDir, "system.db"),
		ClaudeBin:       resolveClaude(),
		RunnerBin:       resolveRunner(),
		RagMCPBin:       resolveSibling("SYSTEM_RAG_MCP_BIN", "rag-mcp"),
		ToolsMCPBin:     resolveSibling("SYSTEM_TOOLS_MCP_BIN", "tools-mcp"),
		AskMCPBin:       resolveSibling("SYSTEM_ASK_MCP_BIN", "ask-mcp"),
		OllamaHost:      ollamaHost(),
		DockerHost:        env("SYSTEM_DOCKER_HOST", "unix:///var/run/docker.sock"),
		RRDToolBin:        env("SYSTEM_RRDTOOL_BIN", "rrdtool"),
		HostSampleSeconds: envInt("SYSTEM_HOST_SAMPLE_SECONDS", 30),
		PublicBaseURL:   env("SYSTEM_PUBLIC_BASE_URL", "https://claude-hub.example.com"),
		CredentialsPath: env("SYSTEM_CLAUDE_CREDENTIALS", filepath.Join(home(), ".claude", ".credentials.json")),
		OAuthTokenURL:   env("SYSTEM_OAUTH_TOKEN_URL", "https://console.anthropic.com/v1/oauth/token"),
		OAuthClientID:   env("SYSTEM_OAUTH_CLIENT_ID", "9d1c250a-e61b-44d9-88ed-5944d1962f5e"),
		ProjectsDir:     env("SYSTEM_PROJECTS_DIR", filepath.Dir(SystemDir)),
		VaultDir:        env("SYSTEM_VAULT_DIR", filepath.Join(home(), "project-vault")),
		PermissionMode:  env("SYSTEM_PERMISSION_MODE", "bypassPermissions"),
		Model:           env("SYSTEM_MODEL", "opus"),
		MemoryMax:       os.Getenv("SYSTEM_SESSION_MEMORY_MAX"),
		CPUQuota:        os.Getenv("SYSTEM_SESSION_CPU_QUOTA"),
		MaxTurns:        envInt("SYSTEM_MAX_TURNS", 40),
		Inactivity:      time.Duration(envInt("SYSTEM_INACTIVITY_SECONDS", 180)) * time.Second,
		AssistantAttachmentDays: envInt("SYSTEM_ASSISTANT_ATTACHMENT_DAYS", 30),
		AssistantImagesPerHour:  envInt("SYSTEM_ASSISTANT_IMAGES_PER_HOUR", 10),

		OIDCIssuer:        os.Getenv("SYSTEM_OIDC_ISSUER"),
		OIDCClientID:      os.Getenv("SYSTEM_OIDC_CLIENT_ID"),
		OIDCClientSecret:  os.Getenv("SYSTEM_OIDC_CLIENT_SECRET"),
		OIDCRedirectURL:   os.Getenv("SYSTEM_OIDC_REDIRECT_URL"),
		AuthAllowedSubs:   splitList(os.Getenv("SYSTEM_AUTH_ALLOWED_SUBS")),
		AuthAllowedEmails: splitList(os.Getenv("SYSTEM_AUTH_ALLOWED_EMAILS")),
		SessionSecret:     os.Getenv("SYSTEM_SESSION_SECRET"),
		TOTPIssuer:        env("SYSTEM_TOTP_ISSUER", "Claude Hub"),
		DisableAuth:       envBool("SYSTEM_DISABLE_AUTH"),
		AllowedOrigins:    splitList(os.Getenv("SYSTEM_ALLOWED_ORIGINS")),
	}
}

// envBool reads a boolean-ish env var (1/true/yes, case-insensitive).
func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// splitList parses a comma-separated env value into a trimmed, non-empty slice.
func splitList(v string) []string {
	if v == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func resolveClaude() string {
	if v := os.Getenv("SYSTEM_CLAUDE_BIN"); v != "" {
		return v
	}
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	return filepath.Join(home(), ".local", "bin", "claude")
}

// resolveRunner defaults to the session-runner binary sitting next to the
// backend executable (how `go build ./cmd/...` lays them out).
func resolveRunner() string { return resolveSibling("SYSTEM_RUNNER_BIN", "session-runner") }

// resolveSibling resolves a companion binary: the env override if set, else the
// binary of the given name sitting next to the backend executable.
func resolveSibling(envKey, name string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), name)
	}
	return name
}

// ollamaHost normalizes OLLAMA_HOST (often "host:port" without a scheme) to a URL,
// defaulting to the local Ollama server.
func ollamaHost() string {
	h := os.Getenv("OLLAMA_HOST")
	if h == "" {
		return "http://127.0.0.1:11434"
	}
	if !strings.HasPrefix(h, "http://") && !strings.HasPrefix(h, "https://") {
		h = "http://" + h
	}
	return strings.TrimRight(h, "/")
}

func home() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return SystemDir
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
