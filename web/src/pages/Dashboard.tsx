import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { fmtRelative } from "../lib/relativeTime";
import { Link } from "react-router-dom";
import { api, hiddenFromRecent, CLAUDE_ACCOUNT_CHANGED, type ClaudeAccount, type Project, type Session } from "../lib/api";
import { ProjectCard, type ProjectStats } from "../components/ProjectCard";

function fmtTokens(n: number): string {
  return n >= 1000 ? `${Math.round(n / 1000)}k` : String(n);
}

// STARRED_FILTER is a sentinel filter value (orthogonal to categories) that
// narrows the board to favorite (starred) projects. The double-underscore sentinel
// is namespaced so it cannot collide with a real, user-typed category name.
const STARRED_FILTER = "__starred__";

export function Dashboard() {
  const { t } = useTranslation();
  const [projects, setProjects] = useState<Project[]>([]);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [accounts, setAccounts] = useState<ClaudeAccount[]>([]);
  const [name, setName] = useState("");
  const [category, setCategory] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [creating, setCreating] = useState(false);
  const [loading, setLoading] = useState(true);
  // null = all categories; "" = uncategorized; otherwise a category name.
  const [filter, setFilter] = useState<string | null>(null);

  const load = () =>
    api.listProjects().then((ps) => { setProjects(ps); setLoading(false); }).catch((e) => { setError(e.message); setLoading(false); });

  useEffect(() => {
    load();
    const loadSessions = () => api.listAllSessions().then(setSessions).catch(() => {});
    const loadAccounts = () => api.listClaudeAccounts().then(setAccounts).catch(() => {});
    loadSessions();
    loadAccounts();
    // Poll so live session state / attention / cost stay fresh.
    const t = setInterval(loadSessions, 5000);
    // Sessions are now shown across all accounts; still refetch projects/accounts
    // when the active subscription changes (the account labels may shift).
    const onAccount = () => {
      load();
      loadSessions();
      loadAccounts();
    };
    window.addEventListener(CLAUDE_ACCOUNT_CHANGED, onAccount);
    return () => {
      clearInterval(t);
      window.removeEventListener(CLAUDE_ACCOUNT_CHANGED, onAccount);
    };
  }, []);

  async function create(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.createProject(name.trim(), category.trim());
      setName("");
      setCategory("");
      setCreating(false);
      await load();
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="p-4 sm:p-8 max-w-5xl">
      <h1 className="text-2xl font-semibold mb-1">{t("dashboard.title")}</h1>
      <p className="text-muted text-sm mb-6">
        {t("dashboard.subtitle")}
      </p>

      <Observability sessions={sessions.filter((s) => !hiddenFromRecent(s, projects))} projects={projects} accounts={accounts} />

      <div className="flex items-center justify-between gap-3 flex-wrap mb-4">
        <CategoryFilter projects={projects} value={filter} onChange={setFilter} />
        <button
          onClick={() => setCreating((c) => !c)}
          className="bg-accent text-bg font-medium rounded px-4 py-2 text-sm hover:bg-accent-dim transition-colors shrink-0"
        >
          {creating ? t("common.cancel") : t("dashboard.newProject")}
        </button>
      </div>

      {creating && (
        <>
          <form
            onSubmit={create}
            className="bg-panel border border-border rounded-lg p-4 mb-2 flex flex-wrap gap-3 items-end"
          >
            <div className="flex flex-col gap-1">
              <label className="text-xs text-muted">{t("common.name")}</label>
              <input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t("dashboard.namePlaceholder")}
                autoFocus
                className="bg-bg border border-border rounded px-3 py-2 text-sm w-48 focus:border-accent-dim outline-none"
              />
            </div>
            <div className="flex flex-col gap-1">
              <label className="text-xs text-muted">{t("dashboard.categoryLabel")}</label>
              <input
                value={category}
                onChange={(e) => setCategory(e.target.value)}
                placeholder={t("dashboard.categoryPlaceholder")}
                className="bg-bg border border-border rounded px-3 py-2 text-sm w-48 focus:border-accent-dim outline-none"
              />
            </div>
            <button
              disabled={busy || !name}
              className="bg-accent text-bg font-medium rounded px-4 py-2 text-sm disabled:opacity-40 hover:bg-accent-dim transition-colors"
            >
              {t("common.create")}
            </button>
          </form>
          <p className="text-xs text-muted/60 mb-6">
            {t("dashboard.dirHint", {
              path: "/home/claude/projects",
              interpolation: { escapeValue: false },
            })}
          </p>
        </>
      )}

      {error && <div className="text-danger text-sm mb-4">{error}</div>}

      {loading ? (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
          {[1, 2, 3].map((i) => (
            <div key={i} className="bg-panel border border-border rounded-lg p-4 animate-pulse">
              <div className="h-4 bg-border rounded w-2/3 mb-3" />
              <div className="h-3 bg-border rounded w-1/2 mb-2" />
              <div className="h-3 bg-border rounded w-1/3" />
            </div>
          ))}
        </div>
      ) : (
        <ProjectGroups
          projects={
            filter === null
              ? projects
              : filter === STARRED_FILTER
                ? projects.filter((p) => p.starred)
                : projects.filter((p) => (p.category || "") === filter)
          }
          sessions={sessions}
          onChanged={load}
        />
      )}
    </div>
  );
}

// CategoryFilter shows a chip per distinct project category (plus "Toutes"), so
// the board can be narrowed to one category. Hidden when there is nothing to
// filter (no category in use).
function CategoryFilter({
  projects,
  value,
  onChange,
}: {
  projects: Project[];
  value: string | null;
  onChange: (v: string | null) => void;
}) {
  const { t } = useTranslation();
  const cats: string[] = [];
  let hasUncat = false;
  let hasStarred = false;
  for (const p of projects) {
    if (p.starred) hasStarred = true;
    if (p.category) {
      if (!cats.includes(p.category)) cats.push(p.category);
    } else {
      hasUncat = true;
    }
  }
  // Nothing meaningful to filter by: no categories AND no favorites.
  if (cats.length === 0 && !hasStarred) return <div />;
  const chips: { label: string; v: string | null }[] = [{ label: t("dashboard.filterAll"), v: null }];
  if (hasStarred) chips.push({ label: t("dashboard.filterStarred"), v: STARRED_FILTER });
  chips.push(...cats.map((c) => ({ label: c, v: c })));
  if (hasUncat) chips.push({ label: t("dashboard.filterUncategorized"), v: "" });

  return (
    <div className="flex items-center gap-2 flex-wrap">
      {chips.map((c) => (
        <button
          key={c.v ?? "_all"}
          onClick={() => onChange(c.v)}
          className={`text-sm rounded-full px-3 py-1 border transition-colors ${
            value === c.v
              ? "bg-accent text-bg border-accent"
              : "border-border text-muted hover:text-fg hover:border-accent-dim"
          }`}
        >
          {c.label}
        </button>
      ))}
    </div>
  );
}

// statsFor aggregates a project's sessions: total count, active count, total
// cost, and cumulative Claude compute time (sum of per-session compute), reusing
// the usage already computed on each session.
function statsFor(projectId: string, sessions: Session[]): ProjectStats {
  let total = 0,
    active = 0,
    unread = 0,
    cost = 0,
    computeMs = 0,
    tokens = 0,
    skillUses = 0,
    ragUses = 0;
  const skills: Record<string, number> = {};
  const rags: Record<string, number> = {};
  for (const s of sessions) {
    if (s.projectId !== projectId) continue;
    total++;
    if (s.state === "running" || s.state === "starting") active++;
    if (s.unread) unread++;
    cost += s.usage?.costUSD ?? 0;
    computeMs += s.usage?.computeMs ?? 0;
    tokens += (s.usage?.inputTokens ?? 0) + (s.usage?.outputTokens ?? 0);
    for (const [name, count] of Object.entries(s.usage?.skills ?? {})) {
      skills[name] = (skills[name] ?? 0) + count;
      skillUses += count;
    }
    for (const [ref, count] of Object.entries(s.usage?.rags ?? {})) {
      rags[ref] = (rags[ref] ?? 0) + count;
      ragUses += count;
    }
  }
  return { total, active, unread, cost, computeMs, tokens, skills, skillUses, rags, ragUses };
}

// ProjectGroups renders the projects grouped by category. Projects keep their
// backend order (system, then starred, then recent) within each group;
// uncategorized projects come last under "Sans catégorie".
function ProjectGroups({
  projects,
  sessions,
  onChanged,
}: {
  projects: Project[];
  sessions: Session[];
  onChanged: () => void;
}) {
  const { t } = useTranslation();
  // Preserve first-seen category order (which follows the backend project order).
  const order: string[] = [];
  const groups = new Map<string, Project[]>();
  for (const p of projects) {
    const key = p.category || "";
    if (!groups.has(key)) {
      groups.set(key, []);
      order.push(key);
    }
    groups.get(key)!.push(p);
  }
  // Uncategorized last.
  order.sort((a, b) => (a === "" ? 1 : b === "" ? -1 : 0));

  return (
    <div className="space-y-6">
      {order.map((key) => (
        <div key={key || "_uncat"}>
          <h2 className="text-xs font-semibold uppercase tracking-wider text-muted mb-2">
            {key || t("dashboard.groupUncategorized")}
          </h2>
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
            {groups.get(key)!.map((p) => (
              <ProjectCard
                key={p.id}
                project={p}
                stats={statsFor(p.id, sessions)}
                onChanged={onChanged}
              />
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}

// Observability is the cross-project overview: sessions needing attention, live
// sessions, recent PRs and the total cost — all derived from the one session
// list. A session links to its project view with the session pre-selected.
function Observability({
  sessions,
  projects,
  accounts,
}: {
  sessions: Session[];
  projects: Project[];
  accounts: ClaudeAccount[];
}) {
  const { t } = useTranslation();
  if (sessions.length === 0) return null;

  const projectName = (id: string) => projects.find((p) => p.id === id)?.name ?? id.slice(0, 8);
  const link = (s: Session) => `/projects/${s.projectId}?session=${s.id}`;
  // With several subscriptions, tag each live session with the account it runs
  // under (sessions now span all accounts). Single-account setups stay uncluttered.
  const accountLabel = (id?: string) =>
    accounts.length > 1 && id ? accounts.find((a) => a.id === id)?.label ?? null : null;

  const byActivity = (a: Session, b: Session) => {
    const ta = new Date(a.lastMessageAt ?? a.createdAt).getTime();
    const tb = new Date(b.lastMessageAt ?? b.createdAt).getTime();
    return tb - ta;
  };
  const needAttention = sessions.filter((s) => s.attention?.needed).sort(byActivity);
  const unreadSessions = sessions.filter((s) => s.unread).sort(byActivity);
  const live = sessions.filter((s) => s.state === "running" || s.state === "starting").sort(byActivity);
  const withPR = sessions.filter((s) => s.prUrl);
  const totalCost = sessions.reduce((a, s) => a + (s.usage?.costUSD ?? 0), 0);

  return (
    <div className="bg-panel border border-border rounded-lg p-4 mb-8 space-y-4">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-semibold uppercase tracking-wider text-muted">{t("dashboard.overview")}</h2>
        <span className="mono text-xs text-muted" title={t("dashboard.totalCostTitle")}>
          {t("dashboard.totalCost", { cost: totalCost.toFixed(2) })}
        </span>
      </div>

      {needAttention.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {needAttention.map((s) => (
            <Link
              key={s.id}
              to={link(s)}
              className="flex items-center gap-1 text-[11px] text-orange-400 border border-orange-400/40 rounded px-2 py-1 hover:bg-orange-400/10 transition-colors"
              title={s.attention.reason}
            >
              <span>⚠</span>
              <span className="mono">{projectName(s.projectId)}</span>
              <span className="truncate max-w-48">{s.attention.reason}</span>
            </Link>
          ))}
        </div>
      )}

      {unreadSessions.length > 0 && (
        <div>
          <div className="text-xs text-muted mb-1">{t("dashboard.unreadSessions")}</div>
          <div className="flex flex-wrap gap-2">
            {unreadSessions.map((s) => (
              <Link
                key={s.id}
                to={link(s)}
                className="flex items-center gap-1.5 text-[11px] font-semibold text-fg bg-accent/15 border border-accent rounded px-2 py-1 hover:bg-accent/25 transition-colors"
              >
                <span className="inline-block w-2 h-2 rounded-full bg-accent animate-pulse" />
                <span className="mono">{projectName(s.projectId)}</span>
                <span className="truncate max-w-48">{s.comment || s.id.slice(0, 8)}</span>
              </Link>
            ))}
          </div>
        </div>
      )}

      <div>
        <div className="text-xs text-muted mb-1">{t("dashboard.activeSessions", { count: live.length })}</div>
        {live.length === 0 ? (
          <div className="text-xs text-muted/60">{t("dashboard.noActiveSessions")}</div>
        ) : (
          <div className="space-y-1">
            {live.map((s) => (
              <Link
                key={s.id}
                to={link(s)}
                className="flex items-center gap-2 text-xs rounded px-2 py-1.5 border border-border hover:border-accent-dim/50 transition-colors"
              >
                <span className="mono truncate">{projectName(s.projectId)}</span>
                <span className="mono text-muted">{s.comment || s.id.slice(0, 8)}</span>
                {accountLabel(s.claudeAccountId) && (
                  <span
                    className="text-3xs uppercase tracking-wider text-muted border border-border rounded-control px-1 py-0.5 shrink-0"
                    title={t("dashboard.sessionAccountTitle")}
                  >
                    {accountLabel(s.claudeAccountId)}
                  </span>
                )}
                {s.autonomous && <span className="text-accent" title={t("dashboard.autonomousSessionTitle")}>◆</span>}
                <StateLabel state={s.state} />
                {s.branch && <span className="mono text-muted truncate hidden sm:inline">{s.branch}</span>}
                <span className="flex-1" />
                {s.lastMessageAt && (
                  <span className="mono text-muted/70">{fmtRelative(s.lastMessageAt, t)}</span>
                )}
                {s.usage?.contextTokens > 0 && (
                  <span className="mono text-muted" title={t("dashboard.contextSizeTitle")}>{t("dashboard.contextTokens", { tokens: fmtTokens(s.usage.contextTokens) })}</span>
                )}
                <span className="mono text-muted">~${(s.usage?.costUSD ?? 0).toFixed(4)}</span>
              </Link>
            ))}
          </div>
        )}
      </div>

      {withPR.length > 0 && (
        <div>
          <div className="text-xs text-muted mb-1">{t("dashboard.recentPRs")}</div>
          <div className="flex flex-wrap gap-2">
            {withPR.map((s) => (
              <a
                key={s.id}
                href={s.prUrl}
                target="_blank"
                rel="noreferrer"
                className="text-[11px] text-accent border border-accent-dim/40 rounded px-2 py-1 hover:bg-panel-2 transition-colors"
                title={s.prUrl}
              >
                {projectName(s.projectId)} · {s.comment || s.id.slice(0, 8)} ↗
              </a>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

function StateLabel({ state }: { state: Session["state"] }) {
  const map: Record<Session["state"], string> = {
    running: "text-ok",
    starting: "text-accent",
    stopped: "text-muted",
    error: "text-danger",
  };
  return <span className={`mono ${map[state]}`}>{state}</span>;
}
