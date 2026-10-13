import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, type Project } from "../lib/api";
import { Button } from "../components/ui";

// ArchivageView lists soft-archived projects: their data is preserved and they
// can be restored to the board or moved to the compressed safe deposit box.
// "Supprimer" never erases directly — it packs the project into a .tar.gz vault
// (Configuration → Projets supprimés), and only there can it be destroyed for
// good. Archiving itself happens from a project's page.
export function ArchivageView() {
  const { t } = useTranslation();
  const [projects, setProjects] = useState<Project[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const load = () => api.listArchivedProjects().then(setProjects).catch((e) => setError(e.message));

  useEffect(() => {
    load();
  }, []);

  async function restore(p: Project) {
    setBusy(p.id + "restore");
    try {
      await api.unarchiveProject(p.id);
      await load();
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy(null);
    }
  }

  async function remove(p: Project) {
    if (!window.confirm(t("archivage.deleteConfirm", { name: p.name }))) return;
    setBusy(p.id + "delete");
    try {
      await api.vaultProject(p.id);
      await load();
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="p-4 md:p-8 max-w-4xl mx-auto w-full">
      <h1 className="text-2xl font-semibold mb-1">{t("archivage.title")}</h1>
      <p className="text-sm text-muted mb-5">
        {t("archivage.intro_pre")}
        <strong>{t("archivage.introLink")}</strong>
        {t("archivage.intro_post")}
      </p>

      {error && <div className="text-danger text-sm mb-3">{error}</div>}

      {projects.length === 0 ? (
        <div className="text-muted text-sm py-6 text-center bg-panel border border-border rounded-lg">
          {t("archivage.empty")}
        </div>
      ) : (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
          {projects.map((p) => (
            <div key={p.id} className="bg-panel border border-border rounded-lg p-4 flex flex-col">
              <span className="font-medium truncate">{p.name}</span>
              <span className="mono text-2xs text-muted break-all mt-1">{p.dir}</span>
              {p.category && <span className="text-2xs text-muted/70 mt-1">{p.category}</span>}
              <div className="flex items-center gap-2 mt-3">
                <Button size="sm" onClick={() => restore(p)} disabled={busy === p.id + "restore"}>
                  {t("archivage.restore")}
                </Button>
                <button
                  onClick={() => remove(p)}
                  disabled={busy === p.id + "delete"}
                  title={t("archivage.deleteTitle")}
                  className="text-danger rounded px-2 py-1 text-sm hover:underline disabled:opacity-40 ml-auto"
                >
                  {t("common.delete")}
                </button>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
