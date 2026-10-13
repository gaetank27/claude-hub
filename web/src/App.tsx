import { Outlet, useLocation } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Sidebar } from "./components/Sidebar";
import { TabBar } from "./components/TabBar";
import { UsageBar } from "./components/UsageBar";
import { RecentSessionsBar } from "./components/RecentSessionsBar";
import { useHandbackNotifications } from "./lib/useHandbackNotifications";
import { routeTitle } from "./lib/routeTitle";

export function App() {
  // Alert on any page when an autonomous session hands back control.
  useHandbackNotifications();
  const { t } = useTranslation();
  // routeTitle returns a translation key (or "" for the fallback, where we show
  // the brand instead).
  const titleKey = routeTitle(useLocation().pathname);
  const title = titleKey ? t(titleKey) : "Claude Hub";

  // `print:` variants release the viewport-locked shell for the PDF manual
  // (cmd/docgen prints /aide?print) and hide the app chrome so only the
  // documentation flows onto the paginated pages.
  return (
    <div className="flex h-full print:block print:h-auto">
      {/* Desktop: static sidebar column. Hidden on mobile, where the bottom tab
          bar takes over as the primary navigation. */}
      <div className="hidden md:block print:hidden">
        <Sidebar />
      </div>

      <div className="flex flex-1 min-w-0 flex-col print:block">
        {/* Mobile-only slim header: brand + current page title + notch-safe top
            inset. The title anchors context now that there's no sidebar. */}
        {/* pr-12 reserves the top-right corner for the global fixed LanguageFlag. */}
        <div className="flex items-center gap-2 border-b border-border px-4 py-3 pr-12 pt-[max(0.75rem,env(safe-area-inset-top))] md:hidden print:hidden">
          <span className="mono text-accent font-semibold tracking-wide shrink-0">◆</span>
          <span className="text-sm font-medium truncate">{title}</span>
        </div>
        <div className="print:hidden">
          <UsageBar />
          <RecentSessionsBar />
        </div>
        {/* The only vertically scrolling region. The session view keeps its own
            internal scroll + composer, so the in-flow tab bar below stays
            reachable for navigation even while inside a session. */}
        <main className="flex-1 min-w-0 overflow-auto print:overflow-visible print:h-auto">
          <Outlet />
        </main>
        <TabBar />
      </div>
    </div>
  );
}
