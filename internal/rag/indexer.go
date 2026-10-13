package rag

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"system/internal/store"
)

// Indexer owns the asynchronous ingestion pipeline: it persists raw sources to
// disk, then parses, chunks and embeds them off the request path, recording
// status transitions in the store. A single worker goroutine drains the queue so
// the embeddings provider is never hammered, and a periodic poll re-drives any
// document left `pending`/`processing` (a drop, or an interrupted boot).
type Indexer struct {
	db   *store.DB
	emb  Embedder // nil when no provider is configured
	root string   // <DataDir>/rag

	queue    chan string
	inflight sync.Map // doc id -> struct{}, dedups queued/processing work
}

// NewIndexer builds the indexer. emb may be nil (no provider configured), in
// which case documents stay `pending` and Available reports false.
func NewIndexer(db *store.DB, emb Embedder, dataDir string) *Indexer {
	return &Indexer{
		db:    db,
		emb:   emb,
		root:  filepath.Join(dataDir, "rag"),
		queue: make(chan string, 1024),
	}
}

// Available reports whether an embeddings provider is configured.
func (ix *Indexer) Available() bool { return ix.emb != nil }

// EmbedderName identifies the active provider/model ("" when none).
func (ix *Indexer) EmbedderName() string {
	if ix.emb == nil {
		return ""
	}
	return ix.emb.Name()
}

// SourceDir is the on-disk home of a document's raw bytes / fetched text.
func (ix *Indexer) SourceDir(id string) string { return filepath.Join(ix.root, id) }

func (ix *Indexer) sourcePath(id string) string { return filepath.Join(ix.SourceDir(id), "source") }

// WriteSource persists a document's raw bytes so it can be re-indexed without
// re-uploading.
func (ix *Indexer) WriteSource(id string, raw []byte) error {
	if err := os.MkdirAll(ix.SourceDir(id), 0o700); err != nil {
		return err
	}
	return os.WriteFile(ix.sourcePath(id), raw, 0o600)
}

// Remove deletes a document's on-disk source directory.
func (ix *Indexer) Remove(id string) error { return os.RemoveAll(ix.SourceDir(id)) }

// ReadSource returns the document's plain-text content for preview. For web
// documents the cached copy is used (no live re-fetch). Returns an error when
// the source file does not yet exist (pending document) or cannot be read.
func (ix *Indexer) ReadSource(_ context.Context, doc store.RagDocument) (string, error) {
	if doc.SourceType == store.RagSourceCode {
		abs, err := ix.codeFilePath(doc)
		if err != nil {
			return "", err
		}
		raw, err := os.ReadFile(abs)
		if err != nil {
			return "", fmt.Errorf("read code file: %w", err)
		}
		return string(raw), nil
	}
	// Web: sourcePath holds already-extracted plain text written by loadText.
	// Other types: raw bytes, run through the appropriate extractor.
	raw, err := os.ReadFile(ix.sourcePath(doc.ID))
	if err != nil {
		return "", fmt.Errorf("source not yet available: %w", err)
	}
	if doc.SourceType == store.RagSourceWeb {
		return string(raw), nil
	}
	return ExtractText(doc.SourceType, raw)
}

// Enqueue schedules a document for (re-)indexing. It dedups against in-flight
// work and never blocks the caller; if the queue is momentarily full the periodic
// poll will pick the document up.
func (ix *Indexer) Enqueue(id string) {
	if _, loaded := ix.inflight.LoadOrStore(id, struct{}{}); loaded {
		return
	}
	select {
	case ix.queue <- id:
	default:
		ix.inflight.Delete(id)
	}
}

// Run is the worker loop. It resumes unfinished work, then processes the queue
// until ctx is cancelled, re-scanning the store periodically as a safety net.
func (ix *Indexer) Run(ctx context.Context) {
	ix.resume()
	poll := time.NewTicker(30 * time.Second)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-ix.queue:
			ix.process(ctx, id)
		case <-poll.C:
			ix.resume()
		}
	}
}

// resume enqueues every document not yet in a terminal state. A `processing` row
// means a previous run was interrupted mid-flight; re-processing is idempotent
// (ReplaceChunks swaps the whole vector set atomically).
func (ix *Indexer) resume() {
	docs, err := ix.db.ListRagDocumentsByStatus(store.RagPending, store.RagProcessing)
	if err != nil {
		log.Printf("rag: list pending: %v", err)
		return
	}
	for _, d := range docs {
		ix.Enqueue(d.ID)
	}
}

func (ix *Indexer) process(ctx context.Context, id string) {
	defer ix.inflight.Delete(id)

	doc, err := ix.db.GetRagDocument(id)
	if err != nil {
		return // deleted in the meantime
	}
	if ix.emb == nil {
		// No provider: leave it pending so it indexes once one is configured.
		return
	}
	_ = ix.db.SetRagDocumentStatus(id, store.RagProcessing, "", doc.ChunkCount, doc.IndexedAt)

	chunks, err := ix.buildChunks(ctx, doc)
	if err != nil {
		log.Printf("rag: index %s: %v", id, err)
		_ = ix.db.SetRagDocumentStatus(id, store.RagError, err.Error(), doc.ChunkCount, doc.IndexedAt)
		return
	}
	if err := ix.db.ReplaceChunks(id, chunks); err != nil {
		_ = ix.db.SetRagDocumentStatus(id, store.RagError, err.Error(), doc.ChunkCount, doc.IndexedAt)
		return
	}
	now := time.Now()
	_ = ix.db.SetRagDocumentStatus(id, store.RagIndexed, "", len(chunks), &now)
	log.Printf("rag: indexed %s (%q) — %d chunks via %s", id, doc.Title, len(chunks), ix.emb.Name())
}

// buildChunks loads the document's text, splits it and embeds the pieces.
func (ix *Indexer) buildChunks(ctx context.Context, doc store.RagDocument) ([]store.RagChunk, error) {
	text, err := ix.loadText(ctx, doc)
	if err != nil {
		return nil, err
	}
	pieces := Chunkify(text)
	if len(pieces) == 0 {
		return nil, fmt.Errorf("no extractable text")
	}
	texts := make([]string, len(pieces))
	for i, p := range pieces {
		texts[i] = p.Text
	}
	vecs, err := ix.emb.Embed(ctx, texts, InputDocument)
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}
	out := make([]store.RagChunk, len(pieces))
	for i, p := range pieces {
		out[i] = store.RagChunk{
			ID:         uuid.NewString(),
			DocID:      doc.ID,
			Idx:        p.Idx,
			Text:       p.Text,
			Embedding:  vecs[i],
			TokenCount: p.TokenCount,
		}
	}
	return out, nil
}

// loadText resolves a document's plain text. Web sources are (re-)fetched and
// cached to disk; on a fetch failure a previously cached copy is used as fallback.
// All other types read their persisted raw bytes and run the matching extractor.
func (ix *Indexer) loadText(ctx context.Context, doc store.RagDocument) (string, error) {
	// Per-project code files are read live from the project directory (never copied
	// into <DataDir>), resolved via the owning collection's project.
	if doc.SourceType == store.RagSourceCode {
		abs, err := ix.codeFilePath(doc)
		if err != nil {
			return "", err
		}
		raw, err := os.ReadFile(abs)
		if err != nil {
			return "", fmt.Errorf("read code file: %w", err)
		}
		return string(raw), nil
	}
	if doc.SourceType == store.RagSourceWeb {
		text, _, err := FetchWeb(ctx, doc.SourceRef)
		if err != nil {
			if cached, rerr := os.ReadFile(ix.sourcePath(doc.ID)); rerr == nil {
				return string(cached), nil
			}
			return "", err
		}
		_ = ix.WriteSource(doc.ID, []byte(text))
		return text, nil
	}
	raw, err := os.ReadFile(ix.sourcePath(doc.ID))
	if err != nil {
		return "", fmt.Errorf("read source: %w", err)
	}
	return ExtractText(doc.SourceType, raw)
}

// codeFilePath resolves a `code` document's absolute path from its owning
// project collection (source_ref is relative to the project directory).
func (ix *Indexer) codeFilePath(doc store.RagDocument) (string, error) {
	col, err := ix.db.GetRagCollection(doc.CollectionID)
	if err != nil {
		return "", err
	}
	if col.ProjectID == "" {
		return "", fmt.Errorf("document %s is not in a project collection", doc.ID)
	}
	proj, err := ix.db.GetProject(col.ProjectID)
	if err != nil {
		return "", err
	}
	return filepath.Join(proj.Dir, doc.SourceRef), nil
}

// Search embeds the query and returns the top-k most similar enabled chunks. When
// collectionRef is non-empty the search is narrowed to that collection (matched by
// id, slug or name); an unknown ref falls back to searching all collections.
func (ix *Indexer) Search(ctx context.Context, query, collectionRef string, k int) ([]Scored, error) {
	if ix.emb == nil {
		return nil, fmt.Errorf("no embeddings provider configured")
	}
	vecs, err := ix.emb.Embed(ctx, []string{query}, InputQuery)
	if err != nil {
		return nil, err
	}
	hits, err := ix.db.SearchableChunks(ix.resolveCollection(collectionRef))
	if err != nil {
		return nil, err
	}
	return TopK(vecs[0], hits, k), nil
}

// resolveCollection maps a user/agent-supplied collection reference (id, slug or
// display name) to a collection id, or "" to search across all collections.
func (ix *Indexer) resolveCollection(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if _, err := ix.db.GetRagCollection(ref); err == nil {
		return ref
	}
	if c, err := ix.db.GetRagCollectionBySlug(strings.ToLower(ref)); err == nil {
		return c.ID
	}
	cols, err := ix.db.ListRagCollections()
	if err == nil {
		for _, c := range cols {
			if strings.EqualFold(c.Name, ref) {
				return c.ID
			}
		}
	}
	return ""
}

// Collections lists the knowledge bases, for the MCP discovery tool.
func (ix *Indexer) Collections() ([]store.RagCollection, error) {
	return ix.db.ListRagCollections()
}
