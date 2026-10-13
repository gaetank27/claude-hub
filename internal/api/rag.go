package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"system/internal/store"
)

// maxRagUpload bounds an uploaded document's size (raw bytes).
const maxRagUpload = 25 << 20 // 25 MB

// collectionSlugRe collapses runs of non-alphanumerics into a single dash.
var collectionSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

// ragDocDTO is the stable JSON shape of a RAG document for the frontend.
type ragDocDTO struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	SourceType   string     `json:"sourceType"`
	SourceRef    string     `json:"sourceRef"`
	Status       string     `json:"status"`
	Enabled      bool       `json:"enabled"`
	Bytes        int64      `json:"bytes"`
	ChunkCount   int        `json:"chunkCount"`
	Error        string     `json:"error,omitempty"`
	CollectionID string     `json:"collectionId"`
	CreatedAt    time.Time  `json:"createdAt"`
	IndexedAt    *time.Time `json:"indexedAt,omitempty"`
}

func toRagDocDTO(d store.RagDocument) ragDocDTO {
	return ragDocDTO{
		ID: d.ID, Title: d.Title, SourceType: d.SourceType, SourceRef: d.SourceRef,
		Status: d.Status, Enabled: d.Enabled, Bytes: d.Bytes, ChunkCount: d.ChunkCount,
		Error: d.Error, CollectionID: d.CollectionID, CreatedAt: d.CreatedAt, IndexedAt: d.IndexedAt,
	}
}

// getRagStatus reports whether an embeddings provider is configured, so the UI
// can warn that ingestion is paused until one is.
func (s *Server) getRagStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"available": s.rag.Available(),
		"embedder":  s.rag.EmbedderName(),
	})
}

// --- Collections ---

type ragCollectionDTO struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Description string    `json:"description"`
	Category    string    `json:"category"`
	DocCount    int       `json:"docCount"`
	IsDefault   bool      `json:"isDefault"`
	CreatedAt   time.Time `json:"createdAt"`
}

func toRagCollectionDTO(c store.RagCollection, docCount int) ragCollectionDTO {
	return ragCollectionDTO{
		ID: c.ID, Name: c.Name, Slug: c.Slug, Description: c.Description, Category: c.Category,
		DocCount: docCount, IsDefault: c.ID == store.DefaultRagCollectionID, CreatedAt: c.CreatedAt,
	}
}

// listRagCollections returns the knowledge bases with their document counts.
func (s *Server) listRagCollections(w http.ResponseWriter, r *http.Request) {
	cols, err := s.db.ListRagCollections()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	docs, err := s.db.ListRagDocuments()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	counts := map[string]int{}
	for _, d := range docs {
		counts[d.CollectionID]++
	}
	out := make([]ragCollectionDTO, 0, len(cols))
	for _, c := range cols {
		if c.ProjectID != "" {
			continue // private per-project RAG, managed from the project view
		}
		out = append(out, toRagCollectionDTO(c, counts[c.ID]))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createRagCollection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Category    string `json:"category"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, errors.New("name is required"))
		return
	}
	c := store.RagCollection{
		ID:          uuid.NewString(),
		Name:        name,
		Slug:        s.uniqueCollectionSlug(name),
		Description: strings.TrimSpace(req.Description),
		Category:    strings.TrimSpace(req.Category),
		CreatedAt:   time.Now().UTC(),
	}
	if err := s.db.CreateRagCollection(c); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, toRagCollectionDTO(c, 0))
}

func (s *Server) updateRagCollection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Category    *string `json:"category"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	id := r.PathValue("id")
	cur, err := s.db.GetRagCollection(id)
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	name := derefOr(req.Name, cur.Name)
	if strings.TrimSpace(name) == "" {
		name = cur.Name
	}
	if err := s.db.UpdateRagCollection(id, strings.TrimSpace(name), derefOr(req.Description, cur.Description), strings.TrimSpace(derefOr(req.Category, cur.Category))); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	c, _ := s.db.GetRagCollection(id)
	writeJSON(w, http.StatusOK, toRagCollectionDTO(c, 0))
}

// deleteRagCollection removes a collection and all its documents (rows, chunks and
// on-disk sources). The seeded default collection cannot be deleted.
func (s *Server) deleteRagCollection(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == store.DefaultRagCollectionID {
		writeErr(w, http.StatusBadRequest, errors.New("the default collection cannot be deleted"))
		return
	}
	if _, err := s.db.GetRagCollection(id); err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	// Remove the on-disk sources of the collection's documents before the rows go.
	docs, err := s.db.ListRagDocuments()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.db.DeleteRagCollection(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	for _, d := range docs {
		if d.CollectionID == id {
			_ = s.rag.Remove(d.ID)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// uniqueCollectionSlug derives a URL-safe slug unique across collections.
func (s *Server) uniqueCollectionSlug(name string) string {
	base := strings.Trim(collectionSlugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if base == "" {
		base = "collection"
	}
	slug := base
	for i := 2; ; i++ {
		if _, err := s.db.GetRagCollectionBySlug(slug); err == store.ErrNotFound {
			return slug
		}
		slug = fmt.Sprintf("%s-%d", base, i)
	}
}

// resolveCollectionID validates a requested collection id, falling back to the
// default collection when empty or unknown.
func (s *Server) resolveCollectionID(raw string) string {
	if raw = strings.TrimSpace(raw); raw != "" {
		if _, err := s.db.GetRagCollection(raw); err == nil {
			return raw
		}
	}
	return store.DefaultRagCollectionID
}

// listRagDocuments returns all documents, optionally filtered by ?q= matched
// against title, source and status (the explorer search/filter).
func (s *Server) listRagDocuments(w http.ResponseWriter, r *http.Request) {
	docs, err := s.db.ListRagDocuments()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	collection := strings.TrimSpace(r.URL.Query().Get("collection"))
	out := make([]ragDocDTO, 0, len(docs))
	for _, d := range docs {
		if collection != "" && d.CollectionID != collection {
			continue
		}
		if q != "" && !ragMatches(d, q) {
			continue
		}
		out = append(out, toRagDocDTO(d))
	}
	writeJSON(w, http.StatusOK, out)
}

func ragMatches(d store.RagDocument, q string) bool {
	hay := strings.ToLower(d.Title + " " + d.SourceRef + " " + d.SourceType + " " + d.Status)
	return strings.Contains(hay, q)
}

// createRagDocument ingests a new document. A JSON body {title,text} ingests
// pasted text/markdown; a multipart/form-data body with a "file" field ingests an
// uploaded file (text/markdown/PDF). The document is created `pending` and queued
// for asynchronous indexing.
func (s *Server) createRagDocument(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		s.createRagText(w, r)
		return
	}
	if err := r.ParseMultipartForm(maxRagUpload); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("missing file field"))
		return
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxRagUpload+1))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if len(raw) > maxRagUpload {
		writeErr(w, http.StatusRequestEntityTooLarge, errors.New("file too large"))
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		title = header.Filename
	}
	s.ingest(w, store.RagDocument{
		Title:        title,
		SourceType:   sourceTypeForName(header.Filename),
		SourceRef:    header.Filename,
		Bytes:        int64(len(raw)),
		CollectionID: s.resolveCollectionID(r.FormValue("collectionId")),
	}, raw)
}

func (s *Server) createRagText(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title        string `json:"title"`
		Text         string `json:"text"`
		CollectionID string `json:"collectionId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("text is required"))
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "Pasted text"
	}
	s.ingest(w, store.RagDocument{
		Title:        title,
		SourceType:   store.RagSourceMarkdown, // pasted text is treated as markdown
		Bytes:        int64(len(req.Text)),
		CollectionID: s.resolveCollectionID(req.CollectionID),
	}, []byte(req.Text))
}

// createRagURL ingests a web page; the worker fetches and extracts it.
func (s *Server) createRagURL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL          string `json:"url"`
		Title        string `json:"title"`
		CollectionID string `json:"collectionId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	url := strings.TrimSpace(req.URL)
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		writeErr(w, http.StatusBadRequest, errors.New("a http(s) url is required"))
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = url
	}
	// No on-disk source: the worker fetches the page at index time.
	s.ingest(w, store.RagDocument{
		Title:        title,
		SourceType:   store.RagSourceWeb,
		SourceRef:    url,
		CollectionID: s.resolveCollectionID(req.CollectionID),
	}, nil)
}

// ingest persists a new document (and its raw bytes, when provided), then queues
// it for indexing. It fills the id/status/timestamps the caller leaves zero.
func (s *Server) ingest(w http.ResponseWriter, d store.RagDocument, raw []byte) {
	d.ID = uuid.NewString()
	d.Status = store.RagPending
	d.Enabled = true
	if d.CollectionID == "" {
		d.CollectionID = store.DefaultRagCollectionID
	}
	d.CreatedAt = time.Now().UTC()
	if raw != nil {
		if err := s.rag.WriteSource(d.ID, raw); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if err := s.db.CreateRagDocument(d); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.rag.Enqueue(d.ID)
	writeJSON(w, http.StatusCreated, toRagDocDTO(d))
}

// updateRagDocument toggles a document's enabled flag and/or renames it. Optional
// pointer fields: only those present are applied.
func (s *Server) updateRagDocument(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool   `json:"enabled"`
		Title   *string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	id := r.PathValue("id")
	if _, err := s.db.GetRagDocument(id); err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	if req.Title != nil {
		if err := s.db.UpdateRagDocumentTitle(id, strings.TrimSpace(*req.Title)); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if req.Enabled != nil {
		if err := s.db.SetRagDocumentEnabled(id, *req.Enabled); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	doc, err := s.db.GetRagDocument(id)
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toRagDocDTO(doc))
}

func (s *Server) deleteRagDocument(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.db.GetRagDocument(id); err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	if err := s.db.DeleteRagDocument(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	_ = s.rag.Remove(id)
	w.WriteHeader(http.StatusNoContent)
}

// getRagDocumentContent returns the document's plain-text content for preview.
// Capped at 500 KB to avoid flooding the browser.
func (s *Server) getRagDocumentContent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	doc, err := s.db.GetRagDocument(id)
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	text, err := s.rag.ReadSource(r.Context(), doc)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	const cap = 500 * 1024
	truncated := false
	if len(text) > cap {
		text = text[:cap]
		truncated = true
	}
	writeJSON(w, http.StatusOK, map[string]any{"content": text, "truncated": truncated})
}

// reindexRagDocument re-runs the pipeline (e.g. after an embedder change or a web
// page update): it resets the document to pending and re-queues it.
func (s *Server) reindexRagDocument(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	doc, err := s.db.GetRagDocument(id)
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	if err := s.db.SetRagDocumentStatus(id, store.RagPending, "", doc.ChunkCount, doc.IndexedAt); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.rag.Enqueue(id)
	doc, _ = s.db.GetRagDocument(id)
	writeJSON(w, http.StatusOK, toRagDocDTO(doc))
}

// sourceTypeForName maps a filename extension to a RAG source type, defaulting to
// plain text.
func sourceTypeForName(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".markdown":
		return store.RagSourceMarkdown
	case ".pdf":
		return store.RagSourcePDF
	default:
		return store.RagSourceText
	}
}
