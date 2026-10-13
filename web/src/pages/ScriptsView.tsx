import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { ScriptKeys } from "./ScriptKeys";
import { ScriptCommands } from "./ScriptCommands";
import { api, type ScriptDashboard, type ScriptCommand, type ScriptHistoryItem, type ScriptImage, type ScriptReport } from "../lib/api";

// Read-only monitoring page for scripts reporting to /webhook/script-status.
// SECURITY: every string and the whole `data` payload come from external scripts
// and are untrusted. Everything below is rendered as React text nodes (escaped);
// there is no dangerouslySetInnerHTML, no href/src built from script data, and
// numeric values are validated before reaching a style attribute.

const REFRESH_MS = 20_000;
const SEVERITY = ["error", "muet", "warn", "running", "done", "ok"];

interface StateStyle {
  icon: string;
  badge: string; // text/bg/border classes
  bar: string; // timeline block colour
  edge: string; // card left edge
}
const STYLES: Record<string, StateStyle> = {
  error: { icon: "✕", badge: "text-red-300 bg-red-500/15 border-red-500/40", bar: "bg-red-500", edge: "border-l-red-500" },
  muet: { icon: "◌", badge: "text-violet-300 bg-violet-500/15 border-violet-500/40", bar: "bg-violet-500", edge: "border-l-violet-500" },
  warn: { icon: "!", badge: "text-amber-300 bg-amber-500/15 border-amber-500/40", bar: "bg-amber-400", edge: "border-l-amber-400" },
  running: { icon: "▶", badge: "text-sky-300 bg-sky-500/15 border-sky-500/40", bar: "bg-sky-400", edge: "border-l-sky-400" },
  done: { icon: "✓", badge: "text-slate-300 bg-slate-500/15 border-slate-500/40", bar: "bg-slate-400", edge: "border-l-slate-400" },
  ok: { icon: "✓", badge: "text-green-300 bg-green-500/15 border-green-500/40", bar: "bg-green-500", edge: "border-l-green-500" },
};
const FALLBACK: StateStyle = STYLES.done;
const styleOf = (s: string) => STYLES[s] ?? FALLBACK;
const severityOf = (s: string) => {
  const i = SEVERITY.indexOf(s);
  return i < 0 ? SEVERITY.length : i;
};

function clip(s: string, n: number): string {
  return s.length > n ? s.slice(0, n - 1) + "…" : s;
}

function fmtAbs(ms: number): string {
  return new Date(ms).toLocaleString(undefined, { dateStyle: "short", timeStyle: "medium", hourCycle: "h23" });
}

function fmtDur(sec: number): string {
  if (sec >= 3600) return `${Math.floor(sec / 3600)}h${String(Math.floor((sec % 3600) / 60)).padStart(2, "0")}`;
  return `${Math.floor(sec / 60)}m${String(Math.floor(sec % 60)).padStart(2, "0")}s`;
}

const isObj = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);
const num = (v: unknown): number | null => (typeof v === "number" && Number.isFinite(v) ? v : null);
const str = (v: unknown): string => (typeof v === "string" ? v : typeof v === "number" || typeof v === "boolean" ? String(v) : "");

export function ScriptsView() {
  const { t } = useTranslation();
  const [data, setData] = useState<ScriptDashboard | null>(null);
  const [fetchedAt, setFetchedAt] = useState(0);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [paused, setPaused] = useState(false);
  const [, setTick] = useState(0);

  const load = useCallback(async () => {
    setBusy(true);
    try {
      const d = await api.scriptDashboard();
      setData(d);
      setFetchedAt(Date.now());
      setErr("");
    } catch (e: any) {
      setErr(String(e?.message ?? e));
    } finally {
      setBusy(false);
    }
  }, []);

  useEffect(() => {
    load();
    if (paused) return;
    const id = setInterval(load, REFRESH_MS);
    return () => clearInterval(id);
  }, [load, paused]);

  // Re-render every 5 s so the relative times keep moving between fetches.
  useEffect(() => {
    const id = setInterval(() => setTick((n) => n + 1), 5_000);
    return () => clearInterval(id);
  }, []);

  const ago = useCallback(
    (sec: number): string => {
      if (sec < 5) return t("scripts.ago.now");
      if (sec < 60) return t("scripts.ago.sec", { count: Math.floor(sec) });
      if (sec < 3600) return t("scripts.ago.min", { count: Math.floor(sec / 60) });
      if (sec < 86400) return t("scripts.ago.hour", { count: Math.floor(sec / 3600) });
      return t("scripts.ago.day", { count: Math.floor(sec / 86400) });
    },
    [t],
  );

  const sorted = useMemo(
    () =>
      [...(data?.scripts ?? [])].sort(
        (a, b) => severityOf(a.status.effective) - severityOf(b.status.effective) || a.status.script.localeCompare(b.status.script),
      ),
    [data],
  );
  const counts = useMemo(() => {
    const c: Record<string, number> = {};
    for (const s of sorted) c[s.status.effective] = (c[s.status.effective] ?? 0) + 1;
    return c;
  }, [sorted]);

  // Age = server-computed age at fetch time + time elapsed since (no client clock skew).
  const sinceFetch = fetchedAt ? (Date.now() - fetchedAt) / 1000 : 0;

  return (
    <div className="mx-auto w-full max-w-4xl p-3 sm:p-6 space-y-4">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <h1 className="text-lg font-semibold">{t("scripts.title")}</h1>
          <p className="text-sm text-muted">{t("scripts.intro")}</p>
        </div>
        <div className="flex items-center gap-2 text-xs text-muted shrink-0">
          <span
            aria-hidden
            className={`inline-block h-2 w-2 rounded-full ${paused ? "bg-muted/50" : "bg-accent"} ${busy ? "animate-pulse" : ""}`}
          />
          <span data-testid="updated">
            {paused ? t("scripts.paused") : fetchedAt ? t("scripts.updated", { ago: ago(sinceFetch) }) : t("common.loading")}
          </span>
          <button
            type="button"
            onClick={() => setPaused((p) => !p)}
            className="rounded-control border border-border px-2 py-1 min-h-[32px] hover:text-fg"
          >
            {paused ? t("scripts.resume") : t("scripts.pause")}
          </button>
          <button
            type="button"
            onClick={load}
            disabled={busy}
            className="rounded-control border border-border px-2 py-1 min-h-[32px] hover:text-fg disabled:opacity-50"
          >
            {t("scripts.refresh")}
          </button>
        </div>
      </div>

      {err && (
        <div role="alert" className="rounded-card border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-300">
          {t("scripts.loadError", { msg: err })}
        </div>
      )}

      {!data && !err && (
        <div className="space-y-3" aria-busy="true">
          {[0, 1].map((i) => (
            <div key={i} className="h-28 rounded-card border border-border bg-panel animate-pulse" />
          ))}
        </div>
      )}

      {data && sorted.length === 0 && (
        <div className="rounded-card border border-border bg-panel p-4 text-sm text-muted">{t("scripts.empty")}</div>
      )}

      {sorted.length > 0 && (
        <div className="flex flex-wrap gap-2" data-testid="summary-chips">
          {SEVERITY.filter((s) => counts[s]).map((s) => (
            <span key={s} className={`inline-flex items-center gap-1 rounded-pill border px-2.5 py-0.5 text-xs ${styleOf(s).badge}`}>
              <span aria-hidden>{styleOf(s).icon}</span>
              {t(`scripts.states.${s}`)} · {counts[s]}
            </span>
          ))}
        </div>
      )}

      <div className="space-y-3">
        {sorted.map(({ status, history, commands, allowedCommands, images }) => (
          <ScriptCard
            key={status.script}
            status={status}
            history={history}
            images={images ?? []}
            commands={commands ?? []}
            allowedCommands={allowedCommands ?? []}
            onChanged={load}
            sinceFetch={sinceFetch}
            ago={ago}
          />
        ))}
      </div>

      <ScriptKeys />
    </div>
  );
}

function StateBadge({ state }: { state: string }) {
  const { t } = useTranslation();
  const st = styleOf(state);
  // Unknown states are shown through the generic label, never as raw script text.
  const label = STYLES[state] ? t(`scripts.states.${state}`) : "?";
  return (
    <span className={`inline-flex items-center gap-1 rounded-pill border px-2.5 py-0.5 text-xs font-medium ${st.badge}`}>
      <span aria-hidden>{st.icon}</span>
      {label}
    </span>
  );
}

function ScriptCard({
  status,
  history,
  images,
  commands,
  allowedCommands,
  onChanged,
  sinceFetch,
  ago,
}: {
  status: ScriptReport;
  history: ScriptHistoryItem[];
  images: ScriptImage[];
  commands: ScriptCommand[];
  allowedCommands: string[];
  onChanged: () => void;
  sinceFetch: number;
  ago: (sec: number) => string;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const st = styleOf(status.effective);
  const age = status.ageSeconds + sinceFetch;
  const chrono = [...history].reverse(); // oldest → newest, left → right

  return (
    <article
      data-testid="script-card"
      data-script={status.script}
      data-state={status.effective}
      className={`rounded-card border border-border border-l-4 ${st.edge} bg-panel p-3 sm:p-4 space-y-3 animate-fadeIn`}
    >
      <header className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="mono text-base font-semibold break-all">{status.script}</h2>
        <StateBadge state={status.effective} />
      </header>

      <div className="text-sm">
        <span className="text-muted">{t("scripts.heartbeat")} : </span>
        <span className="font-medium">{ago(age)}</span>
        <span className="text-muted"> · {fmtAbs(status.receivedAt)}</span>
        <div className="text-xs text-muted">
          {status.silenceMinutes > 0 ? t("scripts.silenceAfter", { count: status.silenceMinutes }) : t("scripts.noSilence")}
          {status.effective === "muet" && STYLES[status.state] && (
            <> · {t("scripts.reportedState", { state: t(`scripts.states.${status.state}`) })}</>
          )}
        </div>
      </div>

      <p className="text-sm whitespace-pre-wrap break-words" data-testid="summary">
        {status.summary ? clip(status.summary, 500) : <span className="text-muted">{t("scripts.noSummary")}</span>}
      </p>

      <DataView data={status.data} />

      <ScriptShots script={status.script} images={images} onChanged={onChanged} />

      <ScriptCommands script={status.script} allowed={allowedCommands} commands={commands} onChanged={onChanged} ago={ago} />

      {chrono.length > 0 && (
        <div className="space-y-2">
          <div className="flex items-center gap-2">
            <span className="text-xs text-muted shrink-0">{t("scripts.history")}</span>
            <div className="flex flex-1 items-end gap-[2px] h-5" role="img" aria-label={t("scripts.history")}>
              {chrono.map((h, i) => (
                <span
                  key={i}
                  title={`${fmtAbs(h.receivedAt)} — ${STYLES[h.state] ? t(`scripts.states.${h.state}`) : "?"}`}
                  className={`flex-1 min-w-[3px] max-w-[14px] rounded-sm ${styleOf(h.state).bar} ${
                    i === chrono.length - 1 ? "h-5" : "h-3 opacity-80"
                  }`}
                />
              ))}
            </div>
            <button
              type="button"
              onClick={() => setOpen((o) => !o)}
              aria-expanded={open}
              className="text-xs text-accent hover:underline shrink-0 min-h-[32px]"
            >
              {open ? t("scripts.historyHide") : t("scripts.historyShow", { count: history.length })}
            </button>
          </div>
          {open && (
            <ul className="divide-y divide-border rounded-control border border-border text-xs" data-testid="history-list">
              {history.map((h, i) => (
                <li key={i} className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 px-2 py-1.5">
                  <span className="text-muted tabular-nums shrink-0">{fmtAbs(h.receivedAt)}</span>
                  <StateBadge state={h.state} />
                  <span className="min-w-0 flex-1 basis-40 break-words">{clip(h.summary ?? "", 160)}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </article>
  );
}

// ScriptShots shows the screenshots a script uploaded (newest first). The image
// URL is built from the script name (re-checked here) and a numeric id only —
// never from anything the script wrote. Kept short-lived by the Hub.
const SHOTS_KEEP = 12;
function ScriptShots({ script, images, onChanged }: { script: string; images: ScriptImage[]; onChanged: () => void }) {
  const { t } = useTranslation();
  const [picked, setPicked] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const list = images.filter((i) => Number.isSafeInteger(i.id) && i.id > 0);
  if (list.length === 0 || !/^[a-z0-9][a-z0-9_-]{0,47}$/.test(script)) return null;
  const cur = list.find((i) => i.id === picked) ?? list[0];
  const src = `/api/script-images/${script}/${cur.id}`;
  const del = async () => {
    if (!window.confirm(t("scripts.shotsDeleteConfirm", { script }))) return;
    setBusy(true);
    try {
      await api.scriptImagesDelete(script);
    } catch {
      /* the next refresh shows what is left */
    } finally {
      setBusy(false);
      onChanged();
    }
  };
  return (
    <div className="space-y-2" data-testid="script-shots">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-xs text-muted">
          {t("scripts.shots")} · {t("scripts.shotsNote", { keep: SHOTS_KEEP })}
        </span>
        <button type="button" onClick={del} disabled={busy} className="text-xs text-accent hover:underline min-h-[32px] disabled:opacity-50">
          {t("scripts.shotsDelete")}
        </button>
      </div>
      <a href={src} target="_blank" rel="noopener noreferrer" title={t("scripts.shotOpen")} className="block">
        <img
          src={src}
          alt={t("scripts.shotAlt", { when: fmtAbs(cur.receivedAt) })}
          loading="lazy"
          className="max-h-80 w-auto max-w-full rounded-control border border-border"
        />
      </a>
      <div className="flex flex-wrap items-center gap-1 text-xs">
        {list.map((i) => (
          <button
            key={i.id}
            type="button"
            onClick={() => setPicked(i.id)}
            aria-pressed={i.id === cur.id}
            className={`rounded-pill border px-2 py-0.5 tabular-nums min-h-[28px] ${
              i.id === cur.id ? "border-accent text-accent" : "border-border text-muted hover:text-fg"
            }`}
          >
            {fmtAbs(i.receivedAt)}
          </button>
        ))}
        <span className="text-muted">
          · {num(cur.width) ?? "?"}×{num(cur.height) ?? "?"} · {Math.round((num(cur.size) ?? 0) / 1024)} KB
        </span>
      </div>
    </div>
  );
}

// DataView renders the free-form `data` payload. Known counters and a `jobs`
// array (CloudStack migrations) get a dedicated layout; every other scalar is
// shown as key/value and nested values as clipped JSON text.
function DataView({ data }: { data: unknown }) {
  const { t } = useTranslation();
  if (!isObj(data)) return null;
  const jobs = Array.isArray(data.jobs) ? data.jobs.filter(isObj) : null;
  const counters = ["running", "stuck", "failed", "succeeded"].filter((k) => num(data[k]) !== null);
  const queue = isObj(data.queue) ? data.queue : null;
  const plan = isObj(data.plan) ? data.plan : null;
  const skip = new Set(["jobs", "queue", "plan", ...counters]);
  const rest = Object.entries(data).filter(([k]) => !skip.has(k)).slice(0, 20);
  const shownJobs = jobs ? jobs.slice(0, 20) : [];
  if (!jobs && !queue && !plan && counters.length === 0 && rest.length === 0) return null;

  return (
    <div className="space-y-2" data-testid="data-view">
      {counters.length > 0 && (
        <div className="flex flex-wrap gap-2 text-xs">
          {counters.map((k) => {
            const n = num(data[k]) as number;
            const bad = (k === "failed" || k === "stuck") && n > 0;
            return (
              <span
                key={k}
                className={`rounded-control border px-2 py-1 ${bad ? "border-red-500/40 text-red-300 bg-red-500/10" : "border-border text-muted"}`}
              >
                <b className="tabular-nums text-fg">{n}</b> {t(`scripts.counters.${k}`)}
              </span>
            );
          })}
        </div>
      )}

      {/* A plan awaiting validation comes first; once it is used, the running queue is what matters. */}
      {plan && !(queue && str(plan.status) === "used") && <MigrationPlan plan={plan} />}
      {queue && <MigrationQueue queue={queue} />}
      {plan && queue && str(plan.status) === "used" && <MigrationPlan plan={plan} />}

      {jobs && (
        <div>
          <div className="text-xs text-muted mb-1">{t("scripts.jobs")}</div>
          {shownJobs.length === 0 ? (
            <div className="text-xs text-muted">{t("scripts.noJobs")}</div>
          ) : (
            <ul className="space-y-1.5" data-testid="jobs">
              {shownJobs.map((j, i) => (
                <JobRow key={i} job={j} />
              ))}
              {jobs.length > shownJobs.length && (
                <li className="text-xs text-muted">{t("scripts.moreJobs", { count: jobs.length - shownJobs.length })}</li>
              )}
            </ul>
          )}
        </div>
      )}

      {rest.length > 0 && (
        <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-xs">
          {rest.map(([k, v]) => (
            <div key={k} className="contents">
              <dt className="text-muted break-all">{clip(k, 40)}</dt>
              <dd className="break-words mono">{clip(typeof v === "object" ? JSON.stringify(v) ?? "" : String(v), 200)}</dd>
            </div>
          ))}
        </dl>
      )}
    </div>
  );
}

function JobRow({ job }: { job: Record<string, unknown> }) {
  const { t } = useTranslation();
  const status = str(job.status);
  const pct = num(job.progressPercent);
  const elapsed = num(job.elapsedSeconds);
  const what = [str(job.vm), str(job.volume)].filter(Boolean).join(" / ") || (str(job.jobId) ? `job ${clip(str(job.jobId), 40)}` : "—");
  const src = str(job.source);
  const dst = str(job.destination);
  const tone = status === "failed" ? "error" : status === "running" ? "running" : status === "success" ? "ok" : "done";
  const label = status === "failed" ? "jobFailed" : status === "running" ? "jobRunning" : status === "success" ? "jobSuccess" : "";
  const err = str(job.error);

  return (
    <li className={`rounded-control border border-border border-l-2 ${styleOf(tone).edge} bg-panel-2 px-2.5 py-1.5 text-xs space-y-0.5`}>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
        <span className="font-medium break-all">{clip(what, 120)}</span>
        {label && <span className={`rounded-pill border px-1.5 ${styleOf(tone).badge}`}>{t(`scripts.${label}`)}</span>}
        {elapsed !== null && <span className="text-muted tabular-nums">{t("scripts.elapsed", { d: fmtDur(elapsed) })}</span>}
      </div>
      {(src || dst) && (
        <div className="text-muted break-all">
          {clip(src || t("scripts.unknown"), 80)} → {clip(dst || t("scripts.unknown"), 80)}
        </div>
      )}
      {pct !== null && (
        <div className="flex items-center gap-2">
          <div className="h-1.5 flex-1 rounded-full bg-border overflow-hidden">
            <div className={`h-full ${styleOf(tone).bar}`} style={{ width: `${Math.max(0, Math.min(100, pct))}%` }} />
          </div>
          <span className="tabular-nums text-muted">{Math.round(Math.max(0, Math.min(100, pct)))}%</span>
        </div>
      )}
      {err && <div className="text-red-300 break-words">{clip(err, 300)}</div>}
    </li>
  );
}

// --- CloudStack storage-migration plan and queue (data.plan / data.queue) -----------------------------
// Same rule as above: everything comes from the script (CloudStack error text included) and is
// rendered as escaped React text only, bounded in length and count.

const ENTRY_TONE: Record<string, string> = { pending: "done", running: "running", done: "ok", failed: "error", skipped: "warn", cancelled: "done" };
const QUEUE_TONE: Record<string, string> = { running: "running", paused: "warn", held: "warn", locked: "warn", cancelling: "warn", finished: "ok" };

function fmtBytes(n: number | null): string {
  if (n === null || n < 0) return "?";
  if (n >= 1024 ** 4) return `${(n / 1024 ** 4).toFixed(1)} TB`;
  if (n >= 1024 ** 3) return `${(n / 1024 ** 3).toFixed(1)} GB`;
  return `${Math.round(n / 1024 ** 2)} MB`;
}

function fmtIso(v: unknown): string {
  const ms = typeof v === "string" ? Date.parse(v) : NaN;
  return Number.isFinite(ms) ? fmtAbs(ms) : "";
}

function Chip({ label, n, bad }: { label: string; n: number | null; bad?: boolean }) {
  if (n === null) return null;
  return (
    <span className={`rounded-control border px-2 py-1 ${bad && n > 0 ? "border-red-500/40 text-red-300 bg-red-500/10" : "border-border text-muted"}`}>
      <b className="tabular-nums text-fg">{n}</b> {label}
    </span>
  );
}

function MigrationQueue({ queue }: { queue: Record<string, unknown> }) {
  const { t } = useTranslation();
  const status = str(queue.status);
  const c = isObj(queue.counters) ? queue.counters : {};
  const entries = (Array.isArray(queue.entries) ? queue.entries.filter(isObj) : []).slice(0, 60);
  const eta = num(queue.etaSeconds);
  const tone = QUEUE_TONE[status] ?? "done";
  const stop = str(queue.stopReason);

  return (
    <section className="space-y-1.5" data-testid="migration-queue">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <span className="text-xs text-muted">{t("scripts.queue.title")}</span>
        <span className="text-xs font-medium break-all">→ {clip(str(queue.destination) || t("scripts.unknown"), 80)}</span>
        {status && <span className={`rounded-pill border px-1.5 text-xs ${styleOf(tone).badge}`}>{t(`scripts.queue.status.${status}`, { defaultValue: clip(status, 20) })}</span>}
        {eta !== null && <span className="text-xs text-muted tabular-nums">{t("scripts.queue.eta", { d: fmtDur(eta) })}</span>}
      </div>
      <div className="flex flex-wrap gap-2 text-xs">
        <Chip label={t("scripts.queue.total")} n={num(c.total)} />
        <Chip label={t("scripts.queue.done")} n={num(c.done)} />
        <Chip label={t("scripts.queue.failed")} n={num(c.failed)} bad />
        <Chip label={t("scripts.queue.skipped")} n={num(c.skipped)} />
        <Chip label={t("scripts.queue.cancelled")} n={num(c.cancelled)} />
        <Chip label={t("scripts.queue.remaining")} n={num(c.remaining)} />
      </div>
      {stop && <div className="text-xs text-amber-300 break-words">{clip(stop, 200)}</div>}
      <ul className="space-y-1.5" data-testid="queue-entries">
        {entries.map((e, i) => (
          <QueueEntry key={i} e={e} />
        ))}
      </ul>
    </section>
  );
}

function QueueEntry({ e }: { e: Record<string, unknown> }) {
  const { t } = useTranslation();
  const state = str(e.state);
  const tone = ENTRY_TONE[state] ?? "done";
  const pct = num(e.progressPercent);
  const vd = num(e.volumesDone);
  const vt = num(e.volumesTotal);
  const dur = num(e.durationSeconds);
  const err = str(e.error);
  const reason = str(e.reason);
  const note = str(e.note);
  const cur = str(e.currentVolume);
  const start = fmtIso(e.startedAt);
  const end = fmtIso(e.finishedAt);
  const vols = Array.isArray(e.volumes) ? e.volumes.filter(isObj).slice(0, 10) : [];

  return (
    <li className={`rounded-control border border-border border-l-2 ${styleOf(tone).edge} bg-panel-2 px-2.5 py-1.5 text-xs space-y-0.5`}>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
        <span className="text-muted tabular-nums">#{num(e.position) ?? "?"}</span>
        <span className="font-medium break-all">{clip(str(e.vm) || "—", 80)}</span>
        <span className={`rounded-pill border px-1.5 ${styleOf(tone).badge}`}>{t(`scripts.queue.state.${state}`, { defaultValue: clip(state, 20) })}</span>
        {e.live === true && <span className="text-muted">{t("scripts.queue.live")}</span>}
        {vd !== null && vt !== null && <span className="text-muted tabular-nums">{t("scripts.queue.volumes", { done: vd, total: vt })}</span>}
        {num(e.sizeBytes) !== null && <span className="text-muted tabular-nums">{fmtBytes(num(e.sizeBytes))}</span>}
        {dur !== null && <span className="text-muted tabular-nums">{fmtDur(dur)}</span>}
      </div>
      {(start || end) && (
        <div className="text-muted tabular-nums">
          {start && <span>{t("scripts.queue.started", { at: start })}</span>}
          {end && <span className="ml-2">{t("scripts.queue.finished", { at: end })}</span>}
        </div>
      )}
      {cur && (
        <div className="flex items-center gap-2">
          <span className="text-muted break-all">{t("scripts.queue.currentVolume", { name: clip(cur, 60) })}</span>
          {pct !== null && (
            <>
              <div className="h-1.5 flex-1 rounded-full bg-border overflow-hidden">
                <div className={`h-full ${styleOf(tone).bar}`} style={{ width: `${Math.max(0, Math.min(100, pct))}%` }} />
              </div>
              <span className="tabular-nums text-muted">{Math.round(Math.max(0, Math.min(100, pct)))}%</span>
            </>
          )}
        </div>
      )}
      {vols.length > 0 && state === "failed" && (
        <ul className="text-muted break-all">
          {vols.map((v, i) => (
            <li key={i}>
              {clip(str(v.name), 60)} — {t(`scripts.queue.state.${str(v.state)}`, { defaultValue: clip(str(v.state), 20) })}
              {str(v.error) && <span className="text-red-300"> : {clip(str(v.error), 300)}</span>}
            </li>
          ))}
        </ul>
      )}
      {err && (
        <div className="text-red-300 break-words" data-testid="queue-error">
          {t("scripts.queue.error")} : {clip(err, 300)}
        </div>
      )}
      {!err && reason && <div className="text-muted break-words">{clip(reason, 300)}</div>}
      {note && <div className="text-amber-300 break-words">{clip(note, 200)}</div>}
    </li>
  );
}

function MigrationPlan({ plan }: { plan: Record<string, unknown> }) {
  const { t } = useTranslation();
  const status = str(plan.status);
  const tone = status === "ready" ? "ok" : status === "invalid" ? "error" : "warn";
  const dest = isObj(plan.destination) ? plan.destination : {};
  const entries = (Array.isArray(plan.entries) ? plan.entries.filter(isObj) : []).slice(0, 30);
  const warnings = (Array.isArray(plan.warnings) ? plan.warnings.map(str).filter(Boolean) : []).slice(0, 10);
  const err = str(plan.error);
  const expires = fmtIso(plan.expiresAt);

  return (
    <section className="space-y-1.5" data-testid="migration-plan">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <span className="text-xs text-muted">{t("scripts.plan.title")}</span>
        <span className="text-xs font-medium mono break-all">{clip(str(plan.planId), 24)}</span>
        <span className={`rounded-pill border px-1.5 text-xs ${styleOf(tone).badge}`}>{t(`scripts.plan.status.${status}`, { defaultValue: clip(status, 20) })}</span>
        {plan.canStart === true && <span className="text-xs text-green-300">{t("scripts.plan.canStart")}</span>}
        {plan.canStart === false && status === "ready" && <span className="text-xs text-amber-300">{t("scripts.plan.cannotStart")}</span>}
      </div>
      <div className="text-xs text-muted break-all">
        → {clip(str(dest.name) || t("scripts.unknown"), 80)} · {t("scripts.plan.free", { size: fmtBytes(num(dest.freeBytes)) })} · {t("scripts.plan.total", { size: fmtBytes(num(plan.totalBytes)) })}
        {expires && <> · {t("scripts.plan.expires", { at: expires })}</>}
      </div>
      <div className="flex flex-wrap gap-2 text-xs">
        <Chip label={t("scripts.plan.toMigrate")} n={num(plan.toMigrate)} />
        <Chip label={t("scripts.plan.skipped")} n={num(plan.skipped)} />
      </div>
      {err && <div className="text-xs text-red-300 break-words">{clip(err, 300)}</div>}
      {warnings.map((w, i) => (
        <div key={i} className="text-xs text-amber-300 break-words">
          {clip(w, 200)}
        </div>
      ))}
      <ul className="space-y-1.5" data-testid="plan-entries">
        {entries.map((e, i) => {
          const mig = str(e.action) === "migrate";
          const vols = Array.isArray(e.volumes) ? e.volumes.filter(isObj).slice(0, 10) : [];
          const ws = Array.isArray(e.warnings) ? e.warnings.map(str).filter(Boolean).slice(0, 5) : [];
          return (
            <li key={i} className={`rounded-control border border-border border-l-2 ${styleOf(mig ? "ok" : "warn").edge} bg-panel-2 px-2.5 py-1.5 text-xs space-y-0.5`}>
              <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
                <span className="font-medium break-all">{clip(str(e.vm) || "—", 80)}</span>
                <span className={`rounded-pill border px-1.5 ${styleOf(mig ? "ok" : "warn").badge}`}>{mig ? t("scripts.plan.migrate") : t("scripts.plan.skip")}</span>
                {str(e.vmState) && <span className="text-muted">{clip(str(e.vmState), 20)}</span>}
                {e.live === true && <span className="text-muted">{t("scripts.queue.live")}</span>}
                {num(e.sizeBytes) !== null && mig && <span className="text-muted tabular-nums">{fmtBytes(num(e.sizeBytes))}</span>}
              </div>
              {str(e.reason) && <div className="text-amber-300 break-words">{clip(str(e.reason), 300)}</div>}
              {vols.length > 0 && (
                <ul className="text-muted break-all">
                  {vols.map((v, j) => (
                    <li key={j}>
                      {clip(str(v.name), 60)} · {fmtBytes(num(v.sizeBytes))} · {clip(str(v.sourcePool) || t("scripts.unknown"), 60)}
                      {v.skip === true && <span> ({t("scripts.plan.alreadyThere")})</span>}
                    </li>
                  ))}
                </ul>
              )}
              {ws.map((w, j) => (
                <div key={j} className="text-muted break-words">
                  {clip(w, 200)}
                </div>
              ))}
            </li>
          );
        })}
      </ul>
    </section>
  );
}
