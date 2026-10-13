import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { api, hiddenFromRecent, type Project, type Session } from "../lib/api";

// Dot indicating session state.
function StateDot({ state, unread }: { state: Session["state"]; unread?: boolean }) {
  if (unread)
    return <span className="inline-block w-2 h-2 rounded-full bg-accent shrink-0 animate-pulse" />;
  if (state === "running")
    return <span className="inline-block w-1.5 h-1.5 rounded-full bg-ok shrink-0 animate-pulse" />;
  if (state === "starting")
    return <span className="inline-block w-1.5 h-1.5 rounded-full bg-accent shrink-0 animate-pulse" />;
  return <span className="inline-block w-1.5 h-1.5 rounded-full bg-muted/40 shrink-0" />;
}

// RecentSessionsBar shows the 3 most recently active sessions across all
// projects. Clicking a chip navigates directly to that session.
export function RecentSessionsBar() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [recent, setRecent] = useState<{ session: Session; projectName: string }[]>([]);

  useEffect(() => {
    let cancelled = false;

    async function load() {
      try {
        const [sessions, projects] = await Promise.all([
          api.listAllSessions(),
          api.listProjects(),
        ]);
        if (cancelled) return;

        const nameMap = new Map<string, string>(
          (projects as Project[]).map((p) => [p.id, p.name]),
        );

        // Sort by most recent activity (lastEventAt), fall back to createdAt.
        const sorted = sessions.filter((s) => !hiddenFromRecent(s, projects as Project[])).sort((a, b) => {
          const ta = a.lastEventAt ?? a.createdAt;
          const tb = b.lastEventAt ?? b.createdAt;
          return new Date(tb).getTime() - new Date(ta).getTime();
        });

        setRecent(
          sorted.slice(0, 3).map((s) => ({
            session: s,
            projectName: nameMap.get(s.projectId) ?? "…",
          })),
        );
      } catch {
        /* best-effort — bar stays hidden on error */
      }
    }

    load();
    const id = setInterval(load, 15_000);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, []);

  if (recent.length === 0) return null;

  return (
    <div className="flex items-center gap-1.5 border-b border-border px-3 pr-12 py-1 text-[11px] overflow-x-auto scrollbar-none">
      <span className="text-muted shrink-0 mr-0.5">{t("nav.recentSessions")}</span>
      {recent.map(({ session: s, projectName }) => (
        <button
          key={s.id}
          onClick={() => navigate(`/projects/${s.projectId}?session=${s.id}`)}
          title={`${projectName} › ${s.comment || s.id.slice(0, 8)}`}
          className={`flex items-center gap-1.5 hover:bg-border rounded px-2 py-0.5 shrink-0
                     hover:text-fg transition-colors max-w-[180px] ${
                       s.unread ? "bg-accent/15 text-fg font-semibold" : "bg-panel-2 text-muted"
                     }`}
        >
          <StateDot state={s.state} unread={s.unread} />
          <span className="truncate">
            <span className="text-fg/70">{projectName}</span>
            <span className="text-muted/60 mx-0.5">›</span>
            <span>{s.comment || s.id.slice(0, 8)}</span>
          </span>
        </button>
      ))}
    </div>
  );
}
