import { useTranslation } from "react-i18next";
import { Badge } from "./ui/Badge";
import type { ContextResource } from "../lib/api";

// Per-resource glyphs for the injected-context badge. Keys match the backend
// ContextResource.Key (connector type, "sandbox", "s3").
const ICONS: Record<string, string> = {
  sandbox: "📦",
  telegram: "✈️",
  proxmox: "🖥️",
  email: "✉️",
  s3: "🗄️",
};

// ContextStatusIndicator shows, on a project tile, which shared Hub resources the
// project's sessions are made aware of at launch (the injected infrastructure
// context). Available resources are highlighted; roadmap ones are dimmed.
export function ContextStatusIndicator({ resources }: { resources?: ContextResource[] }) {
  const { t } = useTranslation();
  if (!resources || resources.length === 0) return null;
  return (
    <div
      className="flex flex-wrap items-center gap-1 mt-2 pt-2 border-t border-border/60"
      title={t("session.contextResourcesTitle")}
    >
      <span className="text-3xs text-muted mr-0.5">{t("session.contextLabel")}</span>
      {resources.map((r) => (
        <Badge
          key={r.key}
          tone={r.future ? "neutral" : "accent"}
          className={`text-3xs ${r.future ? "opacity-60" : ""}`}
        >
          {ICONS[r.key] ?? "•"} {r.label}
        </Badge>
      ))}
    </div>
  );
}
