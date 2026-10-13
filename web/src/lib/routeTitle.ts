// routeTitle maps a pathname to a translation key for the page title, used by the
// mobile header so the user always knows where they are (the slim mobile chrome
// has no persistent sidebar to anchor context). Callers translate the result:
// t(routeTitle(path)). Returns "" for the fallback so callers can show the brand.
export function routeTitle(pathname: string): string {
  if (pathname === "/assistant") return "nav.assistant";
  if (pathname === "/projets") return "nav.dashboard";
  if (pathname.startsWith("/projects/")) return "nav.project";
  if (pathname === "/skills") return "nav.skills";
  if (pathname === "/rag") return "nav.rag";
  if (pathname.startsWith("/rag/")) return "nav.collection";
  if (pathname === "/outils") return "nav.tools";
  if (pathname === "/monitoring") return "nav.monitoring";
  if (pathname === "/configuration") return "nav.configuration";
  if (pathname === "/aide") return "nav.help";
  if (pathname === "/tasks") return "nav.tasks";
  if (pathname === "/scripts") return "nav.scripts";
  return "";
}
