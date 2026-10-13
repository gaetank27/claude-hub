# Claude Hub (`system`)

A Go backend that orchestrates [Claude Code](https://claude.com/claude-code)
sessions from a web UI. Launch a session per project; each runs as a decoupled,
systemd-managed process so the backend can be killed, rebuilt and restarted
**without interrupting live work**. The permanent `system` project points at this
repo itself, so the hub can develop its own code.

> For deep project context (current state, data flow, ADRs, tech debt, next
> steps) see [`CONTEXT_MEMORY.md`](./CONTEXT_MEMORY.md) — the single live source of
> truth, kept in sync with every change.

## Features

- Per-project Claude Code sessions with append-only journals, full replay and
  live WebSocket streaming.
- Self-evolution: sessions survive a backend rebuild/restart (transient systemd
  user units); the backend reattaches on boot.
- Autonomous "plan → approve → execute" flow with one-click approval, hand-back
  supervision, and an isolated `plan/<id>` git branch.
- Autonomous git workflow: per-step commits, push and PR creation via `gh`, with
  a server-side fallback that finishes the job if Claude forgets.
- Paste/drop images in the composer; in-process OIDC auth (PocketID); per-session
  cost/token meters; cross-project dashboard; live + historical monitoring;
  12 color themes; runtime-editable configuration.
- External connectors (Telegram, WhatsApp, Email, Proxmox, Home Assistant,
  Synology, Infomaniak DNS, GitHub, S3) exposed as MCP tools to every session.

## Architecture

```
cmd/backend/          backend entrypoint (HTTP server, boot sequence)
cmd/session-runner/   per-session runner entrypoint
cmd/docgen/           headless-browser doc tool (UI screenshots + PDF manual)
cmd/rag-mcp/          RAG knowledge-base MCP sidecar
cmd/tools-mcp/        connectors MCP sidecar
cmd/ask-mcp/          ask-user MCP sidecar
internal/api/         REST handlers, WebSocket bridge, embedded web assets
internal/session/     Manager: launch/stop/recover, reconcile, plan approval, git
internal/runner/      runner loop, journal, socket hub, exit marker
internal/store/       SQLite store + schema
internal/usage/       Claude subscription usage client (the plan meter)
internal/auth/        in-process OIDC (PocketID) + signed session cookie
internal/config/      env-based configuration
internal/bootstrap/   ensures the permanent `system` project
internal/streamjson/  stream-json helpers
web/                  React + Vite + Tailwind frontend (built into internal/api/assets)
deploy/               systemd unit for the backend
bin/                  built binaries (gitignored)
```

- **Backend** — REST + WebSocket server with the React frontend embedded via
  `//go:embed`. Stateless w.r.t. live sessions.
- **Runner** — one per session, launched as `system-session-<id>.service` via
  `systemd-run --user --collect`. Runs the `claude` CLI, streams stdout to a
  journal and a socket hub.
- **Journal** (`<DataDir>/journals/<id>.jsonl`) — append-only stream-json, the
  immutable source of truth. Sessions are reconstructed/resumed from it alone.
- **Store** — SQLite (WAL), a derived index over the journals. The one exception
  is `usage_snapshots`, which accumulates plan-usage history that can't be
  reconstructed.

See [`CONTEXT_MEMORY.md`](./CONTEXT_MEMORY.md) for the full data flow and the
architectural decision records.

## Prerequisites

### Required

| Dependency | Version | Install |
|---|---|---|
| Go | ≥ 1.22 | [go.dev/dl](https://go.dev/dl/) |
| Node.js | ≥ 20 | `apt install nodejs` or [nvm](https://github.com/nvm-sh/nvm) |
| [Claude Code CLI](https://claude.ai/code) | latest | `npm install -g @anthropic-ai/claude-code` |
| systemd (user units) | — | standard on most Linux distros |

### Optional

| Dependency | Purpose | Install |
|---|---|---|
| `rrdtool` | Host metrics history graphs in the dashboard | `apt install rrdtool` |
| Docker | Sandbox / Live container environments | [docs.docker.com](https://docs.docker.com/engine/install/) |
| `gh` CLI | Autonomous PR creation in sessions | [cli.github.com](https://cli.github.com) |
| `chromium` | PDF manual generation (`cmd/docgen`) | `deploy/chromium-setup.sh` |
| Voyage AI key | RAG embeddings for the knowledge base | [voyageai.com](https://www.voyageai.com/) |
| PocketID | OIDC authentication (production) | [github.com/stonith404/pocket-id](https://github.com/stonith404/pocket-id) |

Without Docker the Sandbox/Live panel is hidden but everything else works.
Without `rrdtool` live gauges still work; only historical charts are disabled.
Without a Voyage AI key the RAG knowledge base stays in `pending` state.

## Build

`go` may not be on `PATH` — add it first:

```sh
export PATH=$PATH:/usr/local/go/bin
```

Build the binaries and the frontend:

```sh
# Frontend first (emits into internal/api/assets, embedded by the backend)
cd web && npm install && npm run build && cd ..

# Backend, runner, and MCP sidecars
go build -o bin/backend        ./cmd/backend
go build -o bin/session-runner ./cmd/session-runner
go build -o bin/rag-mcp        ./cmd/rag-mcp
go build -o bin/tools-mcp      ./cmd/tools-mcp
go build -o bin/ask-mcp        ./cmd/ask-mcp
```

The frontend is embedded at backend build time — **UI changes are live only after
rebuilding the frontend and restarting the backend** (not on `vite build` alone).

## First run

```sh
./bin/backend
# → "auth: first boot — setup wizard active until an admin is created"
# → listening on http://127.0.0.1:8787
```

Open `http://127.0.0.1:8787` in a browser. The **setup wizard** walks you through:
1. Creating an admin account (local auth or OIDC)
2. Verifying the `claude` CLI is accessible
3. Creating your first project

Data is stored in `~/.local/share/system/` by default (`SYSTEM_DATA_DIR`).

## Run (development)

```sh
# Backend on loopback, auth off (no SYSTEM_OIDC_ISSUER set)
./bin/backend

# Frontend with hot reload (backend must be running for WebSocket sessions)
cd web && npm run dev
```

## Deploy (production)

The backend runs as a systemd **user** unit (`deploy/system-backend.service`).

### 1. Install the systemd unit

```sh
mkdir -p ~/.config/systemd/user
cp deploy/system-backend.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now system-backend
```

### 2. Redeploy (non-destructive — live sessions survive)

```sh
export PATH=$PATH:/usr/local/go/bin
cd web && npm run build && cd ..
go build -o bin/backend        ./cmd/backend
go build -o bin/session-runner ./cmd/session-runner
go build -o bin/rag-mcp        ./cmd/rag-mcp
go build -o bin/tools-mcp      ./cmd/tools-mcp
go build -o bin/ask-mcp        ./cmd/ask-mcp
systemctl --user restart system-backend
```

### 3. Reverse proxy (TLS)

The backend speaks plain HTTP on a loopback/LAN address. Terminate TLS with
nginx (or Caddy) in front:

```nginx
server {
    listen 443 ssl;
    server_name hub.example.com;

    ssl_certificate     /etc/letsencrypt/live/hub.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/hub.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8787;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";  # required for WebSocket
        proxy_set_header Host $host;
        proxy_read_timeout 3600s;               # keep sessions alive
    }
}
```

Set `SYSTEM_LISTEN=127.0.0.1:8787` (default) and
`PUBLIC_BASE_URL=https://hub.example.com` in the systemd unit's `Environment=`
or in a `.env` file sourced by the unit.

## Configuration

All settings come from the environment (`internal/config/config.go`); some are
also editable at runtime from the UI (Configuration page) and stored in the
`settings` table, overlaying the env defaults for **new** sessions.

| Variable | Default | Purpose |
|---|---|---|
| `SYSTEM_LISTEN` | `127.0.0.1:8787` | bind address (LAN IP for VPN; never `0.0.0.0`) |
| `SYSTEM_DATA_DIR` | `~/.local/share/system` | DB, journals, sockets |
| `SYSTEM_PERMISSION_MODE` | `bypassPermissions` | new-session permission mode |
| `SYSTEM_MODEL` | `opus` | model alias forced for sessions |
| `SYSTEM_MAX_TURNS` | `40` | autonomous hand-back: max turns |
| `SYSTEM_INACTIVITY_SECONDS` | `180` | autonomous hand-back: idle timeout |
| `SYSTEM_ASSISTANT_ATTACHMENT_DAYS` | `30` | retention of files sent to the assistant and of images it sends |
| `SYSTEM_ASSISTANT_IMAGES_PER_HOUR` | `10` | cap on images the assistant sends (`assistant_send_image`, `whatsapp_send_image`) |
| `SYSTEM_SESSION_MEMORY_MAX` | _(none)_ | per-session memory cap |
| `SYSTEM_SESSION_CPU_QUOTA` | _(none)_ | per-session CPU cap |
| `SYSTEM_PROJECTS_DIR` | `/home/claude/projects` | parent dir for dir-less projects |
| `SYSTEM_CLAUDE_BIN` / `SYSTEM_RUNNER_BIN` | auto-resolved | binary paths |
| `SYSTEM_OIDC_ISSUER` | _(none)_ | enables auth when set (PocketID issuer URL) |
| `SYSTEM_OIDC_CLIENT_ID` / `_CLIENT_SECRET` / `_REDIRECT_URL` | _(none)_ | OIDC client |
| `SYSTEM_AUTH_ALLOWED_SUBS` / `_EMAILS` | _(none)_ | allowlists (empty ⇒ any authenticated) |
| `SYSTEM_SESSION_SECRET` | _(none)_ | HMAC key for the session cookie (stable across restarts) |
| `SYSTEM_ALLOWED_ORIGINS` | _(none)_ | extra WS origin hosts |
| `VOYAGE_API_KEY` | _(none)_ | Voyage AI key — enables RAG embeddings |
| `PUBLIC_BASE_URL` | _(none)_ | External URL, used for container proxy links |

Auth is **off** until `SYSTEM_OIDC_ISSUER` is set; with it set, the backend
refuses to start without the required OIDC secrets.

## Assistant API (web & mobile clients)

All routes (assistant and voice) need the usual Hub authentication: the
`hub_session` cookie (browser) or, for machine/mobile clients, an **unscoped API
key** sent as `Authorization: Bearer hubk_…` (created in the UI; project-scoped keys
cannot reach these routes). Unauthenticated requests get `401`. Errors are
plain text with the HTTP status; `503` = assistant not ready yet (retry).

| Route | Purpose |
|---|---|
| `GET /api/assistant` | `{projectId, busy, person, push, pushWhatsapp, model, modelEffective, modelPending, nightMode, nightStart, nightEnd, nightDeliver, nightIdleMinutes, nightTimezone, nightHeld, batchWakeups, batchMinutes, batchHeld}` (model: see "Assistant model"; night*: see "Assistant night mode") |
| `GET /api/assistant/messages?after=<id>` | `{messages:[{id,ts(ms),channel,role,text,attachments:[…]}], busy}` (`attachments` is always an array, `[]` when none — see below); up to 200 messages with `id > after` (`after=0` = start of history) |
| `POST /api/assistant/messages` | body `{"text": "...", "voice": false, "attachments": ["<id>", …]}` → `202 {"accepted":true}`. Asynchronous; `voice:true` tells the assistant its answer will be read aloud (short, no markdown). `attachments` (optional) are ids from `POST /api/assistant/attachments`; `text` may then be empty. The message joins the thread on channel `web`. Without `attachments` the body is exactly the former `{text, voice}`. |
| `POST /api/assistant/attachments` | upload files for the next message (see below) |
| `GET /api/assistant/attachments/{id}[?download=1]` | the file's bytes (see below) |
| `POST /api/assistant/reset[?history=1]` | `204`; closes the Claude session, `history=1` also wipes the thread |
| `PUT /api/assistant/settings` | any of `{"person": "person.xxx", "push": true, "pushWhatsapp": false, "model": "sonnet", "nightMode": true, "nightStart": "22:00", …}` (only the sent fields change); returns the same object as `GET /api/assistant` |
| `GET /api/assistant/entities?domain=person\|zone` | Home Assistant entities |
| `GET\|POST /api/assistant/triggers`, `PATCH\|DELETE /api/assistant/triggers/{id}`, `POST …/{id}/fire` | place triggers |

`role` is `user` \| `assistant` \| `event`; `channel` is `web` \| `whatsapp` \| `task` \|
`place` \| `message`. **Real-time flow = polling**, no WebSocket/SSE: after a POST,
poll `GET /messages?after=<last id>` every ~0.7–2 s; the answer is a new
`role:"assistant"` message and `busy` returns to `false`. Only speak answers with
`role:"assistant"` and `channel:"web"` that arrive after your send (the thread also
carries WhatsApp/automation traffic).

### Assistant model

Setting `model` (optional everywhere; clients that ignore it keep working):
`PUT /api/assistant/settings {"model": "<value>"}`, value ∈ `"sonnet"` (default
until chosen), `"opus"`, `"haiku"`, `""` (= the Hub's global default model); any
other value → `400`. Suggested labels: Sonnet — faster, lighter on usage / Opus —
most capable / Haiku — fastest, most economical / Hub default. `GET
/api/assistant` returns `model` (the choice), `modelEffective` (the model the live
session really runs on, e.g. `"sonnet"`; `""` when no session is running) and
`modelPending` (`true` while the choice differs from the live session's). The model
is fixed at session start, so a change applies to the next session: automatically
at the next message sent once no answer is pending (the session is reopened, the
thread is kept), or at once with `POST /api/assistant/reset` (no `history`; avoid
while `busy`). Only the assistant is affected, not the Hub default or other projects.

### Assistant phone notifications (push)

Every `role:"assistant"` message added to the thread is also pushed (WebPush, to
the endpoints registered with `POST /api/push/endpoints`) as:

```json
{"sessionId":"assistant","kind":"reply","project":"Assistant","text":"<plain-text excerpt, ≤ 180 chars>"}
```

`sessionId:"assistant"` is **not** a Hub session id: the app must open its
Assistant screen for it (and may cancel the notification keyed by that id when
the screen is shown). `text` has no markdown (code blocks dropped, links reduced
to their label, emphasis/headings/bullets stripped, whitespace collapsed).
Never pushed: `[silence]` answers, user messages, `event` rows. No duplicate:
when the answer is also delivered on WhatsApp (origin channel WhatsApp, or an
automatic run routed to the last WhatsApp contact) nothing is pushed, unless
`pushWhatsapp` is true. `push` (default true) switches the feature off. Bursts
are limited to one push per 20 s: the first goes out at once, the ones that
follow are merged into a single push carrying the latest text.

### Delegated sessions: end detection and grouped wake-ups

A delegated session (`source: "assistant-task"`) is *finished* when its last **turn** is over
and no background task is pending (a `result` event alone is not enough: Claude Code writes
one while waiting for a Gradle build, then resumes by itself). Session objects expose
`turn` (`working|done|error`), `asking` (an `ask_user` question is pending) and, for
delegated sessions, `brief` (one-line brief, 300 chars max); `state` still describes the
process (`running` stays true while the process lives). `hub_session_result` returns
`state` = `working|idle|stopped|error`. The assistant's wake-up stays registered until
the assistant's journal shows it consumed it, and is handed over again if lost.
With **2 or more** delegated sessions watched, ends are held and handed over together at
most once per `batchMinutes` (default 10, counted from the first held end, released early when
no other session is running); a session waiting for the user's answer is never held.
`PUT /api/assistant/settings {"batchWakeups":bool,"batchMinutes":1-120}`.

### Assistant night mode (do not disturb)

During a quiet range, a message the assistant writes **on its own initiative**
(origin channel `task`, `place`, `message` or `session`) is *held*: it is added to the
thread normally but **not** sent on WhatsApp and **not** pushed. Direct replies to a
message of the user (`web`, `whatsapp`) are never held, and nothing is held when the
user sent a message to the assistant (any channel) less than `nightIdleMinutes` ago.
Triggers keep running at night; only the delivery is deferred. Messages to third
parties written at the user's request are not affected.

Settings — all optional and read/written through the existing routes (`GET
/api/assistant`, `PUT /api/assistant/settings`, only the sent fields change; a bad
value is a `400` with a text message):

| Field | Type | Default | Meaning |
|---|---|---|---|
| `nightMode` | bool | `true` | switch the mode on/off (off: held messages are delivered at once) |
| `nightStart` | `"HH:MM"` | `"22:00"` | start of the quiet range (local time) |
| `nightEnd` | `"HH:MM"` | `"07:00"` | end of the range (exclusive); `start > end` wraps midnight |
| `nightDeliver` | `"HH:MM"` | `"07:30"` | delivery of the digest; must be **outside** the range |
| `nightIdleMinutes` | int 1–720 | `20` | the user counts as awake if they wrote within this delay |
| `nightTimezone` | IANA name | `"Europe/Zurich"` | zone of the three times (DST handled) |
| `nightHeld` | int, read-only in `GET` | — | messages waiting for delivery now |

Thread messages (`GET /api/assistant/messages`) gain two fields: `held` (bool) and
`heldUntil` (unix ms, 0 when not held). A held message shows as "held until 07:30";
the fields go back to `false`/`0` once it is delivered, so a client polling with
`after=` must re-read the messages it knows as held (the web page does).

Delivery: at `nightDeliver` (also caught up at backend start if it was down then) the
held messages go out as **one digest** per WhatsApp target, in chronological order
(`• 23:12 — text`, one line each, cut at 300 characters, the whole capped near
3 000 characters with a pointer to the thread) — on WhatsApp when the message would
have gone there, otherwise as a push (`pushWhatsapp` rules as for replies). Nothing is
sent when nothing was held. **Catch-up:** when the user writes to the assistant
(`web` or `whatsapp`) while messages are held, the Hub sends the same digest at once,
titled "Rattrapage", *before* the answer, and tells the model not to repeat it. A held
message is delivered once: no duplicate at 07:30. A spontaneous message that is not
held (e.g. 07:10) first flushes the ones still held, to keep the order.

### Attachments (photos & documents)

Same authentication as the other assistant routes (cookie or `Authorization:
Bearer hubk_…` — **including `GET` of the file bytes**, so an Android client must
send the header when loading thumbnails). Two steps, so a client can show progress
and previews:

1. **Upload** — `POST /api/assistant/attachments`, `multipart/form-data`, repeated
   field **`files`** (1–10 files per request; the request is capped at ~250 MiB).
   `201 {"attachments":[{"id","name","mime","size","kind","ts","expired"}]}` where
   `name` is the sanitized name, `mime` the canonical type, `kind` is `image` or
   `document`. Nothing is sent to the assistant yet: an upload that is not
   referenced by a message within **1 hour** is deleted. On any rejected file the
   whole request is rolled back and a JSON error is returned,
   `{"error":"<message for the user, in French>","code":"…"}`:
   `413` + `too_large`, `415` + `type`, `400` + `invalid` (content does not match its
   extension) / `empty` / `too_many`, `503` assistant not ready.
2. **Send** — `POST /api/assistant/messages` with `{"text":"caption or question",
   "attachments":["<id>",…]}` (ids from step 1, each usable once, ≤10, no duplicates;
   otherwise `400` with the same JSON error). The user message then appears in the
   thread with its `attachments` array (same objects as above, in upload order).
   The assistant receives each file's absolute path, original name, type and size.

**Fetch** — `GET /api/assistant/attachments/{id}`: the raw bytes with the stored
`Content-Type` and `Content-Disposition: inline` for images and PDFs, `attachment`
for other documents (`?download=1` forces `attachment`); `Range` is supported.
`404` unknown id, `410` the file was purged by the retention policy (the thread
still lists it with `"expired": true`; show it as "deleted").

Limits and types (enforced server-side; extension *and* content are checked):

- **25 MiB per file, 10 files per message.**
- Images `jpg jpeg png gif webp`; `pdf`; text `txt md csv tsv json xml yaml yml log
  rtf`; Office/OpenDocument `doc docx xls xlsx ppt pptx odt ods odp`. Everything
  else (videos, archives, executables, SVG/HTML, HEIC…) is refused. A nameless
  image takes its extension from the part's `Content-Type`.
- **Retention**: files are deleted **30 days** after being sent (env
  `SYSTEM_ASSISTANT_ATTACHMENT_DAYS`, checked hourly); records stay with
  `expired:true`. "Clear conversation" (`reset?history=1`) deletes the files at once.
- **Storage**: `<assistant project dir>/pieces-jointes/<uuid>/<sanitized name>`
  (outside every web-served directory; only reachable through the authenticated
  route above).
- **Images sent BY the assistant** (`assistant_send_image`, `whatsapp_send_image`):
  vetted by `internal/imgsafe` (allow-listed dirs, magic bytes, size, re-encode, rate
  limit), stored in `pieces-jointes-sortantes/` with the same retention, exposed in the
  thread as `attachments[{direction:"out", kind:"image", width, height, caption, url}]`
  served by the same authenticated route. See `CONTEXT_MEMORY.md`.

**WhatsApp** (no client work needed): a photo or document the user sends to the
assistant in their private chat is downloaded, stored the same way and forwarded
with its caption. Several files/texts sent within 8 s of each other become one turn.
Refused files (type, >25 MiB, >10 at once) get a clear reply on WhatsApp. Media from
other chats, groups and statuses is never downloaded. Such messages show up in the
thread (channel `whatsapp`) with their `attachments`.

Voice (shared with session voice mode; `503` when local voice is disabled):

| Route | Purpose |
|---|---|
| `GET /api/voice` | `{"available": bool}` |
| `POST /api/voice/stt?lang=fr` | raw audio body (browser MediaRecorder webm/ogg/mp4, ≤16 MiB; faster-whisper/PyAV decodes the usual containers, incl. Android m4a/AAC, ogg, wav) → `{"text": "..."}` |
| `POST /api/voice/tts` | `{"text": "..."}` → `audio/wav` (one sentence per call keeps latency low; strip markdown first) |

Hands-free loop for a client: record an utterance (VAD) → `stt` → `POST
/api/assistant/messages {text, voice:true}` → poll → `tts` each sentence of the new
answer → play; a new utterance while playing cancels playback (barge-in).

## Documentation & PDF manual

The in-app **Aide** section (`/aide`, content in `web/src/lib/help.ts`) is the
single source of truth for user documentation. `cmd/docgen` drives a headless
Chromium (via chromedp, pure Go, no cgo) to (1) capture screenshots of the live
UI into `web/public/help/screenshots/` and (2) compile the Help section
(`/aide?print`) into `dist/claude-hub-manual.pdf`.

```sh
# One-time: install Chromium
deploy/chromium-setup.sh

# Full flow: screenshots → rebuild (embed PNGs) → PDF
export PATH=$PATH:/usr/local/go/bin
make docs DOCS_BASE=http://127.0.0.1:8787 DOCS_SECRET="$SYSTEM_SESSION_SECRET"
systemctl --user restart system-backend   # serve the freshly embedded screenshots
```

**Maintenance rule:** every feature change must update the matching Help chapter,
regenerate its screenshot if the UI changed, and rebuild the PDF — in the same
change set. See `CLAUDE_INSTRUCTIONS.md`.

## Notes

- **No `sqlite3` CLI** — patch the DB with a one-shot Go program importing
  `internal/store`, or rely on the idempotent in-code `migrate()`.
- **No full test suite** — `auth`, `session/git`, `session/usage`, `streamjson`
  and `usage` have unit tests; verify the rest by building and exercising the app.
- **`go` not on PATH** — always `export PATH=$PATH:/usr/local/go/bin` first, or
  add it permanently to `~/.profile`.
- **Loopback ports** — `8787` is the main HTTP server; `8788` is an internal
  loopback used by MCP sidecars (RAG, tools, ask). Never expose `8788` publicly.
