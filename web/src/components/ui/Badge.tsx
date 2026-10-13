import type { ReactNode } from "react";
import { cn } from "../../lib/cn";

export type BadgeTone = "neutral" | "accent" | "ok" | "danger" | "warn";

const TONES: Record<BadgeTone, string> = {
  neutral: "bg-panel-2 text-muted",
  accent: "bg-accent/15 text-accent",
  ok: "bg-ok/15 text-ok",
  danger: "bg-danger/15 text-danger",
  warn: "bg-orange-400/15 text-orange-400",
};

// Badge is a small status pill. Tones map to the theme's semantic colours.
export function Badge({
  tone = "neutral",
  className,
  children,
}: {
  tone?: BadgeTone;
  className?: string;
  children: ReactNode;
}) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-control px-1.5 py-0.5 text-2xs",
        TONES[tone],
        className,
      )}
    >
      {children}
    </span>
  );
}
