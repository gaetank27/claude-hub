package store

import (
	"database/sql"
	"errors"
	"time"
)

// Per-project RAG indexing statuses.
const (
	ProjectRagIdle     = "idle"
	ProjectRagIndexing = "indexing"
	ProjectRagError    = "error"
)

// ProjectRag mirrors a row of the project_rag table: a project's private RAG
// index configuration and status. The vectors live in rag_documents/rag_chunks
// under CollectionID.
type ProjectRag struct {
	ProjectID       string
	CollectionID    string
	Enabled         bool
	AutoOnChange    bool
	IntervalMinutes int
	Status          string
	LastIndexedAt   *time.Time
	LastHeadSHA     string
	FileCount       int
	ChunkCount      int
	Bytes           int64
	Error           string
}

const projectRagCols = `project_id,collection_id,enabled,auto_on_change,interval_minutes,status,last_indexed_at,last_head_sha,file_count,chunk_count,bytes,error`

// CreateProjectRag inserts a project's RAG config durably.
func (db *DB) CreateProjectRag(p ProjectRag) error {
	return db.WithFullSync(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO project_rag(project_id,collection_id,enabled,auto_on_change,interval_minutes,status)
			 VALUES(?,?,?,?,?,?)`,
			p.ProjectID, p.CollectionID, boolToInt(p.Enabled), boolToInt(p.AutoOnChange),
			p.IntervalMinutes, ProjectRagIdle,
		)
		return err
	})
}

func (db *DB) GetProjectRag(projectID string) (ProjectRag, error) {
	row := db.QueryRow(`SELECT `+projectRagCols+` FROM project_rag WHERE project_id=?`, projectID)
	return scanProjectRag(row)
}

func (db *DB) ListProjectRag() ([]ProjectRag, error) {
	rows, err := db.Query(`SELECT ` + projectRagCols + ` FROM project_rag`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectRag
	for rows.Next() {
		p, err := scanProjectRag(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateProjectRagConfig persists the user-editable scheduling settings.
func (db *DB) UpdateProjectRagConfig(projectID string, enabled, autoOnChange bool, intervalMinutes int) error {
	_, err := db.Exec(
		`UPDATE project_rag SET enabled=?, auto_on_change=?, interval_minutes=? WHERE project_id=?`,
		boolToInt(enabled), boolToInt(autoOnChange), intervalMinutes, projectID)
	return err
}

// SetProjectRagScan records the result of a scan pass (the HEAD scanned and the
// file/byte totals) while leaving status as-is: the chunk count is finalized later
// by the scheduler once the async embedding settles.
func (db *DB) SetProjectRagScan(projectID, headSHA string, fileCount int, bytes int64) error {
	_, err := db.Exec(`UPDATE project_rag SET last_head_sha=?, file_count=?, bytes=? WHERE project_id=?`,
		headSHA, fileCount, bytes, projectID)
	return err
}

// SetProjectRagStatus records a status transition (idle|indexing|error) + message.
func (db *DB) SetProjectRagStatus(projectID, status, errMsg string) error {
	_, err := db.Exec(`UPDATE project_rag SET status=?, error=? WHERE project_id=?`, status, errMsg, projectID)
	return err
}

// SetProjectRagIndexed records the outcome of a successful index pass: the git
// HEAD it indexed, the file/chunk/byte counts and the timestamp.
func (db *DB) SetProjectRagIndexed(projectID, headSHA string, fileCount, chunkCount int, bytes int64, at time.Time) error {
	_, err := db.Exec(
		`UPDATE project_rag SET status=?, error='', last_head_sha=?, file_count=?, chunk_count=?, bytes=?, last_indexed_at=? WHERE project_id=?`,
		ProjectRagIdle, headSHA, fileCount, chunkCount, bytes, at.Format(rfc), projectID)
	return err
}

func (db *DB) DeleteProjectRag(projectID string) error {
	_, err := db.Exec(`DELETE FROM project_rag WHERE project_id=?`, projectID)
	return err
}

func scanProjectRag(s scanner) (ProjectRag, error) {
	var (
		p                   ProjectRag
		enabled, autoChange int
		lastIndexed         sql.NullString
	)
	err := s.Scan(&p.ProjectID, &p.CollectionID, &enabled, &autoChange, &p.IntervalMinutes,
		&p.Status, &lastIndexed, &p.LastHeadSHA, &p.FileCount, &p.ChunkCount, &p.Bytes, &p.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	p.Enabled = enabled != 0
	p.AutoOnChange = autoChange != 0
	if lastIndexed.Valid {
		if t, err := time.Parse(rfc, lastIndexed.String); err == nil {
			p.LastIndexedAt = &t
		}
	}
	return p, nil
}
