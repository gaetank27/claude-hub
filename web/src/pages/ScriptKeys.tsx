import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, type ScriptKey } from "../lib/api";

// Admin section of the Scripts page: manage the API keys of /webhook/script-status.
// SECURITY: the secret (hsk_…) lives only in the `reveal` state while the modal is
// open — never in localStorage, the URL or the console. All values are rendered as
// React text nodes (escaped). The server stays authoritative for validation.

const SCRIPT_RE = /^[a-z0-9][a-z0-9_-]{0,47}$/;
const PUBLIC_URL = "https://status.example.com/webhook/script-status/";

function validIp(raw: string): boolean {
  const s = raw.trim();
  const [addr, mask, extra] = s.split("/");
  if (extra !== undefined || !addr) return false;
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(addr)) {
    if (!addr.split(".").every((o) => Number(o) <= 255)) return false;
    return mask === undefined || (/^\d{1,2}$/.test(mask) && Number(mask) <= 32);
  }
  if (addr.includes(":") && /^[0-9a-fA-F:.]+$/.test(addr) && addr.length <= 45) {
    return mask === undefined || (/^\d{1,3}$/.test(mask) && Number(mask) <= 128);
  }
  return false;
}

const CMD_RE = /^[a-z][a-z0-9-]{0,31}$/;
const splitWords = (text: string): string[] => text.split(/[\s,;]+/).map((x) => x.trim()).filter(Boolean);

const splitIps = (text: string): string[] =>
  text
    .split(/[\s,;]+/)
    .map((x) => x.trim())
    .filter(Boolean);

function fmtAbs(ms: number): string {
  return new Date(ms).toLocaleString(undefined, { dateStyle: "short", timeStyle: "short", hourCycle: "h23" });
}

function psExample(script: string, secret: string): string {
  return [
    `$key  = '${secret}'`,
    `$body = @{ state = 'ok'; summary = 'Job terminé' } | ConvertTo-Json`,
    `Invoke-RestMethod -Method Post -Uri '${PUBLIC_URL}${script}' \``,
    `  -Headers @{ 'X-Hub-Script-Key' = $key } \``,
    `  -ContentType 'application/json' -Body $body`,
  ].join("\n");
}

const inputCls =
  "w-full rounded-control border border-border bg-bg px-2 py-2 text-sm min-h-[40px] focus:outline-none focus:border-accent";
const btnCls = "rounded-control border border-border px-3 py-1.5 min-h-[36px] text-sm hover:text-fg disabled:opacity-50";

export function ScriptKeys() {
  const { t } = useTranslation();
  const [keys, setKeys] = useState<ScriptKey[] | null>(null);
  const [err, setErr] = useState("");
  const [actionErr, setActionErr] = useState("");
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<string | null>(null);
  const [reveal, setReveal] = useState<{ script: string; secret: string } | null>(null);
  const [busyId, setBusyId] = useState("");

  const load = useCallback(async () => {
    try {
      setKeys(await api.scriptKeys());
      setErr("");
    } catch (e: any) {
      setErr(String(e?.message ?? e));
    }
  }, []);
  useEffect(() => {
    load();
  }, [load]);

  const run = async (id: string, fn: () => Promise<void>) => {
    setBusyId(id);
    setActionErr("");
    try {
      await fn();
      await load();
    } catch (e: any) {
      setActionErr(t("scripts.keys.actionError", { msg: String(e?.message ?? e) }));
    } finally {
      setBusyId("");
    }
  };

  const regenerate = (k: ScriptKey) => {
    if (!window.confirm(t("scripts.keys.regenerateConfirm", { script: k.script }))) return;
    run(k.id, async () => {
      const r = await api.scriptKeyRegenerate(k.id);
      setReveal({ script: r.key.script, secret: r.secret });
    });
  };
  const remove = (k: ScriptKey) => {
    if (!window.confirm(t("scripts.keys.deleteConfirm", { script: k.script }))) return;
    run(k.id, () => api.scriptKeyDelete(k.id));
  };

  return (
    <section className="space-y-3" data-testid="script-keys">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <h2 className="text-base font-semibold">{t("scripts.keys.title")}</h2>
          <p className="text-sm text-muted">{t("scripts.keys.intro")}</p>
        </div>
        {!adding && (
          <button type="button" onClick={() => setAdding(true)} className={`${btnCls} bg-accent/15 border-accent/40 text-fg`}>
            + {t("scripts.keys.add")}
          </button>
        )}
      </div>

      {err && (
        <div role="alert" className="rounded-card border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
          {t("scripts.keys.loadError", { msg: err })}
        </div>
      )}
      {actionErr && (
        <div role="alert" className="rounded-card border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
          {actionErr}
        </div>
      )}

      {adding && (
        <KeyForm
          onCancel={() => setAdding(false)}
          onSaved={(script, secret) => {
            setAdding(false);
            setReveal({ script, secret });
            load();
          }}
        />
      )}

      {!keys && !err && <div className="h-16 rounded-card border border-border bg-panel animate-pulse" aria-busy="true" />}
      {keys && keys.length === 0 && !adding && (
        <div className="rounded-card border border-border bg-panel p-4 text-sm text-muted">{t("scripts.keys.none")}</div>
      )}

      <div className="space-y-2">
        {keys?.map((k) =>
          editing === k.id ? (
            <KeyForm
              key={k.id}
              existing={k}
              onCancel={() => setEditing(null)}
              onSaved={() => {
                setEditing(null);
                load();
              }}
            />
          ) : (
            <div
              key={k.id}
              data-testid="key-card"
              className={`rounded-card border bg-panel p-3 space-y-2 ${k.enabled ? "border-border" : "border-border opacity-70"}`}
            >
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-mono text-sm font-semibold break-all">{k.script}</span>
                {!k.enabled && (
                  <span className="rounded-pill border border-border px-2 py-0.5 text-xs text-muted">{t("scripts.keys.disabled")}</span>
                )}
                <span className="rounded-pill border border-border px-2 py-0.5 text-xs text-muted">
                  {k.wake ? t("scripts.keys.wakeShort") : t("scripts.keys.noWakeShort")}
                </span>
                {k.allowImages && (
                  <span className="rounded-pill border border-border px-2 py-0.5 text-xs text-muted">{t("scripts.keys.imagesShort")}</span>
                )}
                <span className="rounded-pill border border-border px-2 py-0.5 text-xs text-muted">
                  {k.silenceMinutes > 0 ? t("scripts.silenceAfter", { count: k.silenceMinutes }) : t("scripts.noSilence")}
                </span>
              </div>
              {k.label && <div className="text-xs text-muted break-words">{k.label}</div>}
              <div className="text-xs">
                <span className="text-muted">{t("scripts.keys.ips")} : </span>
                {k.allowedIps.length === 0 ? (
                  <span className="text-amber-300" data-testid="no-ip-badge">
                    ⚠ {t("scripts.keys.noIpBadge")}
                  </span>
                ) : (
                  <span className="font-mono break-all">{k.allowedIps.join(", ")}</span>
                )}
              </div>
              <div className="text-xs" data-testid="key-commands">
                <span className="text-muted">{t("scripts.keys.commands")} : </span>
                {k.allowedCommands.length === 0 ? (
                  <span className="text-muted">{t("scripts.keys.noCommands")}</span>
                ) : (
                  <span className="font-mono break-all">{k.allowedCommands.join(", ")}</span>
                )}
              </div>
              <div className="text-xs text-muted">
                {t("scripts.keys.createdAt", { when: fmtAbs(k.createdAt) })} ·{" "}
                {k.lastUsed ? t("scripts.keys.lastUsed", { when: fmtAbs(k.lastUsed) }) : t("scripts.keys.neverUsed")}
              </div>
              <div className="flex flex-wrap gap-2 pt-1">
                <button type="button" className={btnCls} disabled={busyId === k.id} onClick={() => setEditing(k.id)}>
                  {t("scripts.keys.edit")}
                </button>
                <button type="button" className={btnCls} disabled={busyId === k.id} onClick={() => regenerate(k)}>
                  {t("scripts.keys.regenerate")}
                </button>
                <button
                  type="button"
                  className={btnCls}
                  disabled={busyId === k.id}
                  onClick={() => run(k.id, () => api.scriptKeyUpdate(k.id, { enabled: !k.enabled }))}
                >
                  {k.enabled ? t("scripts.keys.revoke") : t("scripts.keys.enable")}
                </button>
                <button
                  type="button"
                  className={`${btnCls} text-red-300 border-red-500/40`}
                  disabled={busyId === k.id}
                  onClick={() => remove(k)}
                >
                  {t("scripts.keys.delete")}
                </button>
              </div>
            </div>
          ),
        )}
      </div>
      {keys && keys.length > 0 && <p className="text-xs text-muted">{t("scripts.keys.lastIpNote")}</p>}

      {reveal && <SecretModal script={reveal.script} secret={reveal.secret} onClose={() => setReveal(null)} />}
    </section>
  );
}

function KeyForm({
  existing,
  onCancel,
  onSaved,
}: {
  existing?: ScriptKey;
  onCancel: () => void;
  onSaved: (script: string, secret: string) => void;
}) {
  const { t } = useTranslation();
  const [script, setScript] = useState(existing?.script ?? "");
  const [label, setLabel] = useState(existing?.label ?? "");
  const [ips, setIps] = useState((existing?.allowedIps ?? []).join("\n"));
  const [silence, setSilence] = useState(String(existing?.silenceMinutes ?? 0));
  const [wake, setWake] = useState(existing?.wake ?? true);
  const [images, setImages] = useState(existing?.allowImages ?? false);
  const [cmds, setCmds] = useState((existing?.allowedCommands ?? []).join(", "));
  const [saving, setSaving] = useState(false);
  const [submitErr, setSubmitErr] = useState("");
  const [touched, setTouched] = useState(false);

  const list = splitIps(ips);
  const badIp = list.find((x) => !validIp(x));
  const scriptOk = existing ? true : SCRIPT_RE.test(script);
  const silenceN = Number(silence);
  const silenceOk = /^\d{1,5}$/.test(silence.trim()) && silenceN >= 0 && silenceN <= 10080;
  const cmdList = splitWords(cmds);
  const badCmd = cmdList.find((c) => !CMD_RE.test(c));
  const valid = scriptOk && !badIp && silenceOk && !badCmd && cmdList.length <= 20;

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setTouched(true);
    if (!valid) return;
    if (list.length === 0 && !window.confirm(t("scripts.keys.noIpConfirm"))) return;
    setSaving(true);
    setSubmitErr("");
    try {
      if (existing) {
        await api.scriptKeyUpdate(existing.id, { allowedIps: list, silenceMinutes: silenceN, wake, allowedCommands: cmdList, allowImages: images });
        onSaved(existing.script, "");
      } else {
        const r = await api.scriptKeyCreate({ script, label: label.trim(), allowedIps: list, silenceMinutes: silenceN, wake, allowImages: images, ...(cmdList.length ? { allowedCommands: cmdList } : {}) });
        onSaved(r.key.script, r.secret);
      }
    } catch (e: any) {
      setSubmitErr(String(e?.message ?? e));
      setSaving(false);
    }
  };

  const err = "text-xs text-red-300 mt-1";
  return (
    <form onSubmit={submit} className="rounded-card border border-accent/40 bg-panel p-3 space-y-3" data-testid="key-form">
      {existing ? (
        <div className="font-mono text-sm font-semibold break-all">{existing.script}</div>
      ) : (
        <>
          <label className="block text-sm">
            {t("scripts.keys.script")}
            <input
              className={`${inputCls} font-mono mt-1`}
              value={script}
              maxLength={48}
              autoCapitalize="none"
              autoCorrect="off"
              spellCheck={false}
              onChange={(e) => setScript(e.target.value)}
              data-testid="key-script"
            />
            <span className="block text-xs text-muted mt-1">{t("scripts.keys.scriptHint")}</span>
            {touched && !scriptOk && <span className={err}>{t("scripts.keys.scriptInvalid")}</span>}
          </label>
          <label className="block text-sm">
            {t("scripts.keys.label")}
            <input className={`${inputCls} mt-1`} value={label} maxLength={80} onChange={(e) => setLabel(e.target.value)} />
          </label>
        </>
      )}
      <label className="block text-sm">
        {t("scripts.keys.ips")}
        <textarea
          className={`${inputCls} font-mono mt-1`}
          rows={3}
          value={ips}
          spellCheck={false}
          autoCapitalize="none"
          onChange={(e) => setIps(e.target.value)}
          data-testid="key-ips"
        />
        <span className="block text-xs text-muted mt-1">{t("scripts.keys.ipsHint")}</span>
        {badIp && <span className={err}>{t("scripts.keys.ipInvalid", { ip: badIp })}</span>}
      </label>
      {list.length === 0 && (
        <div role="alert" data-testid="no-ip-warning" className="rounded-card border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-300">
          ⚠ {t("scripts.keys.noIpWarn")}
        </div>
      )}
      <label className="block text-sm">
        {t("scripts.keys.silence")}
        <input
          className={`${inputCls} mt-1 sm:w-40`}
          inputMode="numeric"
          value={silence}
          onChange={(e) => setSilence(e.target.value)}
          data-testid="key-silence"
        />
        {touched && !silenceOk && <span className={err}>{t("scripts.keys.silenceInvalid")}</span>}
      </label>
      <label className="flex items-center gap-2 text-sm min-h-[36px]">
        <input type="checkbox" checked={wake} onChange={(e) => setWake(e.target.checked)} />
        {t("scripts.keys.wake")}
      </label>
      <label className="flex items-start gap-2 text-sm min-h-[36px]">
        <input type="checkbox" className="mt-1" checked={images} onChange={(e) => setImages(e.target.checked)} data-testid="key-images" />
        <span>
          {t("scripts.keys.images")}
          <span className="block text-xs text-muted">{t("scripts.keys.imagesHint")}</span>
        </span>
      </label>
      <label className="block text-sm">
        {t("scripts.keys.commands")}
        <input
          className={`${inputCls} font-mono mt-1`}
          value={cmds}
          spellCheck={false}
          autoCapitalize="none"
          placeholder="pause, resume, report-now"
          onChange={(e) => setCmds(e.target.value)}
          data-testid="key-commands-input"
        />
        <span className="block text-xs text-muted mt-1">{t(existing ? "scripts.keys.commandsHint" : "scripts.keys.commandsHintNew")}</span>
        {badCmd && <span className={err}>{t("scripts.keys.commandInvalid", { cmd: badCmd.slice(0, 40) })}</span>}
      </label>
      {submitErr && (
        <div role="alert" className={err}>
          {t("scripts.keys.actionError", { msg: submitErr })}
        </div>
      )}
      <div className="flex gap-2">
        <button type="submit" disabled={saving} className={`${btnCls} bg-accent/15 border-accent/40 text-fg`} data-testid="key-submit">
          {existing ? t("scripts.keys.save") : t("scripts.keys.create")}
        </button>
        <button type="button" onClick={onCancel} className={btnCls}>
          {t("scripts.keys.cancel")}
        </button>
      </div>
    </form>
  );
}

function SecretModal({ script, secret, onClose }: { script: string; secret: string; onClose: () => void }) {
  const { t } = useTranslation();
  const [note, setNote] = useState("");
  const ref = useRef<HTMLTextAreaElement>(null);
  const example = psExample(script, secret);

  const copy = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      setNote(t("scripts.keys.copied"));
    } catch {
      setNote(t("scripts.keys.copyFailed"));
      ref.current?.select();
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-3" role="dialog" aria-modal="true" data-testid="secret-modal">
      <div className="w-full max-w-lg max-h-[90vh] overflow-y-auto rounded-card border border-border bg-panel p-4 space-y-3">
        <h3 className="text-base font-semibold">{t("scripts.keys.secretTitle")}</h3>
        <div role="alert" className="rounded-card border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-300">
          {t("scripts.keys.secretWarn")}
        </div>
        <div className="text-xs text-muted">
          <span className="font-mono break-all">{script}</span>
        </div>
        <textarea
          ref={ref}
          readOnly
          rows={2}
          value={secret}
          onFocus={(e) => e.currentTarget.select()}
          className={`${inputCls} font-mono break-all`}
          data-testid="secret-value"
        />
        <div className="flex flex-wrap items-center gap-2">
          <button type="button" className={`${btnCls} bg-accent/15 border-accent/40 text-fg`} onClick={() => copy(secret)}>
            {t("scripts.keys.copy")}
          </button>
          {note && (
            <span role="status" className="text-xs text-muted">
              {note}
            </span>
          )}
        </div>
        <div className="space-y-1">
          <div className="text-sm font-medium">{t("scripts.keys.exampleTitle")}</div>
          <pre className="rounded-control border border-border bg-bg p-2 text-xs overflow-x-auto whitespace-pre">{example}</pre>
          <button type="button" className={btnCls} onClick={() => copy(example)}>
            {t("scripts.keys.copyExample")}
          </button>
        </div>
        <button type="button" className={`${btnCls} w-full`} onClick={onClose} data-testid="secret-close">
          {t("scripts.keys.close")}
        </button>
      </div>
    </div>
  );
}
