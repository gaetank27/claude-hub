-- Derived, reconstructible state for the hub. The append-only session journals
-- on disk remain the immutable source of truth; this DB is an index over them.

CREATE TABLE IF NOT EXISTS projects (
    id         TEXT PRIMARY KEY,          -- uuid
    name       TEXT NOT NULL,
    slug       TEXT NOT NULL UNIQUE,
    dir        TEXT NOT NULL,             -- absolute working directory
    is_system  INTEGER NOT NULL DEFAULT 0, -- 1 for the permanent `system` project
    protected  INTEGER NOT NULL DEFAULT 0, -- 1 => cannot be archived/deleted
    created_at TEXT NOT NULL,
    starred    INTEGER NOT NULL DEFAULT 0, -- 1 => pinned/favorite, sorted first
    category   TEXT NOT NULL DEFAULT '',   -- free-text grouping label
    hidden     INTEGER NOT NULL DEFAULT 0, -- 1 => excluded from the projects list (e.g. the Skills workspace)
    archived   INTEGER NOT NULL DEFAULT 0, -- 1 => soft-archived: hidden from the board, restorable from Archivage
    comment    TEXT NOT NULL DEFAULT '',   -- free-text human note shown on the project tile/page
    claude_account_id TEXT NOT NULL DEFAULT '', -- Claude subscription this project's sessions run under ('' => inherit the default account)
    model      TEXT NOT NULL DEFAULT '',   -- Claude model alias this project's new sessions run on ('' => inherit the global runtime-settings model)
    show_auto_sessions INTEGER NOT NULL DEFAULT 0 -- 1 => this project's bot/gateway sessions (WhatsApp, API key) also show in the Dashboard / recent bar
);

CREATE TABLE IF NOT EXISTS sessions (
    id                TEXT PRIMARY KEY,      -- uuid, also passed to `claude --session-id`
    project_id        TEXT NOT NULL REFERENCES projects(id),
    journal_path      TEXT NOT NULL,
    socket_path       TEXT NOT NULL,
    state             TEXT NOT NULL,         -- starting|running|stopped|error
    permission_mode   TEXT NOT NULL,         -- default (slice 1); plan/bypass later
    created_at        TEXT NOT NULL,
    last_event_at     TEXT,
    comment           TEXT NOT NULL DEFAULT '', -- free-text human note/label
    pr_url            TEXT NOT NULL DEFAULT '', -- PR opened for approved autonomous work
    claude_account_id TEXT NOT NULL DEFAULT '', -- Claude subscription the session ran under
    model             TEXT NOT NULL DEFAULT '',  -- Claude model alias the session was launched on ('' => resolved at launch)
    archived          INTEGER NOT NULL DEFAULT 0, -- 1 => hidden from normal view, restorable
    stopped_at        TEXT                        -- when the session last left running/starting (NULL while live or unknown)
);

CREATE INDEX IF NOT EXISTS idx_sessions_project ON sessions(project_id);
CREATE INDEX IF NOT EXISTS idx_sessions_state   ON sessions(state);

-- Key/value runtime settings editable from the UI (overlay the env defaults).
CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- Periodic samples of the Claude subscription usage windows (the top-of-page
-- meter). Unlike the rest of this DB, these are NOT reconstructible from the
-- journals: the upstream endpoint only reports the current instant, so we must
-- snapshot it over time to plot history. A NULL column means that window was
-- untracked at sample time. Old rows are pruned by the sampler.
CREATE TABLE IF NOT EXISTS usage_snapshots (
    ts          TEXT NOT NULL,  -- RFC3339Nano, sampling instant
    session     REAL,           -- 5-hour window utilization %, NULL if untracked
    week        REAL,           -- 7-day all-models utilization %
    week_opus   REAL,           -- 7-day Opus utilization %
    week_sonnet REAL,           -- 7-day Sonnet utilization %
    account_id  TEXT NOT NULL DEFAULT '', -- Claude subscription this sample belongs to
    -- Window reset times (RFC3339), persisted so the meter keeps its countdown
    -- even when the live fetch fails and falls back to the latest snapshot.
    session_reset     TEXT,
    week_reset        TEXT,
    week_opus_reset   TEXT,
    week_sonnet_reset TEXT
);

CREATE INDEX IF NOT EXISTS idx_usage_snapshots_ts ON usage_snapshots(ts);

-- Connected Claude Code subscriptions (accounts/organizations). Each maps to a
-- dedicated CLAUDE_CONFIG_DIR so their credentials never overwrite each other.
-- Exactly one row is active at a time: the DEFAULT account. New sessions run under
-- their project's claude_account_id when set, else this default; the top usage
-- meter follows the default page-wide and the project's account inside a project.
-- NOT reconstructible from the journals — this is hub state.
CREATE TABLE IF NOT EXISTS claude_accounts (
    id            TEXT PRIMARY KEY,          -- uuid = internal subscription id
    label         TEXT NOT NULL,             -- human label, e.g. "Perso Max"
    config_dir    TEXT NOT NULL,             -- dedicated CLAUDE_CONFIG_DIR
    kind          TEXT NOT NULL,             -- "oauth" | "apikey"
    org_id        TEXT NOT NULL DEFAULT '',  -- organization/account id discovered from creds
    account_email TEXT NOT NULL DEFAULT '',  -- account email when discoverable
    status        TEXT NOT NULL,             -- "connected" | "loggedout"
    active        INTEGER NOT NULL DEFAULT 0, -- 1 for the single active account
    created_at    TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_claude_accounts_active ON claude_accounts(active);

-- Global, transversal skills (reusable Claude Code competences). The definition
-- lives on disk as ~/.claude/skills/<slug>/SKILL.md (the source of truth, shared
-- across every account); this table indexes the metadata for search/filtering and
-- links the background session that authored the skill.
CREATE TABLE IF NOT EXISTS skills (
    id          TEXT PRIMARY KEY,             -- uuid
    name        TEXT NOT NULL,
    slug        TEXT NOT NULL UNIQUE,         -- dir name under ~/.claude/skills
    description TEXT NOT NULL DEFAULT '',
    keywords    TEXT NOT NULL DEFAULT '',     -- free-text, for quick search
    status      TEXT NOT NULL DEFAULT 'draft', -- draft|enabled|disabled
    session_id  TEXT NOT NULL DEFAULT '',     -- the background authoring session
    created_at  TEXT NOT NULL
);

-- Global RAG knowledge base. A document is an ingested source (file, pasted text
-- or web page); it is parsed, chunked and embedded asynchronously. The raw source
-- is persisted on disk under <DataDir>/rag/<id>/ so a document can be re-indexed
-- (e.g. after an embedder change) without re-uploading. Shared across all projects
-- and queried by sessions through the rag-mcp `knowledge_search` tool.
-- Named, global knowledge bases. Documents belong to exactly one collection;
-- sessions can query across all collections or narrow to one. A seeded "default"
-- collection holds documents created before collections existed.
CREATE TABLE IF NOT EXISTS rag_collections (
    id          TEXT PRIMARY KEY,             -- uuid (or "default" for the seed)
    name        TEXT NOT NULL,
    slug        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    category    TEXT NOT NULL DEFAULT '',     -- free-text grouping for the global RAG tiles
    project_id  TEXT NOT NULL DEFAULT '',     -- non-empty => private per-project RAG (hidden from the global RAG UI)
    created_at  TEXT NOT NULL
);

-- Per-project RAG: each project (including `system`) has a private, isolated index
-- of its own source/config/docs/memory, in its own rag_collections row. This table
-- holds the indexing config + status; the vectors live in rag_documents/rag_chunks
-- under collection_id. Distinct from the global RAG module.
CREATE TABLE IF NOT EXISTS project_rag (
    project_id       TEXT PRIMARY KEY REFERENCES projects(id),
    collection_id    TEXT NOT NULL,            -- the project's private collection
    enabled          INTEGER NOT NULL DEFAULT 1,
    auto_on_change   INTEGER NOT NULL DEFAULT 1, -- reindex when the git HEAD changes (commit/push)
    interval_minutes INTEGER NOT NULL DEFAULT 0, -- periodic reindex; 0 = only on-change/manual
    status           TEXT NOT NULL DEFAULT 'idle', -- idle|indexing|error
    last_indexed_at  TEXT,
    last_head_sha    TEXT NOT NULL DEFAULT '',  -- git HEAD at the last index, for change detection
    file_count       INTEGER NOT NULL DEFAULT 0,
    chunk_count      INTEGER NOT NULL DEFAULT 0,
    bytes            INTEGER NOT NULL DEFAULT 0,
    error            TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS rag_documents (
    id            TEXT PRIMARY KEY,           -- uuid
    title         TEXT NOT NULL,
    source_type   TEXT NOT NULL,              -- text|markdown|pdf|web
    source_ref    TEXT NOT NULL DEFAULT '',   -- original filename or URL
    status        TEXT NOT NULL DEFAULT 'pending', -- pending|processing|indexed|error
    enabled       INTEGER NOT NULL DEFAULT 1, -- 0 => excluded from retrieval
    bytes         INTEGER NOT NULL DEFAULT 0, -- raw source size
    chunk_count   INTEGER NOT NULL DEFAULT 0,
    error         TEXT NOT NULL DEFAULT '',   -- last indexing error, if status=error
    collection_id TEXT NOT NULL DEFAULT '',   -- owning rag_collections.id
    content_hash  TEXT NOT NULL DEFAULT '',   -- source hash, for incremental project reindexing
    created_at    TEXT NOT NULL,
    indexed_at    TEXT                        -- when it last reached status=indexed
);
-- Note: the idx_rag_documents_collection index is created in migrate() (Go), after
-- the collection_id column is ensured, so it also works on pre-existing DBs.

-- Chunks of an indexed document with their embedding vectors. The embedding is a
-- little-endian float32 BLOB; similarity search is brute-force cosine in Go (the
-- pure-Go SQLite driver has no vector extension, and a personal KB is small).
CREATE TABLE IF NOT EXISTS rag_chunks (
    id          TEXT PRIMARY KEY,             -- uuid
    doc_id      TEXT NOT NULL REFERENCES rag_documents(id),
    idx         INTEGER NOT NULL,             -- position within the document
    text        TEXT NOT NULL,
    embedding   BLOB NOT NULL,                -- float32 little-endian vector
    token_count INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_rag_chunks_doc ON rag_chunks(doc_id);

-- Configured external tools / integrations (Proxmox, Telegram, Email, …). Global
-- and shared across all projects. The connector's parameters (URLs, API tokens,
-- passwords) are stored as an AES-GCM encrypted JSON blob in config_enc — the
-- encryption key lives outside the DB (see internal/secretbox), so a DB dump alone
-- never reveals credentials. type/name/status stay in clear for listing.
CREATE TABLE IF NOT EXISTS tools (
    id             TEXT PRIMARY KEY,          -- uuid
    type           TEXT NOT NULL,             -- connector type: proxmox|telegram|email
    name           TEXT NOT NULL,             -- user-facing label
    status         TEXT NOT NULL DEFAULT 'disconnected', -- connected|disconnected|error
    enabled        INTEGER NOT NULL DEFAULT 1, -- 0 => not exposed to sessions
    error          TEXT NOT NULL DEFAULT '',  -- last test/use error
    config_enc     TEXT NOT NULL DEFAULT '',  -- AES-GCM encrypted JSON config
    last_tested_at TEXT,
    created_at     TEXT NOT NULL
);

-- Categories used to group the configured tools on the Tools page. Seeded once
-- with the default set (key = default slug, name '' => localised label); users
-- may rename, add, delete (except "other") and reorder them. tools.category_id ''
-- means "automatic" (default category of the connector type, else "other").
CREATE TABLE IF NOT EXISTS tool_categories (
    id       TEXT PRIMARY KEY,
    key      TEXT NOT NULL DEFAULT '',
    name     TEXT NOT NULL DEFAULT '',
    position INTEGER NOT NULL DEFAULT 0
);

-- Recurring autonomous tasks: each fires a new session on a cron schedule and
-- injects its prompt as the first user message. project_id='' means the task
-- runs under the permanent `system` project (global). next_run_at is pre-computed
-- so the scheduler can query "SELECT … WHERE next_run_at <= now" cheaply.
CREATE TABLE IF NOT EXISTS scheduled_tasks (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    project_id      TEXT NOT NULL DEFAULT '',   -- '' = global (runs under `system`)
    prompt          TEXT NOT NULL,
    schedule        TEXT NOT NULL,              -- 5-field cron "min hr dom mon dow"
    enabled         INTEGER NOT NULL DEFAULT 1,
    permission_mode TEXT NOT NULL DEFAULT 'default',
    model           TEXT NOT NULL DEFAULT '',
    last_run_at     TEXT,                       -- RFC3339Nano, NULL until first run
    next_run_at     TEXT NOT NULL,              -- pre-computed next fire time
    last_session_id TEXT NOT NULL DEFAULT '',
    one_shot        INTEGER NOT NULL DEFAULT 0, -- 1 => disable after first fire
    created_at      TEXT NOT NULL,
    command         TEXT NOT NULL DEFAULT '',   -- non-empty => script task (shell command, no session)
    last_status     TEXT NOT NULL DEFAULT '',   -- script tasks: running | ok | error
    last_output     TEXT NOT NULL DEFAULT ''    -- script tasks: tail of the last run's output
);

-- Local hub operators (login/password + mandatory TOTP 2FA). Distinct from
-- claude_accounts (Claude subscriptions) and from OIDC identities (which are the
-- IdP's concern). The first admin is created interactively through the first-boot
-- setup wizard, never seeded. The TOTP shared secret is encrypted at rest with the
-- same AES-256-GCM box as connector credentials (internal/secretbox), so a DB dump
-- alone never reveals it. NOT reconstructible from the journals — this is hub state.
CREATE TABLE IF NOT EXISTS users (
    id              TEXT PRIMARY KEY,           -- uuid
    username        TEXT NOT NULL UNIQUE,
    email           TEXT NOT NULL DEFAULT '',
    password_hash   TEXT NOT NULL,              -- bcrypt
    totp_secret_enc TEXT NOT NULL DEFAULT '',   -- AES-256-GCM encrypted TOTP shared secret
    totp_confirmed  INTEGER NOT NULL DEFAULT 0, -- 1 once a first valid code was verified
    role            TEXT NOT NULL DEFAULT 'admin',
    created_at      TEXT NOT NULL
);

-- Small key/value store for application-level metadata that is neither a runtime
-- session setting (see `settings`) nor reconstructible state: first-boot setup
-- progress (`setup_complete`), the wizard-collected public base URL, and the
-- optional OIDC parameters set from the UI. Secrets here (e.g. an OIDC client
-- secret) are not encrypted — keep IdP secrets in the systemd drop-in env instead.
CREATE TABLE IF NOT EXISTS app_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- Hub-managed domains: domains served by Caddy with Hub-issued Let's Encrypt certs.
CREATE TABLE IF NOT EXISTS domains (
    id         TEXT PRIMARY KEY,
    domain     TEXT NOT NULL UNIQUE,
    upstream   TEXT NOT NULL,           -- e.g. 127.0.0.1:8787
    cert_file  TEXT NOT NULL DEFAULT '',
    key_file   TEXT NOT NULL DEFAULT '',
    status     TEXT NOT NULL DEFAULT 'pending', -- pending|active|error
    error      TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);

-- Per-project access control. A missing row means the defaults: every enabled
-- tool is usable and project_code_search is limited to the project's own index.
-- tools_mode 'all' => every enabled tool; 'list' => only allowed_tools (tool ids).
-- allowed_projects: ids of OTHER projects whose private code index this project
-- may search (JSON arrays of strings).
CREATE TABLE IF NOT EXISTS project_access (
    project_id       TEXT PRIMARY KEY REFERENCES projects(id),
    tools_mode       TEXT NOT NULL DEFAULT 'all',
    allowed_tools    TEXT NOT NULL DEFAULT '[]',
    allowed_projects TEXT NOT NULL DEFAULT '[]'
);

-- When the user last looked at a session. A session whose latest journal write is
-- newer than seen_at has an unread reply (shown highlighted in the UI).
CREATE TABLE IF NOT EXISTS session_seen (
    session_id TEXT PRIMARY KEY,
    seen_at    TEXT NOT NULL
);

-- API keys for machine clients (Authorization: Bearer hubk_…). Only the SHA-256
-- of the key is stored. project_id '' => full API access; otherwise the key may
-- only launch and drive sessions of that project.
CREATE TABLE IF NOT EXISTS api_keys (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    key_hash     TEXT NOT NULL UNIQUE,
    project_id   TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL DEFAULT 0
);

-- Browser/app sign-ins (the hub_session cookie carries the row id). A cookie is
-- only honoured while its row exists, so deleting a row revokes that device.
-- expires_at 0 => "stay signed in" (no expiry).
CREATE TABLE IF NOT EXISTS login_sessions (
    id           TEXT PRIMARY KEY,
    sub          TEXT NOT NULL,
    email        TEXT NOT NULL DEFAULT '',
    method       TEXT NOT NULL DEFAULT '',
    user_agent   TEXT NOT NULL DEFAULT '',
    ip           TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL DEFAULT 0
);

-- Phones to notify when a session needs the user: WebPush subscriptions declared
-- by the Android app (POST /api/push/endpoints). The endpoint URL is the identity.
CREATE TABLE IF NOT EXISTS push_endpoints (
    endpoint   TEXT PRIMARY KEY,
    p256dh     TEXT NOT NULL,
    auth       TEXT NOT NULL,
    device     TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);

-- Personal assistant (internal/assistant): the single conversation thread shared
-- by every channel (web page, WhatsApp, scheduled tasks, place triggers).
CREATE TABLE IF NOT EXISTS assistant_messages (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    ts      INTEGER NOT NULL,           -- unix ms
    channel TEXT NOT NULL,              -- web | whatsapp | task | place
    role    TEXT NOT NULL,              -- user | assistant | event
    text    TEXT NOT NULL
);

-- Files sent to the assistant (web page, WhatsApp). The bytes live under the
-- assistant project dir (path is relative to it); message_id 0 = uploaded but not
-- yet sent. expired = the file was purged by the retention job (metadata kept).
CREATE TABLE IF NOT EXISTS assistant_attachments (
    id         TEXT PRIMARY KEY,
    message_id INTEGER NOT NULL DEFAULT 0,
    name       TEXT NOT NULL,           -- sanitized original name
    mime       TEXT NOT NULL,
    size       INTEGER NOT NULL,
    path       TEXT NOT NULL,
    ts         INTEGER NOT NULL,        -- unix ms
    expired    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_assistant_attachments_message ON assistant_attachments(message_id);

-- Place triggers: run a prompt when the person entity enters/leaves a zone.
CREATE TABLE IF NOT EXISTS assistant_triggers (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    place      TEXT NOT NULL,           -- zone state as Home Assistant reports it (home, work…)
    event      TEXT NOT NULL,           -- enter | leave
    prompt     TEXT NOT NULL,
    enabled    INTEGER NOT NULL DEFAULT 1,
    last_fired INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);

-- Script status reports (endpoint POST /webhook/script-status/{script}, see
-- internal/scriptstatus). One row per API key; the key itself is never stored,
-- only its SHA-256 (keys are 256-bit random, so a fast hash is enough).
CREATE TABLE IF NOT EXISTS script_keys (
    id          TEXT PRIMARY KEY,
    script      TEXT NOT NULL,
    label       TEXT NOT NULL DEFAULT '',
    key_hash    TEXT NOT NULL UNIQUE,
    allowed_ips TEXT NOT NULL DEFAULT '',   -- comma-separated IPs/CIDRs; '' = any source
    silence_min INTEGER NOT NULL DEFAULT 0, -- minutes without a report before "muet"; 0 = no check
    wake        INTEGER NOT NULL DEFAULT 1, -- wake the assistant on error / muet
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  INTEGER NOT NULL,           -- unix ms
    last_used   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_script_keys_script ON script_keys(script);

-- Latest report per script, plus the state the assistant was last woken for.
CREATE TABLE IF NOT EXISTS script_status (
    script      TEXT PRIMARY KEY,
    state       TEXT NOT NULL,
    summary     TEXT NOT NULL,
    data        TEXT NOT NULL,              -- compact JSON object (or null)
    sent_ts     INTEGER NOT NULL DEFAULT 0, -- client-declared time, unix ms (0 = absent)
    received_at INTEGER NOT NULL,           -- unix ms, server clock
    notified    TEXT NOT NULL DEFAULT ''    -- '' | error | muet
);

-- Short rolling history (the last 50 reports per script are kept).
CREATE TABLE IF NOT EXISTS script_history (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    script      TEXT NOT NULL,
    state       TEXT NOT NULL,
    summary     TEXT NOT NULL,
    data        TEXT NOT NULL,
    received_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_script_history_script ON script_history(script, id);

-- Command channel (internal/scriptstatus/commands.go): instructions queued for a
-- script, delivered in the response of its next heartbeat and acknowledged in the
-- following one. Never code: names are checked against the key's allow-list.
CREATE TABLE IF NOT EXISTS script_commands (
    id           TEXT PRIMARY KEY,
    script       TEXT NOT NULL,
    name         TEXT NOT NULL,
    params       TEXT NOT NULL DEFAULT '{}',   -- compact JSON object
    created_by   TEXT NOT NULL,                -- user | assistant
    created_at   INTEGER NOT NULL,             -- unix ms
    expires_at   INTEGER NOT NULL,
    state        TEXT NOT NULL,                -- pending|delivered|ok|failed|cancelled|expired
    delivered_at INTEGER NOT NULL DEFAULT 0,   -- last delivery
    deliveries   INTEGER NOT NULL DEFAULT 0,
    done_at      INTEGER NOT NULL DEFAULT 0,   -- ack / cancel / expiry time
    message      TEXT NOT NULL DEFAULT ''      -- ack message or expiry reason
);
CREATE INDEX IF NOT EXISTS idx_script_commands_script ON script_commands(script, created_at);

-- Screenshots uploaded by a script (internal/scriptstatus/images.go). Only the
-- metadata lives here; the bytes are files under <DataDir>/script-images/<script>/.
-- Short-lived on purpose: the last 240 per script, 24 hours at most.
CREATE TABLE IF NOT EXISTS script_images (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    script      TEXT NOT NULL,
    mime        TEXT NOT NULL,              -- image/jpeg | image/png
    width       INTEGER NOT NULL,
    height      INTEGER NOT NULL,
    size        INTEGER NOT NULL,           -- bytes on disk (after re-encoding)
    received_at INTEGER NOT NULL            -- unix ms, server clock
);
CREATE INDEX IF NOT EXISTS idx_script_images_script ON script_images(script, id);

-- Remote MCP connector (claude.ai custom connector). Clients come from Dynamic
-- Client Registration; a grant is one authorised connection (one consent). Only
-- SHA-256 hashes of tokens are stored. Revoking = setting revoked_at.
CREATE TABLE IF NOT EXISTS mcp_clients (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    redirect_uris TEXT NOT NULL,
    created_at    INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS mcp_grants (
    id                 TEXT PRIMARY KEY,
    client_id          TEXT NOT NULL,
    client_name        TEXT NOT NULL DEFAULT '',
    user_email         TEXT NOT NULL DEFAULT '',
    created_at         INTEGER NOT NULL,
    last_used_at       INTEGER NOT NULL DEFAULT 0,
    last_ip            TEXT NOT NULL DEFAULT '',
    access_hash        TEXT NOT NULL DEFAULT '',
    access_exp         INTEGER NOT NULL DEFAULT 0,
    refresh_hash       TEXT NOT NULL DEFAULT '',
    prev_refresh_hash  TEXT NOT NULL DEFAULT '',
    refresh_exp        INTEGER NOT NULL DEFAULT 0,
    revoked_at         INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_mcp_grants_access ON mcp_grants(access_hash);
CREATE INDEX IF NOT EXISTS idx_mcp_grants_refresh ON mcp_grants(refresh_hash);
CREATE TABLE IF NOT EXISTS mcp_audit (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ts          INTEGER NOT NULL,
    grant_id    TEXT NOT NULL DEFAULT '',
    client_name TEXT NOT NULL DEFAULT '',
    ip          TEXT NOT NULL DEFAULT '',
    method      TEXT NOT NULL DEFAULT '',
    tool        TEXT NOT NULL DEFAULT '',
    args        TEXT NOT NULL DEFAULT '',
    outcome     TEXT NOT NULL DEFAULT '',
    detail      TEXT NOT NULL DEFAULT '',
    ms          INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_mcp_audit_ts ON mcp_audit(ts);

CREATE TABLE IF NOT EXISTS connector_audit (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    ts        INTEGER NOT NULL,
    connector TEXT NOT NULL DEFAULT '',
    action    TEXT NOT NULL DEFAULT '',
    source    TEXT NOT NULL DEFAULT '',
    args      TEXT NOT NULL DEFAULT '',
    outcome   TEXT NOT NULL DEFAULT '',
    detail    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_connector_audit_ts ON connector_audit(ts);
