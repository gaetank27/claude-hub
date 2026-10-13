import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { api, type MonitoringRow, type OllamaStatus, type Project } from "../lib/api";
import { HostPanel } from "../components/HostPanel";
import { HostCharts } from "../components/HostCharts";
import { ClaudeUsage } from "../components/ClaudeUsage";
import { Badge, Button, Card, EmptyState, PageContainer, SectionHeader, StatusDot } from "../components/ui";

// fmtBytes renders a byte count as o/Ko/Mo/Go.
function fmtBytes(n: number): string {
  if (n <= 0) return "—";
  const u = ["o", "Ko", "Mo", "Go"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${u[i]}`;
}

// fmtCPU renders cumulative CPU nanoseconds as a compact duration.
function fmtCPU(ns: number): string {
  if (ns <= 0) return "—";
  const s = ns / 1e9;
  if (s < 60) return `${s.toFixed(1)} s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m} min ${Math.floor(s % 60)} s`;
  const h = Math.floor(m / 60);
  return `${h} h ${String(m % 60).padStart(2, "0")}`;
}

export function Monitoring() {
  const { t } = useTranslation();
  const [rows, setRows] = useState<MonitoringRow[]>([]);
  const [projects, setProjects] = useState<Project[]>([]);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.listProjects().then(setProjects).catch(() => {});
    const load = () =>
      api
        .getMonitoring()
        .then((r) => {
          setRows(r);
          setError(null);
        })
        .catch((e) => setError(e.message));
    load();
    const t = setInterval(load, 4000);
    return () => clearInterval(t);
  }, []);

  const projectName = (id: string) =>
    projects.find((p) => p.id === id)?.name ?? id.slice(0, 8);

  return (
    <PageContainer>
      <h1 className="text-2xl font-semibold mb-1">{t("monitoring.title")}</h1>
      <p className="text-muted text-sm mb-6">{t("monitoring.subtitle")}</p>

      <HostPanel />

      <HostCharts />

      <ClaudeUsage />

      <OllamaPanel />

      <SectionHeader
        title={t("monitoring.sessionsTitle")}
        hint={t("monitoring.sessionsHint")}
      />

      {error && <div className="text-danger text-sm mb-4">{error}</div>}

      {rows.length === 0 ? (
        <Card>
          <EmptyState title={t("monitoring.noSessions")} />
        </Card>
      ) : (
        <>
          {/* Desktop: dense table. Hidden below md where it would overflow. */}
          <div className="hidden md:block border border-border rounded-card overflow-hidden">
            <table className="w-full text-sm">
              <thead className="bg-panel text-muted text-xs uppercase tracking-wider">
                <tr>
                  <th className="text-left font-medium px-4 py-2">{t("monitoring.colProject")}</th>
                  <th className="text-left font-medium px-4 py-2">{t("monitoring.colSession")}</th>
                  <th className="text-right font-medium px-4 py-2">{t("monitoring.colMemory")}</th>
                  <th className="text-right font-medium px-4 py-2">{t("monitoring.colCpu")}</th>
                  <th className="text-right font-medium px-4 py-2">{t("monitoring.colJournal")}</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => (
                  <tr key={r.id} className="border-t border-border hover:bg-panel-2">
                    <td className="px-4 py-2">
                      <Link
                        to={`/projects/${r.projectId}?session=${r.id}`}
                        className="text-accent hover:underline"
                      >
                        {projectName(r.projectId)}
                      </Link>
                    </td>
                    <td className="px-4 py-2 mono text-muted">{r.id.slice(0, 8)}</td>
                    <td className="px-4 py-2 text-right mono">
                      {r.resources.hasMetrics ? fmtBytes(r.resources.memoryBytes) : "—"}
                    </td>
                    <td className="px-4 py-2 text-right mono">
                      {r.resources.hasMetrics ? fmtCPU(r.resources.cpuNSec) : "—"}
                    </td>
                    <td className="px-4 py-2 text-right mono">{fmtBytes(r.resources.journalBytes)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          {/* Mobile: stacked cards, one per session, with labelled key/value rows
              so nothing overflows the narrow viewport. */}
          <div className="md:hidden space-y-2">
            {rows.map((r) => (
              <Card key={r.id} padding="sm">
                <div className="flex items-center justify-between gap-2">
                  <Link
                    to={`/projects/${r.projectId}?session=${r.id}`}
                    className="text-accent hover:underline font-medium truncate"
                  >
                    {projectName(r.projectId)}
                  </Link>
                  <span className="mono text-2xs text-muted shrink-0">{r.id.slice(0, 8)}</span>
                </div>
                <dl className="mt-2 grid grid-cols-3 gap-2 text-xs">
                  <Metric label={t("monitoring.colMemory")} value={r.resources.hasMetrics ? fmtBytes(r.resources.memoryBytes) : "—"} />
                  <Metric label={t("monitoring.colCpu")} value={r.resources.hasMetrics ? fmtCPU(r.resources.cpuNSec) : "—"} />
                  <Metric label={t("monitoring.colJournal")} value={fmtBytes(r.resources.journalBytes)} />
                </dl>
              </Card>
            ))}
          </div>
        </>
      )}
    </PageContainer>
  );
}

// Metric is one labelled key/value cell in the mobile session card.
function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-2xs uppercase tracking-wider text-muted">{label}</dt>
      <dd className="mono mt-0.5">{value}</dd>
    </div>
  );
}

// OllamaPanel supervises the local Ollama instance: live status, installed models
// and the models currently loaded in RAM (with a one-click unload to free memory).
function OllamaPanel() {
  const { t } = useTranslation();
  const [st, setSt] = useState<OllamaStatus | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  async function load() {
    try {
      setSt(await api.getOllama());
    } catch {
      /* keep last state */
    }
  }
  useEffect(() => {
    load();
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  }, []);

  if (!st || st.status === "disabled") return null;

  const tone = st.status === "active" ? "ok" : st.status === "unreachable" ? "danger" : "warn";
  const label =
    st.status === "active"
      ? t("monitoring.ollamaActive")
      : st.status === "unreachable"
        ? t("monitoring.ollamaUnreachable")
        : t("monitoring.ollamaError");

  async function unload(model: string) {
    setBusy(model);
    try {
      await api.unloadOllamaModel(model);
      await load();
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="mb-8">
      <div className="flex items-center gap-2 mb-2">
        <h2 className="text-xs font-semibold uppercase tracking-wider text-muted">Ollama</h2>
        <Badge tone={tone}>{label}</Badge>
        {st.version && <span className="mono text-2xs text-muted">v{st.version}</span>}
        <span className="mono text-2xs text-muted/60 truncate">{st.host}</span>
      </div>

      {st.status !== "active" ? (
        <div className="text-xs text-danger border border-danger/30 rounded-card px-3 py-2">
          {st.error || t("monitoring.ollamaUnreachableMsg")}
        </div>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
          {/* Loaded in memory */}
          <Card padding="sm">
            <div className="text-xs text-muted mb-2">
              {t("monitoring.loadedInMemory", { count: st.running?.length ?? 0 })}
            </div>
            {(st.running?.length ?? 0) === 0 ? (
              <div className="text-xs text-muted/60">{t("monitoring.noModelLoaded")}</div>
            ) : (
              <div className="space-y-1.5">
                {st.running!.map((r) => (
                  <div key={r.name} className="flex items-center gap-2 text-xs">
                    <span className="font-medium truncate">{r.name}</span>
                    <span className="mono text-muted">{fmtBytes(r.size)}</span>
                    {r.sizeVram > 0 && (
                      <span className="mono text-accent" title={t("monitoring.vramGpu")}>
                        {fmtBytes(r.sizeVram)} GPU
                      </span>
                    )}
                    <Button
                      variant="ghost"
                      size="sm"
                      loading={busy === r.name}
                      onClick={() => unload(r.name)}
                      className="ml-auto !px-2 !py-0.5 hover:text-danger"
                    >
                      {t("monitoring.unload")}
                    </Button>
                  </div>
                ))}
              </div>
            )}
          </Card>

          {/* Installed */}
          <Card padding="sm">
            <div className="text-xs text-muted mb-2">
              {t("monitoring.installedModels", { count: st.models?.length ?? 0 })}
            </div>
            {(st.models?.length ?? 0) === 0 ? (
              <div className="text-xs text-muted/60">{t("monitoring.noModel")}</div>
            ) : (
              <div className="space-y-1.5">
                {st.models!.map((m) => {
                  const loaded = st.running?.some((r) => r.name === m.name);
                  return (
                    <div key={m.name} className="flex items-center gap-2 text-xs">
                      {loaded && <StatusDot tone="ok" title={t("monitoring.loaded")} />}
                      <span className="truncate">{m.name}</span>
                      {m.params && <span className="text-muted/60">{m.params}</span>}
                      <span className="mono text-muted ml-auto">{fmtBytes(m.size)}</span>
                    </div>
                  );
                })}
              </div>
            )}
          </Card>
        </div>
      )}
    </div>
  );
}
