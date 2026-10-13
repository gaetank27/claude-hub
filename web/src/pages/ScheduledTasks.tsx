import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { api, type Project, type ScheduledTask, type ScheduledTaskInput } from "../lib/api";

const PERMISSION_MODES = ["default", "acceptEdits", "plan", "bypassPermissions"];
const MODELS = ["", "opus", "sonnet", "haiku"];

function fmtDateTime(s?: string): string {
  if (!s) return "";
  const d = new Date(s);
  return d.toLocaleString(undefined, { dateStyle: "short", timeStyle: "short", hourCycle: "h23" });
}

function nextLabel(s: string): string {
  const d = new Date(s);
  const diff = d.getTime() - Date.now();
  if (diff < 0) return "—";
  const mins = Math.round(diff / 60000);
  if (mins < 60) return `${mins}min`;
  const hrs = Math.round(diff / 3600000);
  if (hrs < 24) return `${hrs}h`;
  return `${Math.round(diff / 86400000)}j`;
}

// Blank form state
const BLANK: ScheduledTaskInput = {
  name: "", projectId: "", prompt: "", schedule: "0 8 * * *",
  enabled: true, permissionMode: "default", model: "", command: "",
};

export function ScheduledTasksView() {
  const { t } = useTranslation();
  const [tasks, setTasks] = useState<ScheduledTask[]>([]);
  const [projects, setProjects] = useState<Project[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [showForm, setShowForm] = useState(false);
  const [editId, setEditId] = useState<string | null>(null);
  const [form, setForm] = useState<ScheduledTaskInput>(BLANK);
  const [isScript, setIsScript] = useState(false);
  const [busy, setBusy] = useState(false);
  const [runningId, setRunningId] = useState<string | null>(null);

  const load = () =>
    api.listTasks().then(setTasks).catch((e) => setError(e.message));

  useEffect(() => {
    load();
    api.listProjects().then(setProjects).catch(() => {});
    const t = setInterval(load, 10000);
    return () => clearInterval(t);
  }, []);

  function openCreate() {
    setForm(BLANK);
    setIsScript(false);
    setEditId(null);
    setShowForm(true);
  }

  function openEdit(t: ScheduledTask) {
    setForm({
      name: t.name, projectId: t.projectId, prompt: t.prompt,
      schedule: t.schedule, enabled: t.enabled,
      permissionMode: t.permissionMode, model: t.model, command: t.command,
    });
    setIsScript(!!t.command);
    setEditId(t.id);
    setShowForm(true);
  }

  async function save() {
    setBusy(true);
    setError(null);
    // A task is either a Claude session (prompt) or a script (command), never both.
    const input = isScript ? { ...form, prompt: "" } : { ...form, command: "" };
    try {
      if (editId) {
        await api.updateTask(editId, input);
      } else {
        await api.createTask(input);
      }
      setShowForm(false);
      setEditId(null);
      await load();
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  }

  async function remove(id: string, name: string) {
    if (!confirm(t("tasks.confirmDelete", { name }))) return;
    await api.deleteTask(id).catch((e) => setError(e.message));
    await load();
  }

  async function runNow(id: string) {
    setRunningId(id);
    try {
      await api.runTaskNow(id);
      await load();
    } catch (e: any) {
      setError(e.message);
    } finally {
      setRunningId(null);
    }
  }

  const globalTasks = tasks.filter((t) => !t.projectId);
  const projectTasks = tasks.filter((t) => t.projectId);
  const projectName = (id: string) => projects.find((p) => p.id === id)?.name ?? id;

  // Group project tasks by project
  const byProject = projectTasks.reduce<Record<string, ScheduledTask[]>>((acc, t) => {
    (acc[t.projectId] ??= []).push(t);
    return acc;
  }, {});

  return (
    <div className="p-6 max-w-4xl mx-auto">
      <div className="flex items-center justify-between mb-6">
        <h1 className="text-lg font-semibold">{t("tasks.title")}</h1>
        <button
          onClick={openCreate}
          className="text-sm bg-accent text-bg font-medium rounded px-3 py-1.5 hover:bg-accent-dim transition-colors"
        >
          {t("tasks.newTask")}
        </button>
      </div>

      <p className="text-xs text-muted mb-6">{t("tasks.intro")}</p>

      {error && <div className="text-danger text-xs mb-4">{error}</div>}

      {showForm && (
        <TaskForm
          form={form}
          isScript={isScript}
          onKind={setIsScript}
          projects={projects}
          isEdit={!!editId}
          busy={busy}
          onChange={setForm}
          onSave={save}
          onCancel={() => { setShowForm(false); setEditId(null); }}
        />
      )}

      {/* Global tasks */}
      <section className="mb-8">
        <h2 className="text-xs font-medium uppercase tracking-wider text-muted mb-3">
          {t("tasks.globalTasks")}
        </h2>
        {globalTasks.length === 0 ? (
          <p className="text-xs text-muted">{t("tasks.noTasks")}</p>
        ) : (
          <div className="space-y-2">
            {globalTasks.map((task) => (
              <TaskRow
                key={task.id}
                task={task}
                projectName={null}
                running={runningId === task.id}
                onEdit={() => openEdit(task)}
                onDelete={() => remove(task.id, task.name)}
                onRun={() => runNow(task.id)}
              />
            ))}
          </div>
        )}
      </section>

      {/* Project tasks grouped by project */}
      <section>
        <h2 className="text-xs font-medium uppercase tracking-wider text-muted mb-3">
          {t("tasks.projectTasks")}
        </h2>
        {Object.keys(byProject).length === 0 ? (
          <p className="text-xs text-muted">{t("tasks.noTasks")}</p>
        ) : (
          Object.entries(byProject).map(([pid, ptasks]) => (
            <div key={pid} className="mb-4">
              <Link
                to={`/projects/${pid}`}
                className="text-xs text-accent hover:underline mb-2 block"
              >
                {projectName(pid)}
              </Link>
              <div className="space-y-2 pl-3 border-l border-border">
                {ptasks.map((task) => (
                  <TaskRow
                    key={task.id}
                    task={task}
                    projectName={null}
                    running={runningId === task.id}
                    onEdit={() => openEdit(task)}
                    onDelete={() => remove(task.id, task.name)}
                    onRun={() => runNow(task.id)}
                  />
                ))}
              </div>
            </div>
          ))
        )}
      </section>
    </div>
  );
}

function TaskRow({
  task, running, onEdit, onDelete, onRun,
}: {
  task: ScheduledTask;
  projectName: string | null;
  running: boolean;
  onEdit: () => void;
  onDelete: () => void;
  onRun: () => void;
}) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-wrap items-center gap-3 border border-border rounded-lg px-3 py-2 text-xs">
      <div className="flex items-center gap-2 flex-1 min-w-0">
        <span
          className={`w-1.5 h-1.5 rounded-full shrink-0 ${task.enabled ? "bg-ok" : "bg-muted"}`}
          title={task.enabled ? t("tasks.enabled") : t("tasks.disabled")}
        />
        <span className="font-medium truncate">{task.name}</span>
        {task.command && (
          <span className="shrink-0 border border-border rounded px-1 text-2xs text-muted">
            {t("tasks.kindScript")}
          </span>
        )}
        <span className="mono text-muted shrink-0">{task.schedule}</span>
      </div>

      <div className="text-muted flex gap-3 shrink-0">
        <span title={t("tasks.nextRun")}>
          ↻ {task.enabled ? nextLabel(task.nextRunAt) : "—"}
        </span>
        {task.lastRunAt && (
          <span title={t("tasks.lastRun")}>
            ✓ {fmtDateTime(task.lastRunAt)}
          </span>
        )}
        {task.lastStatus === "running" && (
          <span className="text-accent animate-pulse">{t("tasks.statusRunning")}</span>
        )}
        {task.lastStatus === "ok" && <span className="text-ok">{t("tasks.statusOk")}</span>}
        {task.lastStatus === "error" && <span className="text-danger">{t("tasks.statusError")}</span>}
      </div>

      <div className="flex items-center gap-2 shrink-0">
        <button
          onClick={onRun}
          disabled={running}
          className="text-accent hover:underline disabled:opacity-40"
        >
          {running ? t("tasks.running") : t("tasks.runNow")}
        </button>
        <button onClick={onEdit} className="text-muted hover:text-fg">
          {t("tasks.edit")}
        </button>
        <button onClick={onDelete} className="text-danger hover:underline">
          {t("tasks.delete")}
        </button>
      </div>

      {task.command ? (
        <div className="w-full text-muted truncate pl-5 mono" title={task.command}>
          $ {task.command}
        </div>
      ) : task.prompt && (
        <div className="w-full text-muted truncate pl-5" title={task.prompt}>
          {task.prompt}
        </div>
      )}

      {task.command && task.lastOutput && (
        <details className="w-full pl-5">
          <summary className="cursor-pointer text-muted hover:text-fg">{t("tasks.lastOutput")}</summary>
          <pre className="mt-1 mono text-2xs text-muted whitespace-pre-wrap break-all max-h-48 overflow-auto bg-bg border border-border rounded p-2">
            {task.lastOutput}
          </pre>
        </details>
      )}
    </div>
  );
}

function TaskForm({
  form, isScript, onKind, projects, isEdit, busy, onChange, onSave, onCancel,
}: {
  form: ScheduledTaskInput;
  isScript: boolean;
  onKind: (script: boolean) => void;
  projects: Project[];
  isEdit: boolean;
  busy: boolean;
  onChange: (f: ScheduledTaskInput) => void;
  onSave: () => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const set = (patch: Partial<ScheduledTaskInput>) => onChange({ ...form, ...patch });

  return (
    <div className="border border-accent/40 rounded-lg p-4 mb-6 bg-panel-2 space-y-3">
      <h3 className="text-sm font-medium">{isEdit ? t("tasks.formEdit") : t("tasks.formCreate")}</h3>

      <div className="flex gap-4 text-sm">
        <label className="flex items-center gap-2 cursor-pointer">
          <input type="radio" checked={!isScript} onChange={() => onKind(false)} />
          {t("tasks.kindSession")}
        </label>
        <label className="flex items-center gap-2 cursor-pointer">
          <input type="radio" checked={isScript} onChange={() => onKind(true)} />
          {t("tasks.kindScript")}
        </label>
      </div>

      <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
        <label className="block">
          <span className="text-xs text-muted">{t("tasks.fieldName")}</span>
          <input
            value={form.name}
            onChange={(e) => set({ name: e.target.value })}
            className="mt-1 w-full bg-bg border border-border rounded px-2 py-1.5 text-sm focus:border-accent-dim outline-none"
          />
        </label>

        <label className="block">
          <span className="text-xs text-muted">{t("tasks.fieldProject")}</span>
          <select
            value={form.projectId}
            onChange={(e) => set({ projectId: e.target.value })}
            className="mt-1 w-full bg-bg border border-border rounded px-2 py-1.5 text-sm focus:border-accent-dim outline-none"
          >
            <option value="">— {t("tasks.globalTasks").toLowerCase()} —</option>
            {projects.map((p) => (
              <option key={p.id} value={p.id}>{p.name}</option>
            ))}
          </select>
        </label>

        <label className="block">
          <span className="text-xs text-muted">{t("tasks.fieldSchedule")}</span>
          <input
            value={form.schedule}
            onChange={(e) => set({ schedule: e.target.value })}
            placeholder="0 8 * * *"
            className="mt-1 w-full bg-bg border border-border rounded px-2 py-1.5 text-sm mono focus:border-accent-dim outline-none"
          />
          <span className="text-2xs text-muted">{t("tasks.fieldScheduleHint")}</span>
        </label>

        {!isScript && <div className="grid grid-cols-2 gap-2">
          <label className="block">
            <span className="text-xs text-muted">{t("tasks.fieldPermissionMode")}</span>
            <select
              value={form.permissionMode}
              onChange={(e) => set({ permissionMode: e.target.value })}
              className="mt-1 w-full bg-bg border border-border rounded px-2 py-1.5 text-sm focus:border-accent-dim outline-none"
            >
              {PERMISSION_MODES.map((m) => <option key={m} value={m}>{m}</option>)}
            </select>
          </label>
          <label className="block">
            <span className="text-xs text-muted">{t("tasks.fieldModel")}</span>
            <select
              value={form.model}
              onChange={(e) => set({ model: e.target.value })}
              className="mt-1 w-full bg-bg border border-border rounded px-2 py-1.5 text-sm focus:border-accent-dim outline-none"
            >
              {MODELS.map((m) => (
                <option key={m} value={m}>{m || "— défaut —"}</option>
              ))}
            </select>
          </label>
        </div>}
      </div>

      {isScript ? (
        <label className="block">
          <span className="text-xs text-muted">{t("tasks.fieldCommand")}</span>
          <textarea
            value={form.command ?? ""}
            onChange={(e) => set({ command: e.target.value })}
            rows={3}
            spellCheck={false}
            className="mt-1 w-full bg-bg border border-border rounded px-2 py-1.5 text-sm mono focus:border-accent-dim outline-none resize-y"
          />
          <span className="text-2xs text-muted">{t("tasks.fieldCommandHint")}</span>
        </label>
      ) : (
        <label className="block">
          <span className="text-xs text-muted">{t("tasks.fieldPrompt")}</span>
          <textarea
            value={form.prompt}
            onChange={(e) => set({ prompt: e.target.value })}
            rows={4}
            className="mt-1 w-full bg-bg border border-border rounded px-2 py-1.5 text-sm focus:border-accent-dim outline-none resize-y"
          />
          <span className="text-2xs text-muted">{t("tasks.fieldPromptHint")}</span>
        </label>
      )}

      <label className="flex items-center gap-2 text-sm cursor-pointer">
        <input
          type="checkbox"
          checked={form.enabled}
          onChange={(e) => set({ enabled: e.target.checked })}
        />
        {t("tasks.fieldEnabled")}
      </label>

      <div className="flex gap-2 pt-1">
        <button
          onClick={onSave}
          disabled={busy || !form.name || !(isScript ? form.command : form.prompt) || !form.schedule}
          className="bg-accent text-bg font-medium rounded px-3 py-1.5 text-sm disabled:opacity-40 hover:bg-accent-dim transition-colors"
        >
          {t("tasks.save")}
        </button>
        <button
          onClick={onCancel}
          className="border border-border rounded px-3 py-1.5 text-sm hover:border-accent-dim transition-colors"
        >
          {t("tasks.cancel")}
        </button>
      </div>
    </div>
  );
}
