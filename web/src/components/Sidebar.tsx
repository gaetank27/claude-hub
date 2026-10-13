import { useEffect, useState } from "react";
import { NavLink } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { api } from "../lib/api";

// Every primary route is now wired (Archivage, Sandbox and Live included).
// Labels are stored as translation keys (labelKey) and resolved in render.
const NAV = [
  { labelKey: "nav.assistant", to: "/assistant", enabled: true },
  { labelKey: "nav.dashboard", to: "/projets", enabled: true },
  { labelKey: "nav.projects", to: "/projets", enabled: true },
  { labelKey: "nav.archiving", to: "/archivage", enabled: true },
  { labelKey: "nav.sandbox", to: "/sandbox", enabled: true },
  { labelKey: "nav.live", to: "/live", enabled: true },
  { labelKey: "nav.skills", to: "/skills", enabled: true },
  { labelKey: "nav.rag", to: "/rag", enabled: true },
  { labelKey: "nav.tools", to: "/outils", enabled: true },
  { labelKey: "nav.tasks", to: "/tasks", enabled: true },
  { labelKey: "nav.scripts", to: "/scripts", enabled: true },
  { labelKey: "nav.monitoring", to: "/monitoring", enabled: true },
  { labelKey: "nav.configuration", to: "/configuration", enabled: true },
  { labelKey: "nav.help", to: "/aide", enabled: true },
];

export function Sidebar({ onNavigate }: { onNavigate?: () => void }) {
  const { t } = useTranslation();
  return (
    <aside className="w-56 h-full shrink-0 bg-panel border-r border-border flex flex-col">
      <div className="px-5 py-5 border-b border-border">
        <span className="mono text-accent text-lg font-semibold tracking-wide">
          ◆ Claude Hub
        </span>
      </div>
      <nav className="flex-1 py-3">
        {NAV.map((item, i) =>
          item.enabled ? (
            <NavLink
              key={i}
              to={item.to}
              end
              onClick={onNavigate}
              className={({ isActive }) =>
                `block px-5 py-2 text-sm transition-colors ${
                  isActive
                    ? "text-accent border-l-2 border-accent bg-panel-2"
                    : "text-muted hover:text-fg border-l-2 border-transparent"
                }`
              }
            >
              {t(item.labelKey)}
            </NavLink>
          ) : (
            <span
              key={i}
              className="block px-5 py-2 text-sm text-muted/40 cursor-not-allowed border-l-2 border-transparent"
              title={t("common.soon")}
            >
              {t(item.labelKey)}
            </span>
          ),
        )}
      </nav>
      <NotificationToggle />
      <SessionFooter />
    </aside>
  );
}

// SessionFooter shows the signed-in identity and a logout control when auth is
// enabled. With auth off, whoami() returns null and it falls back to the build
// label, so the footer renders the same as before.
function SessionFooter() {
  const { t } = useTranslation();
  const [email, setEmail] = useState<string | null>(null);

  useEffect(() => {
    api.whoami().then((u) => setEmail(u?.email ?? null));
  }, []);

  if (!email) {
    return (
      <div className="px-5 py-3 text-xs text-muted/60 border-t border-border">
        slice 1 · socle
      </div>
    );
  }
  return (
    <div className="px-5 py-3 border-t border-border text-xs">
      <div className="text-muted truncate" title={email}>
        {email}
      </div>
      <button
        onClick={() => api.logout()}
        className="mt-1 text-muted hover:text-fg transition-colors"
      >
        {t("nav.logout")}
      </button>
    </div>
  );
}

// NotificationToggle lets the user grant desktop-notification permission (the
// browser requires a user gesture) and reflects the current state. Hidden when
// the browser has no Notification API.
function NotificationToggle() {
  const { t } = useTranslation();
  const [perm, setPerm] = useState<NotificationPermission | "unsupported">(
    typeof Notification === "undefined" ? "unsupported" : Notification.permission,
  );

  if (perm === "unsupported") return null;
  if (perm === "granted") {
    return (
      <div className="px-5 py-2 text-2xs text-muted/60 border-t border-border">
        🔔 {t("nav.notificationsEnabled")}
      </div>
    );
  }
  if (perm === "denied") {
    return (
      <div
        className="px-5 py-2 text-2xs text-muted/60 border-t border-border"
        title={t("nav.notificationsBlockedHint")}
      >
        🔕 {t("nav.notificationsBlocked")}
      </div>
    );
  }
  return (
    <div className="px-5 py-2 border-t border-border">
      <button
        onClick={() => Notification.requestPermission().then(setPerm)}
        className="text-2xs text-accent hover:underline"
      >
        🔔 {t("nav.enableNotifications")}
      </button>
    </div>
  );
}
