// i18n bootstrap — FR/EN. The detector resolves the active language from
// localStorage (key `claudehub.lang`, also written by cmd/docgen before a
// screenshot/PDF run) and falls back to the browser language, then to English.
// Resources are split per UI domain under ./locales and assembled in ./resources.
import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import LanguageDetector from "i18next-browser-languagedetector";
import { en, fr } from "./resources";

export const SUPPORTED_LANGS = ["en", "fr"] as const;
export type Lang = (typeof SUPPORTED_LANGS)[number];

// Persisted under the same localStorage key the detector reads, so a language
// chosen in the UI survives reloads and is observable by cmd/docgen.
export const LANG_STORAGE_KEY = "claudehub.lang";

i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources: {
      en: { translation: en },
      fr: { translation: fr },
    },
    fallbackLng: "en",
    supportedLngs: SUPPORTED_LANGS as unknown as string[],
    // Map regional variants (e.g. fr-FR, en-US) onto the base language.
    nonExplicitSupportedLngs: true,
    load: "languageOnly",
    interpolation: { escapeValue: false },
    detection: {
      order: ["localStorage", "navigator"],
      lookupLocalStorage: LANG_STORAGE_KEY,
      caches: ["localStorage"],
    },
    react: { useSuspense: false },
  });

export default i18n;
