import type { ReactNode } from "react";
import { cn } from "../../lib/cn";

// EmptyState is the centered placeholder shown when a list/grid has no items.
export function EmptyState({
  title,
  hint,
  action,
  className,
}: {
  title: ReactNode;
  hint?: ReactNode;
  action?: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "flex flex-col items-center justify-center text-center gap-2 py-10 px-4",
        className,
      )}
    >
      <p className="text-sm text-fg">{title}</p>
      {hint && <p className="text-xs text-muted max-w-sm">{hint}</p>}
      {action && <div className="mt-1">{action}</div>}
    </div>
  );
}
