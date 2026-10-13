import { useEffect } from "react";
import type { ReactNode } from "react";
import { createPortal } from "react-dom";
import { cn } from "../../lib/cn";

// Modal is a centered overlay surface for dialogs. Floating, so it uses the
// shadow-pop elevation token. Closes on Escape and backdrop click. On mobile it
// docks to the bottom (sheet-like) and respects the safe-area inset.
export function Modal({
  onClose,
  title,
  children,
  className,
}: {
  onClose: () => void;
  title?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return createPortal(
    <div
      className="fixed inset-0 z-50 flex items-end sm:items-center justify-center bg-black/60 p-0 sm:p-4"
      onClick={onClose}
      role="dialog"
      aria-modal="true"
    >
      <div
        className={cn(
          "bg-panel border border-border w-full max-h-[85vh] overflow-auto shadow-pop",
          "rounded-t-card sm:rounded-card sm:max-w-lg",
          "pb-[max(1.25rem,env(safe-area-inset-bottom))] sm:pb-5 p-5",
          className,
        )}
        onClick={(e) => e.stopPropagation()}
      >
        {title && <h2 className="text-base font-semibold mb-3">{title}</h2>}
        {children}
      </div>
    </div>,
    document.body,
  );
}
