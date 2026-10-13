// Typed REST client for the hub backend.

// ContextResource is one shared Hub capability visible to a project's sessions,
// mirrored from the injected session context (Sandbox, tool connectors, roadmap).
export interface ContextResource {
  key: string;
  label: string;
  available: boolean;
  future: boolean;
}

export interface Project {
  id: string;
  name: string;
  slug: string;
  dir: string;
  isSystem: boolean;
  protected: boolean;
  createdAt: string;
  starred: boolean;
  category: string;
  archived: boolean;
  comment: string;
  // Subscription this project's sessions run under; empty => inherit the default
  // (active) account.
  claudeAccountId: string;
  // Claude model this project's new sessions run on; empty => inherit the global
  // runtime-settings model.
  model: string;
  // Show this project's automatic bot sessions (WhatsApp, API key) in the
  // Dashboard / recent bar (hidden by default).
  showAutoSessions: boolean;
  contextResources: ContextResource[];
}

export interface ProjectAccess {
  toolsMode: "all" | "list";
  allowedTools: string[];
  allowedProjects: string[];
}

export interface Attention {
  needed: boolean;
  reason?: string;
  turns: number;
}

// Device is one signed-in browser or app. Revoking it invalidates its session
// cookie on the server. expiresAt null => "stay signed in".
export interface McpTool { name: string; description: string; class: "read" | "safe" | "sensitive" | "blocked"; enabled: boolean; default: boolean; confirm: boolean; untrusted: boolean }
export interface McpGrant { id: string; clientName: string; userEmail: string; createdAt: string; lastUsedAt: string | null; lastIp: string }
export interface McpRemote { enabled: boolean; publicUrl: string; mcpUrl: string; tools: McpTool[]; grants: McpGrant[] }
export interface McpAudit { id: number; ts: string; client: string; ip: string; method: string; tool: string; args: string; outcome: string; detail: string; ms: number }

export interface Device {
  id: string;
  email: string;
  method: string;
  userAgent: string;
  ip: string;
  createdAt: string;
  lastSeenAt: string;
  expiresAt: string | null;
  current: boolean;
}

// VaultEntry is a compressed archive of a deleted project in the safe deposit
// box (Configuration → Projets supprimés). `name` is the .tar.gz filename used
// as the id for restore/destroy.
export interface VaultEntry {
  name: string;
  projectName: string;
  slug: string;
  originalDir: string;
  deletedAt: string;
  size: number;
  sessionCount: number;
}

export interface SessionUsage {
  costUSD: number;
  inputTokens: number;
  outputTokens: number;
  contextTokens: number;
  computeMs: number;
  // Skill name → number of invocations in the session (via the Skill tool).
  // Absent when the session used no skills.
  skills?: Record<string, number>;
  // RAG knowledge base → number of knowledge_search queries against it. Keys are
  // the collection ref, or the sentinels "*all*" (global search, no explicit
  // collection) and "*project*" (the project's private code RAG). Absent when the
  // session queried no RAG.
  rags?: Record<string, number>;
}

// A file saved to the project working dir via the attachments endpoint. `path`
// is relative to the project dir (where the running claude can read it).
export interface AttachmentResult {
  name: string;
  path: string;
  size: number;
}

export interface Session {
  id: string;
  projectId: string;
  state: "starting" | "running" | "stopped" | "error";
  permissionMode: string;
  autonomous: boolean;
  attention: Attention;
  // Output the user has not looked at yet.
  unread?: boolean;
  /** An ask_user question waits for the user's answer. */
  asking?: boolean;
  /** Outcome of the latest turn (a live session whose turn is over is "done"). */
  turn?: "working" | "done" | "error";
  /** One-line brief of a session delegated by the assistant. */
  brief?: string;
  /** Short summary (3-8 words) of a delegated request. */
  summary?: string;
  createdAt: string;
  lastEventAt?: string;
  lastMessageAt?: string;
  comment: string;
  branch?: string;
  prUrl?: string;
  claudeAccountId?: string;
  // Model this session was launched on (empty on sessions created before the
  // per-session model override existed).
  model?: string;
  usage: SessionUsage;
  source?: string; // "" | "scheduler" | "whatsapp" | "api"
  archived?: boolean;
}

// Sessions opened automatically by a bot/gateway (WhatsApp, API-key clients such
// as ha-ia): hidden from the dashboard/recent views unless the project opts in.
export function isAutoBotSession(s: Pick<Session, "source">): boolean {
  return s.source === "whatsapp" || s.source === "api" || s.source === "assistant" || s.source === "assistant-task";
}

// hiddenFromRecent: an automatic bot session whose project did not opt in to
// showing them (Project.showAutoSessions).
export function hiddenFromRecent(s: Session, projects: { id: string; showAutoSessions?: boolean }[]): boolean {
  return isAutoBotSession(s) && !projects.find((p) => p.id === s.projectId)?.showAutoSessions;
}

// Personal assistant (internal/assistant).
export interface AssistantMessage {
  id: number;
  ts: number; // unix ms
  channel: "web" | "whatsapp" | "task" | "place" | "message" | "session";
  role: "user" | "assistant" | "event";
  text: string;
  attachments: AssistantAttachment[];
  /** Night mode: written by the assistant but held back (not sent) until heldUntil (unix ms). */
  held?: boolean;
  heldUntil?: number;
}
// A file sent to the assistant. Fetch the bytes from assistantAttachmentUrl(id).
export interface AssistantAttachment {
  id: string;
  name: string;
  mime: string;
  size: number;
  kind: "image" | "document";
  ts: number;
  expired: boolean; // purged by the retention policy
  /** Optional (older servers omit them). "out" = an image sent BY the assistant. */
  direction?: "in" | "out";
  width?: number;
  height?: number;
  caption?: string;
  /** Authenticated route serving the bytes (same as assistantAttachmentUrl(id)). */
  url?: string;
}
export const ASSISTANT_MAX_FILE = 25 * 1024 * 1024;
export const ASSISTANT_MAX_FILES = 10;
// Extensions the server accepts (internal/assistant/attachments.go).
export const ASSISTANT_EXTS =
  "jpg jpeg png gif webp pdf txt md csv tsv json xml yaml yml log rtf docx xlsx pptx odt ods odp doc xls ppt".split(" ");
export const assistantAttachmentUrl = (id: string, download = false) =>
  `/api/assistant/attachments/${id}${download ? "?download=1" : ""}`;
export interface AssistantState {
  projectId: string;
  busy: boolean;
  person: string;
  push: boolean;
  pushWhatsapp: boolean;
  /** Chosen alias ("" = Hub default); modelEffective = live session's model ("" = none). */
  model: string;
  modelEffective: string;
  modelPending: boolean;
  /** Night mode (all optional for older servers). */
  nightMode?: boolean;
  nightStart?: string;
  nightEnd?: string;
  nightDeliver?: string;
  nightIdleMinutes?: number;
  nightTimezone?: string;
  nightHeld?: number;
  /** Grouped wake-ups (all optional for older servers). */
  batchWakeups?: boolean;
  batchMinutes?: number;
  batchHeld?: number;
}
export type AssistantNightPatch = Partial<
  Pick<
    AssistantState,
    "nightMode" | "nightStart" | "nightEnd" | "nightDeliver" | "nightIdleMinutes" | "nightTimezone" | "batchWakeups" | "batchMinutes"
  >
>;
export interface AssistantTrigger {
  id: string;
  name: string;
  place: string;
  event: "enter" | "leave" | "message";
  prompt: string;
  enabled: boolean;
  lastFired: number; // unix ms, 0 = never
}
export interface HaEntity {
  id: string;
  state: string;
  name: string;
}

export interface Domain {
  id: string;
  domain: string;
  upstream: string;
  status: "pending" | "active" | "error";
  error?: string;
}

export interface DomainStatus {
  domain: Domain;
  logs: string[];
  done: boolean;
}

export interface CreateDomainInput {
  domain: string;
  upstream: string;
  dnsRecord?: {
    zoneDomain: string;
    source: string;
    ip: string;
  };
}

export interface ScheduledTask {
  id: string;
  name: string;
  projectId: string;  // "" = global
  prompt: string;
  schedule: string;
  enabled: boolean;
  permissionMode: string;
  model: string;
  lastRunAt?: string;
  nextRunAt: string;
  lastSessionId?: string;
  oneShot: boolean;
  createdAt: string;
  command: string;     // non-empty = script task (shell command, no Claude session)
  lastStatus?: string; // script tasks: running | ok | error
  lastOutput?: string; // script tasks: tail of the last run's output
}

export interface ScheduledTaskInput {
  name: string;
  projectId: string;
  prompt: string;
  schedule: string;
  enabled: boolean;
  permissionMode: string;
  model: string;
  command?: string;
  oneShot?: boolean;
  launchAt?: string; // ISO 8601, for one-shot deferred launches
}

// Live resource use of a running session.
export interface SessionResources {
  memoryBytes: number;
  cpuNSec: number;
  journalBytes: number;
  hasMetrics: boolean;
}

export interface MonitoringRow {
  id: string;
  projectId: string;
  resources: SessionResources;
}

// Live physical-host metrics for the Suivi gauges banner.
export interface HostMetrics {
  cpu: number; // 0..100, global
  memUsed: number; // bytes
  memTotal: number;
  netRx: number; // bytes/s
  netTx: number;
  diskRd: number; // bytes/s
  diskWr: number;
  diskUsed: number; // bytes on /
  diskTotal: number;
  sampledAt: string;
}

export type HostRange = "hour" | "day" | "week" | "month";

// RRD-backed host history. series maps a data source (cpu, mem_used, …) to its
// points; a point value is null for an unknown bucket.
export interface HostHistory {
  available: boolean;
  range: HostRange;
  from?: number; // unix seconds
  to?: number;
  step?: number;
  series?: Record<string, { t: number; v: number | null }[]>;
  error?: string;
}

// A global, transversal Claude Code skill. The definition lives on disk
// (~/.claude/skills/<slug>/SKILL.md), shared across every account; this is its
// searchable index plus the background session that authored it.
export interface Skill {
  id: string;
  name: string;
  slug: string;
  description: string;
  keywords: string;
  status: "draft" | "enabled" | "disabled";
  sessionId?: string;
  createdAt: string;
}

// A document in the global RAG knowledge base. Ingested and embedded
// asynchronously, then queried by sessions through the rag-mcp knowledge_search
// tool. Shared across all projects.
export interface RagDocument {
  id: string;
  title: string;
  sourceType: "text" | "markdown" | "pdf" | "web";
  sourceRef: string; // original filename or URL
  status: "pending" | "processing" | "indexed" | "error";
  enabled: boolean;
  bytes: number;
  chunkCount: number;
  error?: string;
  collectionId: string;
  createdAt: string;
  indexedAt?: string;
}

// A named knowledge base. Documents belong to exactly one; sessions can query
// across all or narrow to one.
export interface RagCollection {
  id: string;
  name: string;
  slug: string;
  description: string;
  category: string;
  docCount: number;
  isDefault: boolean;
  createdAt: string;
}

// Whether an embeddings provider is configured; the explorer warns when not.
export interface RagStatus {
  available: boolean;
  embedder: string;
}

// A project's private RAG index status + scheduling config.
export interface ProjectRag {
  projectId: string;
  enabled: boolean;
  autoOnChange: boolean;
  intervalMinutes: number;
  status: "idle" | "indexing" | "error";
  lastIndexedAt?: string;
  fileCount: number;
  chunkCount: number;
  bytes: number;
  pending: number;
  error?: string;
}

// --- Tools (external integrations) ---

// One configuration field of a connector, driving the dynamic form.
export interface ToolField {
  key: string;
  label: string;
  type: "text" | "password" | "url" | "number" | "bool";
  secret: boolean;
  required: boolean;
  placeholder?: string;
}

// A connector type available in the catalog.
export interface Connector {
  type: string;
  label: string;
  fields: ToolField[];
  actions: { name: string; description: string }[]; // MCP actions exposed (searchable)
  defaultCategory: string;
}

// A group of tools on the Tools page. `name` is empty for an unrenamed default
// (the UI shows the localised label of `key`).
export interface ToolCategory {
  id: string;
  key: string;
  name: string;
  position: number;
}

// A configured tool instance. Secrets are never returned; secretsSet says which
// secret fields already hold a value.
export interface Tool {
  id: string;
  type: string;
  name: string;
  status: "connected" | "disconnected" | "error";
  enabled: boolean;
  error?: string;
  config?: Record<string, string>;
  secretsSet?: Record<string, boolean>;
  lastTestedAt?: string;
  createdAt: string;
  category: string; // effective category id
  categoryAuto: boolean; // true => not explicitly chosen
}

export interface ToolTestResult {
  ok: boolean;
  error?: string;
}

// WhatsApp QR pairing status returned by GET /api/tools/{id}/qr.
export interface WhatsAppQRInfo {
  status: "disconnected" | "pairing" | "connected" | "error";
  qr?: string;   // raw QR string, only when status=pairing
  error?: string;
}

// Stored Proton session, GET /api/tools/{id}/proton (no network call).
export interface ProtonStatus {
  connected: boolean;
  username: string;
  pausedUntil?: string;
  lastError?: string;
}

// Step of the one-time Proton login, POST /api/tools/{id}/proton/connect.
export interface ProtonConnectResult {
  ok: boolean;
  status?: "connected" | "needs_captcha" | "needs_2fa" | "needs_mailbox_password";
  message?: string;
  error?: string;
  url?: string; // needs_captcha: https://verify.proton.me link
  expiresAt?: string; // needs_captcha: the wait ends then
}

// Proton Bridge (mail), GET /api/proton-bridge.
export interface BridgeStatus {
  installed: boolean;
  serviceActive: boolean;
  busy: boolean;
  pending?: "needs_captcha" | "needs_2fa" | "needs_mailbox_password";
  captchaUrl?: string;
  state: "not_installed" | "stopped" | "no_account" | "busy" | "awaiting" | "syncing" | "ready" | "error";
  account?: string;
  toolId?: string;
  lastError?: string;
  assistantAccess: boolean;
}

// Step of the Bridge account add, POST /api/proton-bridge/connect.
export interface BridgeConnectResult {
  ok: boolean;
  status?: "needs_captcha" | "needs_2fa" | "needs_mailbox_password" | "added";
  url?: string;
  message?: string;
  error?: string;
  kind?: "abuse" | "human" | "credentials" | "twofa" | "other";
}

// Result of a test-message send (Telegram). result holds the success detail.
export interface ToolMessageResult {
  ok: boolean;
  error?: string;
  result?: string;
}

// --- Docker environments (Sandbox / Live) ---
export interface DockerStatus {
  available: boolean;
  version?: string;
  error?: string;
}
export interface DockerPort {
  private: number;
  public: number;
  type: string;
}
export interface DockerContainer {
  id: string;
  name: string;
  image: string;
  project: string;
  environment: "sandbox" | "live" | string;
  state: string; // running | exited | created | ...
  status: string; // "Up 2 minutes"
  ports: DockerPort[];
  webUrl?: string;      // internal Hub proxy (/x/...)
  directUrl?: string;   // custom domain when provisioned and active
  domainId?: string;    // domain record id (for status polling)
  domainStatus?: "pending" | "active" | "error"; // domain provisioning state
  cpuPercent: number;
  memUsed: number;
  memLimit: number;
  restartPolicy?: string;
  createdAt: number;
}
export interface CreateContainerReq {
  project: string;
  env: "sandbox" | "live";
  image: string;
  name?: string;
  ports?: number[];
  envVars?: Record<string, string>;
  webPort?: number;
}

// One chat captured during assisted chat detection (Telegram).
export interface DetectedChat {
  chatId: number;
  title: string;
  text: string;
}

// One polling round of assisted chat detection. ok=false carries an error
// (e.g. a webhook is active on the bot, so getUpdates is unavailable).
export interface DetectChatResult {
  ok: boolean;
  error?: string;
  chats: DetectedChat[];
  nextOffset: number;
}

// Aggregated Proxmox supervision snapshot (GET /api/tools/{id}/dashboard).
export interface ProxmoxInventory {
  running: number;
  stopped: number;
  total: number;
}
export interface ProxmoxDashboard {
  warning?: string; // non-fatal (e.g. node stats refused: token lacks PVEAuditor)
  cpu: { usage: number; cores: number }; // usage is a 0..1 fraction
  memory: { used: number; total: number }; // bytes
  storage: { used: number; total: number }; // bytes
  vms: ProxmoxInventory;
  containers: ProxmoxInventory;
  nodes: { name: string; status: string; cpu: number; mem: number; maxmem: number }[];
}

export interface S3Dashboard {
  count: number;
  buckets?: { name: string; creationDate: string }[];
  bucket?: string; // set when a default bucket is configured → objects preview
  objects?: { key: string; size: number; lastModified: string }[];
}

// --- Ollama supervision ---
export interface OllamaModel {
  name: string;
  size: number;
  modifiedAt: string;
  family?: string;
  params?: string;
}
export interface OllamaRunning {
  name: string;
  size: number;
  sizeVram: number;
  expiresAt: string;
}
export interface OllamaStatus {
  status: "active" | "unreachable" | "error" | "disabled";
  host?: string;
  version?: string;
  models?: OllamaModel[];
  running?: OllamaRunning[];
  error?: string;
}

// Runtime-editable settings (Configuration page).
export interface Settings {
  permissionMode: string;
  model: string;
  memoryMax: string;
  cpuQuota: string;
  maxTurns: number;
  inactivitySeconds: number;
  outputTruncation: boolean;
  publicBaseUrl: string;
  dnsServerIpMode: string; // "local" | "public" | "custom"
  dnsServerIp: string;     // used only when mode = "custom"
  handsFreeVoice: boolean; // hands-free voice mode (needs a fast server); off by default
}

export interface NetworkInfo {
  localIp: string;
  publicIp: string;
}

// A subscription usage limit: percent consumed and when it resets (ISO 8601).
export interface UsageWindow {
  utilization: number;
  resetsAt?: string | null;
}

// Claude plan usage, as shown in the top-of-page meter.
export interface Usage {
  session?: UsageWindow | null; // 5-hour rolling window
  week?: UsageWindow | null; // 7-day, all models
  weekOpus?: UsageWindow | null;
  weekSonnet?: UsageWindow | null;
  // True when the values come from the last stored snapshot (live fetch failed):
  // the meter shows them dimmed instead of disappearing.
  stale?: boolean;
}

// One time bucket of session-derived activity (Monitoring history charts).
export interface HistoryBucket {
  ts: string; // bucket start (ISO)
  inputTokens: number;
  outputTokens: number;
  costUSD: number;
  computeMs: number;
  sessionsStarted: number;
  sessionsActive: number; // sessions whose lifespan overlaps the bucket
}

// One sampled reading of the plan usage windows (utilization %, nullable).
export interface PlanPoint {
  ts: string;
  session?: number | null;
  week?: number | null;
  weekOpus?: number | null;
  weekSonnet?: number | null;
}

export type ClaudeRange = "hour" | "day" | "week" | "month";

// Account-scoped (or aggregated) Claude usage history backed by per-account RRD.
// series maps a data source (tok_in, tok_out, cost, sess_active) to per-bucket
// points; a point value is null for an unknown bucket. plan holds the % windows
// for a specific account only.
export interface ClaudeHistory {
  available: boolean;
  range: ClaudeRange;
  account: string; // "all" or an account id
  from?: number;
  to?: number;
  step?: number;
  series?: Record<string, { t: number; v: number | null }[]>;
  plan?: PlanPoint[];
}

// A connected Claude Code subscription (account/organization), isolated in its
// own CLAUDE_CONFIG_DIR. Exactly one is active at a time.
export interface ClaudeAccount {
  id: string;
  label: string;
  kind: "oauth" | "apikey";
  status: "connected" | "loggedout";
  active: boolean;
  orgId?: string;
  accountEmail?: string;
  createdAt: string;
  tokenExpiresAt?: string; // ISO 8601 — when the OAuth access token expires
}

// CLAUDE_ACCOUNT_CHANGED fires after the active subscription changes (switch /
// logout / connect / login finish) so the usage meter and dashboard refetch
// immediately instead of waiting for their next poll.
export const CLAUDE_ACCOUNT_CHANGED = "claude-account-changed";
function notifyAccountChange() {
  window.dispatchEvent(new Event(CLAUDE_ACCOUNT_CHANGED));
}

export type HistoryBucketKind = "hour" | "day" | "week";

export interface History {
  bucket: HistoryBucketKind;
  offset: number; // 0 = most recent window, 1 = the one before, …
  from: string;
  to: string;
  hasNewer: boolean;
  buckets: HistoryBucket[];
  plan: PlanPoint[];
}

// loginRedirect bounces to the app root when the session has expired or is
// missing. The SPA gate (main.tsx) then re-checks setup/auth state and renders
// the local login (or setup wizard) screen. Returns a never-resolving promise so
// callers don't proceed before the navigation happens.
function loginRedirect(): Promise<never> {
  window.location.href = "/";
  return new Promise<never>(() => {});
}

async function req<T>(method: string, path: string, body?: unknown, timeoutMs?: number): Promise<T> {
  let res: Response;
  try {
    res = await fetch(path, {
      method,
      headers: body ? { "Content-Type": "application/json" } : undefined,
      body: body ? JSON.stringify(body) : undefined,
      signal: timeoutMs ? AbortSignal.timeout(timeoutMs) : undefined,
    });
  } catch (e: any) {
    if (e?.name === "TimeoutError") throw new Error("Le serveur n'a pas répondu à temps. Vérifiez l'état ci-dessous avant de réessayer.");
    throw e;
  }
  if (res.status === 401) return loginRedirect();
  if (!res.ok) {
    const msg = await res.json().catch(() => ({ error: res.statusText }));
    throw new Error(msg.error ?? `HTTP ${res.status}`);
  }
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

// reqForm posts multipart/form-data (file uploads). The browser sets the
// Content-Type (with the boundary) itself, so we must not set it manually.
async function reqForm<T>(method: string, path: string, body: FormData): Promise<T> {
  const res = await fetch(path, { method, body });
  if (res.status === 401) return loginRedirect();
  if (!res.ok) {
    const msg = await res.json().catch(() => ({ error: res.statusText }));
    throw new Error(msg.error ?? `HTTP ${res.status}`);
  }
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

// Read-only script monitoring (GET /api/script-dashboard). Every string/`data`
// value comes from external scripts: untrusted, render as text only.
export interface ScriptReport {
  script: string;
  state: string;
  effective: string;
  summary: string;
  data: unknown;
  receivedAt: number;
  ageSeconds: number;
  silenceMinutes: number;
}
export interface ScriptHistoryItem {
  state: string;
  summary: string;
  data: unknown;
  receivedAt: number;
}
// Command channel (POST/GET/DELETE /api/script-commands). Never code: names are
// checked server-side against the allow-list of the script's key.
export type ScriptCommandState = "pending" | "delivered" | "ok" | "failed" | "cancelled" | "expired";
export interface ScriptCommand {
  id: string;
  script: string;
  name: string;
  params: unknown;
  createdBy: string;
  createdAt: number;
  expiresAt: number;
  state: ScriptCommandState;
  deliveredAt: number;
  deliveries: number;
  doneAt: number;
  message: string; // written by the script (ack) or by the Hub: untrusted text
}
// Screenshot uploaded by a script (POST /webhook/script-status/<name>/image). The
// bytes are fetched from /api/script-images/<script>/<id> (cookie-gated).
export interface ScriptImage {
  id: number;
  mime: string;
  width: number;
  height: number;
  size: number;
  receivedAt: number;
}
export interface ScriptDashboard {
  scripts: { status: ScriptReport; history: ScriptHistoryItem[]; commands: ScriptCommand[]; allowedCommands: string[]; images?: ScriptImage[] }[];
  now: number;
}

// Script API keys (GET/POST/PATCH/DELETE /api/script-keys). The secret is only
// ever present in the create/regenerate response.
export interface ScriptKey {
  id: string;
  script: string;
  label: string;
  allowedIps: string[];
  silenceMinutes: number;
  wake: boolean;
  enabled: boolean;
  createdAt: number;
  lastUsed: number;
  allowedCommands: string[];
  allowImages: boolean; // may upload screenshots (off by default)
}
export interface ScriptKeyInput {
  script: string;
  label?: string;
  allowedIps: string[];
  silenceMinutes: number;
  wake: boolean;
  allowedCommands?: string[]; // omitted on create = default list of the script
  allowImages?: boolean;
}
export interface ScriptKeyPatch {
  allowedIps?: string[];
  silenceMinutes?: number;
  wake?: boolean;
  enabled?: boolean;
  allowedCommands?: string[];
  allowImages?: boolean;
}

export const api = {
  assistantState: () => req<AssistantState>("GET", "/api/assistant"),
  assistantMessages: (after = 0) =>
    req<{ messages: AssistantMessage[]; busy: boolean }>("GET", `/api/assistant/messages?after=${after}`),
  assistantSend: (text: string, voice = false, attachments: string[] = []) =>
    req<{ accepted: boolean }>("POST", "/api/assistant/messages", {
      text,
      ...(voice ? { voice: true } : {}),
      ...(attachments.length ? { attachments } : {}),
    }),
  assistantUpload: (files: File[]) => {
    const fd = new FormData();
    for (const f of files) fd.append("files", f, f.name);
    return reqForm<{ attachments: AssistantAttachment[] }>("POST", "/api/assistant/attachments", fd);
  },
  assistantReset: (history: boolean) => req<void>("POST", `/api/assistant/reset${history ? "?history=1" : ""}`),
  assistantSetPerson: (person: string) => req<{ person: string }>("PUT", "/api/assistant/settings", { person }),
  assistantSetModel: (model: string) => req<AssistantState>("PUT", "/api/assistant/settings", { model }),
  assistantSetPush: (push: boolean, pushWhatsapp: boolean) =>
    req<AssistantState>("PUT", "/api/assistant/settings", { push, pushWhatsapp }),
  assistantSetNight: (patch: AssistantNightPatch) => req<AssistantState>("PUT", "/api/assistant/settings", patch),
  assistantEntities: (domain: "person" | "zone") => req<HaEntity[]>("GET", `/api/assistant/entities?domain=${domain}`),
  assistantTriggers: () => req<AssistantTrigger[]>("GET", "/api/assistant/triggers"),
  assistantCreateTrigger: (t: Pick<AssistantTrigger, "name" | "place" | "event" | "prompt">) =>
    req<AssistantTrigger>("POST", "/api/assistant/triggers", t),
  assistantUpdateTrigger: (id: string, t: Partial<Omit<AssistantTrigger, "id" | "lastFired">>) =>
    req<AssistantTrigger>("PATCH", `/api/assistant/triggers/${id}`, t),
  assistantDeleteTrigger: (id: string) => req<void>("DELETE", `/api/assistant/triggers/${id}`),
  assistantFireTrigger: (id: string) => req<{ accepted: boolean }>("POST", `/api/assistant/triggers/${id}/fire`),
  getProjectAccess: (id: string) => req<ProjectAccess>("GET", `/api/projects/${id}/access`),
  setProjectAccess: (id: string, a: ProjectAccess) => req<ProjectAccess>("PUT", `/api/projects/${id}/access`, a),
  markSessionSeen: (id: string) => req<void>("POST", `/api/sessions/${id}/seen`),
  listProjects: () => req<Project[]>("GET", "/api/projects"),
  // Soft-archived projects (Archivage page).
  listArchivedProjects: () => req<Project[]>("GET", "/api/projects?archived=true"),
  // Create a project. dir is optional: when empty the backend derives it as
  // <projects-dir>/<slug>. category is an optional grouping label.
  createProject: (name: string, category?: string, dir?: string) =>
    req<Project>("POST", "/api/projects", { name, category: category ?? "", dir: dir ?? "" }),
  getProject: (id: string) => req<Project>("GET", `/api/projects/${id}`),
  // Update user-editable project metadata. Partial: only the provided fields are
  // changed server-side (a star toggle won't wipe the comment, etc.).
  updateProject: (
    id: string,
    meta: Partial<{ starred: boolean; category: string; comment: string; claudeAccountId: string; model: string; showAutoSessions: boolean }>,
  ) => req<void>("PATCH", `/api/projects/${id}`, meta),
  // Permanently delete a project and all its sessions (irreversible). Refused
  // for protected projects or while a session is live.
  deleteProject: (id: string) => req<void>("DELETE", `/api/projects/${id}`),
  // Reversible soft-archive: hides the project from the board (stops live
  // sessions first). Refused (409) for a protected project.
  archiveProject: (id: string) => req<void>("POST", `/api/projects/${id}/archive`),
  unarchiveProject: (id: string) => req<void>("POST", `/api/projects/${id}/unarchive`),
  // Second deletion stage: move an archived project to the safe deposit box
  // (compressed .tar.gz). The on-disk project is destroyed only after the
  // archive is written. Reversible from Configuration → Projets supprimés.
  vaultProject: (id: string) => req<VaultEntry>("POST", `/api/projects/${id}/vault`),
  // Safe deposit box contents (compressed deleted projects).
  listVault: () => req<VaultEntry[]>("GET", "/api/vault"),
  // Signed-in devices: list, sign one out, or sign out all but this one.
  // Cloudflare connector (cookie-only). Reads are structured; every write needs confirm:true.
  cfStatus: () => req<{ configured: boolean; toolId?: string; allowWrites: boolean }>("GET", "/api/cloudflare/status"),
  cfRead: <T = any>(kind: "zones" | "dns" | "tunnels" | "routes" | "apps" | "idps", q: { zone?: string; tunnel?: string } = {}) =>
    req<T>("GET", `/api/cloudflare/read/${kind}?zone=${encodeURIComponent(q.zone ?? "")}&tunnel=${encodeURIComponent(q.tunnel ?? "")}`),
  cfAct: (action: string, args: Record<string, unknown>) =>
    req<{ ok: boolean; result: string }>("POST", "/api/cloudflare/act", { action, args, confirm: true }),
  cfTunnelToken: (id: string) =>
    req<{ token: string; service: string; docker: string }>("POST", `/api/cloudflare/tunnels/${encodeURIComponent(id)}/token`, { confirm: true }),
  cfAudit: () => req<{ id: number; ts: string; action: string; source: string; args: string; outcome: string; detail: string }[]>("GET", "/api/cloudflare/audit"),
  // DNS provider per zone (cookie-only). Switching never touches name servers.
  dnsProviders: () => req<{ default: string; providers: string[]; configured: Record<string, boolean>; zones: Record<string, string> }>("GET", "/api/dns/providers"),
  dnsSetProvider: (zone: string, provider: string) =>
    req<{ default: string; providers: string[]; configured: Record<string, boolean>; zones: Record<string, string> }>("PUT", "/api/dns/providers", { zone, provider }),
  dnsDryRun: (b: { zone: string; op: string; type?: string; name?: string; content?: string }) =>
    req<{ zone: string; provider: string; configured: boolean; steps: { do: string; detail: string }[]; warnings: string[] }>("POST", "/api/dns/dry-run", b),
  cfRevoke: () => req<{ ok: boolean; note: string }>("POST", "/api/cloudflare/revoke", {}),
  // Remote MCP connector (claude.ai custom connector).
  mcpRemote: () => req<McpRemote>("GET", "/api/mcp-remote"),
  mcpRemoteSettings: (b: { enabled?: boolean; publicUrl?: string }) => req<McpRemote>("PUT", "/api/mcp-remote/settings", b),
  mcpRemoteTool: (name: string, enabled: boolean) => req<McpRemote>("PUT", "/api/mcp-remote/tools", { name, enabled }),
  mcpRemoteRevoke: (id: string) => req<McpRemote>("DELETE", `/api/mcp-remote/grants/${encodeURIComponent(id)}`),
  mcpRemoteRevokeAll: () => req<McpRemote>("DELETE", "/api/mcp-remote/grants"),
  mcpRemoteAudit: () => req<McpAudit[]>("GET", "/api/mcp-remote/audit?limit=50"),
  listDevices: () => req<Device[]>("GET", "/api/devices"),
  revokeDevice: (id: string) => req<void>("DELETE", `/api/devices/${encodeURIComponent(id)}`),
  revokeOtherDevices: () => req<void>("DELETE", "/api/devices"),
  // Decompress an archive and bring the project back to the active board.
  restoreVault: (name: string) => req<Project>("POST", `/api/vault/${encodeURIComponent(name)}/restore`),
  // Permanently erase a vault archive — the only irreversible destruction.
  destroyVault: (name: string) => req<void>("DELETE", `/api/vault/${encodeURIComponent(name)}`),
  listSessions: (projectId: string) =>
    req<Session[]>("GET", `/api/projects/${projectId}/sessions`),
  listArchivedSessions: (projectId: string) =>
    req<Session[]>("GET", `/api/projects/${projectId}/sessions?archived=true`),
  archiveSession: (id: string) => req<void>("POST", `/api/sessions/${id}/archive`),
  unarchiveSession: (id: string) => req<void>("POST", `/api/sessions/${id}/unarchive`),
  // Every session across all projects, for the observability dashboard.
  listAllSessions: () => req<Session[]>("GET", "/api/sessions"),
  // Live per-session resource metrics (running sessions only).
  getMonitoring: () => req<MonitoringRow[]>("GET", "/api/monitoring"),
  // Live physical-host metrics (gauges banner).
  getHostMetrics: () => req<HostMetrics>("GET", "/api/host/metrics"),
  // RRD-backed host history for a chart range.
  getHostHistory: (range: HostRange) =>
    req<HostHistory>("GET", `/api/host/history?range=${range}`),
  // Historical activity bucketed by hour/day/week. offset pages the window into
  // the past (0 = most recent).
  getHistory: (bucket: HistoryBucketKind, offset: number) =>
    req<History>("GET", `/api/history?bucket=${bucket}&offset=${offset}`),
  // Runtime settings: read current effective values; update (applies to new sessions).
  getSettings: () => req<Settings>("GET", "/api/settings"),
  updateSettings: (s: Settings) => req<Settings>("PATCH", "/api/settings", s),
  getNetworkInfo: () => req<NetworkInfo>("GET", "/api/settings/network-info"),
  // Launch a session. Pass permissionMode "plan" for an autonomous session that
  // proposes a plan and gates execution behind approval; omit for the default.
  // Pass model to override the model for this one session (empty => project
  // default, then global default).
  launchSession: (projectId: string, permissionMode?: string, model?: string) =>
    req<Session>(
      "POST",
      `/api/projects/${projectId}/sessions`,
      permissionMode || model ? { permissionMode, model } : undefined,
    ),
  getSession: (id: string) => req<Session>("GET", `/api/sessions/${id}`),
  // Raw stream-json transcript, for replaying a finished session (no live socket).
  getJournal: (id: string) =>
    fetch(`/api/sessions/${id}/journal`).then((r) => {
      if (r.status === 401) return loginRedirect();
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      return r.text();
    }),
  stopSession: (id: string) => req<void>("POST", `/api/sessions/${id}/stop`),
  // Set a session's free-text note (human label). Editable in any state.
  setSessionComment: (id: string, comment: string) =>
    req<void>("PATCH", `/api/sessions/${id}`, { comment }),
  // Permanently delete a finished session and its journal (irreversible).
  deleteSession: (id: string) => req<void>("DELETE", `/api/sessions/${id}`),
  // Approve or reject a plan proposed via ExitPlanMode. "approve" resumes the
  // session in bypassPermissions to execute it; "reject" keeps it in plan mode.
  // Backend endpoint lands in slice 2 step (2).
  decidePlan: (sessionId: string, decision: "approve" | "reject") =>
    req<void>("POST", `/api/sessions/${sessionId}/plan-decision`, { decision }),
  // Answer a session's pending mcp__ask__ask_user question. This unblocks the
  // ask-mcp call holding the session, which returns the answer as the tool_result
  // so claude resumes with the real choice (no auto-deny). 409 if nothing pending.
  answerQuestion: (sessionId: string, answer: string) =>
    req<void>("POST", `/api/sessions/${sessionId}/answer`, { answer }),
  // Identity of the signed-in user, or null when auth is disabled / the session
  // is gone. Deliberately not routed through req(): a 401 here must fail quietly
  // (no login bounce), and when auth is off the route falls through to the SPA's
  // index.html (HTTP 200 text/html) — so only a JSON {email} payload counts.
  whoami: async (): Promise<{ email: string } | null> => {
    try {
      const r = await fetch("/auth/me");
      if (!r.ok) return null;
      if (!r.headers.get("content-type")?.includes("application/json")) return null;
      const body = await r.json();
      return typeof body?.email === "string" ? { email: body.email } : null;
    } catch {
      return null;
    }
  },
  // Clear the session cookie, then reload to the app root (which bounces to the
  // OIDC login when auth is on).
  logout: async (): Promise<void> => {
    await fetch("/auth/logout", { method: "POST" }).catch(() => {});
    window.location.href = "/";
  },
  // --- Claude Code subscription management ---
  // List connected/known subscriptions (the active one has active=true).
  listClaudeAccounts: () => req<ClaudeAccount[]>("GET", "/api/claude/accounts"),
  // RRD-backed Claude usage history, scoped to an account ("all" = aggregated).
  getClaudeHistory: (range: ClaudeRange, account: string) =>
    req<ClaudeHistory>("GET", `/api/claude/history?range=${range}&account=${encodeURIComponent(account)}`),
  // Link a new subscription. An apikey account is connected + activated at once;
  // an oauth account is created logged-out and completed via start/finish login.
  connectClaudeAccount: async (
    label: string,
    kind: "oauth" | "apikey",
    apiKey?: string,
  ): Promise<ClaudeAccount> => {
    const a = await req<ClaudeAccount>("POST", "/api/claude/login", { label, kind, apiKey: apiKey ?? "" });
    notifyAccountChange();
    return a;
  },
  // Make an existing connected subscription the active one.
  switchClaudeAccount: async (id: string): Promise<void> => {
    await req<void>("POST", "/api/claude/switch", { id });
    notifyAccountChange();
  },
  // Disconnect a subscription (stops its live sessions, archives its credentials).
  // Omit id to target the active account.
  logoutClaudeAccount: async (id?: string): Promise<void> => {
    await req<void>("POST", "/api/claude/logout", id ? { id } : {});
    notifyAccountChange();
  },
  // Start OAuth sign-in: the backend runs `claude auth login` under a PTY and
  // returns the authorize URL to open in a browser.
  startClaudeLogin: (id: string) =>
    req<{ authUrl: string; configDir?: string }>("POST", `/api/claude/accounts/${id}/login/start`),
  // Submit the code obtained after authorizing; validates + activates.
  finishClaudeLogin: async (id: string, code: string): Promise<ClaudeAccount> => {
    const a = await req<ClaudeAccount>("POST", `/api/claude/accounts/${id}/login/finish`, { code });
    notifyAccountChange();
    return a;
  },
  // Scheduled tasks
  // Domain management: DNS + Let's Encrypt (DNS-01) + Caddy routing.
  listDomains: () => req<Domain[]>("GET", "/api/domains"),
  createDomain: (input: CreateDomainInput) => req<Domain>("POST", "/api/domains", input),
  getDomainStatus: (id: string) => req<DomainStatus>("GET", `/api/domains/${id}/status`),
  deleteDomain: (id: string) => req<void>("DELETE", `/api/domains/${id}`),

  scriptKeys: () => req<ScriptKey[]>("GET", "/api/script-keys"),
  scriptKeyCreate: (k: ScriptKeyInput) => req<{ key: ScriptKey; secret: string }>("POST", "/api/script-keys", k),
  scriptKeyUpdate: (id: string, p: ScriptKeyPatch) => req<void>("PATCH", `/api/script-keys/${encodeURIComponent(id)}`, p),
  scriptKeyRegenerate: (id: string) =>
    req<{ key: ScriptKey; secret: string }>("POST", `/api/script-keys/${encodeURIComponent(id)}/regenerate`),
  scriptKeyDelete: (id: string) => req<void>("DELETE", `/api/script-keys/${encodeURIComponent(id)}`),
  scriptImagesDelete: (script: string) =>
    req<{ deleted: number }>("DELETE", `/api/script-images/${encodeURIComponent(script)}`),
  scriptDashboard: (): Promise<ScriptDashboard> => req<ScriptDashboard>("GET", "/api/script-dashboard"),
  scriptCommandSend: (c: { script: string; name: string; params?: unknown; ttlMinutes?: number }) =>
    req<ScriptCommand>("POST", "/api/script-commands", c),
  scriptCommandCancel: (id: string) => req<void>("DELETE", `/api/script-commands/${encodeURIComponent(id)}`),

  listTasks: (projectId?: string): Promise<ScheduledTask[]> =>
    req<ScheduledTask[]>("GET", projectId !== undefined ? `/api/tasks?project=${encodeURIComponent(projectId)}` : "/api/tasks"),
  createTask: (input: ScheduledTaskInput): Promise<ScheduledTask> =>
    req<ScheduledTask>("POST", "/api/tasks", input),

  // Create a one-shot task that launches a session at the given absolute time.
  createDeferredSession: (
    projectId: string,
    launchAt: Date,
    permissionMode: string,
    model?: string,
  ): Promise<ScheduledTask> =>
    req<ScheduledTask>("POST", "/api/tasks", {
      name: "Lancement différé",
      projectId,
      prompt: "",
      schedule: "",
      enabled: true,
      permissionMode,
      model: model ?? "",
      oneShot: true,
      launchAt: launchAt.toISOString(),
    }),
  updateTask: (id: string, input: ScheduledTaskInput): Promise<ScheduledTask> =>
    req<ScheduledTask>("PATCH", `/api/tasks/${id}`, input),
  deleteTask: (id: string): Promise<void> =>
    req<void>("DELETE", `/api/tasks/${id}`),
  runTaskNow: (id: string): Promise<{ sessionId: string }> =>
    req<{ sessionId: string }>("POST", `/api/tasks/${id}/run`),

  // Rename a subscription's display label.
  renameClaudeAccount: (id: string, label: string): Promise<void> =>
    req<void>("PATCH", `/api/claude/accounts/${id}`, { label }),
  // Permanently remove a non-active, session-free subscription.
  deleteClaudeAccount: async (id: string): Promise<void> => {
    await req<void>("DELETE", `/api/claude/accounts/${id}`);
    notifyAccountChange();
  },
  // --- Skills (global, transversal) ---
  // List skills, optionally filtered server-side by a free-text query.
  listSkills: (q?: string) =>
    req<Skill[]>("GET", `/api/skills${q ? `?q=${encodeURIComponent(q)}` : ""}`),
  // Create a skill: registers it and spins up a background session (its id is
  // returned as sessionId). mode "design" (default) authors a new SKILL.md;
  // "discover" makes the session search the web for an existing Claude Code skill
  // matching the request and install it (the intelligent search of existing skills).
  createSkill: (name: string, description?: string, mode: "design" | "discover" = "design") =>
    req<Skill>("POST", "/api/skills", { name, description: description ?? "", mode }),
  getSkill: (id: string) => req<Skill>("GET", `/api/skills/${id}`),
  // Partial update: any of name/description/keywords/status. Setting status to
  // "enabled" validates the skill (Claude then loads it); "disabled" turns it off.
  updateSkill: (
    id: string,
    patch: Partial<{ name: string; description: string; keywords: string; status: Skill["status"] }>,
  ) => req<Skill>("PATCH", `/api/skills/${id}`, patch),
  // (Re)open an editing session on an existing skill to make it evolve. Reuses a
  // live session or launches a fresh one; returns the skill with its sessionId.
  evolveSkill: (id: string) => req<Skill>("POST", `/api/skills/${id}/evolve`),
  // Stop a skill's editing session without changing its status (no idle session).
  closeSkillSession: (id: string) => req<Skill>("POST", `/api/skills/${id}/close`),
  // Permanently delete a skill: stops its authoring session, removes the on-disk
  // definition and the index row (irreversible).
  deleteSkill: (id: string) => req<void>("DELETE", `/api/skills/${id}`),
  // --- RAG (global knowledge base) ---
  ragStatus: () => req<RagStatus>("GET", "/api/rag/status"),
  // Knowledge bases.
  listRagCollections: () => req<RagCollection[]>("GET", "/api/rag/collections"),
  createRagCollection: (name: string, description?: string, category?: string) =>
    req<RagCollection>("POST", "/api/rag/collections", {
      name,
      description: description ?? "",
      category: category ?? "",
    }),
  updateRagCollection: (
    id: string,
    patch: Partial<{ name: string; description: string; category: string }>,
  ) => req<RagCollection>("PATCH", `/api/rag/collections/${id}`, patch),
  // Single collection (for the dedicated page) — derived from the list.
  getRagCollection: async (id: string) =>
    (await req<RagCollection[]>("GET", "/api/rag/collections")).find((c) => c.id === id) ?? null,
  // Delete a collection and all its documents (the default collection is protected).
  deleteRagCollection: (id: string) => req<void>("DELETE", `/api/rag/collections/${id}`),
  // List documents, optionally filtered server-side by query and/or collection.
  listRagDocuments: (q?: string, collection?: string) => {
    const p = new URLSearchParams();
    if (q) p.set("q", q);
    if (collection) p.set("collection", collection);
    const qs = p.toString();
    return req<RagDocument[]>("GET", `/api/rag/documents${qs ? `?${qs}` : ""}`);
  },
  // Ingest pasted text/markdown into a collection. Queues async indexing.
  addRagText: (title: string, text: string, collectionId?: string) =>
    req<RagDocument>("POST", "/api/rag/documents", { title, text, collectionId: collectionId ?? "" }),
  // Ingest a web page; the worker fetches and extracts it.
  addRagURL: (url: string, title?: string, collectionId?: string) =>
    req<RagDocument>("POST", "/api/rag/documents/url", {
      url,
      title: title ?? "",
      collectionId: collectionId ?? "",
    }),
  // Upload files (zip, tgz, …) into a session's project working dir. They land
  // under .hub-uploads/<ts>/ where the running claude can process them; the
  // composer then references the returned paths in the prompt. Images and small
  // PDFs are sent inline over the WS instead and never reach this endpoint.
  uploadAttachments: (sessionId: string, files: File[]) => {
    const fd = new FormData();
    for (const f of files) fd.append("files", f);
    return reqForm<{ files: AttachmentResult[] }>(
      "POST",
      `/api/sessions/${sessionId}/attachments`,
      fd,
    ).then((r) => r.files);
  },
  // Upload a file (text/markdown/PDF) as multipart form-data.
  uploadRagFile: (file: File, title?: string, collectionId?: string) => {
    const fd = new FormData();
    fd.append("file", file);
    if (title) fd.append("title", title);
    if (collectionId) fd.append("collectionId", collectionId);
    return reqForm<RagDocument>("POST", "/api/rag/documents", fd);
  },
  // Toggle a document in/out of the global context, or rename it.
  updateRagDocument: (id: string, patch: Partial<{ enabled: boolean; title: string }>) =>
    req<RagDocument>("PATCH", `/api/rag/documents/${id}`, patch),
  // Re-run the indexing pipeline (e.g. after an error or a web-page update).
  reindexRagDocument: (id: string) =>
    req<RagDocument>("POST", `/api/rag/documents/${id}/reindex`),
  getRagDocumentContent: (id: string) =>
    req<{ content: string; truncated: boolean }>("GET", `/api/rag/documents/${id}/content`),
  deleteRagDocument: (id: string) => req<void>("DELETE", `/api/rag/documents/${id}`),
  // --- Ollama supervision ---
  getOllama: () => req<OllamaStatus>("GET", "/api/ollama"),
  unloadOllamaModel: (model: string) =>
    req<void>("POST", "/api/ollama/unload", { model }),
  // --- Per-project RAG (private code index) ---
  getProjectRag: (projectId: string) => req<ProjectRag>("GET", `/api/projects/${projectId}/rag`),
  updateProjectRag: (
    projectId: string,
    patch: Partial<{ enabled: boolean; autoOnChange: boolean; intervalMinutes: number }>,
  ) => req<ProjectRag>("PATCH", `/api/projects/${projectId}/rag`, patch),
  reindexProjectRag: (projectId: string) =>
    req<ProjectRag>("POST", `/api/projects/${projectId}/rag/reindex`),
  // --- Tools (external integrations) ---
  listConnectors: () => req<Connector[]>("GET", "/api/tools/connectors"),
  listTools: () => req<Tool[]>("GET", "/api/tools"),
  getTool: (id: string) => req<Tool>("GET", `/api/tools/${id}`),
  createTool: (type: string, name: string, config: Record<string, string>) =>
    req<Tool>("POST", "/api/tools", { type, name, config }),
  // Partial update. A secret field left empty keeps its stored value.
  updateTool: (
    id: string,
    patch: Partial<{ name: string; enabled: boolean; config: Record<string, string>; category: string }>,
  ) => req<Tool>("PATCH", `/api/tools/${id}`, patch),
  // Tool categories (grouping of the Tools page). category "" = automatic.
  listToolCategories: () => req<ToolCategory[]>("GET", "/api/tool-categories"),
  createToolCategory: (name: string) => req<ToolCategory>("POST", "/api/tool-categories", { name }),
  renameToolCategory: (id: string, name: string) =>
    req<ToolCategory[]>("PATCH", `/api/tool-categories/${id}`, { name }),
  deleteToolCategory: (id: string) => req<void>("DELETE", `/api/tool-categories/${id}`),
  reorderToolCategories: (ids: string[]) =>
    req<ToolCategory[]>("PUT", "/api/tool-categories/order", { ids }),
  deleteTool: (id: string) => req<void>("DELETE", `/api/tools/${id}`),
  // Test an unsaved config (before saving).
  testToolConfig: (type: string, config: Record<string, string>) =>
    req<ToolTestResult>("POST", "/api/tools/test", { type, config }),
  // Test a saved tool with its stored credentials; records status server-side.
  testTool: (id: string) => req<ToolTestResult>("POST", `/api/tools/${id}/test`),
  // Assisted chat detection (Telegram): one short-poll round over getUpdates on
  // an unsaved config. Stateless — pass the previous nextOffset back in.
  detectChat: (type: string, config: Record<string, string>, offset: number) =>
    req<DetectChatResult>("POST", "/api/tools/detect-chat", { type, config, offset }),
  // Live supervision snapshot of a saved tool whose connector has a dashboard
  // (Proxmox). Creds are decrypted server-side.
  toolDashboard: (id: string) => req<ProxmoxDashboard>("GET", `/api/tools/${id}/dashboard`),
  s3Dashboard: (id: string) => req<S3Dashboard>("GET", `/api/tools/${id}/dashboard`),
  // Send a canned test message on an unsaved config (token typed in the form).
  testMessage: (type: string, config: Record<string, string>) =>
    req<ToolMessageResult>("POST", "/api/tools/test-message", { type, config }),
  // Send a canned test message for a saved tool, with an optional config override
  // (e.g. a freshly-detected chat_id not yet persisted). Uses the stored secret.
  testMessageTool: (id: string, config: Record<string, string>) =>
    req<ToolMessageResult>("POST", `/api/tools/${id}/test-message`, { config }),
  // WhatsApp: QR pairing status + trigger.
  whatsappQR: (id: string) =>
    req<WhatsAppQRInfo>("GET", `/api/tools/${id}/qr`),
  whatsappConnect: (id: string) =>
    req<{ status: string }>("POST", `/api/tools/${id}/connect`),
  // Proton Calendar: stored session + one-time interactive login.
  protonStatus: (id: string) => req<ProtonStatus>("GET", `/api/tools/${id}/proton`),
  protonConnect: (
    id: string,
    body: { username?: string; password?: string; mailboxPassword?: string; totp?: string; captchaDone?: boolean },
  ) => req<ProtonConnectResult>("POST", `/api/tools/${id}/proton/connect`, body),
  protonCancel: (id: string) => req<{ ok: boolean; cancelled: boolean }>("POST", `/api/tools/${id}/proton/cancel`),
  // Proton Bridge (mail): guided account add + managed IMAP instance.
  bridgeStatus: () => req<BridgeStatus>("GET", "/api/proton-bridge"),
  bridgeConnect: (body: { email?: string; password?: string; totp?: string; mailboxPassword?: string; captchaDone?: boolean }) =>
    req<BridgeConnectResult>("POST", "/api/proton-bridge/connect", body, 170_000),
  bridgeCancel: () => req<{ ok: boolean; cancelled: boolean }>("POST", "/api/proton-bridge/cancel"),
  bridgeDisconnect: () => req<{ ok: boolean; error?: string }>("POST", "/api/proton-bridge/disconnect", undefined, 170_000),
  // --- Docker environments (Sandbox / Live) ---
  dockerStatus: () => req<DockerStatus>("GET", "/api/docker/status"),
  listContainers: (env: "sandbox" | "live") =>
    req<DockerContainer[]>("GET", `/api/docker/containers?env=${env}`),
  containerAction: (id: string, action: "start" | "stop" | "restart") =>
    req<void>("POST", `/api/docker/containers/${id}/action`, { action }),
  removeContainer: (id: string) => req<void>("DELETE", `/api/docker/containers/${id}`),
  createContainer: (req_: CreateContainerReq) =>
    req<{ id: string }>("POST", "/api/docker/containers", req_),
  provisionContainerDomain: (name: string, domain: string, serverIP?: string) =>
    req<Domain>("POST", `/api/docker/containers/${encodeURIComponent(name)}/domain`, { domain, serverIP }),
  removeContainerDomain: (name: string) =>
    req<void>("DELETE", `/api/docker/containers/${encodeURIComponent(name)}/domain`),
  // Claude plan usage for the top-of-page meter. Returns null when unavailable
  // (503: token expired/missing, upstream down) so the meter hides quietly.
  // accountId scopes the meter to a specific subscription (a project's account);
  // omit it for the default (active) account, i.e. the page-wide meter.
  getUsage: async (accountId?: string): Promise<Usage | null> => {
    try {
      const q = accountId ? `?account=${encodeURIComponent(accountId)}` : "";
      const r = await fetch(`/api/usage${q}`);
      if (!r.ok) return null;
      return (await r.json()) as Usage;
    } catch {
      return null;
    }
  },

  // --- First-boot setup wizard + local authentication ---
  // Whether the wizard must run, and which login methods are offered. Returns null
  // when auth is disabled (dev): then the endpoint isn't registered and the SPA
  // catch-all answers with HTML, so a non-JSON body means "no gate — mount the app
  // directly". A real JSON payload drives the setup/login decision.
  setupStatus: async (): Promise<{ needsSetup: boolean; oidcEnabled: boolean } | null> => {
    try {
      const r = await fetch("/setup/status");
      if (!r.ok || !r.headers.get("content-type")?.includes("application/json")) return null;
      return (await r.json()) as { needsSetup: boolean; oidcEnabled: boolean };
    } catch {
      return null;
    }
  },
  // Create the first admin; returns the TOTP enrolment (QR PNG + secret + URI).
  setupAdmin: (username: string, email: string, password: string) =>
    rawPost<{ otpauthUrl: string; secret: string; qrPng: string }>("/setup/admin", {
      username,
      email,
      password,
    }),
  // Validate the first TOTP code to confirm enrolment.
  setupConfirmTotp: (username: string, code: string) =>
    rawPost<{ ok: boolean }>("/setup/totp/confirm", { username, code }),
  // Persist the public base URL and optional OIDC config (discovered immediately).
  setupOptions: (opts: {
    publicBaseUrl: string;
    oidc?: {
      enabled: boolean;
      issuer: string;
      clientId: string;
      clientSecret: string;
      redirectUrl: string;
      allowedEmails: string;
    };
  }) => rawPost<{ ok: boolean }>("/setup/options", opts),
  // Optional wizard step: link a Claude subscription before finishing. apikey is
  // connected + activated at once; oauth is created logged-out and completed via
  // start/finish. Ungated (the wizard is pre-login); refused once setup is done.
  setupConnectClaude: (label: string, kind: "oauth" | "apikey", apiKey?: string) =>
    rawPost<ClaudeAccount>("/setup/claude/connect", { label, kind, apiKey: apiKey ?? "" }),
  setupClaudeLoginStart: (id: string) =>
    rawPost<{ authUrl: string; configDir?: string }>(`/setup/claude/${id}/login/start`, {}),
  setupClaudeLoginFinish: (id: string, code: string) =>
    rawPost<ClaudeAccount>(`/setup/claude/${id}/login/finish`, { code }),
  // Close the wizard; signs the operator in.
  setupFinish: (username: string) => rawPost<{ ok: boolean }>("/setup/finish", { username }),
  // Local login step 1: verify password. totpRequired=true => prompt for a code.
  // remember ("stay signed in") makes the session last until logout instead of 12 h.
  localLogin: (username: string, password: string, remember = false) =>
    rawPost<{ totpRequired: boolean }>("/auth/local-login", { username, password, remember }),
  // Local login step 2: submit the TOTP code to obtain the session.
  totpVerify: (code: string) => rawPost<{ ok: boolean }>("/auth/totp", { code }),
};

// rawPost posts JSON to an ungated /auth or /setup endpoint and surfaces the
// server's {error} message. Unlike req(), it never bounces on 401 — these flows
// ARE the login, so a 401 is a result to display, not a redirect trigger.
async function rawPost<T>(path: string, body: unknown): Promise<T> {
  const res = await fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    const msg = await res.json().catch(() => ({ error: res.statusText }));
    throw new Error(msg.error ?? `HTTP ${res.status}`);
  }
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}
