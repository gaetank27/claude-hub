import { useTranslation } from "react-i18next";

// LanguageFlag is a small fixed flag button pinned to the top-right corner, shown
// on every screen. It displays the *current* language's flag; clicking it toggles
// to the other language (persisted by the i18next detector under `claudehub.lang`).
// Flags are inline SVG, not emoji — flag emoji render as bare letters ("GB"/"FR")
// on Windows, which would defeat the icon.

const NEXT: Record<string, "en" | "fr"> = { en: "fr", fr: "en" };
const ENDONYM: Record<string, string> = { en: "English", fr: "Français" };

export function LanguageFlag() {
  const { i18n } = useTranslation();
  const cur = (i18n.resolvedLanguage || i18n.language || "en").slice(0, 2) === "fr" ? "fr" : "en";
  const next = NEXT[cur];

  return (
    <button
      type="button"
      onClick={() => i18n.changeLanguage(next)}
      title={ENDONYM[next]}
      aria-label={`Switch language to ${ENDONYM[next]}`}
      className="fixed right-3 top-[max(0.6rem,env(safe-area-inset-top))] z-40 h-6 w-9
                 overflow-hidden rounded-[3px] border border-border/70 bg-panel shadow-md
                 transition hover:ring-2 hover:ring-accent print:hidden"
    >
      {cur === "fr" ? <FlagFR /> : <FlagGB />}
    </button>
  );
}

function FlagFR() {
  return (
    <svg viewBox="0 0 3 2" className="h-full w-full" preserveAspectRatio="none" aria-hidden="true">
      <rect width="3" height="2" fill="#fff" />
      <rect width="1" height="2" fill="#0055A4" />
      <rect x="2" width="1" height="2" fill="#EF4135" />
    </svg>
  );
}

function FlagGB() {
  return (
    <svg viewBox="0 0 60 30" className="h-full w-full" preserveAspectRatio="none" aria-hidden="true">
      <clipPath id="lf-gb">
        <path d="M30,15 h30 v15 z v15 h-30 z h-30 v-15 z v-15 h30 z" />
      </clipPath>
      <rect width="60" height="30" fill="#012169" />
      <path d="M0,0 L60,30 M60,0 L0,30" stroke="#fff" strokeWidth="6" />
      <path d="M0,0 L60,30 M60,0 L0,30" clipPath="url(#lf-gb)" stroke="#C8102E" strokeWidth="4" />
      <path d="M30,0 v30 M0,15 h60" stroke="#fff" strokeWidth="10" />
      <path d="M30,0 v30 M0,15 h60" stroke="#C8102E" strokeWidth="6" />
    </svg>
  );
}
