import type { TFunction } from "i18next";

// fmtRelative returns a human-friendly relative time: "now", "5 min ago",
// "2h ago", "yesterday 14:32", or "29/05 19:01" for older dates.
export function fmtRelative(iso: string | undefined, t: TFunction): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const diffMin = Math.floor((Date.now() - d.getTime()) / 60_000);
  if (diffMin < 1) return t("common.timeNow");
  if (diffMin < 60) return t("common.timeMinutesAgo", { count: diffMin });
  const diffH = Math.floor(diffMin / 60);
  if (diffH < 24) return t("common.timeHoursAgo", { count: diffH });
  const yesterday = new Date();
  yesterday.setDate(yesterday.getDate() - 1);
  if (d.toDateString() === yesterday.toDateString()) {
    const time = d.toLocaleTimeString("fr-FR", { hour: "2-digit", minute: "2-digit" });
    return t("common.timeYesterday", { time });
  }
  return d.toLocaleString("fr-FR", { day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit" });
}
