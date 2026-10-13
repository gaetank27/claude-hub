import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { Link } from "react-router-dom";
import { api, type Project, type ProjectRag } from "../lib/api";
import { ContextStatusIndicator } from "./ContextStatusIndicator";

// ProjectStats are the per-project aggregates shown on the card, derived by the
// dashboard from the cross-project session list.
export interface ProjectStats {
  total: number;
  active: number;
  unread: number; // sessions with a reply the user has not read yet
  cost: number;
  computeMs: number; // cumulative Claude compute time across the project's sessions
  tokens: number; // cumulative input+output tokens across the project's sessions
  skills: Record<string, number>; // skill name → invocations, merged across sessions
  skillUses: number; // total skill invocations across the project's sessions
  rags: Record<string, number>; // RAG ref → queries, merged across sessions
  ragUses: number; // total RAG queries across the project's sessions
}

// ragLabel maps a RAG ref (collection slug, or a sentinel) to a display label.
function ragLabel(ref: string, t: TFunction): string {
  if (ref === "*all*") return t("dashboard.ragAllBases");
  if (ref === "*project*") return t("dashboard.ragProjectCode");
  return ref;
}

// fmtDuration renders a millisecond span compactly: "3 h 12", "12 min", "45 s".
function fmtDuration(ms: number): string {
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s} s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60);
  return `${h} h ${String(m % 60).padStart(2, "0")}`;
}

// fmtTokens renders a token count compactly: "1.2M", "261k", or the raw number.
function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1000) return `${Math.round(n / 1000)}k`;
  return String(n);
}

export function ProjectCard({
  project,
  stats,
  onChanged,
}: {
  project: Project;
  stats: ProjectStats;
  onChanged?: () => void;
}) {
  const { t } = useTranslation();
  // Discreet per-project RAG indicator. Fetched once per card; refreshed while a
  // reindex is in flight so the status settles without a manual reload.
  const [rag, setRag] = useState<ProjectRag | null>(null);
  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout>;
    const load = () =>
      api
        .getProjectRag(project.id)
        .then((r) => {
          if (cancelled) return;
          setRag(r);
          if (r.status === "indexing" || r.pending > 0) timer = setTimeout(load, 5000);
        })
        .catch(() => {});
    load();
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [project.id]);

  async function archive(e: React.MouseEvent) {
    // The card is a Link; suppress navigation when archiving.
    e.preventDefault();
    e.stopPropagation();
    const warn = stats.active > 0 ? t("dashboard.archiveActiveWarning", { count: stats.active }) : "";
    if (
      !window.confirm(
        t("dashboard.archiveConfirm", {
          name: project.name,
          warn,
          interpolation: { escapeValue: false },
        }),
      )
    )
      return;
    try {
      await api.archiveProject(project.id);
      onChanged?.();
    } catch (err: any) {
      window.alert(err.message);
    }
  }

  async function toggleStar(e: React.MouseEvent) {
    e.preventDefault();
    e.stopPropagation();
    try {
      await api.updateProject(project.id, { starred: !project.starred });
      onChanged?.();
    } catch (err: any) {
      window.alert(err.message);
    }
  }

  return (
    <Link
      to={`/projects/${project.id}`}
      className={`block rounded-card p-4 transition-colors border ${
        stats.unread > 0
          ? "bg-accent/10 border-accent hover:border-accent"
          : "bg-panel border-border hover:border-accent-dim"
      }`}
    >
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-1.5 min-w-0">
          <button
            onClick={toggleStar}
            aria-label={project.starred ? t("dashboard.removeFromFavorites") : t("dashboard.addToFavorites")}
            title={project.starred ? t("dashboard.removeFromFavorites") : t("dashboard.addToFavorites")}
            className={`shrink-0 text-sm leading-none ${
              project.starred ? "text-yellow-400" : "text-muted/40 hover:text-muted"
            }`}
          >
            {project.starred ? "★" : "☆"}
          </button>
          <span className={`truncate ${stats.unread > 0 ? "font-bold" : "font-medium"}`}>{project.name}</span>
          {stats.unread > 0 && (
            <span
              className="shrink-0 flex items-center gap-1 text-3xs font-semibold text-accent border border-accent rounded-control px-1.5 py-0.5"
              title={t("dashboard.unreadTitle")}
            >
              <span className="inline-block w-1.5 h-1.5 rounded-full bg-accent animate-pulse" />
              {t("dashboard.unreadCount", { count: stats.unread })}
            </span>
          )}
        </div>
        {project.protected ? (
          <span className="mono text-3xs uppercase tracking-wider text-accent border border-accent-dim/50 rounded-control px-1.5 py-0.5 shrink-0">
            {t("dashboard.protected")}
          </span>
        ) : (
          <button
            onClick={archive}
            className="shrink-0 text-muted text-2xs hover:text-fg transition-colors"
            title={t("dashboard.archiveTitle")}
          >
            {t("dashboard.archive")}
          </button>
        )}
      </div>

      <div className="mono text-xs text-muted mt-2 truncate">{project.dir}</div>

      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 mt-3 text-2xs text-muted">
        <span title={t("dashboard.sessionsCountTitle")}>{t("dashboard.sessionsCount", { count: stats.total })}</span>
        {stats.active > 0 && (
          <span className="text-ok" title={t("dashboard.activeCountTitle")}>
            {t("dashboard.activeCount", { count: stats.active })}
          </span>
        )}
        {stats.computeMs > 0 && (
          <span title={t("dashboard.computeTitle")}>⏱ {fmtDuration(stats.computeMs)}</span>
        )}
        {stats.tokens > 0 && (
          <span className="mono" title={t("dashboard.tokensTitle")}>
            {t("dashboard.tokens", { tokens: fmtTokens(stats.tokens) })}
          </span>
        )}
        {stats.cost > 0 && (
          <span className="mono" title={t("dashboard.costTitle")}>~${stats.cost.toFixed(2)}</span>
        )}
        {stats.skillUses > 0 && (
          <span title={t("dashboard.skillUsesTitle")}>
            {t("dashboard.skillUses", { count: stats.skillUses })}
          </span>
        )}
        {stats.ragUses > 0 && (
          <span title={t("dashboard.ragUsesTitle")}>
            {t("dashboard.ragUses", { count: stats.ragUses })}
          </span>
        )}
        {project.category && (
          <span className="ml-auto text-3xs uppercase tracking-wider text-muted border border-border rounded-control px-1.5 py-0.5">
            {project.category}
          </span>
        )}
      </div>

      {stats.skillUses > 0 && <SkillsDetail skills={stats.skills} />}

      {stats.ragUses > 0 && <RagsDetail rags={stats.rags} />}

      {project.comment && (
        <div
          className="mt-2 pt-2 border-t border-border/60 text-2xs text-muted italic whitespace-pre-wrap break-words line-clamp-3"
          title={project.comment}
        >
          {project.comment}
        </div>
      )}

      {rag && <RagIndicator rag={rag} />}
      <ContextStatusIndicator resources={project.contextResources} />
    </Link>
  );
}

// SkillsDetail lists the distinct skills used by a project's sessions, most-used
// first, each with its invocation count (count omitted when it's 1).
function SkillsDetail({ skills }: { skills: Record<string, number> }) {
  const { t } = useTranslation();
  const entries = Object.entries(skills).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
  return (
    <div className="flex flex-wrap items-center gap-1 mt-2 pt-2 border-t border-border/60">
      {entries.map(([name, count]) => (
        <span
          key={name}
          className="mono text-3xs text-muted border border-border rounded-control px-1.5 py-0.5"
          title={t("dashboard.skillUsedTitle", { name, count })}
        >
          {name}
          {count > 1 && <span className="text-accent"> ×{count}</span>}
        </span>
      ))}
    </div>
  );
}

// RagsDetail lists the distinct RAG knowledge bases the project's sessions
// queried (via knowledge_search), most-queried first, each with its query count
// (omitted when 1). Sentinels are rendered with friendly labels (ragLabel).
function RagsDetail({ rags }: { rags: Record<string, number> }) {
  const { t } = useTranslation();
  const entries = Object.entries(rags).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
  return (
    <div className="flex flex-wrap items-center gap-1 mt-2 pt-2 border-t border-border/60">
      <span className="text-3xs text-muted mr-0.5" title={t("dashboard.ragBasesTitle")}>
        📚
      </span>
      {entries.map(([ref, count]) => (
        <span
          key={ref}
          className="text-3xs text-muted border border-border rounded-control px-1.5 py-0.5"
          title={t("dashboard.ragQueriedTitle", { name: ragLabel(ref, t), count })}
        >
          {ragLabel(ref, t)}
          {count > 1 && <span className="text-accent"> ×{count}</span>}
        </span>
      ))}
    </div>
  );
}

// RagIndicator is the discreet per-project RAG widget on the tile: a status dot,
// the indexed file count and the freshness ("à jour" / "il y a …").
function RagIndicator({ rag }: { rag: ProjectRag }) {
  const { t } = useTranslation();
  const dot =
    rag.status === "error"
      ? "bg-danger"
      : rag.status === "indexing" || rag.pending > 0
        ? "bg-accent animate-pulse"
        : rag.fileCount > 0
          ? "bg-ok"
          : "bg-muted/50";
  const label =
    rag.status === "error"
      ? t("dashboard.ragStatusError")
      : rag.status === "indexing" || rag.pending > 0
        ? t("dashboard.ragStatusIndexing", { count: rag.pending })
        : rag.fileCount > 0
          ? t("dashboard.ragStatusReady", { count: rag.fileCount, ago: fmtAgo(rag.lastIndexedAt, t) })
          : t("dashboard.ragStatusPending");
  return (
    <div
      className="flex items-center gap-1.5 mt-2 pt-2 border-t border-border/60 text-3xs text-muted"
      title={rag.error || t("dashboard.ragWidgetTitle")}
    >
      <span className={`inline-block w-1.5 h-1.5 rounded-pill ${dot}`} />
      <span className="truncate">{label}</span>
    </div>
  );
}

// fmtAgo renders a short relative time ("up to date", "12 min ago", "3 h ago").
function fmtAgo(iso: string | undefined, t: TFunction): string {
  if (!iso) return t("dashboard.agoNever");
  const ms = Date.now() - new Date(iso).getTime();
  if (Number.isNaN(ms)) return "";
  if (ms < 60_000) return t("dashboard.agoUpToDate");
  const m = Math.floor(ms / 60_000);
  if (m < 60) return t("dashboard.agoMinutes", { count: m });
  const h = Math.floor(m / 60);
  if (h < 24) return t("dashboard.agoHours", { count: h });
  return t("dashboard.agoDays", { count: Math.floor(h / 24) });
}
