import { CloudflarePanel } from "../components/CloudflarePanel";
import { DnsProviderPanel } from "../components/DnsProviderPanel";
import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { QRCodeSVG } from "qrcode.react";
import {
  api,
  type BridgeStatus,
  type Connector,
  type DetectedChat,
  type DetectChatResult,
  type ProxmoxDashboard,
  type ProtonStatus,
  type S3Dashboard,
  type Tool,
  type ToolCategory,
  type ToolField,
  type WhatsAppQRInfo,
} from "../lib/api";

// ToolsView is the global Tools module: a catalog of connector types (Proxmox,
// Telegram, Email, …) with their connection status, and a dynamic configuration
// form per connector with an inline "test connection" before saving. Credentials
// are encrypted at rest by the backend and never returned here. Once configured
// and enabled, a connector's actions are exposed to sessions via the tools-mcp tool.
type Editing = { connector: Connector; tool: Tool | null };

export function ToolsView() {
  const { t } = useTranslation();
  const [connectors, setConnectors] = useState<Connector[]>([]);
  const [tools, setTools] = useState<Tool[]>([]);
  const [editing, setEditing] = useState<Editing | null>(null);
  const [adding, setAdding] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [cats, setCats] = useState<ToolCategory[]>([]);
  const [query, setQuery] = useState("");
  const [organize, setOrganize] = useState(false);
  const [newCat, setNewCat] = useState("");
  // Collapsed sections are a per-browser preference (the category order itself is server-side).
  const [collapsed, setCollapsed] = useState<string[]>(() => {
    try {
      return JSON.parse(localStorage.getItem("claudehub.tools.collapsed") ?? "[]");
    } catch {
      return [];
    }
  });

  async function load() {
    const [c, t, k] = await Promise.all([api.listConnectors(), api.listTools(), api.listToolCategories()]);
    setConnectors(c);
    setTools(t);
    setCats(k);
  }

  function toggleCollapsed(id: string) {
    setCollapsed((cur) => {
      const next = cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id];
      localStorage.setItem("claudehub.tools.collapsed", JSON.stringify(next));
      return next;
    });
  }

  const catLabel = (c: ToolCategory) => c.name || t(`tools.cat_${c.key}`, { defaultValue: c.key });

  async function run(p: Promise<unknown>) {
    try {
      await p;
      await load();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  function moveCat(idx: number, dir: -1 | 1) {
    const ids = cats.map((c) => c.id);
    const j = idx + dir;
    if (j < 0 || j >= ids.length) return;
    [ids[idx], ids[j]] = [ids[j], ids[idx]];
    run(api.reorderToolCategories(ids));
  }

  const q = query.trim().toLowerCase();
  // A tool matches on its name, its connector label/type, or any action name/description.
  const matches = (tool: Tool): string[] | null => {
    if (!q) return [];
    const conn = connectors.find((c) => c.type === tool.type);
    if ([tool.name, conn?.label ?? "", tool.type].some((x) => x.toLowerCase().includes(q))) return [];
    const hit = (conn?.actions ?? []).filter(
      (a) => a.name.toLowerCase().includes(q) || a.description.toLowerCase().includes(q),
    );
    return hit.length ? hit.map((a) => a.name) : null;
  };

  useEffect(() => {
    load().catch((e) => setError(e.message));
  }, []);

  const labelFor = useMemo(() => {
    const m = new Map(connectors.map((c) => [c.type, c.label]));
    return (type: string) => m.get(type) ?? type;
  }, [connectors]);

  function editTool(t: Tool) {
    const conn = connectors.find((c) => c.type === t.type);
    if (conn) {
      setAdding(false);
      setEditing({ connector: conn, tool: t });
    }
  }

  function addOfType(conn: Connector) {
    setAdding(false);
    setEditing({ connector: conn, tool: null });
  }

  return (
    <div className="p-4 md:p-8 max-w-4xl mx-auto w-full">
      <h1 className="text-2xl font-semibold mb-1">{t("tools.title")}</h1>
      <p className="text-sm text-muted mb-5">
        {t("tools.introBefore")}
        <span className="mono">tools</span>
        {t("tools.introAfter")}
      </p>

      {error && <div className="text-danger text-sm mb-3">{error}</div>}

      <ProtonBridgePanel onChanged={load} />

      {/* Configured tool instances */}
      <div className="flex items-center justify-between mb-2">
        <h2 className="text-xs font-semibold uppercase tracking-wider text-muted">
          {t("tools.configuredTools", { count: tools.length })}
        </h2>
        <button
          onClick={() => {
            setEditing(null);
            setAdding((a) => !a);
          }}
          className="bg-accent text-bg font-medium rounded px-3 py-1.5 text-sm hover:bg-accent-dim transition-colors"
        >
          {adding ? t("common.cancel") : t("tools.addTool")}
        </button>
      </div>

      {tools.length === 0 && !adding && (
        <div className="text-muted text-sm py-6 text-center bg-panel border border-border rounded-lg">
          {t("tools.emptyState")}
        </div>
      )}

      {tools.length > 0 && (
        <div className="flex items-center gap-2 mb-3">
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t("tools.searchPlaceholder")}
            className="flex-1 bg-panel border border-border rounded px-3 py-1.5 text-sm"
          />
          <button
            onClick={() => setOrganize((o) => !o)}
            className="border border-border rounded px-3 py-1.5 text-sm hover:border-accent-dim transition-colors"
          >
            {organize ? t("tools.organizeDone") : t("tools.organize")}
          </button>
        </div>
      )}

      {organize && (
        <form
          className="flex gap-2 mb-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (!newCat.trim()) return;
            run(api.createToolCategory(newCat));
            setNewCat("");
          }}
        >
          <input
            value={newCat}
            onChange={(e) => setNewCat(e.target.value)}
            placeholder={t("tools.newCategory")}
            className="flex-1 bg-panel border border-border rounded px-3 py-1.5 text-sm"
          />
          <button className="bg-accent text-bg rounded px-3 py-1.5 text-sm" type="submit">
            {t("tools.addCategory")}
          </button>
        </form>
      )}

      {q && tools.every((tool) => matches(tool) === null) && (
        <div className="text-muted text-sm py-4 text-center">{t("tools.noMatch", { q: query })}</div>
      )}

      {cats.map((cat, idx) => {
        const items = tools
          .filter((tool) => tool.category === cat.id)
          .map((tool) => ({ tool, hits: matches(tool) }))
          .filter((x) => x.hits !== null) as { tool: Tool; hits: string[] }[];
        if (items.length === 0 && !(organize && !q)) return null;
        const open = q ? true : !collapsed.includes(cat.id);
        return (
          <section key={cat.id} className="mb-4">
            <div className="flex items-center gap-2 mb-2">
              <button
                onClick={() => toggleCollapsed(cat.id)}
                className="flex items-center gap-2 text-xs font-semibold uppercase tracking-wider text-muted hover:text-text"
                aria-expanded={open}
              >
                <span>{open ? "▾" : "▸"}</span>
                <span>{catLabel(cat)}</span>
                <span className="font-normal">({items.length})</span>
              </button>
              {organize && (
                <span className="ml-auto flex items-center gap-1 text-xs">
                  <button title={t("tools.moveUp")} disabled={idx === 0} onClick={() => moveCat(idx, -1)} className="px-1.5 disabled:opacity-30">↑</button>
                  <button title={t("tools.moveDown")} disabled={idx === cats.length - 1} onClick={() => moveCat(idx, 1)} className="px-1.5 disabled:opacity-30">↓</button>
                  <button
                    onClick={() => {
                      const n = window.prompt(t("tools.renamePrompt"), cat.name);
                      if (n !== null) run(api.renameToolCategory(cat.id, n));
                    }}
                    className="px-1.5 hover:text-accent"
                  >
                    {t("tools.renameCategory")}
                  </button>
                  {cat.id !== "other" && (
                    <button
                      onClick={() => {
                        if (window.confirm(t("tools.confirmDeleteCategory", { name: catLabel(cat) }))) {
                          run(api.deleteToolCategory(cat.id));
                        }
                      }}
                      className="px-1.5 hover:text-danger"
                    >
                      {t("tools.deleteCategory")}
                    </button>
                  )}
                </span>
              )}
            </div>
            {open && (
              <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
                {items.map(({ tool, hits }) => {
                  const conn = connectors.find((c) => c.type === tool.type);
                  return (
                    <div key={tool.id} className="flex flex-col gap-1">
                      <button
                        onClick={() => editTool(tool)}
                        className={`text-left bg-panel border rounded-lg p-4 transition-colors ${
                          editing?.tool?.id === tool.id ? "border-accent-dim" : "border-border hover:border-accent-dim/60"
                        }`}
                      >
                        <div className="flex items-center justify-between gap-2">
                          <span className="font-medium truncate">{tool.name}</span>
                          <StatusDot status={tool.status} enabled={tool.enabled} />
                        </div>
                        <div className="text-xs text-muted mt-1">
                          {labelFor(tool.type)} · {statusLabel(tool, t)}
                          {conn && conn.actions.length > 0 && <> · {t("tools.actionsCount", { count: conn.actions.length })}</>}
                        </div>
                        {hits.length > 0 && <div className="text-[11px] mono text-accent mt-1 break-words">{hits.join(" · ")}</div>}
                        {tool.type === "proxmox" && tool.status === "connected" && tool.enabled && (
                          <ProxmoxTileSummary id={tool.id} />
                        )}
                        {tool.type === "s3" && tool.status === "connected" && tool.enabled && (
                          <S3TileSummary id={tool.id} />
                        )}
                      </button>
                      {organize && (
                        <select
                          aria-label={t("tools.moveTo")}
                          value={tool.categoryAuto ? "" : tool.category}
                          onChange={(e) => run(api.updateTool(tool.id, { category: e.target.value }))}
                          className="bg-panel border border-border rounded px-2 py-1 text-xs"
                        >
                          <option value="">
                            {t("tools.categoryAuto", {
                              name: (() => {
                                const d = cats.find((c) => c.id === conn?.defaultCategory) ?? cats.find((c) => c.id === "other");
                                return d ? catLabel(d) : "";
                              })(),
                            })}
                          </option>
                          {cats.map((c) => (
                            <option key={c.id} value={c.id}>
                              {catLabel(c)}
                            </option>
                          ))}
                        </select>
                      )}
                    </div>
                  );
                })}
              </div>
            )}
          </section>
        );
      })}

      {/* Connector-type picker, shown when adding a new tool */}
      {adding && (
        <div className="bg-panel border border-border rounded-lg p-4 mt-3">
          <div className="text-xs text-muted mb-2">{t("tools.pickConnectorType")}</div>
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-2">
            {connectors.map((c) => (
              <button
                key={c.type}
                onClick={() => addOfType(c)}
                className="text-left border border-border rounded-lg px-3 py-2 text-sm hover:border-accent-dim hover:bg-panel-2 transition-colors"
              >
                {c.label}
              </button>
            ))}
          </div>
          <p className="text-[11px] text-muted/70 mt-3">
            {t("tools.extensibleHint")}
          </p>
        </div>
      )}

      {editing && (
        <ConnectorForm
          key={editing.tool?.id ?? `new-${editing.connector.type}`}
          connector={editing.connector}
          tool={editing.tool}
          onClose={() => setEditing(null)}
          onChanged={load}
          onError={setError}
        />
      )}
    </div>
  );
}

function statusLabel(tool: Tool, t: TFunction): string {
  const base =
    tool.status === "connected"
      ? t("tools.statusConnected")
      : tool.status === "error"
        ? t("tools.statusError")
        : t("tools.statusDisconnected");
  return tool.enabled ? base : `${base} · ${t("tools.statusDisabledSuffix")}`;
}

function StatusDot({ status, enabled }: { status?: Tool["status"]; enabled?: boolean }) {
  const color =
    status === "connected" && enabled
      ? "bg-ok"
      : status === "error"
        ? "bg-danger"
        : "bg-muted/50";
  return <span className={`inline-block w-2.5 h-2.5 rounded-full ${color}`} />;
}

// ConnectorForm renders the dynamic configuration form for one connector, with a
// test-connection button and save/delete actions.
function ConnectorForm({
  connector,
  tool,
  onClose,
  onChanged,
  onError,
}: {
  connector: Connector;
  tool: Tool | null;
  onClose: () => void;
  onChanged: () => Promise<void>;
  onError: (msg: string) => void;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState(tool?.name ?? connector.label);
  const [values, setValues] = useState<Record<string, string>>({});
  const [secretsSet, setSecretsSet] = useState<Record<string, boolean>>({});
  const [secretTouched, setSecretTouched] = useState(false);
  const [busy, setBusy] = useState(false);
  const [test, setTest] = useState<{ ok: boolean; error?: string } | null>(null);
  const [msg, setMsg] = useState<{ ok: boolean; error?: string; result?: string } | null>(null);
  const [msgBusy, setMsgBusy] = useState(false);

  // Load the saved (non-secret) values + which secrets are set, when editing.
  useEffect(() => {
    if (!tool) return;
    api
      .getTool(tool.id)
      .then((t) => {
        setValues(t.config ?? {});
        setSecretsSet(t.secretsSet ?? {});
        setName(t.name);
      })
      .catch((e) => onError(e.message));
  }, [tool?.id]);

  function set(key: string, v: string, secret: boolean) {
    setValues((prev) => ({ ...prev, [key]: v }));
    if (secret) setSecretTouched(true);
    setTest(null);
  }

  async function runTest() {
    setBusy(true);
    setTest(null);
    try {
      // For a saved tool whose secrets weren't re-typed, test the stored creds;
      // otherwise test the values currently in the form.
      const res =
        tool && !secretTouched
          ? await api.testTool(tool.id)
          : await api.testToolConfig(connector.type, values);
      setTest(res);
      if (tool) await onChanged();
    } catch (e: any) {
      onError(e.message);
    } finally {
      setBusy(false);
    }
  }

  async function sendTestMessage() {
    setMsgBusy(true);
    setMsg(null);
    try {
      // For a saved tool whose token wasn't re-typed, use the stored secret and
      // pass the current chat_id as an override; otherwise use the typed values.
      const res =
        tool && !secretTouched
          ? await api.testMessageTool(tool.id, { chat_id: values["chat_id"] ?? "" })
          : await api.testMessage(connector.type, values);
      setMsg(res);
    } catch (e: any) {
      onError(e.message);
    } finally {
      setMsgBusy(false);
    }
  }

  async function save() {
    setBusy(true);
    try {
      if (tool) await api.updateTool(tool.id, { name, config: values });
      else await api.createTool(connector.type, name, values);
      await onChanged();
      onClose();
    } catch (e: any) {
      onError(e.message);
    } finally {
      setBusy(false);
    }
  }

  async function toggleEnabled() {
    if (!tool) return;
    try {
      await api.updateTool(tool.id, { enabled: !tool.enabled });
      await onChanged();
    } catch (e: any) {
      onError(e.message);
    }
  }

  async function remove() {
    if (!tool || !window.confirm(t("tools.confirmDelete", { name: tool.name }))) return;
    try {
      await api.deleteTool(tool.id);
      await onChanged();
      onClose();
    } catch (e: any) {
      onError(e.message);
    }
  }

  return (
    <div className="bg-panel border border-border rounded-lg p-4 mt-4">
      <div className="flex items-center justify-between mb-3">
        <h2 className="font-semibold">{connector.label}</h2>
        <button onClick={onClose} className="text-muted hover:text-fg text-sm">
          {t("common.close")}
        </button>
      </div>

      {connector.type === "proxmox" && tool && <ProxmoxDashboardPanel toolId={tool.id} />}
      {connector.type === "s3" && tool && <S3DashboardPanel toolId={tool.id} />}
      {connector.type === "whatsapp" && tool && <WhatsAppPanel toolId={tool.id} onChanged={onChanged} />}
      {connector.type === "proton" && tool && <ProtonPanel toolId={tool.id} onChanged={onChanged} />}
      {(connector.type === "cloudflare" || connector.type === "infomaniak") && tool && <DnsProviderPanel />}
      {connector.type === "cloudflare" && tool && <CloudflarePanel onChanged={onChanged} />}

      <div className="flex flex-col gap-3">
        <Labeled label={t("tools.fieldName")}>
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            className="w-full bg-bg border border-border rounded px-3 py-2 text-sm focus:border-accent-dim outline-none"
          />
        </Labeled>

        {connector.fields.map((f) => (
          <FieldInput
            key={f.key}
            field={f}
            value={values[f.key] ?? ""}
            secretSet={!!secretsSet[f.key]}
            onChange={(v) => set(f.key, v, f.secret)}
          />
        ))}

        {connector.type === "telegram" && (
          <TelegramChatDetect
            botToken={values["bot_token"] ?? ""}
            onPick={(id) => set("chat_id", id, false)}
          />
        )}

        {connector.type === "telegram" && (
          <div className="flex flex-col gap-1">
            <button
              type="button"
              onClick={sendTestMessage}
              disabled={msgBusy || (values["chat_id"] ?? "") === ""}
              className="self-start border border-border text-fg rounded px-3 py-1.5 text-sm hover:border-accent-dim disabled:opacity-40 transition-colors"
            >
              {msgBusy ? t("tools.sending") : t("tools.sendTestMessage")}
            </button>
            {(values["chat_id"] ?? "") === "" && (
              <span className="text-[11px] text-muted/70">
                {t("tools.chatIdHint")}
              </span>
            )}
            {msg && (
              <span className={`text-sm ${msg.ok ? "text-ok" : "text-danger"}`}>
                {msg.ok
                  ? `✓ ${msg.result ?? t("tools.messageSent")}`
                  : `✗ ${msg.error ?? t("tools.messageFailed")}`}
              </span>
            )}
          </div>
        )}
      </div>

      {test && (
        <div className={`text-sm mt-3 ${test.ok ? "text-ok" : "text-danger"}`}>
          {test.ok
            ? `✓ ${t("tools.connectionOk")}`
            : `✗ ${test.error ?? t("tools.connectionFailed")}`}
        </div>
      )}

      <div className="flex items-center gap-2 mt-4 flex-wrap">
        <button
          onClick={runTest}
          disabled={busy}
          className="border border-border text-fg rounded px-3 py-1.5 text-sm hover:border-accent-dim disabled:opacity-40 transition-colors"
        >
          {t("tools.testConnection")}
        </button>
        <button
          onClick={save}
          disabled={busy}
          className="bg-accent text-bg font-medium rounded px-4 py-1.5 text-sm hover:bg-accent-dim disabled:opacity-40 transition-colors"
        >
          {t("common.save")}
        </button>
        {tool && (
          <>
            <button
              onClick={toggleEnabled}
              className="border border-border text-muted rounded px-3 py-1.5 text-sm hover:text-fg transition-colors"
            >
              {tool.enabled ? t("tools.disable") : t("tools.enable")}
            </button>
            <button
              onClick={remove}
              className="text-danger rounded px-2 py-1.5 text-sm hover:underline ml-auto"
            >
              {t("common.delete")}
            </button>
          </>
        )}
      </div>
    </div>
  );
}

function FieldInput({
  field,
  value,
  secretSet,
  onChange,
}: {
  field: ToolField;
  value: string;
  secretSet: boolean;
  onChange: (v: string) => void;
}) {
  const { t } = useTranslation();
  if (field.type === "bool") {
    return (
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={value === "true"}
          onChange={(e) => onChange(e.target.checked ? "true" : "false")}
        />
        <span>{field.label}</span>
      </label>
    );
  }
  const inputType = field.type === "password" ? "password" : field.type === "number" ? "number" : "text";
  return (
    <Labeled label={field.label + (field.required ? " *" : "")}>
      <input
        type={inputType}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={field.secret && secretSet ? t("tools.placeholderUnchanged") : field.placeholder}
        className="w-full bg-bg border border-border rounded px-3 py-2 text-sm focus:border-accent-dim outline-none"
      />
    </Labeled>
  );
}

function Labeled({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-xs text-muted">{label}</span>
      {children}
    </label>
  );
}

// TelegramChatDetect drives the assisted chat_id capture flow: the user types the
// bot token, clicks detect, then sends a message to the bot. We first drain the
// backlog (offset 0 → cursor) so only a message sent from now on is captured, then
// short-poll until a chat appears and let the user confirm it. The backend
// long-polls (~10s) so the loop is self-paced; cancellation stops it cleanly.
function TelegramChatDetect({
  botToken,
  onPick,
}: {
  botToken: string;
  onPick: (chatId: string) => void;
}) {
  const { t } = useTranslation();
  const [phase, setPhase] = useState<"idle" | "waiting" | "found">("idle");
  const [found, setFound] = useState<DetectedChat | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const runningRef = useRef(false);
  const offsetRef = useRef(0);

  // Stop any in-flight poll loop if the form unmounts mid-detection.
  useEffect(() => () => void (runningRef.current = false), []);

  function fail(msg: string) {
    runningRef.current = false;
    setErr(msg);
    setPhase("idle");
  }

  async function poll() {
    while (runningRef.current) {
      let res: DetectChatResult;
      try {
        res = await api.detectChat("telegram", { bot_token: botToken }, offsetRef.current);
      } catch (e: any) {
        fail(e.message);
        return;
      }
      if (!runningRef.current) return;
      if (!res.ok) {
        fail(res.error ?? t("tools.detectionFailed"));
        return;
      }
      offsetRef.current = res.nextOffset;
      if (res.chats.length > 0) {
        runningRef.current = false;
        setFound(res.chats[0]);
        setPhase("found");
        return;
      }
    }
  }

  async function start() {
    setErr(null);
    setFound(null);
    // Drain the backlog first so an old message can't be mistaken for the answer.
    try {
      const first = await api.detectChat("telegram", { bot_token: botToken }, 0);
      if (!first.ok) {
        fail(first.error ?? t("tools.detectionFailed"));
        return;
      }
      offsetRef.current = first.nextOffset;
    } catch (e: any) {
      fail(e.message);
      return;
    }
    runningRef.current = true;
    setPhase("waiting");
    void poll();
  }

  function accept() {
    if (found) onPick(String(found.chatId));
    setPhase("idle");
    setFound(null);
  }

  function reject() {
    // Resume from the advanced offset, so the rejected chat is not re-captured.
    setFound(null);
    runningRef.current = true;
    setPhase("waiting");
    void poll();
  }

  function cancel() {
    runningRef.current = false;
    setPhase("idle");
  }

  return (
    <div className="border border-border rounded-lg p-3 bg-bg/40 flex flex-col gap-2">
      <div className="text-xs text-muted">{t("tools.chatIdAutoDetect")}</div>

      {phase === "idle" && (
        <>
          <button
            type="button"
            onClick={start}
            disabled={!botToken}
            className="self-start border border-border text-fg rounded px-3 py-1.5 text-sm hover:border-accent-dim disabled:opacity-40 transition-colors"
          >
            {t("tools.startDetection")}
          </button>
          {!botToken && (
            <span className="text-[11px] text-muted/70">
              {t("tools.enterBotTokenFirst")}
            </span>
          )}
        </>
      )}

      {phase === "waiting" && (
        <div className="flex items-center justify-between gap-2">
          <span className="text-sm text-fg flex items-center gap-2">
            <span className="inline-block w-3 h-3 rounded-full border-2 border-accent border-t-transparent animate-spin" />
            {t("tools.waitingForMessage")}
          </span>
          <button
            type="button"
            onClick={cancel}
            className="text-muted hover:text-fg text-sm shrink-0"
          >
            {t("common.cancel")}
          </button>
        </div>
      )}

      {phase === "found" && found && (
        <div className="flex flex-col gap-2">
          <div className="text-sm">
            {t("tools.chatIdDetected")} <span className="mono text-accent">{found.chatId}</span>
            {found.title && <span className="text-muted"> ({found.title})</span>}. {t("tools.isThisRight")}
          </div>
          {found.text && (
            <div className="text-[11px] text-muted/70 truncate">« {found.text} »</div>
          )}
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={accept}
              className="bg-accent text-bg font-medium rounded px-3 py-1 text-sm hover:bg-accent-dim transition-colors"
            >
              {t("common.yes")}
            </button>
            <button
              type="button"
              onClick={reject}
              className="border border-border text-fg rounded px-3 py-1 text-sm hover:border-accent-dim transition-colors"
            >
              {t("common.no")}
            </button>
          </div>
        </div>
      )}

      {err && <div className="text-danger text-sm">{err}</div>}
    </div>
  );
}

// Bytes → human size (binary units), e.g. 12.0 Gi. Gi/Ti get one decimal.
function fmtBytes(n: number): string {
  if (!n) return "0";
  const units = ["o", "Ki", "Mi", "Gi", "Ti", "Pi"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i >= 3 ? 1 : 0)} ${units[i]}`;
}

function fmtPct(frac: number): string {
  return `${Math.round((frac || 0) * 100)} %`;
}

function ratio(x: { used: number; total: number }): number {
  return x.total > 0 ? x.used / x.total : 0;
}

// ProxmoxTileSummary shows a one-line resource glance on a connected Proxmox tile.
// Best-effort: stays empty (and silent) if the snapshot can't be fetched.
function ProxmoxTileSummary({ id }: { id: string }) {
  const [d, setD] = useState<ProxmoxDashboard | null>(null);
  useEffect(() => {
    let alive = true;
    api
      .toolDashboard(id)
      .then((r) => alive && setD(r))
      .catch(() => {});
    return () => void (alive = false);
  }, [id]);
  if (!d) return null;
  return (
    <div className="text-[11px] text-muted/80 mt-2 flex flex-wrap gap-x-2 gap-y-0.5">
      <span>CPU {fmtPct(d.cpu.usage)}</span>
      <span>
        · RAM {fmtBytes(d.memory.used)}/{fmtBytes(d.memory.total)}
      </span>
      <span>
        · {d.vms.total} VM / {d.containers.total} LXC
      </span>
    </div>
  );
}

// S3TileSummary shows the bucket count on a connected S3 tile. Best-effort: silent
// if the snapshot can't be fetched.
function S3TileSummary({ id }: { id: string }) {
  const { t } = useTranslation();
  const [d, setD] = useState<S3Dashboard | null>(null);
  useEffect(() => {
    let alive = true;
    api
      .s3Dashboard(id)
      .then((r) => alive && setD(r))
      .catch(() => {});
    return () => void (alive = false);
  }, [id]);
  if (!d) return null;
  return (
    <div className="text-[11px] text-muted/80 mt-2">
      {d.bucket
        ? `${d.bucket} · ${t("tools.objectCount", { count: d.count })}${d.count >= 20 ? "+" : ""}`
        : t("tools.bucketCount", { count: d.count })}
    </div>
  );
}

// S3DashboardPanel lists the buckets of an open S3 tool at the top of the connector
// form, mirroring the Proxmox supervision panel.
function S3DashboardPanel({ toolId }: { toolId: string }) {
  const { t } = useTranslation();
  const [data, setData] = useState<S3Dashboard | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  async function load() {
    setLoading(true);
    setErr(null);
    try {
      setData(await api.s3Dashboard(toolId));
    } catch (e: any) {
      setErr(e.message);
    } finally {
      setLoading(false);
    }
  }
  useEffect(() => {
    load();
  }, [toolId]);

  return (
    <div className="border border-border rounded-lg p-3 mb-3 bg-bg/40">
      <div className="flex items-center justify-between mb-2">
        <span className="text-xs font-semibold uppercase tracking-wider text-muted">
          {data?.bucket ? `${t("tools.objects")} · ${data.bucket}` : t("tools.buckets")}
        </span>
        <button
          type="button"
          onClick={load}
          disabled={loading}
          className="text-muted hover:text-fg text-xs disabled:opacity-40"
        >
          {loading ? "…" : t("common.refresh")}
        </button>
      </div>

      {err && <div className="text-danger text-sm">{err}</div>}
      {!err && !data && loading && <div className="text-muted text-sm">{t("common.loading")}</div>}

      {/* Object preview when a default bucket is configured. */}
      {data?.bucket && (data.objects?.length ?? 0) === 0 && (
        <div className="text-muted text-sm">{t("tools.emptyBucket")}</div>
      )}
      {data?.bucket && (data.objects?.length ?? 0) > 0 && (
        <ul className="flex flex-col gap-1">
          {data.objects!.map((o) => (
            <li
              key={o.key}
              className="flex items-center justify-between bg-bg border border-border rounded px-2 py-1 text-sm"
            >
              <span className="truncate">{o.key}</span>
              <span className="text-[11px] text-muted/70 shrink-0 ml-2">{fmtBytes(o.size)}</span>
            </li>
          ))}
          {data.count >= 20 && <li className="text-[11px] text-muted/70 px-2">{t("tools.firstN")}</li>}
        </ul>
      )}

      {/* Bucket listing otherwise. */}
      {data && !data.bucket && (data.buckets?.length ?? 0) === 0 && (
        <div className="text-muted text-sm">{t("tools.noBucket")}</div>
      )}
      {data && !data.bucket && (data.buckets?.length ?? 0) > 0 && (
        <ul className="flex flex-col gap-1">
          {data.buckets!.map((b) => (
            <li
              key={b.name}
              className="flex items-center justify-between bg-bg border border-border rounded px-2 py-1 text-sm"
            >
              <span className="truncate">{b.name}</span>
              <span className="text-[11px] text-muted/70 shrink-0 ml-2">{b.creationDate}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

// ProxmoxDashboardPanel is the detailed supervision view shown at the top of the
// connector form when a Proxmox tool is open: global CPU/RAM/storage and the
// VM/LXC inventory with running/stopped breakdown.
function ProxmoxDashboardPanel({ toolId }: { toolId: string }) {
  const { t } = useTranslation();
  const [data, setData] = useState<ProxmoxDashboard | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  async function load() {
    setLoading(true);
    setErr(null);
    try {
      setData(await api.toolDashboard(toolId));
    } catch (e: any) {
      setErr(e.message);
    } finally {
      setLoading(false);
    }
  }
  useEffect(() => {
    load();
  }, [toolId]);

  return (
    <div className="border border-border rounded-lg p-3 mb-3 bg-bg/40">
      <div className="flex items-center justify-between mb-2">
        <span className="text-xs font-semibold uppercase tracking-wider text-muted">
          {t("tools.supervision")}
        </span>
        <button
          type="button"
          onClick={load}
          disabled={loading}
          className="text-muted hover:text-fg text-xs disabled:opacity-40"
        >
          {loading ? "…" : t("common.refresh")}
        </button>
      </div>

      {err && <div className="text-danger text-sm">{err}</div>}
      {!err && !data && loading && <div className="text-muted text-sm">{t("common.loading")}</div>}

      {data?.warning && (
        <div className="text-danger/90 text-[11px] mb-2 border border-danger/30 rounded px-2 py-1">
          ⚠ {data.warning}
        </div>
      )}

      {data && (
        <div className="flex flex-col gap-3">
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-2">
            <Stat
              label={t("tools.cpu")}
              value={fmtPct(data.cpu.usage)}
              sub={t("tools.cores", { count: data.cpu.cores })}
            />
            <Stat
              label={t("tools.memory")}
              value={`${fmtBytes(data.memory.used)} / ${fmtBytes(data.memory.total)}`}
              sub={fmtPct(ratio(data.memory))}
            />
            <Stat
              label={t("tools.storage")}
              value={`${fmtBytes(data.storage.used)} / ${fmtBytes(data.storage.total)}`}
              sub={fmtPct(ratio(data.storage))}
            />
          </div>
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
            <Inventory label={t("tools.virtualMachines")} inv={data.vms} />
            <Inventory label={t("tools.lxcContainers")} inv={data.containers} />
          </div>
        </div>
      )}
    </div>
  );
}

function Stat({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div className="bg-bg border border-border rounded-lg px-3 py-2">
      <div className="text-[11px] text-muted">{label}</div>
      <div className="text-sm font-medium mt-0.5">{value}</div>
      {sub && <div className="text-[11px] text-muted/70">{sub}</div>}
    </div>
  );
}

function Inventory({
  label,
  inv,
}: {
  label: string;
  inv: { running: number; stopped: number; total: number };
}) {
  const { t } = useTranslation();
  return (
    <div className="bg-bg border border-border rounded-lg px-3 py-2">
      <div className="flex items-center justify-between">
        <span className="text-[11px] text-muted">{label}</span>
        <span className="text-sm font-medium">{inv.total}</span>
      </div>
      <div className="flex items-center gap-3 mt-1 text-xs">
        <span className="flex items-center gap-1">
          <span className="inline-block w-2 h-2 rounded-full bg-ok" />
          {t("tools.running", { count: inv.running })}
        </span>
        <span className="flex items-center gap-1 text-muted">
          <span className="inline-block w-2 h-2 rounded-full bg-muted/50" />
          {t("tools.stopped", { count: inv.stopped })}
        </span>
      </div>
    </div>
  );
}

// WhatsAppPanel shows the QR pairing flow for a saved WhatsApp tool.
// The backend keeps the QR code in memory; this component polls every 3s during
// pairing so the displayed code stays fresh (WA refreshes the code every ~20s).
function WhatsAppPanel({ toolId }: { toolId: string; onChanged?: () => Promise<void> | void }) {
  const { t } = useTranslation();
  const [info, setInfo] = useState<WhatsAppQRInfo | null>(null);
  const [busy, setBusy] = useState(false);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  function stopPoll() {
    if (pollRef.current) {
      clearInterval(pollRef.current);
      pollRef.current = null;
    }
  }

  async function fetchStatus() {
    try {
      const res = await api.whatsappQR(toolId);
      setInfo(res);
      if (res.status === "connected" || res.status === "error") {
        stopPoll();
      }
    } catch {
      // ignore transient errors during polling
    }
  }

  async function startConnect() {
    setBusy(true);
    try {
      await api.whatsappConnect(toolId);
      await fetchStatus();
      stopPoll();
      pollRef.current = setInterval(fetchStatus, 3000);
    } catch (e: any) {
      setInfo({ status: "error", error: e.message });
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    fetchStatus();
    return stopPoll;
  }, [toolId]);

  if (!info) return null;

  return (
    <div className="border border-border rounded-lg p-3 bg-bg/40 flex flex-col gap-3 mb-3">
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium text-muted uppercase tracking-wide">
          {t("tools.waConnection")}
        </span>
        <span
          className={`text-xs px-2 py-0.5 rounded-full ${
            info.status === "connected"
              ? "bg-ok/15 text-ok"
              : info.status === "pairing"
                ? "bg-accent/15 text-accent"
                : info.status === "error"
                  ? "bg-danger/15 text-danger"
                  : "bg-muted/15 text-muted"
          }`}
        >
          {t(`tools.waStatus_${info.status}`)}
        </span>
      </div>

      {info.status === "connected" && (
        <p className="text-sm text-ok">{t("tools.waConnected")}</p>
      )}

      {info.status === "pairing" && info.qr && (
        <div className="flex flex-col items-center gap-2 animate-fadeIn">
          <p className="text-xs text-muted text-center">{t("tools.waScanPrompt")}</p>
          <div className="bg-white p-3 rounded-lg inline-block shadow-sm">
            <QRCodeSVG value={info.qr} size={200} />
          </div>
          <p className="text-[11px] text-muted/70 text-center">{t("tools.waQrExpiry")}</p>
        </div>
      )}

      {(busy || (info.status === "pairing" && !info.qr)) && (
        <div className="flex flex-col items-center gap-3 py-4">
          <svg className="animate-spin w-8 h-8 text-accent" viewBox="0 0 24 24" fill="none">
            <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="3"/>
            <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"/>
          </svg>
          <p className="text-xs text-muted">{t("tools.waGeneratingQr")}</p>
        </div>
      )}

      {!busy && (info.status === "disconnected" || info.status === "error") && (
        <div className="flex flex-col gap-2">
          {info.error && <p className="text-xs text-danger">{info.error}</p>}
          <button
            onClick={startConnect}
            className="self-start bg-accent text-bg font-medium rounded px-3 py-1.5 text-sm hover:bg-accent-dim transition-colors"
          >
            {t("tools.waConnect")}
          </button>
        </div>
      )}
    </div>
  );
}

// ProtonPanel connects the Proton account once (password, then the 2FA code when
// the account has one). Only the resulting session is stored, encrypted; the
// password and code are never kept. No automatic retry: a refusal is shown as is.
function ProtonPanel({ toolId, onChanged }: { toolId: string; onChanged: () => Promise<void> | void }) {
  const { t } = useTranslation();
  const [st, setSt] = useState<ProtonStatus | null>(null);
  const [open, setOpen] = useState(false);
  const [step, setStep] = useState<"password" | "needs_captcha" | "needs_2fa" | "needs_mailbox_password">("password");
  const [captchaUrl, setCaptchaUrl] = useState("");
  const [copied, setCopied] = useState(false);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [mailbox, setMailbox] = useState("");
  const [totp, setTotp] = useState("");
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);

  async function refresh() {
    try {
      const s = await api.protonStatus(toolId);
      setSt(s);
      setUsername((u) => u || s.username);
    } catch {
      // status is informative only
    }
  }
  useEffect(() => {
    refresh();
  }, [toolId]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setMsg(null);
    try {
      const res = await api.protonConnect(toolId, {
        username,
        password: step === "password" ? password : undefined,
        mailboxPassword: mailbox || undefined,
        totp: totp || undefined,
        captchaDone: step === "needs_captcha" ? true : undefined,
      });
      if (!res.ok) {
        setMsg({ ok: false, text: res.error || t("tools.protonFailed") });
        setStep("password");
        setCaptchaUrl("");
        setTotp("");
      } else if (res.status === "needs_captcha") {
        // The server keeps the password for at most 10 minutes: drop our copy.
        setPassword("");
        setCaptchaUrl(res.url || "");
        setStep("needs_captcha");
        setMsg(null);
      } else if (res.status === "connected") {
        setMsg({ ok: true, text: t("tools.protonConnected") });
        setOpen(false);
        setPassword("");
        setMailbox("");
        setTotp("");
        setStep("password");
        setCaptchaUrl("");
        await onChanged();
      } else {
        setStep(res.status!);
        setCaptchaUrl("");
        setTotp("");
        if (res.message) setMsg({ ok: false, text: res.message });
      }
    } catch (e: any) {
      setMsg({ ok: false, text: e.message });
    } finally {
      setBusy(false);
      refresh();
    }
  }

  async function copyLink() {
    try {
      await navigator.clipboard.writeText(captchaUrl);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // clipboard unavailable (insecure context): the link stays selectable
    }
  }

  // Abandon the attempt: nothing is sent to Proton, the server wipes the
  // password it held for the CAPTCHA wait.
  async function cancel() {
    if (step !== "password") {
      setBusy(true);
      try {
        await api.protonCancel(toolId);
      } catch {
        // the wait expires by itself after 10 minutes
      } finally {
        setBusy(false);
      }
    }
    setOpen(false);
    setStep("password");
    setCaptchaUrl("");
    setPassword("");
    setTotp("");
    setMailbox("");
    setMsg(null);
  }

  if (!st) return null;
  const paused = st.pausedUntil ? new Date(st.pausedUntil) : null;
  const input = "w-full bg-bg border border-border rounded px-3 py-2 text-sm focus:border-accent-dim outline-none";

  return (
    <div className="border border-border rounded-lg p-3 bg-bg/40 flex flex-col gap-3 mb-3">
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium text-muted uppercase tracking-wide">{t("tools.protonSession")}</span>
        <span
          className={`text-xs px-2 py-0.5 rounded-full ${
            paused ? "bg-danger/15 text-danger" : st.connected ? "bg-ok/15 text-ok" : "bg-muted/15 text-muted"
          }`}
        >
          {paused ? t("tools.protonPaused") : st.connected ? t("tools.statusConnected") : t("tools.statusDisconnected")}
        </span>
      </div>
      <p className="text-xs text-muted">{t("tools.protonHint")}</p>
      {paused && (
        <p className="text-xs text-danger">
          {t("tools.protonPausedUntil", { when: paused.toLocaleString() })}
          {st.lastError ? ` — ${st.lastError}` : ""}
        </p>
      )}
      {!paused && !st.connected && st.lastError && <p className="text-xs text-danger">{st.lastError}</p>}
      {msg && <p className={`text-xs ${msg.ok ? "text-ok" : "text-danger"}`}>{msg.text}</p>}

      {!open && (
        <button
          onClick={() => {
            setOpen(true);
            setMsg(null);
          }}
          className="self-start bg-accent text-bg font-medium rounded px-3 py-1.5 text-sm hover:bg-accent-dim transition-colors"
        >
          {st.connected ? t("tools.protonReconnect") : t("tools.protonConnect")}
        </button>
      )}

      {open && (
        <form id="proton-form" onSubmit={submit} className="flex flex-col gap-2 animate-fadeIn" autoComplete="off">
          {step === "password" && (
            <>
              <input
                name="proton-username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                placeholder="user@proton.me"
                className={input}
                aria-label={t("tools.protonUsername")}
              />
              <input
                name="proton-password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder={t("tools.protonPassword")}
                className={input}
                aria-label={t("tools.protonPassword")}
                autoComplete="current-password"
              />
            </>
          )}
          {step === "needs_captcha" && (
            <div id="proton-captcha" className="flex flex-col gap-2 text-xs border border-accent-dim rounded-lg p-3 bg-bg">
              <p className="font-semibold text-fg">{t("tools.bridgeCaptchaTitle")}</p>
              <p className="text-muted">{t("tools.protonCaptchaSteps")}</p>
              {captchaUrl ? (
                <>
                  <a
                    id="proton-captcha-link"
                    href={captchaUrl}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="text-accent hover:underline break-all font-mono"
                  >
                    {captchaUrl}
                  </a>
                  <div>
                    <button
                      type="button"
                      id="proton-captcha-copy"
                      onClick={copyLink}
                      className="border border-border rounded px-2 py-1 hover:border-accent-dim transition-colors"
                    >
                      {copied ? t("tools.bridgeCaptchaCopied") : t("tools.bridgeCaptchaCopy")}
                    </button>
                  </div>
                </>
              ) : (
                <p className="text-danger">{t("tools.bridgeCaptchaNoLink")}</p>
              )}
              <p className="text-muted">{t("tools.protonCaptchaNote")}</p>
            </div>
          )}
          {step === "needs_mailbox_password" && (
            <input
              name="proton-mailbox"
              type="password"
              value={mailbox}
              onChange={(e) => setMailbox(e.target.value)}
              placeholder={t("tools.protonMailboxPassword")}
              className={input}
              aria-label={t("tools.protonMailboxPassword")}
            />
          )}
          {step === "needs_2fa" && (
            <input
              id="totp"
              name="totp"
              type="text"
              inputMode="numeric"
              pattern="[0-9 ]*"
              value={totp}
              onChange={(e) => setTotp(e.target.value)}
              placeholder={t("tools.protonTotp")}
              className={input}
              aria-label={t("tools.protonTotp")}
              autoComplete="one-time-code"
              autoFocus
            />
          )}
          <div className="flex gap-2">
            <button
              type="submit"
              disabled={busy || (step === "needs_captcha" && !captchaUrl)}
              className="bg-accent text-bg font-medium rounded px-3 py-1.5 text-sm hover:bg-accent-dim transition-colors disabled:opacity-60 inline-flex items-center gap-2"
            >
              {busy && (
                <svg className="animate-spin w-4 h-4" viewBox="0 0 24 24" fill="none">
                  <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="3" />
                  <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
                </svg>
              )}
              {busy ? t("tools.protonConnecting") : step === "password" ? t("tools.protonLogin") : step === "needs_captcha" ? t("tools.bridgeCaptchaDone") : t("tools.protonValidate")}
            </button>
            <button
              type="button"
              onClick={cancel}
              disabled={busy}
              className="text-muted hover:text-fg text-sm px-2"
            >
              {t("common.cancel")}
            </button>
          </div>
        </form>
      )}
    </div>
  );
}

// ProtonBridgePanel adds the Proton mail account to the local Bridge from a Hub
// form and keeps the "Proton Bridge" IMAP instance in sync. The password, 2FA
// code and mailbox password only travel in the request; the browser never gets
// them back. One attempt per submit: a refusal is shown as is, never retried.
function ProtonBridgePanel({ onChanged }: { onChanged: () => Promise<void> | void }) {
  const { t } = useTranslation();
  const [st, setSt] = useState<BridgeStatus | null>(null);
  const [open, setOpen] = useState(false);
  const [step, setStep] = useState<"password" | "needs_captcha" | "needs_2fa" | "needs_mailbox_password">("password");
  const [captchaUrl, setCaptchaUrl] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [mailbox, setMailbox] = useState("");
  const [totp, setTotp] = useState("");
  const [busy, setBusy] = useState(false);
  const [copied, setCopied] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string; kind?: string } | null>(null);

  async function refresh() {
    try {
      const s = await api.bridgeStatus();
      setSt(s);
      setEmail((e) => e || s.account || "");
    } catch {
      // status is informative only
    }
  }
  useEffect(() => {
    refresh();
  }, []);
  // Follow the first synchronisation without asking the user to reload.
  const state = st?.state;
  useEffect(() => {
    if (state !== "syncing" && state !== "busy") return;
    const id = setInterval(refresh, 5000);
    return () => clearInterval(id);
  }, [state]);

  // A login waiting for the user (page reloaded meanwhile): offer to resume it.
  const pending = st?.pending;
  useEffect(() => {
    if (!pending || open) return;
    setOpen(true);
    setStep(pending);
    setCaptchaUrl(st?.captchaUrl || "");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pending]);

  // The link may only be known to the server (page reloaded, status refresh).
  useEffect(() => {
    if (step === "needs_captcha" && !captchaUrl && st?.captchaUrl) setCaptchaUrl(st.captchaUrl);
  }, [step, captchaUrl, st?.captchaUrl]);

  async function copyLink() {
    try {
      await navigator.clipboard.writeText(captchaUrl);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // clipboard unavailable (insecure context): the link stays selectable
    }
  }

  // Abandon a login waiting for the user: the server kills the Bridge session
  // without typing anything into it.
  async function cancel() {
    if (step !== "password") {
      setBusy(true);
      try {
        await api.bridgeCancel();
      } catch (e: any) {
        setMsg({ ok: false, text: e.message });
      } finally {
        setBusy(false);
        refresh();
      }
    }
    reset();
  }

  function reset() {
    setOpen(false);
    setCaptchaUrl("");
    setStep("password");
    setPassword("");
    setTotp("");
    setMailbox("");
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setMsg(null);
    try {
      const res = await api.bridgeConnect({
        email,
        password: step === "password" ? password : undefined,
        mailboxPassword: mailbox || undefined,
        totp: totp || undefined,
        captchaDone: step === "needs_captcha" ? true : undefined,
      });
      if (!res.ok) {
        setMsg({ ok: false, text: res.error || t("tools.bridgeFailed"), kind: res.kind });
        setStep("password");
        setPassword("");
        setTotp("");
        setMailbox("");
      } else if (res.status === "added") {
        setMsg({ ok: true, text: res.message || t("tools.bridgeAdded") });
        reset();
        await onChanged();
      } else {
        setStep(res.status!);
        setCaptchaUrl(res.status === "needs_captcha" ? res.url || "" : "");
        setTotp("");
        // The CAPTCHA step explains itself in its own block.
        if (res.message && res.status !== "needs_captcha") setMsg({ ok: true, text: res.message });
      }
    } catch (e: any) {
      setMsg({ ok: false, text: e.message });
    } finally {
      setBusy(false);
      refresh();
    }
  }

  async function disconnect() {
    if (!window.confirm(t("tools.bridgeConfirmDisconnect"))) return;
    setBusy(true);
    setMsg(null);
    try {
      const res = await api.bridgeDisconnect();
      if (!res.ok) setMsg({ ok: false, text: res.error || t("tools.bridgeFailed") });
      await onChanged();
    } catch (e: any) {
      setMsg({ ok: false, text: e.message });
    } finally {
      setBusy(false);
      refresh();
    }
  }

  if (!st) return null;
  const input = "w-full bg-bg border border-border rounded px-3 py-2 text-sm focus:border-accent-dim outline-none";
  const tone =
    st.state === "ready"
      ? "bg-ok/15 text-ok"
      : st.state === "error" || st.state === "stopped" || st.state === "not_installed"
        ? "bg-danger/15 text-danger"
        : "bg-muted/15 text-muted";
  const canConnect = st.installed && st.serviceActive;
  const hasAccount = !!st.toolId;

  return (
    <div id="proton-bridge-panel" className="border border-border rounded-lg p-3 bg-panel flex flex-col gap-3 mb-5">
      <div className="flex items-center justify-between">
        <h2 className="text-xs font-semibold uppercase tracking-wider text-muted">{t("tools.bridgeTitle")}</h2>
        <span className={`text-xs px-2 py-0.5 rounded-full inline-flex items-center gap-1.5 ${tone}`}>
          {(st.state === "syncing" || st.state === "busy") && (
            <span className="inline-block w-2 h-2 rounded-full bg-current animate-pulse" />
          )}
          {t(`tools.bridgeState_${st.state}`)}
        </span>
      </div>
      <p className="text-xs text-muted">{t("tools.bridgeHint")}</p>
      {!st.installed && <p className="text-xs text-danger">{t("tools.bridgeNotInstalled")}</p>}
      {st.installed && !st.serviceActive && st.state !== "awaiting" && st.state !== "busy" && (
        <p className="text-xs text-danger">{t("tools.bridgeStopped")}</p>
      )}
      {hasAccount && <p className="text-xs">{t("tools.bridgeAccount", { account: st.account })}</p>}
      {st.state === "syncing" && <p className="text-xs text-muted animate-pulse">{t("tools.bridgeSyncing")}</p>}
      {st.state === "ready" && (
        <p className={`text-xs ${st.assistantAccess ? "text-ok" : "text-muted"}`}>
          {st.assistantAccess ? t("tools.bridgeAssistant") : t("tools.bridgeNoAssistant")}
        </p>
      )}
      {hasAccount && st.lastError && st.state !== "ready" && <p className="text-xs text-danger">{st.lastError}</p>}
      {msg && (
        <p className={`text-xs ${msg.ok ? "text-ok" : "text-danger"}`}>
          {msg.kind ? <strong>{t(`tools.bridgeKind_${msg.kind}`)} — </strong> : null}
          {msg.text}
        </p>
      )}

      {!open && (
        <div className="flex gap-2 items-center">
          <button
            disabled={!canConnect || busy}
            onClick={() => {
              setOpen(true);
              setMsg(null);
            }}
            className="bg-accent text-bg font-medium rounded px-3 py-1.5 text-sm hover:bg-accent-dim transition-colors disabled:opacity-60"
          >
            {hasAccount ? t("tools.bridgeReconnect") : t("tools.bridgeConnect")}
          </button>
          {hasAccount && (
            <button
              disabled={busy}
              onClick={disconnect}
              className="text-danger hover:underline text-sm px-2 disabled:opacity-60"
            >
              {t("tools.bridgeDisconnect")}
            </button>
          )}
        </div>
      )}

      {open && (
        <form id="bridge-form" onSubmit={submit} className="flex flex-col gap-2 animate-fadeIn" autoComplete="off">
          {step === "password" && (
            <>
              <input
                name="bridge-email"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                placeholder="user@proton.me"
                className={input}
                aria-label={t("tools.bridgeEmail")}
              />
              <input
                name="bridge-password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder={t("tools.protonPassword")}
                className={input}
                aria-label={t("tools.protonPassword")}
                autoComplete="current-password"
              />
            </>
          )}
          {step === "needs_captcha" && (
            <div id="bridge-captcha" className="flex flex-col gap-2 text-xs border border-accent-dim rounded-lg p-3 bg-bg">
              <p className="font-semibold text-fg">{t("tools.bridgeCaptchaTitle")}</p>
              <p className="text-muted">{t("tools.bridgeCaptchaSteps")}</p>
              {captchaUrl ? (
                <>
                  <a
                    id="bridge-captcha-link"
                    href={captchaUrl}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="text-accent hover:underline break-all font-mono"
                  >
                    {captchaUrl}
                  </a>
                  <div>
                    <button
                      type="button"
                      id="bridge-captcha-copy"
                      onClick={copyLink}
                      className="border border-border rounded px-2 py-1 hover:border-accent-dim transition-colors"
                    >
                      {copied ? t("tools.bridgeCaptchaCopied") : t("tools.bridgeCaptchaCopy")}
                    </button>
                  </div>
                </>
              ) : (
                <p className="text-danger">{t("tools.bridgeCaptchaNoLink")}</p>
              )}
            </div>
          )}
          {step === "needs_mailbox_password" && (
            <input
              name="bridge-mailbox"
              type="password"
              value={mailbox}
              onChange={(e) => setMailbox(e.target.value)}
              placeholder={t("tools.protonMailboxPassword")}
              className={input}
              aria-label={t("tools.protonMailboxPassword")}
              autoFocus
            />
          )}
          {step === "needs_2fa" && (
            <input
              id="bridge-totp"
              name="bridge-totp"
              type="text"
              inputMode="numeric"
              pattern="[0-9 ]*"
              value={totp}
              onChange={(e) => setTotp(e.target.value)}
              placeholder={t("tools.protonTotp")}
              className={input}
              aria-label={t("tools.protonTotp")}
              autoComplete="one-time-code"
              autoFocus
            />
          )}
          <p className="text-[11px] text-muted/70">{t("tools.bridgeRestartNote")}</p>
          <div className="flex gap-2">
            <button
              type="submit"
              disabled={busy || (step === "needs_captcha" && !captchaUrl)}
              className="bg-accent text-bg font-medium rounded px-3 py-1.5 text-sm hover:bg-accent-dim transition-colors disabled:opacity-60 inline-flex items-center gap-2"
            >
              {busy && (
                <svg className="animate-spin w-4 h-4" viewBox="0 0 24 24" fill="none">
                  <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="3" />
                  <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
                </svg>
              )}
              {busy ? t("tools.bridgeConnecting") : step === "password" ? t("tools.protonLogin") : step === "needs_captcha" ? t("tools.bridgeCaptchaDone") : t("tools.protonValidate")}
            </button>
            <button type="button" id="bridge-cancel" onClick={cancel} className="text-muted hover:text-fg text-sm px-2">
              {t("common.cancel")}
            </button>
          </div>
        </form>
      )}
    </div>
  );
}
