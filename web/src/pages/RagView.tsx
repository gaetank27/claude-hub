import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { api, type RagCollection, type RagStatus } from "../lib/api";

type Sort = "recent" | "name" | "docs";

// RagView is the global RAG knowledge base, organized as tiles: each collection is
// a unit ("mini-project"), with a top bar for search, category filters and sort.
// Clicking a tile opens its dedicated page (/rag/:id). Collections are shared by
// all projects and queried by sessions through the knowledge_search MCP tool.
export function RagView() {
  const { t } = useTranslation();
  const [cols, setCols] = useState<RagCollection[]>([]);
  const [status, setStatus] = useState<RagStatus | null>(null);
  const [query, setQuery] = useState("");
  const [category, setCategory] = useState<string | null>(null); // null = all
  const [sort, setSort] = useState<Sort>("recent");
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function load() {
    const [c, st] = await Promise.all([api.listRagCollections(), api.ragStatus()]);
    setCols(c);
    setStatus(st);
  }
  useEffect(() => {
    load().catch((e) => setError(e.message));
    const timer = setInterval(() => load().catch(() => {}), 5000);
    return () => clearInterval(timer);
  }, []);

  const categories = useMemo(() => {
    const set = new Set<string>();
    for (const c of cols) if (c.category) set.add(c.category);
    return Array.from(set).sort();
  }, [cols]);

  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    let out = cols.filter((c) => {
      if (category !== null && (c.category || "") !== category) return false;
      if (!q) return true;
      return `${c.name} ${c.category} ${c.description}`.toLowerCase().includes(q);
    });
    out = [...out].sort((a, b) => {
      if (sort === "name") return a.name.localeCompare(b.name);
      if (sort === "docs") return b.docCount - a.docCount;
      return b.createdAt.localeCompare(a.createdAt); // recent
    });
    return out;
  }, [cols, query, category, sort]);

  async function create(name: string, cat: string) {
    setError(null);
    await api.createRagCollection(name, undefined, cat);
    setCreating(false);
    await load();
  }

  return (
    <div className="p-4 md:p-8 max-w-5xl mx-auto w-full">
      <div className="flex items-baseline justify-between gap-2 flex-wrap mb-1">
        <h1 className="text-2xl font-semibold">{t("rag.title")}</h1>
        <span className="text-xs text-muted">
          {t("rag.knowledgeBasesCount", { count: cols.length })}
        </span>
      </div>
      <p className="text-sm text-muted mb-4">
        {t("rag.subtitle", { tool: "knowledge_search" }).split("knowledge_search").flatMap((part, i) =>
          i === 0 ? [part] : [<span key={i} className="mono">knowledge_search</span>, part],
        )}
      </p>

      {status && !status.available && (
        <div className="bg-panel border border-danger/40 text-danger rounded-lg px-4 py-3 text-sm mb-4">
          {t("rag.embeddingsPaused")}
        </div>
      )}
      {error && <div className="text-danger text-sm mb-3">{error}</div>}

      {/* Top bar: search + sort + create */}
      <div className="flex items-center gap-2 flex-wrap mb-3">
        <input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={t("rag.searchBasePlaceholder")}
          className="flex-1 min-w-[12rem] bg-bg border border-border rounded px-3 py-2 text-sm
                     focus:border-accent-dim outline-none"
        />
        <select
          value={sort}
          onChange={(e) => setSort(e.target.value as Sort)}
          className="bg-bg border border-border rounded px-3 py-2 text-sm focus:border-accent-dim outline-none"
        >
          <option value="recent">{t("rag.sortRecent")}</option>
          <option value="name">{t("rag.sortName")}</option>
          <option value="docs">{t("rag.sortDocs")}</option>
        </select>
        <button
          onClick={() => setCreating((c) => !c)}
          className="bg-accent text-bg font-medium rounded px-3 py-2 text-sm hover:bg-accent-dim transition-colors"
        >
          {creating ? t("common.cancel") : t("rag.newBase")}
        </button>
      </div>

      {/* Category filters */}
      {categories.length > 0 && (
        <div className="flex items-center gap-2 flex-wrap mb-4">
          <FilterChip label={t("rag.filterAll")} active={category === null} onClick={() => setCategory(null)} />
          {categories.map((c) => (
            <FilterChip key={c} label={c} active={category === c} onClick={() => setCategory(c)} />
          ))}
        </div>
      )}

      {creating && <CreateForm onCreate={create} onError={setError} />}

      {/* Tiles */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
        {shown.map((c) => (
          <Link
            key={c.id}
            to={`/rag/${c.id}`}
            className="bg-panel border border-border rounded-lg p-4 hover:border-accent-dim transition-colors"
          >
            <div className="flex items-center justify-between gap-2">
              <span className="font-medium truncate">{c.name}</span>
              {c.isDefault && <span className="mono text-[10px] uppercase text-muted">{t("rag.defaultBadge")}</span>}
            </div>
            {c.description && (
              <div className="text-xs text-muted mt-1 line-clamp-2">{c.description}</div>
            )}
            <div className="flex items-center gap-2 mt-3 text-[11px] text-muted">
              <span>{t("rag.docCount", { count: c.docCount })}</span>
              {c.category && (
                <span className="ml-auto uppercase tracking-wider border border-border rounded px-1.5 py-0.5">
                  {c.category}
                </span>
              )}
            </div>
          </Link>
        ))}
        {shown.length === 0 && (
          <div className="text-muted text-sm py-8 col-span-full text-center">{t("rag.noBase")}</div>
        )}
      </div>
    </div>
  );
}

function FilterChip({ label, active, onClick }: { label: string; active: boolean; onClick: () => void }) {
  return (
    <button
      onClick={onClick}
      className={`text-sm rounded-full px-3 py-1 border transition-colors ${
        active
          ? "bg-accent text-bg border-accent"
          : "border-border text-muted hover:text-fg hover:border-accent-dim"
      }`}
    >
      {label}
    </button>
  );
}

// CreateForm collects a new collection's name and category.
function CreateForm({
  onCreate,
  onError,
}: {
  onCreate: (name: string, category: string) => Promise<void>;
  onError: (msg: string) => void;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState("");
  const [category, setCategory] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    try {
      await onCreate(name.trim(), category.trim());
      setName("");
      setCategory("");
    } catch (err: any) {
      onError(err.message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={submit} className="bg-panel border border-border rounded-lg p-3 mb-4 flex flex-wrap gap-2 items-end">
      <div className="flex flex-col gap-1">
        <label className="text-xs text-muted">{t("rag.createNameLabel")}</label>
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={t("rag.createNamePlaceholder")}
          autoFocus
          className="bg-bg border border-border rounded px-3 py-2 text-sm w-48 focus:border-accent-dim outline-none"
        />
      </div>
      <div className="flex flex-col gap-1">
        <label className="text-xs text-muted">{t("rag.createCategoryLabel")}</label>
        <input
          value={category}
          onChange={(e) => setCategory(e.target.value)}
          placeholder={t("rag.createCategoryPlaceholder")}
          className="bg-bg border border-border rounded px-3 py-2 text-sm w-48 focus:border-accent-dim outline-none"
        />
      </div>
      <button
        type="submit"
        disabled={busy || !name.trim()}
        className="bg-accent text-bg font-medium rounded px-4 py-2 text-sm disabled:opacity-40 hover:bg-accent-dim transition-colors"
      >
        {t("common.create")}
      </button>
    </form>
  );
}
