import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, type ClaudeAccount, type ClaudeHistory, type ClaudeRange } from "../lib/api";

// Range buttons. Labels resolve at render via i18n (labelKey under "session").
const RANGES: { id: ClaudeRange; labelKey: string }[] = [
  { id: "hour", labelKey: "session.rangeHour" },
  { id: "day", labelKey: "session.rangeDay" },
  { id: "week", labelKey: "session.rangeWeek" },
  { id: "month", labelKey: "session.rangeMonth" },
];

// --- formatters ---
function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1000) return `${Math.round(n / 1000)}k`;
  return String(Math.round(n));
}
function niceMax(v: number): number {
  if (v <= 0) return 1;
  const pow = Math.pow(10, Math.floor(Math.log10(v)));
  const n = v / pow;
  const step = n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10;
  return step * pow;
}
function fmtTime(sec: number, range: ClaudeRange): string {
  const d = new Date(sec * 1000);
  if (range === "hour" || range === "day")
    return d.toLocaleTimeString("fr-FR", { hour: "2-digit", minute: "2-digit" });
  return d.toLocaleDateString("fr-FR", { day: "2-digit", month: "2-digit" });
}

// sum of non-null point values of a series (for the window counters).
function seriesTotal(pts?: { t: number; v: number | null }[]): number {
  if (!pts) return 0;
  return pts.reduce((a, p) => a + (p.v ?? 0), 0);
}
function seriesMax(pts?: { t: number; v: number | null }[]): number {
  if (!pts) return 0;
  return pts.reduce((m, p) => (p.v != null && p.v > m ? p.v : m), 0);
}

// --- chart geometry (matches HostCharts) ---
const H = 150;
const PAD = { l: 52, r: 12, t: 10, b: 22 };
const FONT = 11;
const FALLBACK_W = 560;

function useWidth(): [React.RefObject<HTMLDivElement>, number] {
  const ref = useRef<HTMLDivElement>(null);
  const [w, setW] = useState(0);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver((e) => {
      const cw = e[0]?.contentRect.width ?? 0;
      if (cw > 0) setW(cw);
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return [ref, w];
}

type Series = { name: string; cls: string; points: { t: number; v: number | null }[] };

function LineChart({
  series,
  from,
  to,
  max,
  fmtY,
  range,
}: {
  series: Series[];
  from: number;
  to: number;
  max: number;
  fmtY: (n: number) => string;
  range: ClaudeRange;
}) {
  const [ref, measured] = useWidth();
  const W = measured || FALLBACK_W;
  const IW = W - PAD.l - PAD.r;
  const span = Math.max(1, to - from);
  const x = (t: number) => PAD.l + (IW * (t - from)) / span;
  const y = (v: number) => PAD.t + (H - PAD.t - PAD.b) * (1 - v / max);

  const segments = (pts: Series["points"]): string[] => {
    const segs: string[] = [];
    let cur: string[] = [];
    for (const p of pts) {
      if (p.v == null) {
        if (cur.length > 1) segs.push(cur.join(" "));
        cur = [];
      } else cur.push(`${x(p.t).toFixed(1)},${y(p.v).toFixed(1)}`);
    }
    if (cur.length > 1) segs.push(cur.join(" "));
    return segs;
  };

  return (
    <figure ref={ref} className="m-0">
      <svg viewBox={`0 0 ${W} ${H}`} width="100%" height={H} className="block" role="img">
        {[0, 0.5, 1].map((g) => (
          <g key={g}>
            <line x1={PAD.l} x2={W - PAD.r} y1={y(max * g)} y2={y(max * g)} className="stroke-border" strokeWidth={1} />
            <text x={PAD.l - 6} y={y(max * g) + 3} textAnchor="end" className="fill-muted" fontSize={FONT}>
              {fmtY(max * g)}
            </text>
          </g>
        ))}
        {series.map((s) =>
          segments(s.points).map((pts, i) => (
            <polyline key={`${s.name}-${i}`} fill="none" className={s.cls} strokeWidth={1.5} points={pts} />
          )),
        )}
        {[0, 0.25, 0.5, 0.75, 1].map((g) => (
          <text key={g} x={PAD.l + IW * g} y={H - 6} textAnchor="middle" className="fill-muted" fontSize={FONT}>
            {from > 0 ? fmtTime(from + span * g, range) : ""}
          </text>
        ))}
      </svg>
    </figure>
  );
}

function Panel({ title, hint, children }: { title: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="bg-panel border border-border rounded-lg p-4">
      <div className="flex items-baseline justify-between gap-2 mb-2">
        <h3 className="text-sm font-medium">{title}</h3>
        {hint && <span className="text-[11px] text-muted">{hint}</span>}
      </div>
      {children}
    </div>
  );
}

function Legend({ items }: { items: { name: string; cls: string }[] }) {
  return (
    <figcaption className="flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-muted mt-1">
      {items.map((it) => (
        <span key={it.name} className="flex items-center gap-1.5">
          <span className={`inline-block w-3 h-0.5 ${it.cls.replace("stroke-", "bg-")}`} />
          {it.name}
        </span>
      ))}
    </figcaption>
  );
}

function Counter({ label, value }: { label: string; value: string }) {
  return (
    <div className="bg-panel border border-border rounded-card px-3 py-2">
      <div className="text-2xs uppercase tracking-wider text-muted">{label}</div>
      <div className="text-sm font-medium mono mt-0.5">{value}</div>
    </div>
  );
}

// ClaudeUsage is the Claude-usage block of the Suivi page: a quick account filter
// (Tous / per-account) above RRD-backed historical charts (tokens, sessions, cost)
// and the plan windows. Switching account or range refetches instantly (no reload).
export function ClaudeUsage() {
  const { t } = useTranslation();
  const [accounts, setAccounts] = useState<ClaudeAccount[]>([]);
  const [account, setAccount] = useState("all");
  const [range, setRange] = useState<ClaudeRange>("hour");
  const [data, setData] = useState<ClaudeHistory | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.listClaudeAccounts().then(setAccounts).catch(() => {});
  }, []);

  useEffect(() => {
    let cancelled = false;
    api
      .getClaudeHistory(range, account)
      .then((d) => {
        if (cancelled) return;
        setData(d);
        setError(null);
      })
      .catch((e) => !cancelled && setError(e.message));
    return () => {
      cancelled = true;
    };
  }, [account, range]);

  const s = data?.series;
  const from = data?.from ?? 0;
  const to = data?.to ?? 0;
  const accountLabel = (a: ClaudeAccount) => a.accountEmail || a.label || a.id.slice(0, 8);

  return (
    <section className="space-y-4 mb-8">
      <div className="flex items-center gap-2">
        <h2 className="text-xs font-semibold uppercase tracking-wider text-muted">{t("session.usageHeading")}</h2>
        <div className="inline-flex rounded-lg border border-border overflow-hidden ml-auto">
          {RANGES.map((b) => (
            <button
              key={b.id}
              onClick={() => setRange(b.id)}
              className={`px-3 py-1.5 text-xs transition-colors ${
                range === b.id ? "bg-accent text-bg font-medium" : "text-muted hover:text-fg hover:bg-panel-2"
              }`}
            >
              {t(b.labelKey)}
            </button>
          ))}
        </div>
      </div>

      {/* Account quick filter — horizontally scrollable on mobile. */}
      <div className="flex gap-2 overflow-x-auto pb-1 -mx-1 px-1">
        <Chip active={account === "all"} onClick={() => setAccount("all")}>
          {t("common.all")}
        </Chip>
        {accounts.map((a) => (
          <Chip key={a.id} active={account === a.id} onClick={() => setAccount(a.id)}>
            {accountLabel(a)}
          </Chip>
        ))}
      </div>

      {error && <div className="text-danger text-sm">{error}</div>}

      {data && !data.available && (
        <div className="text-xs text-muted/70 py-8 text-center bg-panel border border-border rounded-lg">
          {t("session.historyUnavailable")}
        </div>
      )}

      {data && data.available && s && (
        <>
          {/* window counters */}
          <div className="grid grid-cols-3 gap-3">
            <Counter label={t("session.tokensIn")} value={fmtTokens(seriesTotal(s.tok_in))} />
            <Counter label={t("session.tokensOut")} value={fmtTokens(seriesTotal(s.tok_out))} />
            <Counter label={t("session.estimatedCost")} value={`$${seriesTotal(s.cost).toFixed(2)}`} />
          </div>

          <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
            <Panel title={t("session.panelTokens")} hint={t("session.panelTokensHint")}>
              <LineChart
                range={range}
                from={from}
                to={to}
                max={niceMax(Math.max(1, seriesMax(s.tok_in), seriesMax(s.tok_out)))}
                fmtY={fmtTokens}
                series={[
                  { name: t("session.seriesInput"), cls: "stroke-accent", points: s.tok_in ?? [] },
                  { name: t("session.seriesOutput"), cls: "stroke-accent-dim", points: s.tok_out ?? [] },
                ]}
              />
              <Legend
                items={[
                  { name: t("session.seriesInput"), cls: "stroke-accent" },
                  { name: t("session.seriesOutput"), cls: "stroke-accent-dim" },
                ]}
              />
            </Panel>

            <Panel title={t("session.panelActiveSessions")} hint={t("session.panelActiveSessionsHint")}>
              <LineChart
                range={range}
                from={from}
                to={to}
                max={niceMax(Math.max(1, seriesMax(s.sess_active)))}
                fmtY={(n) => String(Math.round(n))}
                series={[{ name: t("session.seriesActive"), cls: "stroke-ok", points: s.sess_active ?? [] }]}
              />
            </Panel>

            <Panel title={t("session.panelCost")} hint={t("session.panelCostHint")}>
              <LineChart
                range={range}
                from={from}
                to={to}
                max={niceMax(Math.max(0.01, seriesMax(s.cost)))}
                fmtY={(n) => `$${n.toFixed(2)}`}
                series={[{ name: t("session.seriesCost"), cls: "stroke-accent", points: s.cost ?? [] }]}
              />
            </Panel>

            {account !== "all" && (
              <Panel title={t("session.panelPlanLimits")} hint={t("session.panelPlanLimitsHint")}>
                {data.plan && data.plan.length > 0 ? (
                  <PlanChart plan={data.plan} from={from} to={to} />
                ) : (
                  <div className="text-xs text-muted/70 py-8 text-center">
                    {t("session.noPlanData")}
                  </div>
                )}
              </Panel>
            )}
          </div>
        </>
      )}
    </section>
  );
}

function Chip({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      onClick={onClick}
      className={`shrink-0 px-3 py-1.5 rounded-pill text-xs border transition-colors ${
        active
          ? "bg-accent text-bg border-accent font-medium"
          : "border-border text-muted hover:text-fg hover:border-accent-dim"
      }`}
    >
      {children}
    </button>
  );
}

// PlanChart plots the sampled plan-usage windows (0–100%) by real timestamp.
function PlanChart({ plan, from, to }: { plan: NonNullable<ClaudeHistory["plan"]>; from: number; to: number }) {
  const { t } = useTranslation();
  const [ref, measured] = useWidth();
  const W = measured || FALLBACK_W;
  const IW = W - PAD.l - PAD.r;
  const span = Math.max(1, to - from);
  const x = (iso: string) => PAD.l + (IW * (new Date(iso).getTime() / 1000 - from)) / span;
  const y = (v: number) => PAD.t + (H - PAD.t - PAD.b) * (1 - v / 100);

  const series: { key: "session" | "week" | "weekOpus"; name: string; cls: string }[] = [
    { key: "session", name: t("session.planSession5h"), cls: "stroke-accent" },
    { key: "week", name: t("session.planWeekly"), cls: "stroke-ok" },
    { key: "weekOpus", name: t("session.planOpus"), cls: "stroke-danger" },
  ];

  return (
    <figure ref={ref} className="m-0">
      <svg viewBox={`0 0 ${W} ${H}`} width="100%" height={H} className="block" role="img">
        {[0, 50, 100].map((g) => (
          <g key={g}>
            <line x1={PAD.l} x2={W - PAD.r} y1={y(g)} y2={y(g)} className="stroke-border" strokeWidth={1} />
            <text x={PAD.l - 6} y={y(g) + 3} textAnchor="end" className="fill-muted" fontSize={FONT}>
              {g}%
            </text>
          </g>
        ))}
        {series.map((sd) => {
          const pts = plan
            .filter((p) => p[sd.key] != null)
            .map((p) => `${x(p.ts).toFixed(1)},${y(p[sd.key] as number).toFixed(1)}`);
          if (pts.length === 0) return null;
          return <polyline key={sd.key} fill="none" className={sd.cls} strokeWidth={1.5} points={pts.join(" ")} />;
        })}
      </svg>
      <Legend items={series.map((sd) => ({ name: sd.name, cls: sd.cls }))} />
    </figure>
  );
}
