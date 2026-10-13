import { cn } from "../../lib/cn";

export type DotTone = "ok" | "accent" | "danger" | "muted" | "warn";

const TONES: Record<DotTone, string> = {
  ok: "bg-ok",
  accent: "bg-accent",
  danger: "bg-danger",
  muted: "bg-muted/50",
  warn: "bg-orange-400",
};

// StatusDot is the small coloured indicator used across project tiles, the RAG
// panel, tools and the Ollama widget. `pulse` animates it for in-progress states.
export function StatusDot({
  tone = "muted",
  pulse = false,
  className,
  title,
}: {
  tone?: DotTone;
  pulse?: boolean;
  className?: string;
  title?: string;
}) {
  return (
    <span
      title={title}
      className={cn(
        "inline-block w-1.5 h-1.5 rounded-pill shrink-0",
        TONES[tone],
        pulse && "motion-safe:animate-pulse",
        className,
      )}
    />
  );
}
