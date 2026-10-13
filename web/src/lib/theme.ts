// Color-theme registry and persistence. Themes are pure CSS: each id maps to a
// `html[data-theme=…]` block in theme.css that overrides the palette variables.
// We only need to set the attribute and remember the choice.

export type ThemeId =
  | "midnight"
  | "nord"
  | "ember"
  | "slate"
  | "light"
  | "dracula"
  | "solarized"
  | "rose"
  | "forest"
  | "oled"
  | "grape"
  | "sand"
  | "matrix";

export interface Theme {
  id: ThemeId;
  label: string;
  // Representative swatches for the picker (background, panel, accent).
  swatch: [string, string, string];
}

export const THEMES: Theme[] = [
  { id: "midnight", label: "Midnight", swatch: ["#0b0e14", "#161b27", "#2de2c0"] },
  { id: "nord", label: "Nord", swatch: ["#2e3440", "#3b4252", "#88c0d0"] },
  { id: "ember", label: "Ember", swatch: ["#14100d", "#261f18", "#ff8c42"] },
  { id: "slate", label: "Slate", swatch: ["#0f1115", "#1d212b", "#6ea8fe"] },
  { id: "dracula", label: "Dracula", swatch: ["#282a36", "#343746", "#bd93f9"] },
  { id: "solarized", label: "Solarized", swatch: ["#002b36", "#0a4453", "#2aa198"] },
  { id: "rose", label: "Rosé", swatch: ["#191724", "#26233a", "#ebbcba"] },
  { id: "forest", label: "Forest", swatch: ["#0b130f", "#18291f", "#4ade80"] },
  { id: "oled", label: "OLED", swatch: ["#000000", "#141414", "#22d3ee"] },
  { id: "grape", label: "Grape", swatch: ["#13111f", "#241f3d", "#a78bfa"] },
  { id: "light", label: "Daylight", swatch: ["#f5f7fa", "#eef2f7", "#0ea5e9"] },
  { id: "sand", label: "Sand", swatch: ["#f7f3ec", "#efe8db", "#c2702f"] },
  { id: "matrix", label: "Matrix", swatch: ["#000000", "#0f1e0f", "#00ff41"] },
];

const KEY = "claudehub.theme";
const DEFAULT: ThemeId = "midnight";

export function getTheme(): ThemeId {
  try {
    const t = localStorage.getItem(KEY);
    if (THEMES.some((x) => x.id === t)) return t as ThemeId;
  } catch {
    /* localStorage may be unavailable (private mode); fall back to default */
  }
  return DEFAULT;
}

export function applyTheme(id: ThemeId): void {
  document.documentElement.setAttribute("data-theme", id);
}

export function setTheme(id: ThemeId): void {
  try {
    localStorage.setItem(KEY, id);
  } catch {
    /* ignore persistence failure; the in-session choice still applies */
  }
  applyTheme(id);
}
