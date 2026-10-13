import { cn } from "../../lib/cn";

// Spinner is a small motion-safe loading indicator. Inherits text colour via
// currentColor so it adapts to whatever surface it sits on.
export function Spinner({ className }: { className?: string }) {
  return (
    <svg
      className={cn("animate-spin motion-reduce:animate-none", className)}
      viewBox="0 0 24 24"
      fill="none"
      aria-hidden="true"
    >
      <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="3" />
      <path
        className="opacity-90"
        fill="currentColor"
        d="M12 2a10 10 0 0 1 10 10h-3a7 7 0 0 0-7-7V2z"
      />
    </svg>
  );
}
