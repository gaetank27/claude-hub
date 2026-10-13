import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api, type ScriptCommand } from "../lib/api";

// Command channel of one script: send an allow-listed command, follow its life
// (pending → delivered → ok/failed, or cancelled/expired), cancel a pending one.
// SECURITY: a command is a name from the script's allow-list plus small JSON
// params — never code; the server re-validates everything. `message` (the
// script's acknowledgement) and params are rendered as escaped text nodes only.

const TTL_CHOICES = [60, 360, 1440, 4320]; // minutes (default 24 h)

const TONE: Record<string, string> = {
  pending: "text-amber-300 bg-amber-500/15 border-amber-500/40",
  delivered: "text-sky-300 bg-sky-500/15 border-sky-500/40",
  ok: "text-green-300 bg-green-500/15 border-green-500/40",
  failed: "text-red-300 bg-red-500/15 border-red-500/40",
  cancelled: "text-slate-300 bg-slate-500/15 border-slate-500/40",
  expired: "text-slate-300 bg-slate-500/15 border-slate-500/40",
};
const ICON: Record<string, string> = { pending: "…", delivered: "→", ok: "✓", failed: "✕", cancelled: "⊘", expired: "⏱" };

const clip = (s: string, n: number) => (s.length > n ? s.slice(0, n - 1) + "…" : s);
const fmt = (ms: number) => new Date(ms).toLocaleString(undefined, { dateStyle: "short", timeStyle: "short", hourCycle: "h23" });
const inputCls = "rounded-control border border-border bg-bg px-2 py-1.5 text-sm min-h-[36px] focus:outline-none focus:border-accent";
const btnCls = "rounded-control border border-border px-3 py-1.5 min-h-[36px] text-sm hover:text-fg disabled:opacity-50";

function paramsText(p: unknown): string {
  if (p === null || typeof p !== "object" || Object.keys(p as object).length === 0) return "";
  try {
    return clip(JSON.stringify(p) ?? "", 200);
  } catch {
    return "";
  }
}

export function ScriptCommands({
  script,
  allowed,
  commands,
  onChanged,
  ago,
}: {
  script: string;
  allowed: string[];
  commands: ScriptCommand[];
  onChanged: () => void;
  ago: (sec: number) => string;
}) {
  const { t } = useTranslation();
  // Open from the start when something is in flight, so the user sees its progress.
  const [open, setOpen] = useState(() => commands.some((c) => c.state === "pending" || c.state === "delivered"));
  const [name, setName] = useState("");
  const [params, setParams] = useState("");
  const [ttl, setTtl] = useState(1440);
  const [busy, setBusy] = useState("");
  const [err, setErr] = useState("");
  const [sent, setSent] = useState("");

  const chosen = allowed.includes(name) ? name : allowed[0] ?? "";
  const active = commands.filter((c) => c.state === "pending" || c.state === "delivered").length;

  const send = async (e: React.FormEvent) => {
    e.preventDefault();
    setErr("");
    setSent("");
    let parsed: unknown = undefined;
    if (params.trim()) {
      try {
        parsed = JSON.parse(params);
      } catch {
        setErr(t("scripts.cmd.paramsInvalid"));
        return;
      }
      if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
        setErr(t("scripts.cmd.paramsInvalid"));
        return;
      }
    }
    setBusy("send");
    try {
      await api.scriptCommandSend({ script, name: chosen, params: parsed, ttlMinutes: ttl });
      setSent(t("scripts.cmd.queued", { cmd: chosen }));
      setParams("");
      onChanged();
    } catch (e: any) {
      setErr(t("scripts.cmd.sendError", { msg: String(e?.message ?? e) }));
    } finally {
      setBusy("");
    }
  };

  const cancel = async (c: ScriptCommand) => {
    setBusy(c.id);
    setErr("");
    try {
      await api.scriptCommandCancel(c.id);
      onChanged();
    } catch (e: any) {
      setErr(t("scripts.cmd.cancelError", { msg: String(e?.message ?? e) }));
    } finally {
      setBusy("");
    }
  };

  return (
    <div className="space-y-2" data-testid="script-commands">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-xs text-muted">{t("scripts.cmd.title")}</span>
        {active > 0 && (
          <span className="rounded-pill border border-amber-500/40 bg-amber-500/15 px-2 py-0.5 text-xs text-amber-300">
            {t("scripts.cmd.active", { count: active })}
          </span>
        )}
        <button
          type="button"
          onClick={() => setOpen((o) => !o)}
          aria-expanded={open}
          className="text-xs text-accent hover:underline min-h-[32px]"
          data-testid="cmd-toggle"
        >
          {open ? t("scripts.cmd.hide") : t("scripts.cmd.show", { count: commands.length })}
        </button>
      </div>

      {open && (
        <div className="space-y-3 rounded-control border border-border bg-panel-2 p-2.5">
          {allowed.length === 0 ? (
            <p className="text-xs text-muted" data-testid="cmd-none">
              {t("scripts.cmd.noneAllowed")}
            </p>
          ) : (
            <form onSubmit={send} className="flex flex-wrap items-end gap-2" data-testid="cmd-form">
              <label className="text-xs text-muted">
                {t("scripts.cmd.command")}
                <select className={`${inputCls} block mt-1 font-mono`} value={chosen} onChange={(e) => setName(e.target.value)} data-testid="cmd-name">
                  {allowed.map((c) => (
                    <option key={c} value={c}>
                      {c}
                    </option>
                  ))}
                </select>
              </label>
              <label className="text-xs text-muted flex-1 basis-40 min-w-0">
                {t("scripts.cmd.params")}
                <input
                  className={`${inputCls} block w-full mt-1 font-mono`}
                  value={params}
                  placeholder='{"seconds":120}'
                  spellCheck={false}
                  autoCapitalize="none"
                  onChange={(e) => setParams(e.target.value)}
                  data-testid="cmd-params"
                />
              </label>
              <label className="text-xs text-muted">
                {t("scripts.cmd.validity")}
                <select className={`${inputCls} block mt-1`} value={ttl} onChange={(e) => setTtl(Number(e.target.value))}>
                  {TTL_CHOICES.map((m) => (
                    <option key={m} value={m}>
                      {m >= 1440 ? t("scripts.cmd.days", { count: m / 1440 }) : t("scripts.cmd.hours", { count: m / 60 })}
                    </option>
                  ))}
                </select>
              </label>
              <button type="submit" disabled={busy === "send" || !chosen} className={`${btnCls} bg-accent/15 border-accent/40 text-fg`} data-testid="cmd-send">
                {busy === "send" ? t("scripts.cmd.sending") : t("scripts.cmd.send")}
              </button>
            </form>
          )}
          <p className="text-xs text-muted">{t("scripts.cmd.delayNote")}</p>
          {sent && (
            <div role="status" className="text-xs text-green-300">
              {sent}
            </div>
          )}
          {err && (
            <div role="alert" className="text-xs text-red-300 break-words">
              {err}
            </div>
          )}

          {commands.length === 0 ? (
            <p className="text-xs text-muted">{t("scripts.cmd.empty")}</p>
          ) : (
            <ul className="divide-y divide-border rounded-control border border-border text-xs" data-testid="cmd-list">
              {commands.map((c) => (
                <li key={c.id} className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 px-2 py-1.5" data-state={c.state}>
                  <span className={`inline-flex items-center gap-1 rounded-pill border px-2 py-0.5 ${TONE[c.state] ?? TONE.expired}`}>
                    <span aria-hidden>{ICON[c.state] ?? "?"}</span>
                    {TONE[c.state] ? t(`scripts.cmd.states.${c.state}`) : "?"}
                  </span>
                  <span className="font-mono font-medium break-all">{clip(c.name, 40)}</span>
                  {paramsText(c.params) && <span className="font-mono text-muted break-all">{paramsText(c.params)}</span>}
                  <span className="text-muted">
                    {t("scripts.cmd.by", { who: c.createdBy === "assistant" ? t("scripts.cmd.assistant") : t("scripts.cmd.user") })} · {fmt(c.createdAt)}
                  </span>
                  {c.state === "pending" && <span className="text-muted">{t("scripts.cmd.expires", { when: fmt(c.expiresAt) })}</span>}
                  {c.state === "delivered" && c.deliveredAt > 0 && (
                    <span className="text-muted">{t("scripts.cmd.deliveredAt", { ago: ago(Math.max(0, (Date.now() - c.deliveredAt) / 1000)) })}</span>
                  )}
                  {c.message && <span className="basis-full break-words">{clip(c.message, 200)}</span>}
                  {c.state === "pending" && (
                    <button type="button" className={`${btnCls} ml-auto text-red-300 border-red-500/40`} disabled={busy === c.id} onClick={() => cancel(c)} data-testid="cmd-cancel">
                      {t("scripts.cmd.cancel")}
                    </button>
                  )}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}
