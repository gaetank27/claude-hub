import type { ReactNode } from "react";
import { cn } from "../../lib/cn";

type Width = "narrow" | "default" | "wide";

const MAX: Record<Width, string> = {
  narrow: "max-w-2xl",
  default: "max-w-4xl",
  wide: "max-w-5xl",
};

// PageContainer normalises the per-page padding + max-width lane that pages used
// to hand-roll inconsistently (p-4 sm:p-8 with max-w-2xl/4xl/5xl). Uses the
// semantic spacing tokens so density is tunable in one place.
export function PageContainer({
  width = "wide",
  className,
  children,
}: {
  width?: Width;
  className?: string;
  children: ReactNode;
}) {
  return (
    <div className={cn("p-page-sm sm:p-page w-full mx-auto", MAX[width], className)}>{children}</div>
  );
}
