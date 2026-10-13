import React, { useEffect, useState } from "react";
import ReactDOM from "react-dom/client";
import { createBrowserRouter, Navigate, RouterProvider } from "react-router-dom";
import "./theme.css";
import "./i18n";
import { api } from "./lib/api";
import { Login } from "./pages/Login";
import { Setup } from "./pages/Setup";
import { App } from "./App";
import { LanguageFlag } from "./components/LanguageSwitcher";
import { Dashboard } from "./pages/Dashboard";
import { ProjectView } from "./pages/ProjectView";
import { SkillsView } from "./pages/SkillsView";
import { RagView } from "./pages/RagView";
import { RagCollectionView } from "./pages/RagCollectionView";
import { ToolsView } from "./pages/ToolsView";
import { SandboxView, LiveView } from "./pages/EnvironmentsView";
import { ArchivageView } from "./pages/ArchivageView";
import { Monitoring } from "./pages/Monitoring";
import { Configuration } from "./pages/Configuration";
import { HelpView } from "./pages/HelpView";
import { ScheduledTasksView } from "./pages/ScheduledTasks";
import { ScriptsView } from "./pages/ScriptsView";
import { AssistantView } from "./pages/AssistantView";

const router = createBrowserRouter([
  {
    path: "/",
    element: <App />,
    children: [
      { index: true, element: <Navigate to="/assistant" replace /> },
      { path: "assistant", element: <AssistantView /> },
      { path: "projets", element: <Dashboard /> },
      { path: "projects/:id", element: <ProjectView /> },
      { path: "skills", element: <SkillsView /> },
      { path: "rag", element: <RagView /> },
      { path: "rag/:id", element: <RagCollectionView /> },
      { path: "outils", element: <ToolsView /> },
      { path: "sandbox", element: <SandboxView /> },
      { path: "live", element: <LiveView /> },
      { path: "archivage", element: <ArchivageView /> },
      { path: "tasks", element: <ScheduledTasksView /> },
      { path: "scripts", element: <ScriptsView /> },
      { path: "monitoring", element: <Monitoring /> },
      { path: "configuration", element: <Configuration /> },
      { path: "aide", element: <HelpView /> },
    ],
  },
]);

// Gate is the auth/setup front door. Before mounting the app it asks the backend
// whether the first-boot wizard must run, and whether a session exists. It renders
// the Setup wizard, the local Login screen, or the full app accordingly. When auth
// is off (dev), /setup/status reports no setup and /auth/me returns an identity-less
// 200/HTML — whoami() yields null but the API calls succeed, so we still mount the
// app: a 401 from a real gated call would bounce back here.
type GateState =
  | { kind: "loading" }
  | { kind: "setup" }
  | { kind: "login"; oidcEnabled: boolean }
  | { kind: "app" };

function Gate() {
  const [state, setState] = useState<GateState>({ kind: "loading" });

  useEffect(() => {
    let alive = true;
    (async () => {
      const status = await api.setupStatus();
      if (!alive) return;
      // Auth disabled (dev): no gate — mount the app directly.
      if (status === null) {
        setState({ kind: "app" });
        return;
      }
      if (status.needsSetup) {
        setState({ kind: "setup" });
        return;
      }
      const me = await api.whoami();
      if (!alive) return;
      if (me) setState({ kind: "app" });
      else setState({ kind: "login", oidcEnabled: status.oidcEnabled });
    })();
    return () => {
      alive = false;
    };
  }, []);

  switch (state.kind) {
    case "loading":
      return <div className="flex min-h-full items-center justify-center bg-bg text-muted text-sm">…</div>;
    case "setup":
      return <Setup />;
    case "login":
      return <Login oidcEnabled={state.oidcEnabled} />;
    case "app":
      return <RouterProvider router={router} />;
  }
}

if ("serviceWorker" in navigator) {
  window.addEventListener("load", () => {
    navigator.serviceWorker.register("/sw.js").catch(() => {});
  });
}

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <Gate />
    {/* Global top-right language flag, shown on every screen (setup, login, app). */}
    <LanguageFlag />
  </React.StrictMode>,
);
