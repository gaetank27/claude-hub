import { cn } from "../../lib/cn";

// Skeleton is a pulsing placeholder block for loading states.
export function Skeleton({ className }: { className?: string }) {
  return <div className={cn("rounded-card bg-panel-2 motion-safe:animate-pulse", className)} />;
}
