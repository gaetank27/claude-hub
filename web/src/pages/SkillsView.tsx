import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, type Skill } from "../lib/api";
import { SessionStream } from "../components/SessionStream";

// SkillsView is the global, transversal Skills module: a searchable library of
// reusable Claude Code skills plus a creation workflow that drives a dedicated
// background Claude session to author the SKILL.md. Layout mirrors ProjectView
// (list/detail split, mobile-aware) for consistency with the rest of the app.
export function SkillsView() {
  const { t } = useTranslation();
  const [skills, setSkills] = useState<Skill[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  // Which creation workflow form is open: author a new skill, search the web for
  // an existing one, or none.
  const [formMode, setFormMode] = useState<"design" | "discover" | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function load() {
    const s = await api.listSkills();
    setSkills(s);
  }

  useEffect(() => {
    load().catch((e) => setError(e.message));
    // Poll so a draft skill flips to its live authoring state and status edits
    // from elsewhere stay fresh.
    const t = setInterval(() => load().catch(() => {}), 5000);
    return () => clearInterval(t);
  }, []);

  // Client-side filtering keeps the search instant; the list is small.
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return skills;
    return skills.filter((sk) =>
      `${sk.name} ${sk.slug} ${sk.description} ${sk.keywords}`.toLowerCase().includes(q),
    );
  }, [skills, query]);

  const sel = skills.find((s) => s.id === selected) ?? null;

  async function createSkill(name: string, description: string, mode: "design" | "discover") {
    setError(null);
    const sk = await api.createSkill(name, description, mode);
    setFormMode(null);
    await load();
    setSelected(sk.id);
  }

  async function setStatus(id: string, status: Skill["status"]) {
    try {
      await api.updateSkill(id, { status });
      await load();
    } catch (e: any) {
      setError(e.message);
    }
  }

  async function remove(sk: Skill) {
    if (!window.confirm(t("skills.deleteConfirm", { name: sk.name }))) return;
    try {
      await api.deleteSkill(sk.id);
      if (selected === sk.id) setSelected(null);
      await load();
    } catch (e: any) {
      setError(e.message);
    }
  }

  async function evolve(id: string) {
    try {
      await api.evolveSkill(id);
      await load();
    } catch (e: any) {
      setError(e.message);
    }
  }

  async function closeSession(id: string) {
    try {
      await api.closeSkillSession(id);
      await load();
    } catch (e: any) {
      setError(e.message);
    }
  }

  return (
    <div className="flex h-full">
      {/* Left: library (search + create + list) */}
      <div
        className={`${
          selected ? "hidden md:block" : "block"
        } w-full md:w-72 shrink-0 border-r border-border p-5 overflow-auto`}
      >
        <div className="flex items-center justify-between mb-1">
          <h2 className="font-semibold">{t("skills.heading")}</h2>
          <span className="mono text-[10px] uppercase text-muted">{skills.length}</span>
        </div>
        <p className="text-xs text-muted mb-4">
          {t("skills.intro")}
        </p>

        <input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={t("skills.searchPlaceholder")}
          className="w-full bg-bg border border-border rounded px-3 py-2 text-sm
                     focus:border-accent-dim outline-none mb-3"
        />

        <div className="flex gap-2 mb-3">
          <button
            onClick={() => setFormMode((m) => (m === "design" ? null : "design"))}
            className={`flex-1 font-medium rounded px-3 py-2 text-sm transition-colors ${
              formMode === "design"
                ? "bg-panel-2 text-fg border border-accent-dim"
                : "bg-accent text-bg hover:bg-accent-dim"
            }`}
          >
            {t("skills.create")}
          </button>
          <button
            onClick={() => setFormMode((m) => (m === "discover" ? null : "discover"))}
            title={t("skills.discoverTitle")}
            className={`flex-1 font-medium rounded px-3 py-2 text-sm border transition-colors ${
              formMode === "discover"
                ? "bg-panel-2 text-fg border-accent-dim"
                : "border-accent-dim text-accent hover:bg-panel-2"
            }`}
          >
            {t("skills.discover")}
          </button>
        </div>

        {formMode && (
          <CreateForm key={formMode} mode={formMode} onCreate={createSkill} onError={setError} />
        )}

        <div className="space-y-1">
          {filtered.map((sk) => (
            <div
              key={sk.id}
              onClick={() => setSelected(sk.id)}
              className={`cursor-pointer rounded px-2 py-2 text-xs border ${
                selected === sk.id
                  ? "border-accent-dim bg-panel-2"
                  : "border-border hover:border-accent-dim/50"
              }`}
            >
              <div className="flex items-center justify-between gap-2">
                <span className="truncate font-medium text-fg">{sk.name}</span>
                <StatusBadge status={sk.status} />
              </div>
              <div className="mono text-[11px] text-muted truncate">{sk.slug}</div>
              {sk.description && (
                <div className="mt-1 text-[11px] text-muted line-clamp-2" title={sk.description}>
                  {sk.description}
                </div>
              )}
            </div>
          ))}
          {filtered.length === 0 && (
            <div className="text-muted text-xs">
              {skills.length === 0 ? t("skills.emptyNoSkills") : t("skills.emptyNoResults")}
            </div>
          )}
        </div>

        {error && <div className="text-danger text-xs mt-4">{error}</div>}
      </div>

      {/* Right: selected skill detail + its authoring session */}
      <div className={`${selected ? "flex" : "hidden md:flex"} flex-1 min-w-0 flex-col`}>
        {sel ? (
          <SkillDetail
            key={sel.id}
            skill={sel}
            onBack={() => setSelected(null)}
            onValidate={() => setStatus(sel.id, "enabled")}
            onDisable={() => setStatus(sel.id, "disabled")}
            onEnable={() => setStatus(sel.id, "enabled")}
            onEvolve={() => evolve(sel.id)}
            onCloseSession={() => closeSession(sel.id)}
            onDelete={() => remove(sel)}
          />
        ) : (
          <div className="p-8 text-muted">
            {t("skills.selectPrompt")}
          </div>
        )}
      </div>
    </div>
  );
}

// CreateForm collects the skill's name and goal, then launches the background
// session. In "design" mode the goal is the design brief; in "discover" mode it
// is the search query Claude uses to find an existing skill on the web.
function CreateForm({
  mode,
  onCreate,
  onError,
}: {
  mode: "design" | "discover";
  onCreate: (name: string, description: string, mode: "design" | "discover") => Promise<void>;
  onError: (msg: string) => void;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);
  const discover = mode === "discover";

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    try {
      await onCreate(name.trim(), description.trim(), mode);
      setName("");
      setDescription("");
    } catch (err: any) {
      onError(err.message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={submit} className="bg-panel border border-border rounded-lg p-3 mb-3 flex flex-col gap-2">
      {discover && (
        <p className="text-[11px] text-muted">
          {t("skills.discoverHint")}
        </p>
      )}
      <input
        value={name}
        onChange={(e) => setName(e.target.value)}
        placeholder={discover ? t("skills.namePlaceholderDiscover") : t("skills.namePlaceholder")}
        className="bg-bg border border-border rounded px-3 py-2 text-sm focus:border-accent-dim outline-none"
        autoFocus
      />
      <textarea
        value={description}
        onChange={(e) => setDescription(e.target.value)}
        placeholder={
          discover
            ? t("skills.descPlaceholderDiscover")
            : t("skills.descPlaceholder")
        }
        rows={3}
        className="bg-bg border border-border rounded px-3 py-2 text-sm focus:border-accent-dim outline-none resize-y"
      />
      <button
        type="submit"
        disabled={busy || !name.trim()}
        className="bg-accent text-bg font-medium rounded px-3 py-2 text-sm
                   disabled:opacity-40 hover:bg-accent-dim transition-colors"
      >
        {busy ? t("skills.launching") : discover ? t("skills.submitDiscover") : t("skills.submitDesign")}
      </button>
    </form>
  );
}

// SkillDetail shows the skill header, validation controls, and the live stream
// of its background authoring session (read-only replay once finished).
function SkillDetail({
  skill,
  onBack,
  onValidate,
  onDisable,
  onEnable,
  onEvolve,
  onCloseSession,
  onDelete,
}: {
  skill: Skill;
  onBack: () => void;
  onValidate: () => void;
  onDisable: () => void;
  onEnable: () => void;
  onEvolve: () => void;
  onCloseSession: () => void;
  onDelete: () => void;
}) {
  const { t } = useTranslation();
  // Resolve and poll the editing session's liveness so SessionStream attaches
  // (live) or replays the journal (finished), and the action buttons reflect
  // whether a session is currently running. No session id → nothing live.
  const [live, setLive] = useState(false);
  useEffect(() => {
    if (!skill.sessionId) {
      setLive(false);
      return;
    }
    let cancelled = false;
    const check = () =>
      api
        .getSession(skill.sessionId!)
        .then((s) => {
          if (!cancelled) setLive(s.state === "running" || s.state === "starting");
        })
        .catch(() => {});
    check();
    const t = setInterval(check, 4000);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
  }, [skill.sessionId]);

  return (
    <>
      <button
        onClick={onBack}
        className="md:hidden text-left text-accent text-sm px-4 py-2 border-b border-border"
      >
        {t("skills.backToSkills")}
      </button>
      <div className="border-b border-border px-4 py-3 flex items-center gap-3 flex-wrap">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="font-semibold truncate">{skill.name}</span>
            <StatusBadge status={skill.status} />
          </div>
          <div className="mono text-[11px] text-muted truncate">
            ~/.claude/skills/{skill.slug}/SKILL.md
          </div>
          <div className="text-[11px] mt-0.5">
            {live ? (
              <span className="text-accent">{t("skills.sessionActive")}</span>
            ) : (
              <span className="text-muted">{t("skills.sessionClosed")}</span>
            )}
          </div>
        </div>
        <div className="flex items-center gap-2 ml-auto text-sm flex-wrap justify-end">
          {live ? (
            <button
              onClick={onCloseSession}
              className="border border-border text-muted rounded px-3 py-1.5 hover:text-fg transition-colors"
              title={t("skills.closeSessionTitle")}
            >
              {t("skills.closeSession")}
            </button>
          ) : (
            <button
              onClick={onEvolve}
              className="border border-accent-dim text-accent rounded px-3 py-1.5 hover:bg-panel-2 transition-colors"
              title={t("skills.evolveTitle")}
            >
              {t("skills.evolve")}
            </button>
          )}
          {skill.status !== "enabled" ? (
            <button
              onClick={skill.status === "disabled" ? onEnable : onValidate}
              className="bg-accent text-bg font-medium rounded px-3 py-1.5 hover:bg-accent-dim transition-colors"
              title={t("skills.enableTitle")}
            >
              {skill.status === "disabled" ? t("skills.reactivate") : t("skills.validate")}
            </button>
          ) : (
            <button
              onClick={onDisable}
              className="border border-border text-muted rounded px-3 py-1.5 hover:text-fg transition-colors"
            >
              {t("skills.disable")}
            </button>
          )}
          <button
            onClick={onDelete}
            className="text-danger rounded px-2 py-1.5 hover:underline"
          >
            {t("common.delete")}
          </button>
        </div>
      </div>
      <div className="flex-1 min-h-0">
        {skill.sessionId ? (
          <SessionStream key={skill.sessionId} sessionId={skill.sessionId} live={live} />
        ) : (
          <div className="p-8 text-muted">
            {t("skills.noSession")}
          </div>
        )}
      </div>
    </>
  );
}

function StatusBadge({ status }: { status: Skill["status"] }) {
  const { t } = useTranslation();
  const map: Record<Skill["status"], string> = {
    draft: "text-accent",
    enabled: "text-ok",
    disabled: "text-muted",
  };
  const label: Record<Skill["status"], string> = {
    draft: t("skills.statusDraft"),
    enabled: t("skills.statusEnabled"),
    disabled: t("skills.statusDisabled"),
  };
  return <span className={`mono text-[10px] uppercase shrink-0 ${map[status]}`}>{label[status]}</span>;
}
