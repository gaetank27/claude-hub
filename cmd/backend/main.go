// Command backend is the system hub web server: REST API + WebSocket + embedded
// React frontend. It is stateless with respect to live sessions — those run in
// decoupled systemd-managed runners — so it can be killed, restarted or
// recompiled (self-evolution) without interrupting work.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"crypto/rand"
	"encoding/hex"

	"system/internal/api"
	"system/internal/ask"
	"system/internal/assistant"
	"system/internal/auth"
	"system/internal/bootstrap"
	"system/internal/claudemetrics"
	"system/internal/config"
	"system/internal/docker"
	"system/internal/hostmetrics"
	"system/internal/imgsafe"
	"system/internal/mcpremote"
	"system/internal/ollama"
	"system/internal/push"
	"system/internal/rag"
	"system/internal/scheduler"
	"system/internal/scriptstatus"
	"system/internal/secretbox"
	"system/internal/session"
	"system/internal/store"
	"system/internal/tools"
	"system/internal/usage"
	"system/internal/voice"
	"system/internal/whatsapp"
)

func main() {
	cfg := config.Load()

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		log.Fatalf("data dir: %v", err)
	}

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer db.Close()

	// Prefer the public base URL saved by the setup wizard over the env default,
	// but let SYSTEM_PUBLIC_BASE_URL take explicit precedence when set.
	if _, ok := os.LookupEnv("SYSTEM_PUBLIC_BASE_URL"); !ok {
		if v, err := db.GetAppMeta("public_base_url"); err == nil && v != "" {
			cfg.PublicBaseURL = v
		}
	}

	if err := bootstrap.EnsureSystemProject(db); err != nil {
		log.Fatalf("bootstrap system project: %v", err)
	}
	// Seed the default Claude subscription (pointing at the pre-existing config
	// dir) so multi-account isolation has a starting point and legacy sessions
	// stay attributed.
	if err := bootstrap.EnsureDefaultClaudeAccount(db, cfg.CredentialsPath); err != nil {
		log.Fatalf("bootstrap claude account: %v", err)
	}
	// Seed the hidden `skills` workspace (rooted at the shared skills dir) that
	// hosts skill-authoring sessions.
	if err := bootstrap.EnsureSkillsProject(db, cfg.CredentialsPath); err != nil {
		log.Fatalf("bootstrap skills project: %v", err)
	}
	// Seed the default RAG knowledge base (and adopt any pre-collections documents).
	if err := bootstrap.EnsureDefaultRagCollection(db); err != nil {
		log.Fatalf("bootstrap rag collection: %v", err)
	}
	// Ensure every project has its own private RAG collection + index config.
	if err := db.SeedSessionSeen(); err != nil {
		log.Printf("seed session_seen: %v", err)
	}
	if err := bootstrap.EnsureProjectRag(db); err != nil {
		log.Fatalf("bootstrap project rag: %v", err)
	}

	// Token gating the loopback endpoints, shared with the MCP servers the manager
	// wires into each session. Persisted (0600 file in DataDir, or SYSTEM_LOOPBACK_TOKEN
	// env) so it survives a backend restart: live sessions bake the token into their
	// already-spawned MCP child processes at launch, so a per-boot random token would
	// 403 them on /tools/list, /search and /ask until they are relaunched.
	ragToken, err := loadOrCreateLoopbackToken(filepath.Join(cfg.DataDir, "loopback.token"))
	if err != nil {
		log.Fatalf("loopback token: %v", err)
	}

	mgr, err := session.NewManager(db, session.Config{
		DataDir:         cfg.DataDir,
		ProjectsDir:     cfg.ProjectsDir,
		PublicBaseURL:   cfg.PublicBaseURL,
		VaultDir:        cfg.VaultDir,
		RunnerBin:       cfg.RunnerBin,
		ClaudeBin:       cfg.ClaudeBin,
		CredentialsPath: cfg.CredentialsPath,
		OAuthTokenURL:   cfg.OAuthTokenURL,
		OAuthClientID:   cfg.OAuthClientID,
		PermissionMode:  cfg.PermissionMode,
		Model:           cfg.Model,
		MemoryMax:       cfg.MemoryMax,
		CPUQuota:        cfg.CPUQuota,
		MaxTurns:        cfg.MaxTurns,
		Inactivity:      cfg.Inactivity,
		// The manager composes a per-project claude --mcp-config at launch (RAG
		// knowledge + tools + the project's private code index) from these.
		RagMCPBin:   cfg.RagMCPBin,
		ToolsMCPBin: cfg.ToolsMCPBin,
		AskMCPBin:   cfg.AskMCPBin,
		RagAddr:     cfg.RagAddr,
		RagToken:    ragToken,
	})
	if err != nil {
		log.Fatalf("session manager: %v", err)
	}
	// Overlay any UI-edited runtime settings onto the env defaults.
	if err := mgr.LoadSettings(); err != nil {
		log.Printf("load settings: %v", err)
	}
	// Share the Claude context (CLAUDE.md, skills, commands, …) across every
	// subscription so switching accounts keeps the same memory and tooling; only
	// the per-account secrets stay isolated. Best-effort, repaired each boot.
	if err := mgr.SyncAllAccounts(); err != nil {
		log.Printf("share claude context: %v", err)
	}
	// Apply the output-truncation hook to all account settings.json files based
	// on the persisted setting (idempotent — rewrites the script on each boot so
	// the hook stays up-to-date after a backend upgrade).
	if err := mgr.SyncOutputTruncation(mgr.Settings().OutputTruncation); err != nil {
		log.Printf("sync output truncation hook: %v", err)
	}

	// Reattach to / resume sessions that were running before this process started.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := mgr.Recover(ctx); err != nil {
		log.Printf("session recovery: %v", err)
	}
	// Fill the active account's org/email from the CLI (best-effort, off the hot path).
	go mgr.EnrichActiveAccount()
	// Reconcile stored state with reality for the lifetime of the process, so a
	// runner that dies on its own does not leave its session stuck on "running".
	go mgr.RunReconciler(ctx, 0)
	go mgr.RunAutoArchive(ctx)

	// Encryption box for secrets at rest: connector credentials AND the local
	// operators' TOTP secrets. The key lives outside the DB (key file or
	// SYSTEM_SECRET_KEY) so a DB dump alone never leaks plaintext. Opened before
	// auth because the local-account 2FA enrolment encrypts the TOTP secret with it.
	secrets, err := secretbox.Open(filepath.Join(cfg.DataDir, "secret.key"))
	if err != nil {
		log.Fatalf("secretbox: %v", err)
	}

	// Auth gate: local accounts (login/password + mandatory TOTP 2FA) plus an
	// optional OIDC path. Always on once the first-boot wizard has created an admin
	// (until then the wizard is served); the cookie HMAC key is persisted so
	// sessions survive a restart. TLS is terminated by the reverse proxy in front,
	// so the backend stays plain HTTP on loopback.
	// Stateful connectors (Proton session tokens) persist their state encrypted.
	tools.SetConfigUpdater(tools.NewConfigUpdater(db, secrets))
	// Sensitive connector actions (Cloudflare writes) are journaled, 1 year kept.
	tools.SetAuditSink(func(e tools.AuditEntry) {
		db.AddConnectorAudit(store.ConnectorAudit{Connector: e.Connector, Action: e.Action, Source: e.Source, Args: e.Args, Outcome: e.Outcome, Detail: e.Detail})
	})
	db.PurgeConnectorAudit(365 * 24 * time.Hour)

	authn := buildAuthenticator(ctx, cfg, db, secrets)

	// The credentials path follows the active subscription (switchable at runtime),
	// so the usage meter and sampler always read the current account's token.
	usageClient := usage.New(mgr.ActiveCredentialsPath)
	// Snapshot the plan usage on a cadence so Monitoring can chart it over time:
	// the upstream endpoint only reports the current instant, so history must be
	// accumulated (unlike session activity, which is derived from the journals).
	go runUsageSampler(ctx, db, usageClient, mgr)
	// Keep idle accounts' OAuth tokens fresh so the usage meter survives a token
	// expiring while no session is running under that subscription.
	go runTokenRefresher(ctx, mgr)
	// Guard the 5-hour subscription window: pause an account's live sessions when it
	// nears its limit (clean shutdown) and resume them at the next window reset.
	go runSessionGuard(ctx, db, usageClient, mgr, secrets)

	// RAG knowledge base: asynchronous indexing worker + a loopback-only retrieval
	// endpoint queried by the rag-mcp tool. The endpoint is gated by a per-boot
	// token shared with the MCP config the runner injects into every session.
	emb, embOK := rag.NewEmbedderFromEnv()
	if embOK {
		log.Printf("rag: embeddings via %s", emb.Name())
	} else {
		log.Printf("rag: no embeddings provider configured (set VOYAGE_API_KEY); documents stay pending")
	}
	indexer := rag.NewIndexer(db, emb, cfg.DataDir)
	// Detect an embedding-model change (different vector dimension) and force a
	// full re-embed before the worker starts, so old vectors are never mixed in.
	indexer.ReconcileEmbedder()
	go indexer.Run(ctx)
	// Per-project RAG: ensure each project has an index and keep it fresh
	// (never-indexed, git HEAD change, or interval), all asynchronously.
	go indexer.RunProjectScheduler(ctx, 30*time.Second)

	// One loopback-only listener serves both the RAG retrieval endpoints and the
	// external-tools action endpoints, gated by the same per-boot token shared with
	// the MCP servers the runner injects into every session.
	loop := http.NewServeMux()
	rag.RegisterLoopback(loop, indexer, ragToken)
	tools.RegisterLoopback(loop, db, secrets, ragToken)
	ask.RegisterLoopback(loop, mgr.Asks(), ragToken)
	ragSrv := &http.Server{Addr: cfg.RagAddr, Handler: loop}
	go func() {
		log.Printf("hub loopback endpoint on %s (rag + tools + ask)", cfg.RagAddr)
		if err := ragSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("loopback listen: %v", err)
		}
	}()

	// Docker environments (Sandbox/Live). Best-effort: a missing/unreachable daemon
	// is surfaced through /api/docker/status, the rest of the hub runs regardless.
	dockerClient := docker.New(cfg.DockerHost)
	if _, err := dockerClient.Ping(ctx); err != nil {
		log.Printf("docker: daemon unreachable (%v) — Sandbox/Live disabled until available", err)
	} else {
		for _, net := range []string{"claudehub_sandbox", "claudehub_live"} {
			if err := dockerClient.EnsureNetwork(ctx, net); err != nil {
				log.Printf("docker: ensure network %s: %v", net, err)
			}
		}
	}

	// Host metrics (CPU/RAM/network/disk) sampled into a fixed-size RRD so the Suivi
	// page can show live gauges + historical charts. Best-effort: if rrdtool is
	// absent the sampler no-ops and only the history (not the live gauges) degrades.
	hostRRD := hostmetrics.NewStore(filepath.Join(cfg.DataDir, "rrd", "host.rrd"), cfg.RRDToolBin, cfg.HostSampleSeconds)
	if hostRRD.Available() {
		if err := hostRRD.EnsureCreated(ctx); err != nil {
			log.Printf("hostmetrics: rrd create: %v", err)
		}
		go runHostSampler(ctx, hostRRD, time.Duration(cfg.HostSampleSeconds)*time.Second)
	} else {
		log.Printf("hostmetrics: rrdtool %q not found — host history disabled (live gauges still work)", cfg.RRDToolBin)
	}

	// Claude usage metrics (tokens/cost/sessions) sampled per subscription into
	// per-account RRD files for the Suivi account-filtered history. Pure DB/journal
	// reads (no OAuth usage calls — that endpoint rate-limits).
	claudeRRD := claudemetrics.NewStore(filepath.Join(cfg.DataDir, "rrd", "claude"), cfg.RRDToolBin, cfg.HostSampleSeconds)
	if claudeRRD.Available() {
		go runClaudeSampler(ctx, db, mgr, claudeRRD, time.Duration(cfg.HostSampleSeconds)*time.Second)
	} else {
		log.Printf("claudemetrics: rrdtool %q not found — Claude usage history disabled", cfg.RRDToolBin)
	}

	go scheduler.Run(ctx, db, mgr, "system")
	// Push to the user's phones the questions and plans waiting for their answer.
	pusher := push.New(db)
	go pusher.Run(ctx)

	// WhatsApp manager: start persistent connections for already-paired tools.
	waMgr := whatsapp.New(cfg.DataDir, mgr)
	tools.SetWhatsAppManager(waMgr)
	if tls, err := db.ListTools(); err == nil {
		for _, t := range tls {
			if t.Type == "whatsapp" && t.Enabled {
				cfg2 := decryptToolCfg(secrets, t.ConfigEnc)
				botMode := cfg2["bot_mode"] == "true"
				botProject := cfg2["bot_project_id"]
				allowedNumbers := parseAllowedNumbersMain(cfg2["allowed_numbers"])
				if err := waMgr.Ensure(t.ID, botMode, botProject, allowedNumbers); err != nil {
					log.Printf("whatsapp init %s: %v", t.Name, err)
				}
			}
		}
	}

	// Personal assistant: owns the WhatsApp bot conversation, the scheduled tasks of
	// its project and the place triggers.
	asst := assistant.New(db, mgr, waMgr, secrets)
	asst.SetRetention(time.Duration(cfg.AssistantAttachmentDays) * 24 * time.Hour)
	waMgr.SetAssistant(asst)
	waMgr.SetObserver(asst)
	asst.SetAskState(mgr.Asks())
	asst.SetPhoneNotifier(pusher.NotifyAssistant)
	tools.SetHubBridge(asst)
	// Images the assistant may send: files under the projects dir and a dedicated
	// temp dir; the Hub's data dir, ~/.ssh, ~/.config… are refused (imgsafe).
	imgTmp := filepath.Join(os.TempDir(), "hub-assistant-images")
	_ = os.MkdirAll(imgTmp, 0o700)
	home, _ := os.UserHomeDir()
	imgGuard := imgsafe.NewGuard(imgsafe.DefaultPolicy(cfg.ProjectsDir, home, cfg.DataDir, imgTmp))
	imgGuard.Limiter = imgsafe.NewLimiter(cfg.AssistantImagesPerHour)
	asst.SetImageGuard(imgGuard)
	tools.SetImageGuard(imgGuard)
	scheduler.SetAssistant(asst)
	go asst.Run(ctx)

	// Script status endpoint (POST /webhook/script-status/{script}).
	scriptSvc := scriptstatus.New(db, cfg.DataDir)
	asst.SetScripts(scriptSvc)
	go scriptSvc.Run(ctx)

	// Remote MCP connector (claude.ai custom connector): off until enabled in the
	// UI, and never served when auth is disabled.
	var mcpSvc *mcpremote.Service
	if authn != nil {
		mcpSvc = mcpremote.New(db, secrets, authn)
		go func() {
			for {
				mcpSvc.Purge()
				select {
				case <-ctx.Done():
					return
				case <-time.After(6 * time.Hour):
				}
			}
		}()
	}

	srv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: api.NewServer(mgr, db, authn, allowedOrigins(cfg), usageClient, indexer, secrets, ollama.New(cfg.OllamaHost), dockerClient, cfg.PublicBaseURL, hostRRD, claudeRRD, waMgr, voice.New(cfg.DataDir)).WithAssistant(asst).WithScripts(scriptSvc).WithMCPRemote(mcpSvc).Handler(),
	}

	go func() {
		log.Printf("system backend listening on %s (data: %s)", cfg.ListenAddr, cfg.DataDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down backend (runners keep running)")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutCtx)
	ragSrv.Shutdown(shutCtx)
}

// randToken returns a random hex token used to gate the loopback RAG endpoint.
func randToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "rag-local-dev-token" // dev fallback; the endpoint is loopback-only anyway
	}
	return hex.EncodeToString(b)
}

// loadOrCreateLoopbackToken returns the token gating the loopback RAG/tools/ask
// endpoints. It prefers SYSTEM_LOOPBACK_TOKEN, otherwise reads the token file at
// path, minting and persisting (0600) a fresh one on first run. Persisting it is
// what lets the token survive a backend restart: a session's MCP child processes
// bake the token into their env at launch, so a per-boot random token would 403
// every already-running session until it is relaunched. Mirrors secretbox's key
// file handling.
func loadOrCreateLoopbackToken(path string) (string, error) {
	if env := strings.TrimSpace(os.Getenv("SYSTEM_LOOPBACK_TOKEN")); env != "" {
		return env, nil
	}
	if b, err := os.ReadFile(path); err == nil {
		if tok := strings.TrimSpace(string(b)); tok != "" {
			return tok, nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	tok := randToken()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(tok), 0o600); err != nil {
		return "", fmt.Errorf("write token file: %w", err)
	}
	return tok, nil
}

// runUsageSampler periodically records the Claude subscription usage windows
// into the store so the Monitoring page can plot their history. It samples once
// shortly after boot, then on a fixed cadence, and prunes old rows to bound the
// table. Unavailable usage (missing/expired token, upstream down) is skipped
// quietly — the chart just shows a gap.
func runUsageSampler(ctx context.Context, db *store.DB, client *usage.Client, mgr *session.Manager) {
	const interval = 10 * time.Minute
	const retention = 90 * 24 * time.Hour

	// sampleAccount snapshots one subscription's usage windows. Per-account (not
	// just the default) so the per-project meter's stale fallback works for any
	// subscription a project runs on.
	sampleAccount := func(acct store.ClaudeAccount) {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		u, err := client.GetCachedFor(cctx, mgr.AccountCredentialsPath(acct))
		if err != nil {
			return
		}
		snap := store.UsageSnapshot{TS: time.Now(), AccountID: acct.ID}
		if u.Session != nil {
			v := u.Session.Utilization
			snap.Session = &v
			snap.SessionReset = u.Session.ResetsAt
		}
		if u.Week != nil {
			v := u.Week.Utilization
			snap.Week = &v
			snap.WeekReset = u.Week.ResetsAt
		}
		if u.WeekOpus != nil {
			v := u.WeekOpus.Utilization
			snap.WeekOpus = &v
			snap.WeekOpusReset = u.WeekOpus.ResetsAt
		}
		if u.WeekSonnet != nil {
			v := u.WeekSonnet.Utilization
			snap.WeekSonnet = &v
			snap.WeekSonnetReset = u.WeekSonnet.ResetsAt
		}
		// Nothing tracked → don't write an empty row.
		if snap.Session == nil && snap.Week == nil && snap.WeekOpus == nil && snap.WeekSonnet == nil {
			return
		}
		if err := db.InsertUsageSnapshot(snap); err != nil {
			log.Printf("usage snapshot: %v", err)
		}
	}

	sample := func() {
		accts, err := mgr.Accounts()
		if err != nil {
			return
		}
		for _, a := range accts {
			if a.Status == store.AccountConnected && a.Kind == store.AccountKindOAuth {
				sampleAccount(a)
			}
		}
		_ = db.PruneUsageSnapshots(time.Now().Add(-retention))
	}

	t := time.NewTimer(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sample()
			t.Reset(interval)
		}
	}
}

// runTokenRefresher periodically refreshes the OAuth token of idle connected
// accounts (those with no live session), so the usage meter keeps working even
// when the active subscription has no session keeping its token warm. Runs once
// shortly after boot, then on a fixed cadence. All work is best-effort.
func runTokenRefresher(ctx context.Context, mgr *session.Manager) {
	const interval = 5 * time.Minute
	t := time.NewTimer(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			mgr.RefreshIdleTokens(ctx)
			t.Reset(interval)
		}
	}
}

// sessionPauseThreshold is the 5-hour-window utilization (percent) at which the
// guard pauses an account's sessions, leaving a small margin before the hard cap
// so the shutdown is clean rather than a mid-turn upstream rejection.
const sessionPauseThreshold = 95.0

// sessionWarnThreshold is the utilization (percent) at which the guard sends a
// Telegram notification. One notification per window (tracked via warnedAt map).
const sessionWarnThreshold = 80.0

// runSessionGuard polls every connected subscription's 5-hour window and, when one
// crosses sessionPauseThreshold, cleanly pauses that account's live sessions; it
// resumes them at the next window reset. The pause is scoped to the account that
// hit its limit — another subscription with quota left keeps running. The resume
// pass is network-free (it acts on the reset time captured at pause), so paused
// work restarts even if the usage endpoint is unreachable at reset time.
func runSessionGuard(ctx context.Context, db *store.DB, client *usage.Client, mgr *session.Manager, box *secretbox.Box) {
	const interval = 60 * time.Second

	// warnedAt maps accountID → the resetsAt time of the window we last warned on.
	// We send at most one Telegram notification per 5-hour window.
	warnedAt := make(map[string]time.Time)

	tick := func() {
		// Resume first (cheap, no network): restart anything whose window has reset.
		mgr.ResumeDueSessions(ctx)

		accts, err := db.ListClaudeAccounts()
		if err != nil {
			return
		}
		for _, a := range accts {
			// Only OAuth subscriptions have a 5-hour window to read; skip logged-out
			// accounts (no usable token).
			if a.Kind != store.AccountKindOAuth || a.Status != store.AccountConnected {
				continue
			}
			cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			// Cached read (per-path TTL): the guard, sampler and per-project meter
			// share one upstream call per account per TTL, so we don't self-trigger
			// the usage endpoint's rate limit (HTTP 429). A ≤60s-stale window is fine
			// for a 95% pause threshold the guard re-checks every 60s anyway.
			u, err := client.GetCachedFor(cctx, mgr.AccountCredentialsPath(a))
			cancel()
			// Unreachable / expired token / no active window → can't measure, skip.
			if err != nil || u.Session == nil {
				continue
			}

			if u.Session.Utilization >= sessionWarnThreshold {
				var resetAt time.Time
				if u.Session.ResetsAt != nil {
					resetAt = *u.Session.ResetsAt
				}
				// Warn once per window (identified by its reset time).
				if warnedAt[a.ID].IsZero() || !warnedAt[a.ID].Equal(resetAt) {
					warnedAt[a.ID] = resetAt
					msg := fmt.Sprintf(
						"⚠ Abonnement \"%s\" : utilisation à %.0f%% (réinitialisation dans %s). Les sessions seront mises en pause automatiquement à %.0f%%.",
						a.Label,
						u.Session.Utilization,
						time.Until(resetAt).Round(time.Minute),
						sessionPauseThreshold,
					)
					go sendGuardTelegram(ctx, db, box, msg)
				}
			}

			if u.Session.Utilization >= sessionPauseThreshold {
				var reset time.Time
				if u.Session.ResetsAt != nil {
					reset = *u.Session.ResetsAt
				}
				mgr.PauseAccountSessions(ctx, a.ID, reset)
			}
		}
	}

	t := time.NewTimer(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
			t.Reset(interval)
		}
	}
}

// sendGuardTelegram delivers msg via the first enabled Telegram connector found in
// the DB. Best-effort: errors are logged and swallowed so the guard never blocks.
func sendGuardTelegram(ctx context.Context, db *store.DB, box *secretbox.Box, msg string) {
	all, err := db.ListTools()
	if err != nil {
		return
	}
	for _, tool := range all {
		if tool.Type != "telegram" || !tool.Enabled || tool.Status != store.ToolConnected {
			continue
		}
		conn, ok := tools.Get("telegram")
		if !ok {
			return
		}
		b, err := box.Decrypt(tool.ConfigEnc)
		if err != nil {
			log.Printf("guard telegram: decrypt config: %v", err)
			return
		}
		var cfg map[string]string
		if err := json.Unmarshal(b, &cfg); err != nil {
			log.Printf("guard telegram: unmarshal config: %v", err)
			return
		}
		tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if _, err := conn.Call(tctx, cfg, "telegram_send_message", map[string]any{"text": msg}); err != nil {
			log.Printf("guard telegram: send: %v", err)
		}
		cancel()
		return // send to first configured connector only
	}
}

// runHostSampler reads host metrics on a fixed cadence and appends them to the
// RRD. It primes the collector first so the very first stored sample already
// carries network/disk rates. Errors are logged and skipped (a gap in history).
func runHostSampler(ctx context.Context, store *hostmetrics.Store, interval time.Duration) {
	c := hostmetrics.New()
	_, _ = c.Snapshot() // prime the rate baseline
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s, err := c.Snapshot()
			if err != nil {
				log.Printf("hostmetrics: %v", err)
				continue
			}
			uctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			if err := store.Update(uctx, s); err != nil {
				log.Printf("hostmetrics: rrd update: %v", err)
			}
			cancel()
		}
	}
}

// runClaudeSampler appends each Claude account's cumulative usage (tokens, cost,
// live sessions) to its per-account RRD on a fixed cadence. It primes once so the
// DERIVE counters get a baseline quickly. No external API calls.
func runClaudeSampler(ctx context.Context, db *store.DB, mgr *session.Manager, store *claudemetrics.Store, interval time.Duration) {
	sample := func() {
		accts, err := db.ListClaudeAccounts()
		if err != nil {
			return
		}
		for _, a := range accts {
			t := mgr.AccountUsageTotals(a.ID)
			uctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			if err := store.Update(uctx, a.ID, claudemetrics.Sample{
				TokIn: t.InputTokens, TokOut: t.OutputTokens, Cost: t.CostUSD, SessActive: t.Active,
			}); err != nil {
				log.Printf("claudemetrics: update %s: %v", a.ID, err)
			}
			cancel()
		}
	}
	sample()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sample()
		}
	}
}

// allowedOrigins is the set of browser origin hosts accepted on the WebSocket
// upgrade. The public host is derived from the OIDC redirect URL (the single
// source of truth for where the hub is served), plus any extra hosts from
// SYSTEM_ALLOWED_ORIGINS. Empty when neither is set (dev), which tells the
// upgrade to skip the origin check so a local browser still connects.
func allowedOrigins(cfg config.Config) []string {
	out := append([]string(nil), cfg.AllowedOrigins...)
	// The public host the hub is served on, for the WS origin check. Local-auth
	// deployments behind the bundled reverse proxy have no OIDC redirect URL, so
	// the public base URL is the source of truth there.
	for _, raw := range []string{cfg.OIDCRedirectURL, cfg.PublicBaseURL} {
		if raw == "" {
			continue
		}
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			out = append([]string{u.Host}, out...)
		}
	}
	return out
}

// buildAuthenticator constructs the always-on auth gate (local accounts + TOTP,
// optional OIDC), or returns nil only when explicitly disabled for dev. The
// cookie-signing key is persisted so sessions survive a restart, and OIDC — when
// configured via the wizard or the environment — is discovered best-effort so a
// bad/unreachable issuer never blocks boot (local auth still works).
func buildAuthenticator(ctx context.Context, cfg config.Config, db *store.DB, secrets *secretbox.Box) *auth.Authenticator {
	if cfg.DisableAuth {
		log.Printf("auth: DISABLED (SYSTEM_DISABLE_AUTH=1) — serving an open hub (dev only)")
		return nil
	}
	cookieSecret, err := loadOrCreateSessionSecret(cfg, filepath.Join(cfg.DataDir, "session.key"))
	if err != nil {
		log.Fatalf("auth: session secret: %v", err)
	}
	authn := auth.New(auth.Config{
		CookieSecret: cookieSecret,
		DB:           db,
		Secrets:      secrets,
		TOTPIssuer:   cfg.TOTPIssuer,
	})
	if s, ok := oidcSettings(db, cfg); ok {
		if err := authn.ConfigureOIDC(ctx, s); err != nil {
			log.Printf("auth: OIDC init failed (%v) — OIDC sign-in disabled until reconfigured", err)
		} else {
			log.Printf("auth: OIDC enabled (issuer %s)", s.Issuer)
		}
	}
	if n, err := db.CountUsers(); err == nil && n == 0 && !authn.SetupComplete() {
		log.Printf("auth: first boot — setup wizard active until an admin is created")
	} else {
		log.Printf("auth: local accounts enabled")
	}
	return authn
}

// oidcSettings resolves the OIDC parameters, preferring the wizard-saved values
// (app_meta) over the environment. The bool is false when OIDC is not configured.
func oidcSettings(db *store.DB, cfg config.Config) (auth.OIDCSettings, bool) {
	if v, err := db.GetAppMeta("oidc_enabled"); err == nil && v == "true" {
		issuer, _ := db.GetAppMeta("oidc_issuer")
		if issuer != "" {
			clientID, _ := db.GetAppMeta("oidc_client_id")
			secret, _ := db.GetAppMeta("oidc_client_secret")
			redirect, _ := db.GetAppMeta("oidc_redirect_url")
			emails, _ := db.GetAppMeta("oidc_allowed_emails")
			return auth.OIDCSettings{
				Issuer:        issuer,
				ClientID:      clientID,
				ClientSecret:  secret,
				RedirectURL:   redirect,
				AllowedEmails: splitCSV(emails),
			}, true
		}
	}
	if cfg.OIDCIssuer != "" {
		return auth.OIDCSettings{
			Issuer:        cfg.OIDCIssuer,
			ClientID:      cfg.OIDCClientID,
			ClientSecret:  cfg.OIDCClientSecret,
			RedirectURL:   cfg.OIDCRedirectURL,
			AllowedSubs:   cfg.AuthAllowedSubs,
			AllowedEmails: cfg.AuthAllowedEmails,
		}, true
	}
	return auth.OIDCSettings{}, false
}

// loadOrCreateSessionSecret returns the HMAC key signing the session cookie. It
// prefers SYSTEM_SESSION_SECRET, otherwise reads the key file at path, minting
// and persisting (0600) a fresh 48-byte key on first run — so local auth works
// out of the box without the operator having to set a secret before the wizard,
// while the key stays stable across restarts. Mirrors the loopback-token handling.
func loadOrCreateSessionSecret(cfg config.Config, path string) ([]byte, error) {
	if cfg.SessionSecret != "" {
		return []byte(cfg.SessionSecret), nil
	}
	if b, err := os.ReadFile(path); err == nil {
		if len(b) > 0 {
			return b, nil
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	secret := make([]byte, 48)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, secret, 0o600); err != nil {
		return nil, fmt.Errorf("write session key: %w", err)
	}
	return secret, nil
}

// decryptToolCfg decrypts a tool's stored config blob into a plain map.
// Returns an empty map on any error (callers treat missing keys as "not set").
func parseAllowedNumbersMain(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if n := strings.TrimSpace(p); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func decryptToolCfg(box *secretbox.Box, enc string) map[string]string {
	out := map[string]string{}
	if enc == "" {
		return out
	}
	b, err := box.Decrypt(enc)
	if err != nil {
		return out
	}
	_ = json.Unmarshal(b, &out)
	return out
}

// splitCSV parses a comma-separated list into a trimmed, non-empty slice.
func splitCSV(v string) []string {
	if strings.TrimSpace(v) == "" {
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
