import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { fmtRelative } from "../lib/relativeTime";
import type { TFunction } from "i18next";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";
import { api, CLAUDE_ACCOUNT_CHANGED, type ClaudeAccount, type Project, type ProjectAccess, type ProjectRag, type Session, type Tool } from "../lib/api";
import { SessionStream } from "../components/SessionStream";
import { ContextStatusIndicator } from "../components/ContextStatusIndicator";
import { Button, StatusDot, type DotTone } from "../components/ui";

// fmtBytes renders a size compactly: "1.2 MB", "340 kB", "12 B".
function fmtBytes(n: number): string {
  if (n >= 1_048_576) return `${(n / 1_048_576).toFixed(1)} MB`;
  if (n >= 1024) return `${Math.round(n / 1024)} kB`;
  return `${n} B`;
}

// ProjectRagPanel manages a project's private RAG index: status + vector-store
// stats, an immediate reindex, and the scheduling config (auto-on-commit +
// optional interval). Collapsible to keep the sidebar compact.
function ProjectRagPanel({ projectId }: { projectId: string }) {
  const { t } = useTranslation();
  const [rag, setRag] = useState<ProjectRag | null>(null);
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);

  async function load() {
    try {
      setRag(await api.getProjectRag(projectId));
    } catch {
      /* ignore — the panel just stays hidden */
    }
  }
  useEffect(() => {
    load();
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  }, [projectId]);

  if (!rag) return null;

  const dotTone: DotTone =
    rag.status === "error"
      ? "danger"
      : rag.status === "indexing" || rag.pending > 0
        ? "accent"
        : rag.fileCount > 0
          ? "ok"
          : "muted";
  const dotPulse = rag.status === "indexing" || rag.pending > 0;

  async function reindex() {
    setBusy(true);
    try {
      await api.reindexProjectRag(projectId);
      await load();
    } finally {
      setBusy(false);
    }
  }
  async function patch(p: Partial<{ enabled: boolean; autoOnChange: boolean; intervalMinutes: number }>) {
    setRag(await api.updateProjectRag(projectId, p));
  }

  return (
    <div className="border border-border rounded-card mb-4 text-xs">
      <button
        onClick={() => setOpen((o) => !o)}
        className="w-full flex items-center gap-2 px-3 py-2"
        title={rag.error || t("project.ragTooltip")}
      >
        <StatusDot tone={dotTone} pulse={dotPulse} />
        <span className="font-medium text-fg">{t("project.ragTitle")}</span>
        <span className="text-muted truncate">
          {rag.status === "indexing" || rag.pending > 0
            ? t("project.ragIndexing", { count: rag.pending })
            : rag.fileCount > 0
              ? t("project.ragFilesChunks", { files: rag.fileCount, chunks: rag.chunkCount })
              : t("project.ragWaiting")}
        </span>
        <span className="ml-auto text-muted">{open ? "▾" : "▸"}</span>
      </button>

      {open && (
        <div className="px-3 pb-3 pt-1 space-y-2 border-t border-border/60">
          <div className="text-muted">
            {t("project.ragFilesChunks", { files: rag.fileCount, chunks: rag.chunkCount })} · {fmtBytes(rag.bytes)}
            <br />
            {rag.lastIndexedAt
              ? t("project.ragLastIndexed", { date: fmtDateTime(rag.lastIndexedAt) })
              : t("project.ragNeverIndexed")}
          </div>
          {rag.status === "error" && rag.error && <div className="text-danger">{rag.error}</div>}

          <Button
            size="sm"
            fullWidth
            onClick={reindex}
            disabled={busy || rag.status === "indexing"}
            loading={busy}
          >
            {rag.status === "indexing" ? t("project.ragReindexing") : t("project.ragReindexNow")}
          </Button>

          <label className="flex items-center gap-2">
            <input
              type="checkbox"
              checked={rag.autoOnChange}
              onChange={(e) => patch({ autoOnChange: e.target.checked })}
            />
            <span>{t("project.ragAutoOnCommit")}</span>
          </label>
          <label className="flex items-center gap-2">
            <span className="text-muted">{t("project.ragEvery")}</span>
            <input
              type="number"
              min={0}
              value={rag.intervalMinutes}
              onChange={(e) => patch({ intervalMinutes: Math.max(0, Number(e.target.value) || 0) })}
              className="w-16 bg-bg border border-border rounded-control px-2 py-1 outline-none focus:border-accent-dim"
            />
            <span className="text-muted">{t("project.ragMinutesOff")}</span>
          </label>
          <label className="flex items-center gap-2">
            <input
              type="checkbox"
              checked={rag.enabled}
              onChange={(e) => patch({ enabled: e.target.checked })}
            />
            <span>{t("project.ragIndexEnabled")}</span>
          </label>
        </div>
      )}
    </div>
  );
}

// fmtTokens renders a token count compactly: "1.2M", "261k", or the raw number.
function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1000) return `${Math.round(n / 1000)}k`;
  return String(n);
}

// fmtDateTime renders an ISO timestamp as "29/05 19:01" in local time.
function fmtDateTime(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleString("fr-FR", {
    day: "2-digit",
    month: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

// CategoryEditor shows a project's category and lets the user change it inline.
// Existing categories (across all projects) are offered as a datalist so the
// grouping stays consistent. Saving persists via PATCH /api/projects/{id}.
function CategoryEditor({ project, onChanged }: { project: Project; onChanged: () => void }) {
  const { t } = useTranslation();
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState(project.category);
  const [busy, setBusy] = useState(false);
  const [suggestions, setSuggestions] = useState<string[]>([]);

  useEffect(() => {
    // Suggestions are best-effort; a failure just leaves the datalist empty.
    api
      .listProjects()
      .then((ps) => {
        const cats = Array.from(new Set(ps.map((p) => p.category).filter(Boolean)));
        setSuggestions(cats.sort((a, b) => a.localeCompare(b)));
      })
      .catch(() => {});
  }, []);

  function begin() {
    setValue(project.category);
    setEditing(true);
  }

  async function save() {
    setBusy(true);
    try {
      await api.updateProject(project.id, { category: value.trim() });
      setEditing(false);
      onChanged();
    } finally {
      setBusy(false);
    }
  }

  if (!editing) {
    return (
      <button
        onClick={begin}
        className="group flex items-center gap-1.5 text-2xs text-muted hover:text-fg transition-colors mb-3"
        title={t("project.categoryChangeTooltip")}
      >
        <span className="uppercase tracking-wider">{t("project.categoryLabel")}</span>
        {project.category ? (
          <span className="text-fg border border-border rounded-control px-1.5 py-0.5">{project.category}</span>
        ) : (
          <span className="italic">{t("project.categoryNone")}</span>
        )}
        <span className="opacity-0 group-hover:opacity-100 transition-opacity">✎</span>
      </button>
    );
  }

  return (
    <div className="flex items-center gap-1.5 mb-3">
      <input
        list="project-categories"
        value={value}
        autoFocus
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter") save();
          if (e.key === "Escape") setEditing(false);
        }}
        placeholder={t("project.categoryPlaceholder")}
        className="bg-bg border border-border rounded px-2 py-1 text-xs w-40 focus:border-accent-dim outline-none"
      />
      <datalist id="project-categories">
        {suggestions.map((c) => (
          <option key={c} value={c} />
        ))}
      </datalist>
      <button
        onClick={save}
        disabled={busy}
        className="text-2xs bg-accent text-bg rounded px-2 py-1 disabled:opacity-40 hover:bg-accent-dim transition-colors"
      >
        OK
      </button>
      <button
        onClick={() => setEditing(false)}
        className="text-2xs text-muted hover:text-fg transition-colors px-1"
      >
        {t("common.cancel")}
      </button>
    </div>
  );
}

// CommentEditor shows a project's free-text comment and lets the user edit it
// inline (multi-line). Saving persists via PATCH /api/projects/{id} (partial, so
// it never touches the star/category).
function CommentEditor({ project, onChanged }: { project: Project; onChanged: () => void }) {
  const { t } = useTranslation();
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState(project.comment);
  const [busy, setBusy] = useState(false);

  function begin() {
    setValue(project.comment);
    setEditing(true);
  }

  async function save() {
    setBusy(true);
    try {
      await api.updateProject(project.id, { comment: value.trim() });
      setEditing(false);
      onChanged();
    } finally {
      setBusy(false);
    }
  }

  if (!editing) {
    return (
      <button
        onClick={begin}
        className="group flex items-start gap-1.5 text-2xs text-muted hover:text-fg transition-colors mb-3 text-left w-full"
        title={t("project.commentEditTooltip")}
      >
        <span className="uppercase tracking-wider shrink-0">{t("project.commentLabel")}</span>
        {project.comment ? (
          <span className="text-fg whitespace-pre-wrap break-words flex-1">{project.comment}</span>
        ) : (
          <span className="italic flex-1">{t("project.commentNone")}</span>
        )}
        <span className="opacity-0 group-hover:opacity-100 transition-opacity shrink-0">✎</span>
      </button>
    );
  }

  return (
    <div className="flex flex-col gap-1.5 mb-3">
      <textarea
        value={value}
        autoFocus
        rows={3}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={(e) => {
          // Cmd/Ctrl+Enter saves; Escape cancels (plain Enter inserts a newline).
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) save();
          if (e.key === "Escape") setEditing(false);
        }}
        placeholder={t("project.commentPlaceholder")}
        className="bg-bg border border-border rounded px-2 py-1 text-xs w-full resize-y focus:border-accent-dim outline-none"
      />
      <div className="flex items-center gap-1.5">
        <button
          onClick={save}
          disabled={busy}
          className="text-2xs bg-accent text-bg rounded px-2 py-1 disabled:opacity-40 hover:bg-accent-dim transition-colors"
        >
          OK
        </button>
        <button
          onClick={() => setEditing(false)}
          className="text-2xs text-muted hover:text-fg transition-colors px-1"
        >
          {t("common.cancel")}
        </button>
      </div>
    </div>
  );
}

// Claude model aliases offered in the per-project and per-session pickers. Aliases
// (not pinned ids) so each tier tracks its latest version automatically. Labels are
// reused from the Configuration domain (flat i18n namespace).
const MODELS = ["opus", "sonnet", "haiku"];

// ModelEditor lets the user pick which Claude model this project's new sessions
// run on. Empty value = inherit the global runtime-settings model. Changing it
// re-stamps only NEW sessions; in-flight ones keep the model they launched on.
// This is the "hybrid" lever: default the project to Sonnet, bump a heavy project
// to Opus, and still override per session at launch.
function ModelEditor({ project, onChanged }: { project: Project; onChanged: () => void }) {
  const { t } = useTranslation();
  const [busy, setBusy] = useState(false);

  const change = async (model: string) => {
    setBusy(true);
    try {
      await api.updateProject(project.id, { model });
      onChanged();
    } finally {
      setBusy(false);
    }
  };

  const current = project.model;
  const custom = current && !MODELS.includes(current) ? current : undefined;

  return (
    <div className="mb-3">
      <div className="text-2xs uppercase tracking-wide text-muted mb-1">{t("project.modelLabel")}</div>
      <select
        value={current}
        onChange={(e) => change(e.target.value)}
        disabled={busy}
        className="w-full text-xs bg-panel-2 border border-border rounded px-2 py-1 disabled:opacity-40 focus:border-accent-dim outline-none"
      >
        <option value="">{t("project.modelDefault")}</option>
        {custom && <option value={custom}>{custom}</option>}
        {MODELS.map((m) => (
          <option key={m} value={m}>{t(`configuration.model_${m}`)}</option>
        ))}
      </select>
    </div>
  );
}

// AccessEditor manages what this project's sessions may reach: which configured
// tools they can call, and which other projects' code index they can search.
function AccessEditor({ project, projects }: { project: Project; projects: Project[] }) {
  const { t } = useTranslation();
  const [acc, setAcc] = useState<ProjectAccess | null>(null);
  const [tools, setTools] = useState<Tool[]>([]);
  const [open, setOpen] = useState(false);

  useEffect(() => {
    api.getProjectAccess(project.id).then(setAcc).catch(() => {});
    api.listTools().then(setTools).catch(() => {});
  }, [project.id]);

  if (!acc) return null;
  const save = (next: ProjectAccess) => {
    setAcc(next);
    api.setProjectAccess(project.id, next).then(setAcc).catch(() => {});
  };
  const toggle = (list: string[], id: string) =>
    list.includes(id) ? list.filter((x) => x !== id) : [...list, id];
  const others = projects.filter((p) => p.id !== project.id);

  return (
    <div className="mb-3">
      <button
        onClick={() => setOpen(!open)}
        className="text-2xs uppercase tracking-wide text-muted mb-1 hover:text-fg"
      >
        {open ? "▾" : "▸"} {t("project.accessTitle")}
      </button>
      {open && (
        <div className="space-y-2 text-xs">
          <div>
            <div className="text-muted mb-1">{t("project.accessTools")}</div>
            <select
              value={acc.toolsMode}
              onChange={(e) => save({ ...acc, toolsMode: e.target.value as ProjectAccess["toolsMode"] })}
              className="w-full bg-panel-2 border border-border rounded px-2 py-1 outline-none focus:border-accent-dim"
            >
              <option value="all">{t("project.accessToolsAll")}</option>
              <option value="list">{t("project.accessToolsList")}</option>
            </select>
            {acc.toolsMode === "list" && (
              <div className="mt-1 space-y-0.5">
                {tools.length === 0 && <div className="text-muted">{t("project.accessNoTools")}</div>}
                {tools.map((tl) => (
                  <label key={tl.id} className="flex items-center gap-1.5 cursor-pointer">
                    <input
                      type="checkbox"
                      checked={acc.allowedTools.includes(tl.id)}
                      onChange={() => save({ ...acc, allowedTools: toggle(acc.allowedTools, tl.id) })}
                    />
                    <span className={tl.enabled ? "" : "text-muted line-through"}>{tl.name}</span>
                    <span className="text-muted/60">{tl.type}</span>
                  </label>
                ))}
              </div>
            )}
          </div>
          <div>
            <div className="text-muted mb-1">{t("project.accessProjects")}</div>
            {others.length === 0 && <div className="text-muted">{t("project.accessNoProjects")}</div>}
            {others.map((p) => (
              <label key={p.id} className="flex items-center gap-1.5 cursor-pointer">
                <input
                  type="checkbox"
                  checked={acc.allowedProjects.includes(p.id)}
                  onChange={() => save({ ...acc, allowedProjects: toggle(acc.allowedProjects, p.id) })}
                />
                <span>{p.name}</span>
              </label>
            ))}
          </div>
          <div className="text-2xs text-muted/70">{t("project.accessHint")}</div>
        </div>
      )}
    </div>
  );
}

// SubscriptionEditor lets the user pick which Claude subscription this project's
// new sessions run on. Empty value = inherit the default (active) account.
// Changing it re-stamps only NEW sessions; in-flight ones keep their account.
// It fires CLAUDE_ACCOUNT_CHANGED so the top usage meter (which follows the
// project's account while on this page) refetches at once.
function SubscriptionEditor({ project, onChanged }: { project: Project; onChanged: () => void }) {
  const { t } = useTranslation();
  const [accounts, setAccounts] = useState<ClaudeAccount[]>([]);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api.listClaudeAccounts().then(setAccounts).catch(() => {});
  }, []);

  const change = async (accountId: string) => {
    setBusy(true);
    try {
      await api.updateProject(project.id, { claudeAccountId: accountId });
      window.dispatchEvent(new Event(CLAUDE_ACCOUNT_CHANGED));
      onChanged();
    } finally {
      setBusy(false);
    }
  };

  // Only connected accounts are selectable (the backend rejects logged-out ones).
  const connected = accounts.filter((a) => a.status === "connected");
  const def = accounts.find((a) => a.active);
  // If the project still points at an account that has since been disconnected,
  // surface it (disabled) so the select reflects the stored value rather than
  // silently showing "Défaut" while sessions fall back to the default account.
  const current = project.claudeAccountId;
  const stale = current && !connected.some((a) => a.id === current)
    ? accounts.find((a) => a.id === current)
    : undefined;

  return (
    <div className="mb-3">
      <div className="text-2xs uppercase tracking-wide text-muted mb-1">{t("project.subscription")}</div>
      <select
        value={current}
        onChange={(e) => change(e.target.value)}
        disabled={busy}
        className="w-full text-xs bg-panel-2 border border-border rounded px-2 py-1 disabled:opacity-40 focus:border-accent-dim outline-none"
      >
        <option value="">{t("project.subscriptionDefault")}{def ? ` (${def.label})` : ""}</option>
        {connected.map((a) => (
          <option key={a.id} value={a.id}>
            {a.label}
          </option>
        ))}
        {stale && (
          <option value={stale.id} disabled>
            {stale.label} ({t("project.subscriptionDisconnected")})
          </option>
        )}
      </select>
    </div>
  );
}

function SessionCard({
  s, selected, onSelect, onStop, onRemove, onArchive, onUnarchive, t,
}: {
  s: Session; selected: string | null;
  onSelect: (id: string) => void;
  onStop: (id: string) => void;
  onRemove: (id: string) => void;
  onArchive: (id: string) => void;
  onUnarchive: (id: string) => void;
  t: TFunction;
}) {
  return (
    <div
      onClick={() => onSelect(s.id)}
      className={`cursor-pointer rounded-control px-2 py-2 text-xs border ${
        s.archived
          ? "border-border/50 opacity-60"
          : selected === s.id ? "border-accent-dim bg-panel-2"
          : s.unread ? "border-accent bg-accent/10 hover:border-accent"
          : "border-border hover:border-accent-dim/50"
      }`}
    >
      <div className="flex items-center justify-between">
        <span className={`truncate ${s.unread && selected !== s.id ? "font-bold" : "font-medium"}`}>
          {s.unread && selected !== s.id && (
            <span className="inline-block w-2 h-2 rounded-full bg-accent mr-1.5 animate-pulse align-middle" title={t("project.unreadTooltip")} />
          )}
          {s.summary || s.comment || s.id.slice(0, 8)}
          {s.autonomous && <span className="text-accent ml-1" title={t("project.autonomousTooltip")}>◆</span>}
        </span>
        <StateBadge state={s.state} />
      </div>
      <div className="mt-1 text-2xs text-muted flex items-center gap-1.5">
        <span className="mono">{s.id.slice(0, 8)}</span>
        <span className="text-muted/40">·</span>
        {s.state === "running" && s.turn === "done" && !s.asking ? (
          <span className="text-muted" title={t("project.idleTurn")}>{t("project.idleTurn")}</span>
        ) : s.state === "running" || s.state === "starting" ? (
          <span className="text-ok">{t("project.inProgress")}</span>
        ) : s.lastMessageAt ? (
          <span title={`${fmtDateTime(s.createdAt)} → ${fmtDateTime(s.lastMessageAt)}`}>
            {fmtRelative(s.lastMessageAt, t)}
          </span>
        ) : (
          <span>{fmtDateTime(s.createdAt)}</span>
        )}
      </div>
      {((s.usage?.inputTokens ?? 0) + (s.usage?.outputTokens ?? 0) > 0 || (s.usage?.costUSD ?? 0) > 0) && (
        <div className="mt-1 mono text-2xs text-muted" title={t("project.usageTooltip")}>
          {fmtTokens((s.usage?.inputTokens ?? 0) + (s.usage?.outputTokens ?? 0))} {t("project.tokensSuffix")}
          {(s.usage?.costUSD ?? 0) > 0 && ` · ~$${s.usage.costUSD.toFixed(4)}`}
        </div>
      )}
      {s.attention?.needed && (
        <div className="mt-1 text-2xs text-orange-400 flex items-center gap-1" title={s.attention.reason}>
          <span>⚠</span><span className="truncate">{s.attention.reason}</span>
        </div>
      )}
      {s.state === "running" && (
        <button onClick={(e) => { e.stopPropagation(); onStop(s.id); }} className="text-danger text-2xs mt-1 hover:underline">
          {t("project.stop")}
        </button>
      )}
      {(s.state === "stopped" || s.state === "error") && !s.archived && (
        <span className="flex items-center gap-2">
          <button onClick={(e) => { e.stopPropagation(); onArchive(s.id); }} className="text-muted text-2xs mt-1 hover:text-fg hover:underline">
            {t("project.archiveSessionLink")}
          </button>
          <button onClick={(e) => { e.stopPropagation(); onRemove(s.id); }} className="text-danger text-2xs mt-1 hover:underline">
            {t("project.deleteLink")}
          </button>
        </span>
      )}
      {s.archived && (
        <button onClick={(e) => { e.stopPropagation(); onUnarchive(s.id); }} className="text-muted text-2xs mt-1 hover:text-fg hover:underline">
          {t("project.unarchiveSessionLink")}
        </button>
      )}
    </div>
  );
}

function SessionList({
  sessions, archivedSessions, showArchived, onToggleArchived,
  selected, onSelect, onStop, onRemove, onArchive, onUnarchive, t,
}: {
  sessions: Session[];
  archivedSessions: Session[];
  showArchived: boolean;
  onToggleArchived: () => void;
  selected: string | null;
  onSelect: (id: string) => void;
  onStop: (id: string) => void;
  onRemove: (id: string) => void;
  onArchive: (id: string) => void;
  onUnarchive: (id: string) => void;
  t: TFunction;
}) {
  const byActivity = (a: Session, b: Session) => {
    const ta = new Date(a.lastMessageAt ?? a.createdAt).getTime();
    const tb = new Date(b.lastMessageAt ?? b.createdAt).getTime();
    return tb - ta;
  };
  const manual = sessions.filter((s) => s.source !== "scheduler").sort(byActivity);
  const auto = sessions.filter((s) => s.source === "scheduler").sort(byActivity);

  const cardProps = { selected, onSelect, onStop, onRemove, onArchive, onUnarchive, t };

  return (
    <>
      <div className="text-xs text-muted uppercase tracking-wider mb-2">{t("project.sessions")}</div>
      <div className="space-y-1 mb-3">
        {manual.map((s) => (
          <SessionCard key={s.id} s={s} {...cardProps} />
        ))}
        {manual.length === 0 && <div className="text-muted text-xs">{t("project.noSessions")}</div>}
      </div>

      {auto.length > 0 && (
        <>
          <div className="text-xs text-muted uppercase tracking-wider mb-2 mt-4 flex items-center gap-1">
            <span>⏱</span>
            <span>{t("project.scheduledSessions")}</span>
          </div>
          <div className="space-y-1 mb-3">
            {auto.map((s) => (
              <SessionCard key={s.id} s={s} {...cardProps} />
            ))}
          </div>
        </>
      )}

      <button
        onClick={onToggleArchived}
        className="text-2xs text-muted hover:text-fg transition-colors mt-1"
      >
        {showArchived
          ? t("project.hideArchived")
          : t("project.showArchived", { count: archivedSessions.length })}
      </button>

      {showArchived && archivedSessions.length > 0 && (
        <>
          <div className="text-xs text-muted uppercase tracking-wider mb-2 mt-3">
            {t("project.archivedSessions")}
          </div>
          <div className="space-y-1">
            {archivedSessions.map((s) => (
              <SessionCard key={s.id} s={s} {...cardProps} />
            ))}
          </div>
        </>
      )}
      {showArchived && archivedSessions.length === 0 && (
        <div className="text-muted text-xs mt-2">{t("project.noArchivedSessions")}</div>
      )}
    </>
  );
}

export function ProjectView() {
  const { t } = useTranslation();
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const [project, setProject] = useState<Project | null>(null);
  const [allProjects, setAllProjects] = useState<Project[]>([]);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [archivedSessions, setArchivedSessions] = useState<Session[]>([]);
  const [showArchived, setShowArchived] = useState(false);
  const [selected, setSelected] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // Per-session model override for the next launch ("" => project/global default).
  const [sessionModel, setSessionModel] = useState("");
  // Usage warning dialog: shown when session window >= 80% before launching.
  const [usageWarn, setUsageWarn] = useState<{
    utilization: number;
    resetsAt: Date | null;
    mode: string | undefined;
  } | null>(null);

  async function loadSessions(pid: string) {
    const s = await api.listSessions(pid);
    setSessions(s);
    // Honor a ?session=<id> deep-link (e.g. from the dashboard) when it points at
    // a real session. Never clobbers a selection the user already made.
    const wanted = searchParams.get("session");
    const deepLink = wanted && s.some((x) => x.id === wanted) ? wanted : null;
    setSelected((cur) => cur ?? deepLink ?? null);
  }

  async function loadArchivedSessions(pid: string) {
    const s = await api.listArchivedSessions(pid).catch(() => [] as Session[]);
    setArchivedSessions(s);
  }

  useEffect(() => {
    if (!id) return;
    setSelected(null);
    api.getProject(id).then(setProject).catch((e) => setError(e.message));
    api.listProjects().then(setAllProjects).catch(() => {});
    loadSessions(id).catch((e) => setError(e.message));
    loadArchivedSessions(id);
    // Poll so autonomous hand-back state (attention) stays fresh in the list.
    const t = setInterval(() => loadSessions(id).catch(() => {}), 5000);
    return () => clearInterval(t);
  }, [id]);

  // Viewing a session reads it: mark it seen on open, and again whenever the poll
  // reports new output while it stays open.
  const selectedUnread = sessions.find((x) => x.id === selected)?.unread;
  useEffect(() => {
    if (!selected || selectedUnread === false) return;
    api.markSessionSeen(selected).then(() => { if (id) loadSessions(id).catch(() => {}); }).catch(() => {});
  }, [selected, selectedUnread]);

  async function launch(permissionMode?: string) {
    if (!id) return;
    // Check usage before launching — warn if session window is ≥ 80%.
    const usage = await api.getUsage(project?.claudeAccountId || undefined).catch(() => null);
    if (usage?.session && usage.session.utilization >= 80) {
      const resetsAt = usage.session.resetsAt ? new Date(usage.session.resetsAt) : null;
      setUsageWarn({ utilization: usage.session.utilization, resetsAt, mode: permissionMode });
      return;
    }
    await doLaunch(permissionMode);
  }

  async function doLaunch(permissionMode?: string) {
    if (!id) return;
    setBusy(true);
    setError(null);
    try {
      // sessionModel "" => fall through to the project/global default.
      const s = await api.launchSession(id, permissionMode, sessionModel || undefined);
      await loadSessions(id);
      setSelected(s.id);
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  }

  async function deferLaunch(launchAt: Date, mode: string | undefined) {
    if (!id) return;
    setUsageWarn(null);
    setBusy(true);
    setError(null);
    try {
      await api.createDeferredSession(id, launchAt, mode ?? "default", sessionModel || undefined);
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  }

  async function stop(sid: string) {
    await api.stopSession(sid).catch((e) => setError(e.message));
    if (id) await loadSessions(id);
  }

  async function archive() {
    if (!id || !project) return;
    const liveCount = sessions.filter(
      (s) => s.state === "running" || s.state === "starting",
    ).length;
    const warn = liveCount > 0 ? t("project.archiveWarnSessions", { count: liveCount }) : "";
    if (!window.confirm(t("project.archiveConfirm", { name: project.name, warn })))
      return;
    setBusy(true);
    setError(null);
    try {
      await api.archiveProject(id);
      navigate("/archivage");
    } catch (e: any) {
      setError(e.message);
      setBusy(false);
    }
  }

  async function archiveSession(sid: string) {
    await api.archiveSession(sid).catch((e) => setError(e.message));
    if (selected === sid) setSelected(null);
    if (id) {
      await loadSessions(id);
      await loadArchivedSessions(id);
    }
  }

  async function unarchiveSession(sid: string) {
    await api.unarchiveSession(sid).catch((e) => setError(e.message));
    if (id) {
      await loadSessions(id);
      await loadArchivedSessions(id);
    }
  }

  async function remove(sid: string) {
    if (!window.confirm(t("project.deleteSessionConfirm"))) return;
    await api.deleteSession(sid).catch((e) => setError(e.message));
    if (selected === sid) setSelected(null);
    if (id) await loadSessions(id);
  }

  if (!project) return <div className="p-8 text-muted">{t("common.loading")}</div>;

  // A finished session (stopped/error) has no live runner socket: SessionStream
  // replays its journal read-only. Unknown (not yet loaded) → assume live.
  // Must search archivedSessions too, otherwise archived sessions default to
  // live=true and SessionStream tries to open a WebSocket that doesn't exist.
  const sel =
    sessions.find((s) => s.id === selected) ??
    archivedSessions.find((s) => s.id === selected);
  const live = sel ? sel.state === "running" || sel.state === "starting" : true;

  return (
    <div className="flex h-full">
      <div
        className={`${
          selected ? "hidden md:block" : "block"
        } w-full md:w-64 shrink-0 border-r border-border p-5 overflow-auto`}
      >
        <div className="flex items-center gap-2 mb-1">
          <h2 className="font-semibold">{project.name}</h2>
          {project.protected && (
            <span className="mono text-3xs uppercase text-accent">{t("project.protected")}</span>
          )}
        </div>
        <div className="mono text-xs text-muted break-all mb-3">{project.dir}</div>

        <ContextStatusIndicator resources={project.contextResources} />

        <CategoryEditor
          project={project}
          onChanged={() => id && api.getProject(id).then(setProject).catch(() => {})}
        />

        <CommentEditor
          project={project}
          onChanged={() => id && api.getProject(id).then(setProject).catch(() => {})}
        />

        <SubscriptionEditor
          project={project}
          onChanged={() => id && api.getProject(id).then(setProject).catch(() => {})}
        />

        <ModelEditor
          project={project}
          onChanged={() => id && api.getProject(id).then(setProject).catch(() => {})}
        />

        <label className="flex items-center gap-2 text-xs mb-3 cursor-pointer">
          <input
            type="checkbox"
            checked={project.showAutoSessions}
            onChange={(e) =>
              api
                .updateProject(project.id, { showAutoSessions: e.target.checked })
                .then(() => {
                  if (id) api.getProject(id).then(setProject).catch(() => {});
                })
                .catch(() => {})
            }
          />
          <span title={t("project.showAutoSessionsHint")}>{t("project.showAutoSessions")}</span>
        </label>

        <AccessEditor project={project} projects={allProjects} />

        {!project.protected && (
          <button
            onClick={archive}
            disabled={busy}
            className="text-2xs text-muted hover:text-fg disabled:opacity-40 transition-colors mb-4"
            title={t("project.archiveTooltip")}
          >
            {t("project.archive")}
          </button>
        )}

        {/* Per-session model override for the next launch: "" keeps the project /
            global default; pick Opus here for a one-off heavy session. */}
        <div className="mb-2">
          <div className="text-2xs uppercase tracking-wide text-muted mb-1">{t("project.sessionModelLabel")}</div>
          <select
            value={sessionModel}
            onChange={(e) => setSessionModel(e.target.value)}
            disabled={busy}
            className="w-full text-xs bg-panel-2 border border-border rounded px-2 py-1 disabled:opacity-40 focus:border-accent-dim outline-none"
          >
            <option value="">{t("project.sessionModelDefault")}</option>
            {MODELS.map((m) => (
              <option key={m} value={m}>{t(`configuration.model_${m}`)}</option>
            ))}
          </select>
        </div>

        <Button
          onClick={() => launch()}
          disabled={busy}
          fullWidth
          title={t("project.newSessionTooltip")}
          className="mb-2"
        >
          {t("project.newSession")}
        </Button>
        <Button
          variant="secondary"
          onClick={() => launch("plan")}
          disabled={busy}
          fullWidth
          title={t("project.autonomousSessionTooltip")}
          className="mb-4"
        >
          {t("project.autonomousSession")}
        </Button>

        {id && <ProjectRagPanel projectId={id} />}

        <SessionList
          sessions={sessions}
          archivedSessions={archivedSessions}
          showArchived={showArchived}
          onToggleArchived={() => setShowArchived((v) => !v)}
          selected={selected}
          onSelect={setSelected}
          onStop={stop}
          onRemove={remove}
          onArchive={archiveSession}
          onUnarchive={unarchiveSession}
          t={t}
        />

        {error && <div className="text-danger text-xs mt-4">{error}</div>}
      </div>

      {/* Usage warning dialog */}
      {usageWarn && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
          <div className="bg-panel border border-border rounded-lg p-5 w-80 shadow-xl">
            <div className="text-sm font-semibold mb-1">
              {t("project.usageWarnTitle", { pct: Math.round(usageWarn.utilization) })}
            </div>
            {usageWarn.resetsAt && (
              <div className="text-xs text-muted mb-4">
                {t("project.usageWarnReset", {
                  time: usageWarn.resetsAt.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hourCycle: "h23" }),
                })}
              </div>
            )}
            <div className="space-y-2">
              <Button
                fullWidth
                onClick={() => { setUsageWarn(null); doLaunch(usageWarn.mode); }}
              >
                {t("project.usageWarnStartAnyway")}
              </Button>
              {usageWarn.resetsAt && (
                <Button
                  variant="secondary"
                  fullWidth
                  onClick={() => deferLaunch(usageWarn.resetsAt!, usageWarn.mode)}
                >
                  {t("project.usageWarnDefer", {
                    time: usageWarn.resetsAt.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hourCycle: "h23" }),
                  })}
                </Button>
              )}
              <Button
                variant="secondary"
                fullWidth
                onClick={() => {
                  const tonight = new Date();
                  tonight.setDate(tonight.getDate() + (tonight.getHours() >= 2 ? 1 : 0));
                  tonight.setHours(2, 0, 0, 0);
                  deferLaunch(tonight, usageWarn.mode);
                }}
              >
                {t("project.usageWarnTonight")}
              </Button>
              <button
                className="w-full text-xs text-muted hover:text-fg py-1"
                onClick={() => setUsageWarn(null)}
              >
                {t("common.cancel")}
              </button>
            </div>
          </div>
        </div>
      )}

      <div
        className={`${
          selected ? "flex" : "hidden md:flex"
        } flex-1 min-w-0 flex-col`}
      >
        {selected ? (
          <>
            <button
              onClick={() => setSelected(null)}
              className="md:hidden text-left text-accent text-sm px-4 py-2 border-b border-border"
            >
              {t("project.mobileSessions")}
            </button>
            {sel && (
              <SessionHeader
                session={sel}
                onCommentSaved={() => id && loadSessions(id).catch(() => {})}
              />
            )}
            <div className="flex-1 min-h-0">
              <SessionStream key={selected} sessionId={selected} live={live} />
            </div>
          </>
        ) : (
          <div className="p-8 text-muted">
            {t("project.launchToStart")}
          </div>
        )}
      </div>
    </div>
  );
}

function SessionHeader({
  session,
  onCommentSaved,
}: {
  session: Session;
  onCommentSaved: () => void;
}) {
  const { t } = useTranslation();
  const [note, setNote] = useState(session.comment ?? "");

  // Reset the editable note when switching to another session.
  useEffect(() => {
    setNote(session.comment ?? "");
  }, [session.id, session.comment]);

  async function save() {
    if (note === (session.comment ?? "")) return;
    try {
      await api.setSessionComment(session.id, note);
      onCommentSaved();
    } catch {
      // Best-effort: keep the typed value so the user can retry.
    }
  }

  return (
    <div className="border-b border-border px-4 py-2 flex items-center gap-3 text-xs">
      <input
        value={note}
        onChange={(e) => setNote(e.target.value)}
        onBlur={save}
        onKeyDown={(e) => {
          if (e.key === "Enter") (e.target as HTMLInputElement).blur();
        }}
        placeholder={t("project.notePlaceholder")}
        className="flex-1 min-w-0 bg-transparent border-b border-transparent focus:border-accent-dim outline-none text-fg placeholder:text-muted py-1"
      />
      {session.branch && (
        <span className="mono text-muted shrink-0" title={t("project.branchTooltip")}>
          {session.branch}
        </span>
      )}
      {session.prUrl && (
        <a
          href={session.prUrl}
          target="_blank"
          rel="noreferrer"
          className="mono text-accent shrink-0 hover:underline"
          title={session.prUrl}
        >
          PR
        </a>
      )}
    </div>
  );
}

function StateBadge({ state }: { state: Session["state"] }) {
  const map: Record<Session["state"], string> = {
    running: "text-ok",
    starting: "text-accent",
    stopped: "text-muted",
    error: "text-danger",
  };
  return <span className={`mono ${map[state]}`}>{state}</span>;
}
