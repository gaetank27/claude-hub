package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"system/internal/rag"
	"system/internal/store"
)

// projectRagDTO is the per-project RAG status + config for the project view and
// the dashboard tile. File/chunk/byte counts are reported live from the index.
type projectRagDTO struct {
	ProjectID       string     `json:"projectId"`
	Enabled         bool       `json:"enabled"`
	AutoOnChange    bool       `json:"autoOnChange"`
	IntervalMinutes int        `json:"intervalMinutes"`
	Status          string     `json:"status"` // idle|indexing|error
	LastIndexedAt   *time.Time `json:"lastIndexedAt,omitempty"`
	FileCount       int        `json:"fileCount"`
	ChunkCount      int        `json:"chunkCount"`
	Bytes           int64      `json:"bytes"`
	Pending         int        `json:"pending"`
	Error           string     `json:"error,omitempty"`
}

func (s *Server) projectRagDTO(pr store.ProjectRag) projectRagDTO {
	dto := projectRagDTO{
		ProjectID: pr.ProjectID, Enabled: pr.Enabled, AutoOnChange: pr.AutoOnChange,
		IntervalMinutes: pr.IntervalMinutes, Status: pr.Status,
		LastIndexedAt: pr.LastIndexedAt, Error: pr.Error,
		FileCount: pr.FileCount, ChunkCount: pr.ChunkCount, Bytes: pr.Bytes,
	}
	// Prefer live counts from the index (accurate during/after an in-flight pass).
	if st, err := s.rag.CollectionStats(pr.CollectionID); err == nil {
		dto.FileCount, dto.ChunkCount, dto.Bytes, dto.Pending = st.Files, st.Chunks, st.Bytes, st.Pending
	}
	return dto
}

// projectRag loads a project's RAG row, ensuring one exists (a project created
// moments ago may not have been picked up by the scheduler yet).
func (s *Server) projectRag(id string) (store.ProjectRag, error) {
	if _, err := s.db.GetProject(id); err != nil {
		return store.ProjectRag{}, err
	}
	pr, err := s.db.GetProjectRag(id)
	if err == store.ErrNotFound {
		if eerr := rag.EnsureProjectCollections(s.db); eerr != nil {
			return store.ProjectRag{}, eerr
		}
		pr, err = s.db.GetProjectRag(id)
	}
	return pr, err
}

func (s *Server) getProjectRag(w http.ResponseWriter, r *http.Request) {
	pr, err := s.projectRag(r.PathValue("id"))
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.projectRagDTO(pr))
}

// updateProjectRag edits the scheduling config (enabled, on-change, interval).
func (s *Server) updateProjectRag(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled         *bool `json:"enabled"`
		AutoOnChange    *bool `json:"autoOnChange"`
		IntervalMinutes *int  `json:"intervalMinutes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	pr, err := s.projectRag(r.PathValue("id"))
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	enabled, auto, interval := pr.Enabled, pr.AutoOnChange, pr.IntervalMinutes
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if req.AutoOnChange != nil {
		auto = *req.AutoOnChange
	}
	if req.IntervalMinutes != nil && *req.IntervalMinutes >= 0 {
		interval = *req.IntervalMinutes
	}
	if err := s.db.UpdateProjectRagConfig(pr.ProjectID, enabled, auto, interval); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	pr, _ = s.db.GetProjectRag(pr.ProjectID)
	writeJSON(w, http.StatusOK, s.projectRagDTO(pr))
}

// reindexProjectRag triggers an immediate reindex. The scan + embedding run
// asynchronously (a detached context) so the request returns at once.
func (s *Server) reindexProjectRag(w http.ResponseWriter, r *http.Request) {
	pr, err := s.projectRag(r.PathValue("id"))
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	go s.rag.Reindex(context.Background(), pr.ProjectID)
	pr.Status = store.ProjectRagIndexing
	writeJSON(w, http.StatusAccepted, s.projectRagDTO(pr))
}
