import type { ReactNode } from "react";
import { cn } from "../../lib/cn";

// Field is the label + control + hint wrapper. Unifies the duplicate `Labeled`
// (ToolsView) and `Field` (Configuration) helpers into one.
export function Field({
  label,
  hint,
  htmlFor,
  className,
  children,
}: {
  label?: ReactNode;
  hint?: ReactNode;
  htmlFor?: string;
  className?: string;
  children: ReactNode;
}) {
  return (
    <label htmlFor={htmlFor} className={cn("flex flex-col gap-1", className)}>
      {label && <span className="text-xs text-muted">{label}</span>}
      {children}
      {hint && <span className="text-xs text-muted/80">{hint}</span>}
    </label>
  );
}
