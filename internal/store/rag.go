package store

import (
	"database/sql"
	"encoding/binary"
	"errors"
	"math"
	"time"
)

// RAG document statuses. A document is created `pending`, flips to `processing`
// while the worker parses/chunks/embeds it, then `indexed` (queryable) or `error`.
const (
	RagPending    = "pending"
	RagProcessing = "processing"
	RagIndexed    = "indexed"
	RagError      = "error"
)

// RAG source types.
const (
	RagSourceText     = "text"
	RagSourceMarkdown = "markdown"
	RagSourcePDF      = "pdf"
	RagSourceWeb      = "web"
	RagSourceCode     = "code" // a project file, read live from the project dir (per-project RAG)
)

// DefaultRagCollectionID is the stable id of the seeded collection that owns
// documents created before collections existed (and the default target for new
// uploads). It can be renamed but not deleted.
const DefaultRagCollectionID = "default"

// RagCollection mirrors a row of the rag_collections table: a named knowledge
// base. A non-empty ProjectID marks a private per-project RAG (hidden from the
// global RAG module and queried only by that project's sessions).
type RagCollection struct {
	ID          string
	Name        string
	Slug        string
	Description string
	Category    string
	ProjectID   string
	CreatedAt   time.Time
}

// RagDocument mirrors a row of the rag_documents table: one ingested knowledge
// source. The raw bytes live on disk; chunks + embeddings live in rag_chunks.
type RagDocument struct {
	ID           string
	Title        string
	SourceType   string
	SourceRef    string // original filename or URL
	Status       string
	Enabled      bool
	Bytes        int64
	ChunkCount   int
	Error        string
	CollectionID string
	ContentHash  string // source hash, for incremental per-project reindexing
	CreatedAt    time.Time
	IndexedAt    *time.Time
}

// RagChunk is one embedded slice of a document.
type RagChunk struct {
	ID         string
	DocID      string
	Idx        int
	Text       string
	Embedding  []float32
	TokenCount int
}

// RagChunkHit is a searchable chunk joined with its document's display fields.
type RagChunkHit struct {
	RagChunk
	DocTitle       string
	DocSourceRef   string
	CollectionID   string
	CollectionName string
}

// --- RAG collections ---

const ragCollectionCols = `id,name,slug,description,category,project_id,created_at`

func (db *DB) CreateRagCollection(c RagCollection) error {
	return db.WithFullSync(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO rag_collections(id,name,slug,description,category,project_id,created_at) VALUES(?,?,?,?,?,?,?)`,
			c.ID, c.Name, c.Slug, c.Description, c.Category, c.ProjectID, c.CreatedAt.Format(rfc),
		)
		return err
	})
}

func (db *DB) GetRagCollection(id string) (RagCollection, error) {
	row := db.QueryRow(`SELECT `+ragCollectionCols+` FROM rag_collections WHERE id=?`, id)
	return scanRagCollection(row)
}

func (db *DB) GetRagCollectionBySlug(slug string) (RagCollection, error) {
	row := db.QueryRow(`SELECT `+ragCollectionCols+` FROM rag_collections WHERE slug=?`, slug)
	return scanRagCollection(row)
}

func (db *DB) ListRagCollections() ([]RagCollection, error) {
	rows, err := db.Query(`SELECT ` + ragCollectionCols + ` FROM rag_collections ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RagCollection
	for rows.Next() {
		c, err := scanRagCollection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (db *DB) CountRagCollections() (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM rag_collections`).Scan(&n)
	return n, err
}

func (db *DB) UpdateRagCollection(id, name, description, category string) error {
	_, err := db.Exec(`UPDATE rag_collections SET name=?, description=?, category=? WHERE id=?`,
		name, description, category, id)
	return err
}

// DeleteRagCollection removes a collection together with all its documents and
// their chunks. The caller removes the documents' on-disk sources separately.
func (db *DB) DeleteRagCollection(id string) error {
	return db.WithFullSync(func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`DELETE FROM rag_chunks WHERE doc_id IN (SELECT id FROM rag_documents WHERE collection_id=?)`, id,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM rag_documents WHERE collection_id=?`, id); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM rag_collections WHERE id=?`, id)
		return err
	})
}

// BackfillRagCollection assigns every document with no collection to the given
// one, so documents predating collections land in the seeded default.
func (db *DB) BackfillRagCollection(id string) error {
	_, err := db.Exec(`UPDATE rag_documents SET collection_id=? WHERE collection_id=''`, id)
	return err
}

func scanRagCollection(s scanner) (RagCollection, error) {
	var c RagCollection
	var created string
	err := s.Scan(&c.ID, &c.Name, &c.Slug, &c.Description, &c.Category, &c.ProjectID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	c.CreatedAt, _ = time.Parse(rfc, created)
	return c, nil
}

// --- RAG documents ---

const ragDocCols = `id,title,source_type,source_ref,status,enabled,bytes,chunk_count,error,collection_id,content_hash,created_at,indexed_at`

// CreateRagDocument inserts a document durably (FULL sync): it is a root entity
// whose on-disk source bytes are keyed by this row's id.
func (db *DB) CreateRagDocument(d RagDocument) error {
	return db.WithFullSync(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO rag_documents(id,title,source_type,source_ref,status,enabled,bytes,chunk_count,error,collection_id,content_hash,created_at)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			d.ID, d.Title, d.SourceType, d.SourceRef, d.Status, boolToInt(d.Enabled),
			d.Bytes, d.ChunkCount, d.Error, d.CollectionID, d.ContentHash, d.CreatedAt.Format(rfc),
		)
		return err
	})
}

// ResetAllRagVectors clears every embedding and marks all documents pending so
// they are re-embedded from scratch. Used when the embedding model changes: its
// vector dimension differs, making previously-stored vectors uncomparable.
func (db *DB) ResetAllRagVectors() error {
	return db.WithFullSync(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM rag_chunks`); err != nil {
			return err
		}
		if _, err := tx.Exec(
			`UPDATE rag_documents SET status='pending', chunk_count=0, content_hash='', error=''`); err != nil {
			return err
		}
		_, err := tx.Exec(
			`UPDATE project_rag SET status='idle', last_indexed_at=NULL, last_head_sha='', chunk_count=0, file_count=0, bytes=0`)
		return err
	})
}

// CreateRagCodeDocument inserts a per-project `code` document with NORMAL sync
// (no per-row fsync): unlike user uploads these are fully re-derivable from the
// repo, and a project scan inserts hundreds of them — a FULL-sync fsync per row
// would serialize against API queries on the single DB connection and stall the UI.
func (db *DB) CreateRagCodeDocument(d RagDocument) error {
	_, err := db.Exec(
		`INSERT INTO rag_documents(id,title,source_type,source_ref,status,enabled,bytes,chunk_count,error,collection_id,content_hash,created_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.ID, d.Title, d.SourceType, d.SourceRef, d.Status, boolToInt(d.Enabled),
		d.Bytes, d.ChunkCount, d.Error, d.CollectionID, d.ContentHash, d.CreatedAt.Format(rfc),
	)
	return err
}

// ListRagDocumentsByCollection returns a collection's documents (used by the
// per-project indexer to diff against the filesystem and for stats).
func (db *DB) ListRagDocumentsByCollection(collectionID string) ([]RagDocument, error) {
	rows, err := db.Query(`SELECT `+ragDocCols+` FROM rag_documents WHERE collection_id=?`, collectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RagDocument
	for rows.Next() {
		d, err := scanRagDocument(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// MarkRagDocumentStale resets a document to pending with a new content hash, so
// the worker re-embeds it (a changed project file). Durable: it carries content.
func (db *DB) MarkRagDocumentStale(id, contentHash string, bytes int64) error {
	_, err := db.Exec(
		`UPDATE rag_documents SET status=?, error='', content_hash=?, bytes=? WHERE id=?`,
		RagPending, contentHash, bytes, id)
	return err
}

func (db *DB) GetRagDocument(id string) (RagDocument, error) {
	row := db.QueryRow(`SELECT `+ragDocCols+` FROM rag_documents WHERE id=?`, id)
	return scanRagDocument(row)
}

func (db *DB) ListRagDocuments() ([]RagDocument, error) {
	rows, err := db.Query(`SELECT ` + ragDocCols + ` FROM rag_documents ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RagDocument
	for rows.Next() {
		d, err := scanRagDocument(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ListRagDocumentsByStatus returns documents in any of the given states, oldest
// first — used to drain the indexing queue and to resume unfinished work at boot.
func (db *DB) ListRagDocumentsByStatus(states ...string) ([]RagDocument, error) {
	if len(states) == 0 {
		return nil, nil
	}
	q := `SELECT ` + ragDocCols + ` FROM rag_documents WHERE status IN (`
	args := make([]any, len(states))
	for i, s := range states {
		if i > 0 {
			q += ","
		}
		q += "?"
		args[i] = s
	}
	q += `) ORDER BY created_at ASC`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RagDocument
	for rows.Next() {
		d, err := scanRagDocument(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SetRagDocumentStatus records a state transition. On success the caller passes
// the fresh chunk count and indexedAt; on error it passes the message. NORMAL
// sync — the worker re-derives everything from the on-disk source if lost.
func (db *DB) SetRagDocumentStatus(id, status, errMsg string, chunkCount int, indexedAt *time.Time) error {
	var idx any
	if indexedAt != nil {
		idx = indexedAt.Format(rfc)
	}
	_, err := db.Exec(
		`UPDATE rag_documents SET status=?, error=?, chunk_count=?, indexed_at=? WHERE id=?`,
		status, errMsg, chunkCount, idx, id)
	return err
}

func (db *DB) SetRagDocumentEnabled(id string, enabled bool) error {
	_, err := db.Exec(`UPDATE rag_documents SET enabled=? WHERE id=?`, boolToInt(enabled), id)
	return err
}

func (db *DB) UpdateRagDocumentTitle(id, title string) error {
	_, err := db.Exec(`UPDATE rag_documents SET title=? WHERE id=?`, title, id)
	return err
}

// DeleteRagDocument removes the document and its chunks durably. The caller
// removes the on-disk source separately.
func (db *DB) DeleteRagDocument(id string) error {
	return db.WithFullSync(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM rag_chunks WHERE doc_id=?`, id); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM rag_documents WHERE id=?`, id)
		return err
	})
}

// ReplaceChunks atomically swaps a document's chunks for a freshly-computed set,
// so a re-index never leaves a half-updated vector set behind.
func (db *DB) ReplaceChunks(docID string, chunks []RagChunk) error {
	return db.WithFullSync(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM rag_chunks WHERE doc_id=?`, docID); err != nil {
			return err
		}
		for _, c := range chunks {
			if _, err := tx.Exec(
				`INSERT INTO rag_chunks(id,doc_id,idx,text,embedding,token_count) VALUES(?,?,?,?,?,?)`,
				c.ID, docID, c.Idx, c.Text, encodeEmbedding(c.Embedding), c.TokenCount,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

// SearchableChunks returns every chunk eligible for retrieval: those belonging to
// an enabled, fully-indexed document. When collectionID is non-empty the search is
// narrowed to that collection. Brute-force cosine ranking happens in Go.
func (db *DB) SearchableChunks(collectionID string) ([]RagChunkHit, error) {
	q := `SELECT c.id,c.doc_id,c.idx,c.text,c.embedding,c.token_count,
	             d.title,d.source_ref,d.collection_id,COALESCE(col.name,'')
	      FROM rag_chunks c
	      JOIN rag_documents d ON d.id = c.doc_id
	      LEFT JOIN rag_collections col ON col.id = d.collection_id
	      WHERE d.enabled=1 AND d.status='indexed'`
	var args []any
	if collectionID != "" {
		// Explicit collection (incl. a project's private code index).
		q += ` AND d.collection_id=?`
		args = append(args, collectionID)
	} else {
		// Global search: never leak project-private collections into it.
		q += ` AND COALESCE(col.project_id,'') = ''`
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RagChunkHit
	for rows.Next() {
		var (
			h   RagChunkHit
			emb []byte
		)
		if err := rows.Scan(&h.ID, &h.DocID, &h.Idx, &h.Text, &emb, &h.TokenCount,
			&h.DocTitle, &h.DocSourceRef, &h.CollectionID, &h.CollectionName); err != nil {
			return nil, err
		}
		h.Embedding = decodeEmbedding(emb)
		out = append(out, h)
	}
	return out, rows.Err()
}

func scanRagDocument(s scanner) (RagDocument, error) {
	var (
		d         RagDocument
		enabled   int
		created   string
		indexedAt sql.NullString
	)
	err := s.Scan(&d.ID, &d.Title, &d.SourceType, &d.SourceRef, &d.Status, &enabled,
		&d.Bytes, &d.ChunkCount, &d.Error, &d.CollectionID, &d.ContentHash, &created, &indexedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	if err != nil {
		return d, err
	}
	d.Enabled = enabled != 0
	d.CreatedAt, _ = time.Parse(rfc, created)
	if indexedAt.Valid {
		if t, err := time.Parse(rfc, indexedAt.String); err == nil {
			d.IndexedAt = &t
		}
	}
	return d, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// encodeEmbedding/decodeEmbedding serialize a vector as a little-endian float32
// BLOB. Keeping it compact (4 bytes/dim) keeps the brute-force scan cache-friendly.
func encodeEmbedding(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

func decodeEmbedding(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}
