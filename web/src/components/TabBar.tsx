import { NavLink } from "react-router-dom";
import { useTranslation } from "react-i18next";
import {
  GridIcon,
  FolderIcon,
  BoltIcon,
  DatabaseIcon,
  WrenchIcon,
  BoxIcon,
  RocketIcon,
  ArchiveIcon,
  PulseIcon,
  TerminalIcon,
  GearIcon,
  HelpIcon,
  ClockIcon,
  ChatIcon,
} from "./icons";

// Bottom tab bar for mobile only (md:hidden). It replaces the slide-in drawer as
// the thumb-reachable primary navigation on small screens. Rendered in-flow at
// the bottom of the content column (not position:fixed), so it never overlaps the
// scrolling content and needs no compensating padding on <main>. The desktop
// Sidebar is unaffected — it stays the navigation on md+.
//
// Only the wired routes are exposed here; the disabled "Bientôt" stubs from the
// Sidebar are intentionally omitted on mobile.
// Labels are stored as translation keys (labelKey) and resolved in render. The
// mobile bar uses shortened labels (tab*) so they don't crowd the bar at ~360px.
const TABS = [
  { labelKey: "nav.assistant", to: "/assistant", icon: ChatIcon },
  { labelKey: "nav.tabDashboard", to: "/projets", icon: GridIcon },
  // "Projets" shares the "/projets" route with the dashboard, mirroring the Sidebar NAV.
  { labelKey: "nav.projects", to: "/projets", icon: FolderIcon },
  { labelKey: "nav.skills", to: "/skills", icon: BoltIcon },
  { labelKey: "nav.rag", to: "/rag", icon: DatabaseIcon },
  { labelKey: "nav.tools", to: "/outils", icon: WrenchIcon },
  { labelKey: "nav.tasks", to: "/tasks", icon: ClockIcon },
  { labelKey: "nav.scripts", to: "/scripts", icon: TerminalIcon },
  { labelKey: "nav.sandbox", to: "/sandbox", icon: BoxIcon },
  { labelKey: "nav.live", to: "/live", icon: RocketIcon },
  { labelKey: "nav.archives", to: "/archivage", icon: ArchiveIcon },
  // "Suivi" rather than "Monitoring": the longer label crowds the bar at ~360px
  // and truncates to "Monit…".
  { labelKey: "nav.tabMonitoring", to: "/monitoring", icon: PulseIcon },
  { labelKey: "nav.tabConfiguration", to: "/configuration", icon: GearIcon },
  { labelKey: "nav.help", to: "/aide", icon: HelpIcon },
];

export function TabBar() {
  const { t } = useTranslation();
  return (
    <nav
      className="md:hidden shrink-0 flex items-stretch border-t border-border bg-panel
                 pb-[env(safe-area-inset-bottom)]"
      aria-label={t("nav.primaryNav")}
    >
      {TABS.map((tab, i) => {
        const Icon = tab.icon;
        return (
          <NavLink
            key={i}
            to={tab.to}
            end
            className={({ isActive }) =>
              `flex-1 min-w-0 flex flex-col items-center justify-center gap-0.5
               py-2 min-h-[44px] text-2xs transition-colors motion-safe:active:scale-95 ${
                 isActive ? "text-accent" : "text-muted hover:text-fg"
               }`
            }
          >
            <Icon />
            <span className="truncate max-w-full">{t(tab.labelKey)}</span>
          </NavLink>
        );
      })}
    </nav>
  );
}
