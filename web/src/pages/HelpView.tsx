import { useEffect, useMemo, useRef, useState } from "react";
import { useLocation } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { getHelpChapters, type HelpBlock, type HelpChapter } from "../lib/help";

// HelpView renders the in-app documentation from the structured content in
// lib/help.ts. Two layouts share the same content:
//   - interactive (default): a chapter table of contents on the left (desktop)
//     or an accordion (mobile), a live text filter, and one chapter shown at a
//     time;
//   - print (?print): every chapter expanded top-to-bottom with chrome removed,
//     used by cmd/docgen as the single source for the exported PDF manual.
export function HelpView() {
  const { t, i18n } = useTranslation();
  const lang = (i18n.resolvedLanguage || i18n.language || "en").startsWith("fr")
    ? "fr"
    : "en";
  const isPrint = new URLSearchParams(useLocation().search).has("print");
  const [query, setQuery] = useState("");

  // Source chapters in the active UI language.
  const allChapters = useMemo(() => getHelpChapters(lang), [lang]);
  const [active, setActive] = useState<string>(allChapters[0]?.id ?? "");

  // Filter chapters by a case-insensitive match across titles, taglines,
  // headings and block text. Keeps the whole chapter when any part matches.
  const chapters = useMemo(
    () => filterChapters(allChapters, query),
    [allChapters, query],
  );

  // Keep the active chapter valid as the filter narrows the list.
  useEffect(() => {
    if (chapters.length && !chapters.some((c) => c.id === active)) {
      setActive(chapters[0].id);
    }
  }, [chapters, active]);

  if (isPrint) {
    return (
      <div className="print-manual p-8 max-w-3xl mx-auto w-full">
        <header className="mb-8">
          <h1 className="text-3xl font-semibold">{t("help.printTitle")}</h1>
          <p className="text-muted mt-1">{t("help.printSubtitle")}</p>
        </header>
        {allChapters.map((ch) => (
          <ChapterArticle key={ch.id} chapter={ch} eagerImages />
        ))}
      </div>
    );
  }

  const current = chapters.find((c) => c.id === active) ?? chapters[0] ?? null;

  return (
    <div className="p-4 md:p-8 max-w-5xl mx-auto w-full">
      <h1 className="text-2xl font-semibold mb-1">{t("help.title")}</h1>
      <p className="text-muted text-sm mb-4">{t("help.intro")}</p>

      <input
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        placeholder={t("help.searchPlaceholder")}
        className="w-full bg-bg border border-border rounded px-3 py-2 text-sm focus:border-accent-dim outline-none mb-6"
      />

      {chapters.length === 0 ? (
        <div className="text-muted text-sm py-6 text-center bg-panel border border-border rounded-lg">
          {t("help.noResults", { query })}
        </div>
      ) : (
        <div className="md:flex md:gap-8 md:items-start">
          {/* Table of contents: sticky list on desktop, plain list on mobile. */}
          <nav className="md:w-56 md:shrink-0 md:sticky md:top-4 mb-4 md:mb-0">
            <ul className="flex flex-col gap-1">
              {chapters.map((ch) => (
                <li key={ch.id}>
                  <button
                    onClick={() => setActive(ch.id)}
                    className={`w-full text-left rounded px-3 py-2 text-sm transition-colors ${
                      current?.id === ch.id
                        ? "bg-panel-2 text-accent border-l-2 border-accent"
                        : "text-muted hover:text-fg border-l-2 border-transparent"
                    }`}
                  >
                    {ch.title}
                  </button>
                </li>
              ))}
            </ul>
          </nav>

          <div className="min-w-0 flex-1">
            {current && <ChapterArticle chapter={current} />}
          </div>
        </div>
      )}
    </div>
  );
}

// ChapterArticle renders one chapter (title + tagline + its sections). Shared by
// the interactive and print layouts so the PDF matches the online help exactly.
// In print mode (eagerImages) screenshots load eagerly so PrintToPDF captures
// every figure — lazy off-screen images never decode for a non-scrolled print.
function ChapterArticle({
  chapter,
  eagerImages = false,
}: {
  chapter: HelpChapter;
  eagerImages?: boolean;
}) {
  return (
    <article className="help-chapter mb-10">
      <h2 className="text-xl font-semibold">{chapter.title}</h2>
      <p className="text-muted text-sm mb-4">{chapter.tagline}</p>
      {chapter.sections.map((sec, i) => (
        <section key={i} className="mb-6">
          <h3 className="text-xs font-semibold uppercase tracking-wider text-muted mb-2">
            {sec.heading}
          </h3>
          <div className="flex flex-col gap-3">
            {sec.blocks.map((b, j) => (
              <Block key={j} block={b} eagerImages={eagerImages} />
            ))}
          </div>
        </section>
      ))}
    </article>
  );
}

function Block({ block, eagerImages }: { block: HelpBlock; eagerImages: boolean }) {
  switch (block.kind) {
    case "p":
      return <p className="text-sm leading-relaxed">{block.text}</p>;
    case "list":
      return (
        <ul className="list-disc pl-5 text-sm leading-relaxed flex flex-col gap-1">
          {block.items.map((it, i) => (
            <li key={i}>{it}</li>
          ))}
        </ul>
      );
    case "steps":
      return (
        <ol className="list-decimal pl-5 text-sm leading-relaxed flex flex-col gap-1">
          {block.items.map((it, i) => (
            <li key={i}>{it}</li>
          ))}
        </ol>
      );
    case "note":
      return (
        <div className="text-sm leading-relaxed border-l-2 border-accent bg-panel-2 rounded-r px-3 py-2 text-muted">
          {block.text}
        </div>
      );
    case "shot":
      return <Shot src={block.src} caption={block.caption} eager={eagerImages} />;
  }
}

// Shot renders a screenshot figure. If the PNG hasn't been generated yet
// (cmd/docgen not run), it degrades to a discreet placeholder instead of a
// broken-image icon.
function Shot({ src, caption, eager }: { src: string; caption?: string; eager?: boolean }) {
  const { t } = useTranslation();
  const [failed, setFailed] = useState(false);
  const ref = useRef<HTMLImageElement>(null);

  if (failed) {
    return (
      <figure className="my-1">
        <div className="border border-dashed border-border rounded-lg bg-panel-2 text-muted text-xs px-4 py-8 text-center">
          {t("help.shotPlaceholder")}
        </div>
        {caption && <figcaption className="text-xs text-muted mt-1">{caption}</figcaption>}
      </figure>
    );
  }

  return (
    <figure className="my-1">
      <img
        ref={ref}
        src={src}
        alt={caption ?? t("help.imageAlt")}
        loading={eager ? "eager" : "lazy"}
        onError={() => setFailed(true)}
        className="w-full rounded-lg border border-border bg-panel"
      />
      {caption && <figcaption className="text-xs text-muted mt-1">{caption}</figcaption>}
    </figure>
  );
}

function filterChapters(chapters: HelpChapter[], query: string): HelpChapter[] {
  const q = query.trim().toLowerCase();
  if (!q) return chapters;
  return chapters.filter((ch) => {
    const hay = [
      ch.title,
      ch.tagline,
      ...ch.sections.flatMap((s) => [
        s.heading,
        ...s.blocks.flatMap((b) =>
          b.kind === "p" || b.kind === "note"
            ? [b.text]
            : b.kind === "list" || b.kind === "steps"
              ? b.items
              : b.kind === "shot"
                ? [b.caption ?? ""]
                : [],
        ),
      ]),
    ]
      .join(" ")
      .toLowerCase();
    return hay.includes(q);
  });
}
