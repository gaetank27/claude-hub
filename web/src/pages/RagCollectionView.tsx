import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { api, type RagCollection, type RagDocument, type RagStatus } from "../lib/api";

// ContentModal shows the raw text content of a RAG document in a scrollable overlay.
function ContentModal({ doc, onClose }: { doc: RagDocument; onClose: () => void }) {
  const { t } = useTranslation();
  const [content, setContent] = useState<string | null>(null);
  const [truncated, setTruncated] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.getRagDocumentContent(doc.id)
      .then((r) => { setContent(r.content); setTruncated(r.truncated); })
      .catch(() => setError(t("rag.contentNotAvailable")));
  }, [doc.id, t]);

  // Close on Escape key
  useEffect(() => {
    const handler = (e: KeyboardEvent) => { if (e.key === "Escape") onClose(); };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, [onClose]);

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60"
      onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}
    >
      <div className="bg-panel border border-border rounded-xl shadow-2xl flex flex-col w-full max-w-3xl max-h-[85vh] mx-4">
        <div className="flex items-center justify-between px-4 py-3 border-b border-border shrink-0">
          <h2 className="font-semibold text-sm truncate pr-4">
            {t("rag.contentModalTitle", { title: doc.title })}
          </h2>
          <button
            onClick={onClose}
            className="text-muted hover:text-fg transition-colors text-lg leading-none shrink-0"
          >
            ×
          </button>
        </div>
        <div className="overflow-y-auto flex-1 p-4">
          {error && <p className="text-danger text-sm">{error}</p>}
          {!error && content === null && (
            <p className="text-muted text-sm animate-pulse">{t("common.loading")}</p>
          )}
          {content !== null && (
            <>
              {truncated && (
                <p className="text-xs text-muted mb-3 italic">{t("rag.contentTruncated")}</p>
              )}
              <pre className="text-xs font-mono whitespace-pre-wrap break-words leading-relaxed text-fg">
                {content}
              </pre>
            </>
          )}
        </div>
      </div>
    </div>
  );
}

// RagCollectionView is the dedicated page for one global RAG collection (a
// "mini-project"): its document explorer (search, status filter, enable/disable,
// reindex, delete), an importer (file / web link / pasted text) scoped to this
// collection, and collection management (rename, category, delete).
export function RagCollectionView() {
  const { t } = useTranslation();
  const { id = "" } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [col, setCol] = useState<RagCollection | null>(null);
  const [docs, setDocs] = useState<RagDocument[]>([]);
  const [status, setStatus] = useState<RagStatus | null>(null);
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState<"all" | RagDocument["status"]>("all");
  const [error, setError] = useState<string | null>(null);
  const [viewingDoc, setViewingDoc] = useState<RagDocument | null>(null);

  const load = useCallback(async () => {
    const [c, d, st] = await Promise.all([
      api.getRagCollection(id),
      api.listRagDocuments(undefined, id),
      api.ragStatus(),
    ]);
    setCol(c);
    setDocs(d);
    setStatus(st);
  }, [id]);

  useEffect(() => {
    load().catch((e) => setError(e.message));
    const timer = setInterval(() => load().catch(() => {}), 4000);
    return () => clearInterval(timer);
  }, [load]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    return docs.filter((d) => {
      if (filter !== "all" && d.status !== filter) return false;
      if (!q) return true;
      return `${d.title} ${d.sourceRef} ${d.sourceType}`.toLowerCase().includes(q);
    });
  }, [docs, query, filter]);

  async function afterMutate(fn: () => Promise<unknown>) {
    setError(null);
    try {
      await fn();
      await load();
    } catch (e: any) {
      setError(e.message);
    }
  }

  async function rename() {
    if (!col) return;
    const name = window.prompt(t("rag.renamePrompt"), col.name)?.trim();
    if (name && name !== col.name) await afterMutate(() => api.updateRagCollection(col.id, { name }));
  }
  async function editCategory() {
    if (!col) return;
    const category = window.prompt(t("rag.categoryPrompt"), col.category)?.trim();
    if (category !== undefined && category !== col.category)
      await afterMutate(() => api.updateRagCollection(col.id, { category }));
  }
  async function remove() {
    if (!col) return;
    if (window.confirm(t("rag.deleteCollectionConfirm", { name: col.name, count: col.docCount }))) {
      try {
        await api.deleteRagCollection(col.id);
        navigate("/rag");
      } catch (e: any) {
        setError(e.message);
      }
    }
  }

  return (
    <div className="p-4 md:p-8 max-w-5xl mx-auto w-full">
      <Link to="/rag" className="text-accent text-sm hover:underline">
        {t("rag.backToGlobal")}
      </Link>

      <div className="flex items-start justify-between gap-3 flex-wrap mt-2 mb-1">
        <div>
          <h1 className="text-2xl font-semibold">{col?.name ?? t("rag.collectionFallback")}</h1>
          <div className="flex items-center gap-2 mt-1 text-sm text-muted">
            {col?.category && (
              <span className="text-[11px] uppercase tracking-wider border border-border rounded px-1.5 py-0.5">
                {col.category}
              </span>
            )}
            <span>{t("rag.documentCount", { count: docs.length })}</span>
          </div>
        </div>
        {col && (
          <div className="flex items-center gap-3 text-sm">
            <button onClick={rename} className="text-muted hover:text-fg transition-colors">
              {t("rag.rename")}
            </button>
            <button onClick={editCategory} className="text-muted hover:text-fg transition-colors">
              {t("rag.category")}
            </button>
            {!col.isDefault && (
              <button onClick={remove} className="text-muted hover:text-danger transition-colors">
                {t("rag.delete")}
              </button>
            )}
          </div>
        )}
      </div>

      {col && <DescriptionEditor col={col} onSave={(description) => afterMutate(() => api.updateRagCollection(col.id, { description }))} />}

      {status && !status.available && (
        <div className="bg-panel border border-danger/40 text-danger rounded-lg px-4 py-3 text-sm my-4">
          {t("rag.embeddingsPaused")}
        </div>
      )}

      <div className="my-4">
        <AddSources collectionId={id} collectionLabel={col?.name ?? ""} onAdd={afterMutate} onError={setError} />
      </div>

      {error && <div className="text-danger text-sm my-3">{error}</div>}

      <div className="flex gap-2 items-center my-4 flex-wrap">
        <input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={t("rag.searchDocPlaceholder")}
          className="flex-1 min-w-[12rem] bg-bg border border-border rounded px-3 py-2 text-sm
                     focus:border-accent-dim outline-none"
        />
        <select
          value={filter}
          onChange={(e) => setFilter(e.target.value as typeof filter)}
          className="bg-bg border border-border rounded px-3 py-2 text-sm focus:border-accent-dim outline-none"
        >
          <option value="all">{t("rag.statusAll")}</option>
          <option value="pending">{t("rag.statusPending")}</option>
          <option value="processing">{t("rag.statusProcessing")}</option>
          <option value="indexed">{t("rag.statusIndexed")}</option>
          <option value="error">{t("rag.statusError")}</option>
        </select>
      </div>

      <div className="space-y-2">
        {filtered.length === 0 && (
          <div className="text-muted text-sm py-8 text-center">{t("rag.noDocument")}</div>
        )}
        {filtered.map((d) => (
          <DocRow
            key={d.id}
            doc={d}
            onToggle={() => afterMutate(() => api.updateRagDocument(d.id, { enabled: !d.enabled }))}
            onReindex={() => afterMutate(() => api.reindexRagDocument(d.id))}
            onDelete={() => {
              if (confirm(t("rag.deleteDocConfirm", { title: d.title }))) afterMutate(() => api.deleteRagDocument(d.id));
            }}
            onView={() => setViewingDoc(d)}
          />
        ))}
      </div>

      {viewingDoc && <ContentModal doc={viewingDoc} onClose={() => setViewingDoc(null)} />}
    </div>
  );
}

// DescriptionEditor shows a collection's free-text description (its note) and
// lets the user edit it inline (multi-line). The description doubles as the
// collection's comment and is shown on the RAG tile. Cmd/Ctrl+Enter saves,
// Escape cancels.
function DescriptionEditor({
  col,
  onSave,
}: {
  col: RagCollection;
  onSave: (description: string) => Promise<unknown>;
}) {
  const { t } = useTranslation();
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState(col.description);
  const [busy, setBusy] = useState(false);

  function begin() {
    setValue(col.description);
    setEditing(true);
  }

  async function save() {
    setBusy(true);
    try {
      await onSave(value.trim());
      setEditing(false);
    } finally {
      setBusy(false);
    }
  }

  if (!editing) {
    return (
      <button
        onClick={begin}
        className="group flex items-start gap-1.5 text-xs text-muted hover:text-fg transition-colors mt-2 mb-1 text-left w-full max-w-2xl"
        title={t("rag.editDescriptionTitle")}
      >
        <span className="uppercase tracking-wider shrink-0 text-[11px]">{t("rag.noteLabel")}</span>
        {col.description ? (
          <span className="text-fg whitespace-pre-wrap break-words flex-1">{col.description}</span>
        ) : (
          <span className="italic flex-1">{t("rag.noDescription")}</span>
        )}
        <span className="opacity-0 group-hover:opacity-100 transition-opacity shrink-0">✎</span>
      </button>
    );
  }

  return (
    <div className="flex flex-col gap-1.5 mt-2 mb-1 max-w-2xl">
      <textarea
        value={value}
        autoFocus
        rows={3}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) save();
          if (e.key === "Escape") setEditing(false);
        }}
        placeholder={t("rag.descriptionPlaceholder")}
        className="bg-bg border border-border rounded px-2 py-1 text-sm w-full resize-y focus:border-accent-dim outline-none"
      />
      <div className="flex items-center gap-1.5">
        <button
          onClick={save}
          disabled={busy}
          className="text-xs bg-accent text-bg rounded px-2 py-1 disabled:opacity-40 hover:bg-accent-dim transition-colors"
        >
          OK
        </button>
        <button
          onClick={() => setEditing(false)}
          className="text-xs text-muted hover:text-fg transition-colors px-1"
        >
          {t("common.cancel")}
        </button>
      </div>
    </div>
  );
}

function DocRow({
  doc,
  onToggle,
  onReindex,
  onDelete,
  onView,
}: {
  doc: RagDocument;
  onToggle: () => void;
  onReindex: () => void;
  onDelete: () => void;
  onView: () => void;
}) {
  const { t } = useTranslation();
  return (
    <div
      className={`bg-panel border border-border rounded-lg p-3 flex items-start gap-3 ${
        doc.enabled ? "" : "opacity-50"
      }`}
    >
      <SourceIcon type={doc.sourceType} />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2 flex-wrap">
          <button
            onClick={onView}
            className="font-medium truncate hover:text-accent transition-colors text-left"
            title={t("rag.viewContent")}
          >
            {doc.title}
          </button>
          <StatusBadge status={doc.status} />
          {!doc.enabled && <span className="text-[11px] text-muted">{t("rag.docDisabled")}</span>}
        </div>
        <div className="text-xs text-muted truncate mt-0.5">
          {doc.sourceType}
          {doc.sourceRef ? ` · ${doc.sourceRef}` : ""}
          {doc.status === "indexed" ? ` · ${t("rag.chunkCount", { count: doc.chunkCount })}` : ""}
        </div>
        {doc.status === "error" && doc.error && (
          <div className="text-xs text-danger mt-1 break-words">{doc.error}</div>
        )}
      </div>
      <div className="flex items-center gap-3 shrink-0 text-xs">
        <button onClick={onView} className="text-muted hover:text-accent transition-colors">
          {t("rag.viewContent")}
        </button>
        <button onClick={onToggle} className="text-muted hover:text-fg transition-colors">
          {doc.enabled ? t("rag.docDisable") : t("rag.docEnable")}
        </button>
        {(doc.status === "error" || doc.status === "indexed") && (
          <button onClick={onReindex} className="text-muted hover:text-accent transition-colors">
            {t("rag.reindex")}
          </button>
        )}
        <button onClick={onDelete} className="text-muted hover:text-danger transition-colors">
          {t("rag.delete")}
        </button>
      </div>
    </div>
  );
}

function StatusBadge({ status }: { status: RagDocument["status"] }) {
  const { t } = useTranslation();
  const map: Record<RagDocument["status"], { label: string; cls: string }> = {
    pending: { label: t("rag.badgePending"), cls: "bg-panel-2 text-muted" },
    processing: { label: t("rag.badgeProcessing"), cls: "bg-panel-2 text-accent animate-pulse" },
    indexed: { label: t("rag.badgeIndexed"), cls: "bg-ok/15 text-ok" },
    error: { label: t("rag.badgeError"), cls: "bg-danger/15 text-danger" },
  };
  const m = map[status];
  return <span className={`text-[11px] px-1.5 py-0.5 rounded ${m.cls}`}>{m.label}</span>;
}

// AddSources imports a file (click/drag-drop), a web link, or pasted text into the
// collection; each mode queues asynchronous indexing on the backend.
function AddSources({
  collectionId,
  collectionLabel,
  onAdd,
  onError,
}: {
  collectionId: string;
  collectionLabel: string;
  onAdd: (fn: () => Promise<unknown>) => Promise<void>;
  onError: (msg: string) => void;
}) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<"file" | "url" | "text">("file");
  const [url, setUrl] = useState("");
  const [title, setTitle] = useState("");
  const [text, setText] = useState("");
  const [dragging, setDragging] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);

  async function submitFiles(files: FileList | null) {
    if (!files || files.length === 0) return;
    for (const f of Array.from(files)) {
      await onAdd(() => api.uploadRagFile(f, undefined, collectionId));
    }
  }

  return (
    <div className="bg-panel border border-border rounded-lg p-3">
      <div className="flex gap-2 mb-3 flex-wrap items-center">
        {(["file", "url", "text"] as const).map((m) => (
          <button
            key={m}
            onClick={() => setMode(m)}
            className={`px-3 py-1.5 rounded text-sm transition-colors ${
              mode === m ? "bg-panel-2 text-fg border border-accent-dim" : "text-muted hover:text-fg"
            }`}
          >
            {m === "file" ? t("rag.modeFile") : m === "url" ? t("rag.modeUrl") : t("rag.modeText")}
          </button>
        ))}
        {collectionLabel && (
          <span className="text-xs text-muted ml-auto">
            {t("rag.addingTo")} <span className="text-fg">{collectionLabel}</span>
          </span>
        )}
      </div>

      {mode === "file" && (
        <div
          onDragOver={(e) => {
            e.preventDefault();
            setDragging(true);
          }}
          onDragLeave={() => setDragging(false)}
          onDrop={(e) => {
            e.preventDefault();
            setDragging(false);
            submitFiles(e.dataTransfer.files);
          }}
          onClick={() => fileInput.current?.click()}
          className={`border border-dashed rounded-lg px-4 py-8 text-center cursor-pointer
                      transition-colors text-sm ${
                        dragging ? "border-accent bg-panel-2" : "border-border text-muted hover:border-accent-dim"
                      }`}
        >
          {t("rag.dropFile")}
          <div className="text-xs text-muted mt-1">{t("rag.fileHint")}</div>
          <input
            ref={fileInput}
            type="file"
            accept=".txt,.md,.markdown,.pdf,text/plain,text/markdown,application/pdf"
            multiple
            className="hidden"
            onChange={(e) => {
              submitFiles(e.target.files);
              e.target.value = "";
            }}
          />
        </div>
      )}

      {mode === "url" && (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (!url.trim()) return;
            onAdd(() => api.addRagURL(url.trim(), title.trim() || undefined, collectionId)).then(() => {
              setUrl("");
              setTitle("");
            });
          }}
          className="flex flex-col gap-2"
        >
          <input
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder={t("rag.urlPlaceholder")}
            className="bg-bg border border-border rounded px-3 py-2 text-sm focus:border-accent-dim outline-none"
          />
          <input
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder={t("rag.titleOptionalPlaceholder")}
            className="bg-bg border border-border rounded px-3 py-2 text-sm focus:border-accent-dim outline-none"
          />
          <SubmitButton disabled={!url.trim()} label={t("rag.importLink")} />
        </form>
      )}

      {mode === "text" && (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (!text.trim()) {
              onError(t("rag.textRequired"));
              return;
            }
            onAdd(() => api.addRagText(title.trim() || t("rag.pastedTextDefaultTitle"), text.trim(), collectionId)).then(() => {
              setText("");
              setTitle("");
            });
          }}
          className="flex flex-col gap-2"
        >
          <input
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder={t("rag.titlePlaceholder")}
            className="bg-bg border border-border rounded px-3 py-2 text-sm focus:border-accent-dim outline-none"
          />
          <textarea
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder={t("rag.textContentPlaceholder")}
            rows={5}
            className="bg-bg border border-border rounded px-3 py-2 text-sm focus:border-accent-dim outline-none resize-y"
          />
          <SubmitButton disabled={!text.trim()} label={t("rag.addText")} />
        </form>
      )}
    </div>
  );
}

function SubmitButton({ disabled, label }: { disabled: boolean; label: string }) {
  return (
    <button
      type="submit"
      disabled={disabled}
      className="self-start bg-accent text-bg font-medium rounded px-4 py-2 text-sm
                 disabled:opacity-40 hover:bg-accent-dim transition-colors"
    >
      {label}
    </button>
  );
}

function SourceIcon({ type }: { type: RagDocument["sourceType"] }) {
  const label = { text: "TXT", markdown: "MD", pdf: "PDF", web: "WEB", code: "CODE" }[type] ?? "DOC";
  return (
    <span className="mono text-[10px] text-muted border border-border rounded px-1.5 py-1 shrink-0 mt-0.5">
      {label}
    </span>
  );
}
