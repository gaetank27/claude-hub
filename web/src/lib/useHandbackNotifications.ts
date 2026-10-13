import { useEffect, useRef } from "react";
import i18n from "../i18n";
import { api, type Project, type Session } from "./api";

// useHandbackNotifications polls every session across all projects and fires a
// desktop notification whenever an autonomous session newly hands back control
// (attention.needed flips to true) — so the user is alerted from any page, not
// only the dashboard. Mount it once, near the app root.
//
// It is intentionally pull-based (reusing the existing /api/sessions poll): there
// is no backend push channel, and the 5s cadence matches the dashboard.
export function useHandbackNotifications() {
  // Session IDs already notified for their current attention episode. An id is
  // dropped once its attention clears, so a later re-trigger notifies again.
  const notified = useRef<Set<string>>(new Set());
  const projectNames = useRef<Map<string, string>>(new Map());

  useEffect(() => {
    if (!("Notification" in window)) return;

    let cancelled = false;

    // Project names rarely change; fetch once for nicer notification titles.
    api
      .listProjects()
      .then((ps: Project[]) => projectNames.current = new Map(ps.map((p) => [p.id, p.name])))
      .catch(() => {});

    const tick = async () => {
      let sessions: Session[];
      try {
        sessions = await api.listAllSessions();
      } catch {
        return;
      }
      if (cancelled) return;

      const stillNeeding = new Set<string>();
      for (const s of sessions) {
        if (!s.attention?.needed) continue;
        stillNeeding.add(s.id);
        if (notified.current.has(s.id)) continue;
        notified.current.add(s.id);
        if (Notification.permission === "granted") {
          const title = projectNames.current.get(s.projectId) ?? "Claude Hub";
          const n = new Notification(title, {
            body: s.attention.reason ?? i18n.t("session.sessionNeedsAttention"),
            tag: s.id, // collapse repeats for the same session
          });
          n.onclick = () => {
            window.focus();
            window.location.href = `/projects/${s.projectId}?session=${s.id}`;
          };
        }
      }
      // Forget sessions whose attention has cleared so they can notify again.
      for (const id of notified.current) {
        if (!stillNeeding.has(id)) notified.current.delete(id);
      }
    };

    tick();
    const t = setInterval(tick, 5000);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
  }, []);
}
