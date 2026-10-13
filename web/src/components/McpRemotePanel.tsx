import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, type McpAudit, type McpRemote } from "../lib/api";

const classColor: Record<string, string> = {
  read: "text-ok border-ok/40",
  safe: "text-ok border-ok/40",
  sensitive: "text-danger border-danger/40",
  blocked: "text-muted border-border",
};

// McpRemotePanel manages the claude.ai custom connector: master switch, public
// URL, per-tool whitelist, live connections (one-click revoke) and audit log.
// Hidden when the endpoint is unavailable (auth disabled).
export function McpRemotePanel() {
  const { t } = useTranslation();
  const [st, setSt] = useState<McpRemote | null>(null);
  const [url, setUrl] = useState("");
  const [audit, setAudit] = useState<McpAudit[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = () =>
    api
      .mcpRemote()
      .then((s) => {
        setSt(s);
        setUrl((u) => u || s.publicUrl);
      })
      .catch(() => setSt(null));
  const loadAudit = () => api.mcpRemoteAudit().then(setAudit).catch(() => {});
  useEffect(() => {
    load();
    loadAudit();
  }, []);

  async function run(action: () => Promise<McpRemote>) {
    setBusy(true);
    setError(null);
    try {
      setSt(await action());
      loadAudit();
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  }

  if (!st) return null;
  const date = (s: string) => new Date(s).toLocaleString();

  return (
    <section className="bg-panel border border-border rounded-lg p-5 mb-6">
      <h2 className="text-sm font-medium mb-1">{t("configuration.mcpTitle")}</h2>
      <p className="text-xs text-muted mb-3">{t("configuration.mcpIntro")}</p>
      <p className="text-xs text-danger mb-4">{t("configuration.mcpWarn")}</p>
      {error && <div className="text-danger text-sm mb-3">{error}</div>}

      <label className="block text-xs text-muted mb-1">{t("configuration.mcpUrlLabel")}</label>
      <div className="flex gap-2 mb-1">
        <input
          value={url}
          onChange={(e) => setUrl(e.target.value)}
          placeholder="https://mcp.example.com"
          className="flex-1 bg-bg border border-border rounded px-2 py-1 text-sm mono"
        />
        <button
          type="button"
          disabled={busy}
          onClick={() => run(() => api.mcpRemoteSettings({ publicUrl: url }))}
          className="text-xs border border-border rounded px-3 py-1 hover:border-accent-dim disabled:opacity-40"
        >
          {t("configuration.mcpSave")}
        </button>
      </div>
      <p className="text-2xs text-muted mb-3">{t("configuration.mcpUrlHint")}</p>

      <label className="flex items-center gap-2 text-sm mb-3">
        <input
          type="checkbox"
          checked={st.enabled}
          disabled={busy}
          onChange={(e) => run(() => api.mcpRemoteSettings({ enabled: e.target.checked }))}
        />
        {t("configuration.mcpEnable")}
      </label>
      {st.enabled && st.mcpUrl && (
        <div className="mb-4 text-xs">
          <div className="text-muted">{t("configuration.mcpConnectorUrl")}</div>
          <code className="mono select-all break-all">{st.mcpUrl}</code>
        </div>
      )}

      <h3 className="text-xs font-semibold text-muted uppercase tracking-wide mt-4 mb-2">{t("configuration.mcpTools")}</h3>
      <ul className="space-y-1 mb-4">
        {st.tools.map((tool) => (
          <li key={tool.name} className="flex items-center gap-2 text-xs">
            <input
              type="checkbox"
              checked={tool.enabled}
              disabled={busy || tool.class === "blocked"}
              onChange={(e) => run(() => api.mcpRemoteTool(tool.name, e.target.checked))}
            />
            <span className="mono">{tool.name}</span>
            <span className={`text-2xs border rounded px-1.5 py-0.5 ${classColor[tool.class]}`}>
              {t(`configuration.mcpClass${tool.class[0].toUpperCase()}${tool.class.slice(1)}`)}
            </span>
          </li>
        ))}
      </ul>

      <h3 className="text-xs font-semibold text-muted uppercase tracking-wide mb-2">{t("configuration.mcpGrants")}</h3>
      {st.grants.length === 0 ? (
        <div className="text-xs text-muted mb-3">{t("configuration.mcpNoGrants")}</div>
      ) : (
        <ul className="space-y-2 mb-3">
          {st.grants.map((g) => (
            <li key={g.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border border-border px-3 py-2 text-xs">
              <span className="font-medium">{g.clientName}</span>
              <span className="text-2xs text-muted">{g.userEmail}</span>
              <span className="text-2xs text-muted">
                {g.lastUsedAt ? t("configuration.mcpLastUsed", { date: date(g.lastUsedAt) }) : t("configuration.mcpNeverUsed")}
              </span>
              {g.lastIp && <span className="text-2xs text-muted mono">{g.lastIp}</span>}
              <button
                type="button"
                disabled={busy}
                onClick={() => run(() => api.mcpRemoteRevoke(g.id))}
                className="ml-auto border border-danger/50 text-danger rounded px-2 py-1 hover:bg-danger/10 disabled:opacity-40"
              >
                {t("configuration.mcpRevoke")}
              </button>
            </li>
          ))}
        </ul>
      )}
      {st.grants.length > 0 && (
        <button
          type="button"
          disabled={busy}
          onClick={() => window.confirm(t("configuration.mcpConfirmRevokeAll")) && run(() => api.mcpRemoteRevokeAll())}
          className="text-xs border border-danger/50 text-danger rounded px-2 py-1 hover:bg-danger/10 disabled:opacity-40 mb-4"
        >
          {t("configuration.mcpRevokeAll")}
        </button>
      )}

      <h3 className="text-xs font-semibold text-muted uppercase tracking-wide mb-2">{t("configuration.mcpAudit")}</h3>
      {audit.length === 0 ? (
        <div className="text-xs text-muted">{t("configuration.mcpAuditEmpty")}</div>
      ) : (
        <div className="max-h-56 overflow-auto text-2xs mono space-y-0.5">
          {audit.map((a) => (
            <div key={a.id} className={a.outcome === "ok" ? "text-muted" : "text-danger"}>
              {date(a.ts)} · {a.client || "-"} · {a.method}
              {a.tool ? ` ${a.tool}` : ""} · {a.outcome}
              {a.detail ? ` (${a.detail})` : ""} · {a.ip}
            </div>
          ))}
        </div>
      )}
    </section>
  );
}
