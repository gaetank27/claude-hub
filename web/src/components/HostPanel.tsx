import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, type HostMetrics } from "../lib/api";

// fmtBytes renders a byte count as o/Ko/Mo/Go/To (binary steps).
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

// fmtRate renders a bytes/second rate, switching Ko/s ↔ Mo/s for legibility.
function fmtRate(bps: number): string {
  if (bps < 1024) return `${Math.round(bps)} o/s`;
  if (bps < 1024 * 1024) return `${(bps / 1024).toFixed(0)} Ko/s`;
  return `${(bps / 1024 / 1024).toFixed(2)} Mo/s`;
}

const pct = (used: number, total: number) => (total > 0 ? (used / total) * 100 : 0);

// barClass colours a usage bar: green under 70 %, accent 70–90 %, danger above.
function barClass(p: number): string {
  if (p >= 90) return "bg-danger";
  if (p >= 70) return "bg-accent";
  return "bg-ok";
}

// HostPanel is the real-time host gauges banner at the top of Suivi: CPU, RAM,
// network and disk, polled every 5s. Compact 2-col (mobile) / 4-col (desktop)
// grid. Hidden quietly if the host metrics endpoint is unreachable.
export function HostPanel() {
  const { t } = useTranslation();
  const [m, setM] = useState<HostMetrics | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    const load = () =>
      api
        .getHostMetrics()
        .then((d) => {
          setM(d);
          setFailed(false);
        })
        .catch(() => setFailed(true));
    load();
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  }, []);

  if (!m) return failed ? null : <div className="h-[88px] mb-6" />;

  const ram = pct(m.memUsed, m.memTotal);
  const disk = pct(m.diskUsed, m.diskTotal);

  return (
    <div className="grid grid-cols-2 lg:grid-cols-4 gap-3 mb-6">
      <Gauge label={t("monitoring.cpu")} value={`${Math.round(m.cpu)} %`} pct={m.cpu} />
      <Gauge
        label={t("monitoring.ram")}
        value={`${fmtBytes(m.memUsed)} / ${fmtBytes(m.memTotal)}`}
        pct={ram}
        sub={`${Math.round(ram)} %`}
      />
      <Rate label={t("monitoring.network")} down={fmtRate(m.netRx)} up={fmtRate(m.netTx)} />
      <Gauge
        label={t("monitoring.disk")}
        value={`${fmtBytes(m.diskUsed)} / ${fmtBytes(m.diskTotal)}`}
        pct={disk}
        sub={`I/O ↓ ${fmtRate(m.diskRd)} · ↑ ${fmtRate(m.diskWr)}`}
      />
    </div>
  );
}

// Gauge is a labelled value with a usage bar (for percentage-bounded metrics).
function Gauge({ label, value, pct: p, sub }: { label: string; value: string; pct: number; sub?: string }) {
  const clamped = Math.max(0, Math.min(100, p));
  return (
    <div className="bg-panel border border-border rounded-card p-3">
      <div className="text-2xs uppercase tracking-wider text-muted">{label}</div>
      <div className="text-sm font-medium mt-0.5 mono truncate">{value}</div>
      <div className="h-1.5 rounded-pill bg-border mt-2 overflow-hidden">
        <div className={`h-full rounded-pill ${barClass(clamped)}`} style={{ width: `${clamped}%` }} />
      </div>
      {sub && <div className="text-2xs text-muted/70 mt-1 truncate">{sub}</div>}
    </div>
  );
}

// Rate shows a down/up pair (network) with no bounded bar.
function Rate({ label, down, up }: { label: string; down: string; up: string }) {
  const { t } = useTranslation();
  return (
    <div className="bg-panel border border-border rounded-card p-3">
      <div className="text-2xs uppercase tracking-wider text-muted">{label}</div>
      <div className="text-sm font-medium mt-0.5 mono truncate" title={t("monitoring.download")}>
        ↓ {down}
      </div>
      <div className="text-xs text-muted mono mt-1 truncate" title={t("monitoring.upload")}>
        ↑ {up}
      </div>
    </div>
  );
}
