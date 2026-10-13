import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, type HostHistory, type HostRange } from "../lib/api";

const RANGES: { id: HostRange; labelKey: string }[] = [
  { id: "hour", labelKey: "monitoring.rangeHour" },
  { id: "day", labelKey: "monitoring.rangeDay" },
  { id: "week", labelKey: "monitoring.rangeWeek" },
  { id: "month", labelKey: "monitoring.rangeMonth" },
];

// --- formatters ---
function fmtBytes(n: number): string {
  if (n <= 0) return "0";
  const u = ["o", "Ko", "Mo", "Go", "To"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i >= 3 ? 1 : 0)} ${u[i]}`;
}
function fmtRate(bps: number): string {
  if (bps < 1024) return `${Math.round(bps)} o/s`;
  if (bps < 1024 * 1024) return `${(bps / 1024).toFixed(0)} Ko/s`;
  return `${(bps / 1024 / 1024).toFixed(1)} Mo/s`;
}
function niceMax(v: number): number {
  if (v <= 0) return 1;
  const pow = Math.pow(10, Math.floor(Math.log10(v)));
  const n = v / pow;
  const step = n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10;
  return step * pow;
}
function fmtTime(sec: number, range: HostRange): string {
  const d = new Date(sec * 1000);
  if (range === "hour" || range === "day")
    return d.toLocaleTimeString("fr-FR", { hour: "2-digit", minute: "2-digit" });
  return d.toLocaleDateString("fr-FR", { day: "2-digit", month: "2-digit" });
}

// --- chart geometry (mirrors HistoryCharts: viewBox tracks real px width) ---
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

// LineChart plots one or more time-positioned series on a shared left scale,
// breaking each line across UNKNOWN (null) buckets so gaps stay visible.
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
  range: HostRange;
}) {
  const [ref, measured] = useWidth();
  const W = measured || FALLBACK_W;
  const IW = W - PAD.l - PAD.r;
  const span = Math.max(1, to - from);
  const x = (t: number) => PAD.l + (IW * (t - from)) / span;
  const y = (v: number) => PAD.t + (H - PAD.t - PAD.b) * (1 - v / max);

  // Split a series into polyline segments at null gaps.
  const segments = (pts: Series["points"]): string[] => {
    const segs: string[] = [];
    let cur: string[] = [];
    for (const p of pts) {
      if (p.v == null) {
        if (cur.length > 1) segs.push(cur.join(" "));
        cur = [];
      } else {
        cur.push(`${x(p.t).toFixed(1)},${y(p.v).toFixed(1)}`);
      }
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
        {/* x labels: a few evenly-spaced ticks across the window */}
        {[0, 0.25, 0.5, 0.75, 1].map((g) => (
          <text key={g} x={PAD.l + IW * g} y={H - 6} textAnchor="middle" className="fill-muted" fontSize={FONT}>
            {fmtTime(from + span * g, range)}
          </text>
        ))}
      </svg>
    </figure>
  );
}

function lastDefined(pts?: { t: number; v: number | null }[]): number {
  if (!pts) return 0;
  for (let i = pts.length - 1; i >= 0; i--) if (pts[i].v != null) return pts[i].v as number;
  return 0;
}
function seriesMax(pts?: { t: number; v: number | null }[]): number {
  if (!pts) return 0;
  return pts.reduce((m, p) => (p.v != null && p.v > m ? p.v : m), 0);
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

// HostCharts is the historical host-metrics block: trend charts (CPU, RAM,
// network, disk I/O, disk space) over a selectable range, fed by the RRD export.
export function HostCharts() {
  const { t } = useTranslation();
  const [range, setRange] = useState<HostRange>("hour");
  const [data, setData] = useState<HostHistory | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    api
      .getHostHistory(range)
      .then((d) => {
        if (cancelled) return;
        setData(d);
        setError(null);
      })
      .catch((e) => !cancelled && setError(e.message));
    return () => {
      cancelled = true;
    };
  }, [range]);

  const s = data?.series;
  const from = data?.from ?? 0;
  const to = data?.to ?? 0;

  return (
    <section className="space-y-4 mb-8">
      <div className="flex items-center gap-3">
        <h2 className="text-xs font-semibold uppercase tracking-wider text-muted">{t("monitoring.historyTitle")}</h2>
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

      {error && <div className="text-danger text-sm">{error}</div>}

      {data && !data.available && (
        <div className="text-xs text-muted/70 py-8 text-center bg-panel border border-border rounded-lg">
          {t("monitoring.historyUnavailable")}
        </div>
      )}

      {data && data.available && s && (
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
          <Panel title={t("monitoring.chartCpu")} hint={t("monitoring.chartCpuHint")}>
            <LineChart
              range={range}
              from={from}
              to={to}
              max={100}
              fmtY={(n) => `${Math.round(n)}%`}
              series={[{ name: t("monitoring.chartCpu"), cls: "stroke-accent", points: s.cpu ?? [] }]}
            />
          </Panel>

          <Panel title={t("monitoring.chartMemory")} hint={t("monitoring.chartMemoryHint")}>
            <LineChart
              range={range}
              from={from}
              to={to}
              max={Math.max(1, lastDefined(s.mem_total) || seriesMax(s.mem_total))}
              fmtY={fmtBytes}
              series={[{ name: t("monitoring.chartMemUsed"), cls: "stroke-accent", points: s.mem_used ?? [] }]}
            />
          </Panel>

          <Panel title={t("monitoring.chartNetwork")} hint={t("monitoring.chartNetworkHint")}>
            <LineChart
              range={range}
              from={from}
              to={to}
              max={niceMax(Math.max(1, seriesMax(s.net_rx), seriesMax(s.net_tx)))}
              fmtY={fmtRate}
              series={[
                { name: t("monitoring.chartNetRx"), cls: "stroke-ok", points: s.net_rx ?? [] },
                { name: t("monitoring.chartNetTx"), cls: "stroke-accent", points: s.net_tx ?? [] },
              ]}
            />
            <Legend items={[{ name: t("monitoring.chartNetRx"), cls: "stroke-ok" }, { name: t("monitoring.chartNetTx"), cls: "stroke-accent" }]} />
          </Panel>

          <Panel title={t("monitoring.chartDiskIo")} hint={t("monitoring.chartDiskIoHint")}>
            <LineChart
              range={range}
              from={from}
              to={to}
              max={niceMax(Math.max(1, seriesMax(s.disk_rd), seriesMax(s.disk_wr)))}
              fmtY={fmtRate}
              series={[
                { name: t("monitoring.chartRead"), cls: "stroke-ok", points: s.disk_rd ?? [] },
                { name: t("monitoring.chartWrite"), cls: "stroke-danger", points: s.disk_wr ?? [] },
              ]}
            />
            <Legend items={[{ name: t("monitoring.chartRead"), cls: "stroke-ok" }, { name: t("monitoring.chartWrite"), cls: "stroke-danger" }]} />
          </Panel>

          <Panel title={t("monitoring.chartDiskSpace")} hint={t("monitoring.chartDiskSpaceHint")}>
            <LineChart
              range={range}
              from={from}
              to={to}
              max={Math.max(1, lastDefined(s.disk_total) || seriesMax(s.disk_total))}
              fmtY={fmtBytes}
              series={[{ name: t("monitoring.chartUsed"), cls: "stroke-accent", points: s.disk_used ?? [] }]}
            />
          </Panel>
        </div>
      )}
    </section>
  );
}
