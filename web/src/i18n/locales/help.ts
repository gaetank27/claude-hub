// Chrome strings for the in-app Help (HelpView) and the printed PDF manual.
// The chapter content itself lives in lib/help.ts (bilingual arrays); these keys
// only cover the surrounding UI (titles, intro, search, placeholders, alt text).
export const help = {
  en: {
    printTitle: "Claude Hub — User manual",
    printSubtitle: "Documentation generated automatically from the online help.",
    title: "Help",
    intro:
      "The Claude Hub user guide. Search for a topic or browse the chapters.",
    searchPlaceholder: "Search the help…",
    noResults: "No chapter matches “{{query}}”.",
    imageAlt: "Screenshot of Claude Hub",
    shotPlaceholder: "Screenshot to be generated (cmd/docgen).",
  },
  fr: {
    printTitle: "Claude Hub — Manuel d'utilisation",
    printSubtitle: "Documentation générée automatiquement depuis l'aide en ligne.",
    title: "Aide",
    intro:
      "Le guide d'utilisation de Claude Hub. Cherchez un sujet ou parcourez les chapitres.",
    searchPlaceholder: "Rechercher dans l'aide…",
    noResults: "Aucun chapitre ne correspond à « {{query}} ».",
    imageAlt: "Capture d'écran de Claude Hub",
    shotPlaceholder: "Capture d'écran à générer (cmd/docgen).",
  },
};
