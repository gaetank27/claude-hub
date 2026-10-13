package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"system/internal/store"
)

type apiKeyDTO struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	ProjectID  string     `json:"projectId"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	Key        string     `json:"key,omitempty"` // only in the creation response
}

func toAPIKeyDTO(k store.APIKey) apiKeyDTO {
	return apiKeyDTO{ID: k.ID, Name: k.Name, ProjectID: k.ProjectID, CreatedAt: k.CreatedAt, LastUsedAt: k.LastUsedAt}
}

func (s *Server) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.db.ListAPIKeys()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]apiKeyDTO, len(keys))
	for i, k := range keys {
		out[i] = toAPIKeyDTO(k)
	}
	writeJSON(w, http.StatusOK, out)
}

// createAPIKey handles POST /api/apikeys {name, projectId}. The secret is
// returned once, in this response, and cannot be read back afterwards.
func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string `json:"name"`
		ProjectID string `json:"projectId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("name is required"))
		return
	}
	if req.ProjectID != "" {
		if _, err := s.db.GetProject(req.ProjectID); err != nil {
			writeNotFoundOr500(w, err)
			return
		}
	}
	k, secret, err := s.db.CreateAPIKey(strings.TrimSpace(req.Name), req.ProjectID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	dto := toAPIKeyDTO(k)
	dto.Key = secret
	writeJSON(w, http.StatusCreated, dto)
}

func (s *Server) deleteAPIKey(w http.ResponseWriter, r *http.Request) {
	if err := s.db.DeleteAPIKey(r.PathValue("id")); err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
