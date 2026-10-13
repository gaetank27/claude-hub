import type { ReactNode } from "react";
import { cn } from "../../lib/cn";

// Chip is a pill-shaped toggle used for filters and segmented choices. Collapses
// the duplicate FilterChip helpers from Dashboard / RagView.
export function Chip({
  active = false,
  onClick,
  className,
  children,
}: {
  active?: boolean;
  onClick?: () => void;
  className?: string;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "rounded-pill border px-3 py-1 text-xs transition-colors duration-150",
        active
          ? "bg-accent text-bg border-accent"
          : "border-border text-muted hover:text-fg hover:border-accent-dim",
        className,
      )}
    >
      {children}
    </button>
  );
}
