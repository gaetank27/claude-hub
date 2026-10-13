import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useLocation } from "react-router-dom";
import i18n from "../i18n";
import { api, CLAUDE_ACCOUNT_CHANGED, type Usage, type UsageWindow } from "../lib/api";

// fmtReset renders a reset time: a relative countdown within a day ("resets in 4 h
// 1 min"), otherwise a weekday + time ("resets Thu 11:00"), matching how the
// CLI's /usage view reads. Plain helper (no component), so it reads the i18n
// instance directly.
function fmtReset(iso?: string | null): string {
  if (!iso) return "";
  const d = new Date(iso);
  const ms = d.getTime() - Date.now();
  if (Number.isNaN(ms)) return "";
  if (ms <= 0) return i18n.t("session.resetImminent");
  if (ms < 24 * 3_600_000) {
    const h = Math.floor(ms / 3_600_000);
    const m = Math.floor((ms % 3_600_000) / 60_000);
    return i18n.t("session.resetIn", { value: `${h > 0 ? `${h} h ` : ""}${m} min` });
  }
  const day = d.toLocaleDateString("fr-FR", { weekday: "short" });
  const time = d.toLocaleTimeString("fr-FR", { hour: "2-digit", minute: "2-digit" });
  return i18n.t("session.resetAt", { day, time });
}

function Meter({ label, window }: { label: string; window?: UsageWindow | null }) {
  if (!window) return null;
  const pct = Math.min(100, Math.max(0, window.utilization));
  // Warn as the limit nears, matching the rest of the UI's danger/accent palette.
  const tone = pct >= 90 ? "bg-danger" : pct >= 75 ? "bg-orange-400" : "bg-accent";
  return (
    <div className="flex items-center gap-2 min-w-0" title={fmtReset(window.resetsAt)}>
      <span className="text-muted shrink-0">{label}</span>
      <div className="h-1.5 w-20 rounded-full bg-panel-2 overflow-hidden shrink-0">
        <div className={`h-full ${tone}`} style={{ width: `${pct}%` }} />
      </div>
      <span className="mono shrink-0">{Math.round(pct)}%</span>
      <span className="text-muted truncate">{fmtReset(window.resetsAt)}</span>
    </div>
  );
}

// UsageBar shows the Claude plan usage (session + weekly windows) as a thin strip
// at the top of the app. It polls every minute and hides itself when the data is
// unavailable (so it never shows a broken meter).
//
// The meter follows the default (active) subscription page-wide, but switches to
// the current project's subscription while inside a project (route /projects/:id)
// — so the consumption shown matches the account that project's sessions run on.
export function UsageBar() {
  const { t } = useTranslation();
  const [usage, setUsage] = useState<Usage | null>(null);
  const projectId = useLocation().pathname.match(/^\/projects\/([^/]+)/)?.[1];

  useEffect(() => {
    let cancelled = false;
    // Resolve which subscription the meter tracks: the project's assigned account
    // when inside a project (empty => it inherits the default, so we omit it and
    // fall back to the page-wide default meter), else the default account.
    const resolveAccount = async (): Promise<string | undefined> => {
      if (!projectId) return undefined;
      try {
        return (await api.getProject(projectId)).claudeAccountId || undefined;
      } catch {
        return undefined;
      }
    };
    const load = async () => {
      const u = await api.getUsage(await resolveAccount());
      if (!cancelled) setUsage(u);
    };
    load();
    const t = setInterval(load, 60_000);
    // Refetch at once when the active/project subscription changes, so the meter
    // reflects the new account instead of the previous one for up to a minute.
    window.addEventListener(CLAUDE_ACCOUNT_CHANGED, load);
    return () => {
      cancelled = true;
      clearInterval(t);
      window.removeEventListener(CLAUDE_ACCOUNT_CHANGED, load);
    };
  }, [projectId]);

  if (!usage || (!usage.session && !usage.week)) return null;
  return (
    <div
      className={`flex flex-wrap items-center gap-x-5 gap-y-1 border-b border-border px-4 pr-12 py-1.5 text-[11px] ${
        usage.stale ? "opacity-60" : ""
      }`}
      title={usage.stale ? t("session.usageBarStaleTitle") : undefined}
    >
      <Meter label={t("session.usageBarSession")} window={usage.session} />
      <Meter label={t("session.usageBarWeekly")} window={usage.week} />
      {usage.weekOpus && <Meter label={t("session.usageBarOpus")} window={usage.weekOpus} />}
    </div>
  );
}
