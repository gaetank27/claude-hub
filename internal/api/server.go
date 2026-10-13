// Package api exposes the hub's HTTP REST + WebSocket surface and serves the
// embedded React frontend. It is a thin layer over the session.Manager and the
// store — deliberately stateless so the backend can restart without losing
// anything (live sessions live in the decoupled runners).
package api

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"system/internal/assistant"
	"system/internal/auth"
	"system/internal/claudemetrics"
	"system/internal/docker"
	"system/internal/hostmetrics"
	"system/internal/mcpremote"
	"system/internal/ollama"
	"system/internal/rag"
	"system/internal/scriptstatus"
	"system/internal/secretbox"
	"system/internal/session"
	"system/internal/store"
	"system/internal/usage"
	"system/internal/voice"
	"system/internal/whatsapp"
)

// assets holds the built frontend. A placeholder index.html ships in the repo so
// the backend compiles before the frontend is built; `vite build` overwrites it.
//
//go:embed all:assets
var assets embed.FS

// Server wires HTTP routes to the manager and store.
type Server struct {
	mgr  *session.Manager
	db   *store.DB
	auth *auth.Authenticator // nil when auth is disabled
	// allowedOrigins are the browser origin hosts accepted on the WebSocket
	// upgrade (host only, no scheme). Empty => skip the origin check (dev).
	allowedOrigins []string
	usage          *usage.Client          // nil when no credentials path is configured
	rag            *rag.Indexer           // RAG knowledge-base ingestion + status
	secrets        *secretbox.Box         // encrypts tool/connector credentials at rest
	ollama         *ollama.Client         // local Ollama supervision (nil when unconfigured)
	docker         *docker.Client         // Sandbox/Live container control (nil when unconfigured)
	dockerProxy    *containerProxy        // reverse proxy to container web ports (/x/...)
	publicBaseURL  string                 // external origin for sandbox "open in browser" links
	hostRRD        *hostmetrics.Store     // host-metrics history (RRD); nil-safe via Available()
	hostMetrics    *hostmetrics.Collector // live host metrics for the gauges banner
	claudeRRD      *claudemetrics.Store   // per-account Claude usage history (RRD)
	waMgr          *whatsapp.Manager      // WhatsApp connection manager (nil when no WA tools)
	voice          *voice.Service         // local STT/TTS for voice mode (nil when disabled)
	assistant      *assistant.Service     // personal assistant (nil until wired)
	scripts        *scriptstatus.Service  // script status endpoint (nil until wired)
	mcpRemote      *mcpremote.Service     // remote MCP connector for claude.ai (nil = off)
}

func NewServer(mgr *session.Manager, db *store.DB, authn *auth.Authenticator, allowedOrigins []string, usageClient *usage.Client, indexer *rag.Indexer, secrets *secretbox.Box, ollamaClient *ollama.Client, dockerClient *docker.Client, publicBaseURL string, hostRRD *hostmetrics.Store, claudeRRD *claudemetrics.Store, waMgr *whatsapp.Manager, voiceSvc *voice.Service) *Server {
	s := &Server{mgr: mgr, db: db, auth: authn, allowedOrigins: allowedOrigins, usage: usageClient, rag: indexer, secrets: secrets, ollama: ollamaClient, docker: dockerClient, publicBaseURL: publicBaseURL, hostRRD: hostRRD, hostMetrics: hostmetrics.New(), claudeRRD: claudeRRD, waMgr: waMgr, voice: voiceSvc}
	if dockerClient != nil {
		s.dockerProxy = newContainerProxy(dockerClient)
	}
	return s
}

// Handler builds the full HTTP handler (API + static SPA).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// REST API
	mux.HandleFunc("GET /api/projects", s.listProjects)
	mux.HandleFunc("POST /api/projects", s.createProject)
	mux.HandleFunc("GET /api/projects/{id}", s.getProject)
	mux.HandleFunc("PATCH /api/projects/{id}", s.updateProject)
	mux.HandleFunc("POST /api/projects/{id}/archive", s.archiveProject)
	mux.HandleFunc("POST /api/projects/{id}/unarchive", s.unarchiveProject)
	mux.HandleFunc("POST /api/projects/{id}/vault", s.vaultProject)
	mux.HandleFunc("DELETE /api/projects/{id}", s.deleteProject)
	// Vault (safe deposit box): list, restore, and the single permanent-destroy.
	mux.HandleFunc("GET /api/voice", s.voiceStatus)
	mux.HandleFunc("POST /api/voice/stt", s.voiceSTT)
	mux.HandleFunc("POST /api/voice/tts", s.voiceTTS)

	mux.HandleFunc("GET /api/vault", s.listVault)
	mux.HandleFunc("POST /api/vault/{name}/restore", s.restoreVault)
	mux.HandleFunc("DELETE /api/vault/{name}", s.destroyVault)
	mux.HandleFunc("GET /api/projects/{id}/sessions", s.listSessions)
	mux.HandleFunc("POST /api/projects/{id}/sessions", s.launchSession)
	// Per-project private RAG: status/config + manual reindex.
	mux.HandleFunc("GET /api/projects/{id}/access", s.getProjectAccess)
	mux.HandleFunc("PUT /api/projects/{id}/access", s.updateProjectAccess)
	mux.HandleFunc("GET /api/projects/{id}/rag", s.getProjectRag)
	mux.HandleFunc("PATCH /api/projects/{id}/rag", s.updateProjectRag)
	mux.HandleFunc("POST /api/projects/{id}/rag/reindex", s.reindexProjectRag)
	mux.HandleFunc("GET /api/sessions", s.listAllSessions)
	mux.HandleFunc("GET /api/sessions/{id}", s.getSession)
	mux.HandleFunc("PATCH /api/sessions/{id}", s.setSessionComment)
	mux.HandleFunc("GET /api/sessions/{id}/journal", s.getJournal)
	mux.HandleFunc("POST /api/sessions/{id}/seen", s.markSessionSeen)
	mux.HandleFunc("POST /api/sessions/{id}/attachments", s.uploadAttachment)
	mux.HandleFunc("GET /api/usage", s.getUsage)
	mux.HandleFunc("GET /api/help", s.getHelp)
	mux.HandleFunc("GET /api/history", s.getHistory)
	mux.HandleFunc("GET /api/monitoring", s.getMonitoring)
	mux.HandleFunc("GET /api/host/metrics", s.getHostMetrics)
	mux.HandleFunc("GET /api/host/history", s.getHostHistory)
	mux.HandleFunc("GET /api/claude/history", s.getClaudeHistory)
	mux.HandleFunc("GET /api/ollama", s.getOllama)
	mux.HandleFunc("POST /api/ollama/unload", s.unloadOllamaModel)
	mux.HandleFunc("GET /api/settings", s.getSettings)
	mux.HandleFunc("PATCH /api/settings", s.updateSettings)
	mux.HandleFunc("GET /api/settings/network-info", s.getNetworkInfo)

	// Skills: global, transversal Claude Code skills. Creation spins up a
	// background authoring session streamed via the existing /api/sessions routes.
	mux.HandleFunc("GET /api/skills", s.listSkills)
	mux.HandleFunc("POST /api/skills", s.createSkill)
	mux.HandleFunc("GET /api/skills/{id}", s.getSkill)
	mux.HandleFunc("PATCH /api/skills/{id}", s.updateSkill)
	mux.HandleFunc("POST /api/skills/{id}/evolve", s.evolveSkill)
	mux.HandleFunc("POST /api/skills/{id}/close", s.closeSkillSession)
	mux.HandleFunc("DELETE /api/skills/{id}", s.deleteSkill)

	// RAG: global knowledge base. Documents are ingested and embedded
	// asynchronously, then queried by sessions through the rag-mcp tool.
	mux.HandleFunc("GET /api/rag/status", s.getRagStatus)
	mux.HandleFunc("GET /api/rag/collections", s.listRagCollections)
	mux.HandleFunc("POST /api/rag/collections", s.createRagCollection)
	mux.HandleFunc("PATCH /api/rag/collections/{id}", s.updateRagCollection)
	mux.HandleFunc("DELETE /api/rag/collections/{id}", s.deleteRagCollection)
	mux.HandleFunc("GET /api/rag/documents", s.listRagDocuments)
	mux.HandleFunc("POST /api/rag/documents", s.createRagDocument)
	mux.HandleFunc("POST /api/rag/documents/url", s.createRagURL)
	mux.HandleFunc("PATCH /api/rag/documents/{id}", s.updateRagDocument)
	mux.HandleFunc("DELETE /api/rag/documents/{id}", s.deleteRagDocument)
	mux.HandleFunc("POST /api/rag/documents/{id}/reindex", s.reindexRagDocument)
	mux.HandleFunc("GET /api/rag/documents/{id}/content", s.getRagDocumentContent)

	// Tools: global external integrations (Proxmox, Telegram, Email, …). Credentials
	// are encrypted at rest; secrets are never returned to the client.
	mux.HandleFunc("GET /api/tools/connectors", s.listConnectors)
	mux.HandleFunc("GET /api/tool-categories", s.listToolCategories)
	mux.HandleFunc("POST /api/tool-categories", s.createToolCategory)
	mux.HandleFunc("PUT /api/tool-categories/order", s.reorderToolCategories)
	mux.HandleFunc("PATCH /api/tool-categories/{id}", s.renameToolCategory)
	mux.HandleFunc("DELETE /api/tool-categories/{id}", s.deleteToolCategory)
	mux.HandleFunc("GET /api/tools", s.listTools)
	mux.HandleFunc("POST /api/tools", s.createTool)
	mux.HandleFunc("POST /api/tools/test", s.testToolConfig)
	mux.HandleFunc("POST /api/tools/detect-chat", s.detectChat)
	mux.HandleFunc("POST /api/tools/test-message", s.testMessage)
	mux.HandleFunc("GET /api/tools/{id}", s.getTool)
	mux.HandleFunc("PATCH /api/tools/{id}", s.updateTool)
	mux.HandleFunc("DELETE /api/tools/{id}", s.deleteTool)
	mux.HandleFunc("POST /api/tools/{id}/test", s.testTool)
	mux.HandleFunc("GET /api/tools/{id}/dashboard", s.toolDashboard)
	mux.HandleFunc("POST /api/tools/{id}/test-message", s.testMessageTool)
	// WhatsApp-specific: QR pairing status + connect trigger.
	mux.HandleFunc("GET /api/tools/{id}/qr", s.whatsappQR)
	mux.HandleFunc("POST /api/tools/{id}/connect", s.whatsappConnect)
	mux.HandleFunc("GET /api/tools/{id}/proton", s.protonStatus)
	mux.HandleFunc("POST /api/tools/{id}/proton/connect", s.protonConnect)
	mux.HandleFunc("POST /api/tools/{id}/proton/cancel", s.protonCancel)
	// Proton Bridge (mail): guided account add + managed IMAP instance.
	mux.HandleFunc("GET /api/proton-bridge", s.bridgeStatus)
	mux.HandleFunc("POST /api/proton-bridge/connect", s.bridgeConnect)
	mux.HandleFunc("POST /api/proton-bridge/cancel", s.bridgeCancel)
	mux.HandleFunc("POST /api/proton-bridge/disconnect", s.bridgeDisconnect)

	// API keys for machine clients (management is cookie-only, see auth/apikey.go).
	mux.HandleFunc("GET /api/apikeys", s.listAPIKeys)
	mux.HandleFunc("POST /api/apikeys", s.createAPIKey)
	mux.HandleFunc("DELETE /api/apikeys/{id}", s.deleteAPIKey)
	// Domain management: DNS + Let's Encrypt cert (DNS-01) + Caddy routing.
	mux.HandleFunc("GET /api/cloudflare/status", s.cloudflareStatus)
	mux.HandleFunc("GET /api/cloudflare/read/{kind}", s.cloudflareRead)
	mux.HandleFunc("POST /api/cloudflare/act", s.cloudflareAct)
	mux.HandleFunc("POST /api/cloudflare/tunnels/{id}/token", s.cloudflareTunnelToken)
	mux.HandleFunc("GET /api/cloudflare/audit", s.cloudflareAudit)
	mux.HandleFunc("POST /api/cloudflare/revoke", s.cloudflareRevoke)
	mux.HandleFunc("GET /api/dns/providers", s.dnsProviderStatus)
	mux.HandleFunc("PUT /api/dns/providers", s.dnsProviderSet)
	mux.HandleFunc("POST /api/dns/dry-run", s.dnsDryRun)
	mux.HandleFunc("GET /api/domains", s.listDomains)
	mux.HandleFunc("POST /api/domains", s.createDomain)
	mux.HandleFunc("GET /api/domains/{id}/status", s.domainStatus)
	mux.HandleFunc("DELETE /api/domains/{id}", s.deleteDomain)

	// Docker environments: Sandbox (ephemeral, web-exposed) and Live (persistent).
	// Containers are grouped per project via labels; actions drive their lifecycle.
	mux.HandleFunc("GET /api/docker/status", s.dockerStatus)
	mux.HandleFunc("GET /api/docker/containers", s.listContainers)
	mux.HandleFunc("POST /api/docker/containers", s.createContainer)
	mux.HandleFunc("POST /api/docker/containers/{id}/action", s.containerAction)
	mux.HandleFunc("DELETE /api/docker/containers/{id}", s.removeContainer)
	mux.HandleFunc("POST /api/docker/containers/{name}/domain", s.containerProvisionDomain)
	mux.HandleFunc("DELETE /api/docker/containers/{name}/domain", s.containerRemoveDomain)

	// Claude Code subscription management (logout / login / switch + isolation).
	mux.HandleFunc("GET /api/claude/accounts", s.listAccounts)
	mux.HandleFunc("PATCH /api/claude/accounts/{id}", s.renameAccount)
	mux.HandleFunc("POST /api/claude/login", s.connectAccount)
	mux.HandleFunc("POST /api/claude/switch", s.switchAccount)
	mux.HandleFunc("POST /api/claude/logout", s.logoutAccount)
	mux.HandleFunc("POST /api/claude/accounts/{id}/login/start", s.startLogin)
	mux.HandleFunc("POST /api/claude/accounts/{id}/login/finish", s.finishLogin)
	mux.HandleFunc("DELETE /api/claude/accounts/{id}", s.deleteAccount)
	mux.HandleFunc("GET /api/tasks", s.listTasks)
	mux.HandleFunc("POST /api/tasks", s.createTask)
	mux.HandleFunc("PATCH /api/tasks/{id}", s.updateTask)
	mux.HandleFunc("DELETE /api/tasks/{id}", s.deleteTask)
	mux.HandleFunc("POST /api/tasks/{id}/run", s.runTaskNow)
	mux.HandleFunc("DELETE /api/sessions/{id}", s.deleteSession)
	mux.HandleFunc("POST /api/sessions/{id}/stop", s.stopSession)
	mux.HandleFunc("POST /api/sessions/{id}/archive", s.archiveSession)
	mux.HandleFunc("POST /api/sessions/{id}/unarchive", s.unarchiveSession)
	mux.HandleFunc("POST /api/sessions/{id}/plan-decision", s.decidePlan)
	mux.HandleFunc("POST /api/sessions/{id}/answer", s.answerQuestion)

	// Personal assistant: one thread, place triggers, its own tasks (plain /api/tasks
	// with the assistant project id).
	mux.HandleFunc("GET /api/assistant", s.assistantStatus)
	mux.HandleFunc("GET /api/assistant/messages", s.assistantMessages)
	mux.HandleFunc("POST /api/assistant/messages", s.assistantSend)
	mux.HandleFunc("POST /api/assistant/attachments", s.assistantUpload)
	mux.HandleFunc("GET /api/assistant/attachments/{id}", s.assistantAttachment)
	mux.HandleFunc("POST /api/assistant/reset", s.assistantReset)
	mux.HandleFunc("PUT /api/assistant/settings", s.assistantSettings)
	mux.HandleFunc("GET /api/assistant/entities", s.assistantEntities)
	mux.HandleFunc("GET /api/assistant/triggers", s.assistantListTriggers)
	mux.HandleFunc("POST /api/assistant/triggers", s.assistantCreateTrigger)
	mux.HandleFunc("PATCH /api/assistant/triggers/{id}", s.assistantUpdateTrigger)
	mux.HandleFunc("DELETE /api/assistant/triggers/{id}", s.assistantDeleteTrigger)
	mux.HandleFunc("POST /api/assistant/triggers/{id}/fire", s.assistantFireTrigger)

	// Phones to notify when a session needs the user (see internal/push).
	mux.HandleFunc("POST /api/push/endpoints", s.registerPushEndpoint)
	mux.HandleFunc("DELETE /api/push/endpoints", s.unregisterPushEndpoint)

	// WebSocket stream
	mux.HandleFunc("GET /api/sessions/{id}/stream", s.streamSession)

	// GitHub webhook for site auto-deploy (no Hub auth — validated via HMAC-SHA256)
	mux.HandleFunc("POST /webhook/site-deploy", s.siteDeployWebhook)

	// Script status: the public endpoint (under /webhook/, outside the Hub login,
	// protected by its own per-script API key) and the cookie-gated admin API.
	if s.scripts != nil {
		mux.HandleFunc("POST /webhook/script-status/{script}", s.scripts.Receive)
		mux.HandleFunc("POST /webhook/script-status/{script}/image", s.scripts.ReceiveImage)
		mux.HandleFunc("GET /api/script-images/{script}", s.scripts.ImageListHandler)
		mux.HandleFunc("GET /api/script-images/{script}/{id}", s.scripts.ImageGetHandler)
		mux.HandleFunc("DELETE /api/script-images/{script}", s.scripts.ImageDeleteHandler)
		mux.HandleFunc("GET /api/script-keys", s.scripts.ListKeysHandler)
		mux.HandleFunc("POST /api/script-keys", s.scripts.CreateKeyHandler)
		mux.HandleFunc("PATCH /api/script-keys/{id}", s.scripts.UpdateKeyHandler)
		mux.HandleFunc("POST /api/script-keys/{id}/regenerate", s.scripts.RegenerateKeyHandler)
		mux.HandleFunc("DELETE /api/script-keys/{id}", s.scripts.DeleteKeyHandler)
		mux.HandleFunc("GET /api/script-status", s.scripts.StatusListHandler)
		mux.HandleFunc("GET /api/script-status/{script}", s.scripts.StatusGetHandler)
		mux.HandleFunc("GET /api/script-dashboard", s.scripts.DashboardHandler)
		mux.HandleFunc("GET /api/script-commands", s.scripts.ListCommandsHandler)
		mux.HandleFunc("POST /api/script-commands", s.scripts.SendCommandHandler)
		mux.HandleFunc("DELETE /api/script-commands/{id}", s.scripts.CancelCommandHandler)
	}

	// Remote MCP connector: public routes (own OAuth, outside the Hub login) + the
	// cookie-only management API.
	if s.mcpRemote != nil {
		s.mcpRemote.Register(mux)
		s.mcpRemote.RegisterAdmin(mux)
	}

	// Auth endpoints (when auth is on). Local login + TOTP, the first-boot setup
	// wizard, and the optional OIDC flow. All live outside the middleware gate.
	if s.auth != nil {
		mux.HandleFunc("GET /auth/me", s.auth.WhoamiHandler)
		mux.HandleFunc("POST /auth/logout", s.auth.LogoutHandler)
		// Signed-in devices (cookie-only: see auth.apiKey).
		mux.HandleFunc("GET /api/devices", s.auth.DevicesHandler)
		mux.HandleFunc("DELETE /api/devices", s.auth.RevokeOtherDevicesHandler)
		mux.HandleFunc("DELETE /api/devices/{id}", s.auth.RevokeDeviceHandler)
		// Local accounts: password step then mandatory TOTP step.
		mux.HandleFunc("POST /auth/local-login", s.auth.LocalLoginHandler)
		mux.HandleFunc("POST /auth/totp", s.auth.TOTPVerifyHandler)
		// Optional OIDC.
		mux.HandleFunc("GET /auth/login", s.auth.LoginHandler)
		mux.HandleFunc("GET /auth/callback", s.auth.CallbackHandler)
		// First-boot setup wizard (locked once completed).
		mux.HandleFunc("GET /setup/status", s.auth.SetupStatusHandler)
		mux.HandleFunc("POST /setup/admin", s.auth.SetupAdminHandler)
		mux.HandleFunc("POST /setup/totp/confirm", s.auth.SetupTOTPConfirmHandler)
		mux.HandleFunc("POST /setup/options", s.auth.SetupOptionsHandler)
		// Optional Claude-subscription step (uses the account service on the Server,
		// so it is registered here rather than on the Authenticator).
		mux.HandleFunc("POST /setup/claude/connect", s.setupConnectClaude)
		mux.HandleFunc("POST /setup/claude/{id}/login/start", s.setupClaudeLoginStart)
		mux.HandleFunc("POST /setup/claude/{id}/login/finish", s.setupClaudeLoginFinish)
		mux.HandleFunc("POST /setup/finish", s.auth.SetupFinishHandler)
	}

	// Sandbox/Live web access: reverse-proxy to a container's published port.
	// Registered before the SPA catch-all; stays behind the auth middleware.
	if s.docker != nil {
		mux.HandleFunc("/x/{name}/{rest...}", s.sandboxProxy)
		mux.HandleFunc("/x/{name}", s.sandboxRedirect)
	}

	// Static SPA (everything else)
	mux.Handle("/", s.spaHandler())

	// Gate everything behind the session cookie when auth is on. The WS upgrade is
	// an ordinary GET carrying the cookie, so it is covered without special-casing.
	if s.auth != nil {
		return s.auth.Middleware(mux)
	}
	return mux
}

// spaHandler serves the embedded build, falling back to index.html for unknown
// paths so client-side routing works.
func (s *Server) spaHandler() http.Handler {
	sub, _ := fs.Sub(assets, "assets")
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		isAsset := false
		if path != "/" {
			if _, err := fs.Stat(sub, trimLeadingSlash(path)); err != nil {
				// Unknown path → SPA entry point.
				r.URL.Path = "/"
			} else {
				isAsset = true
			}
		}
		// embed.FS files have a zero ModTime, so http.FileServer emits no
		// Last-Modified/ETag — without an explicit Cache-Control, browsers
		// (notably iOS Safari) heuristically cache index.html and keep loading a
		// stale build's asset hashes after a deploy. Hashed assets under /assets/
		// are content-addressed and safe to cache forever; the HTML entry point
		// must always be revalidated so a new build is picked up immediately.
		if isAsset && strings.HasPrefix(path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	})
}

func trimLeadingSlash(p string) string {
	if len(p) > 0 && p[0] == '/' {
		return p[1:]
	}
	return p
}
