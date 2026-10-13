package api

import (
	"encoding/json"
	"net/http"

	"system/internal/store"
)

// accessDTO is a project's access policy. Tools are connector ids (the Tools
// page); Projects are other project ids whose code index it may search.
type accessDTO struct {
	ToolsMode       string   `json:"toolsMode"` // "all" | "list"
	AllowedTools    []string `json:"allowedTools"`
	AllowedProjects []string `json:"allowedProjects"`
}

func (s *Server) getProjectAccess(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.db.GetProject(id); err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	a, err := s.db.GetProjectAccess(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, accessDTO{a.ToolsMode, a.AllowedTools, a.AllowedProjects})
}

// updateProjectAccess replaces the policy. It applies live to tool calls (the
// backend checks it per request); the extra searchable projects are baked into
// the session's MCP config, so they take effect for newly launched sessions.
func (s *Server) updateProjectAccess(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.db.GetProject(id); err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	var req accessDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.ToolsMode != "list" {
		req.ToolsMode = "all"
	}
	others := make([]string, 0, len(req.AllowedProjects))
	for _, pid := range req.AllowedProjects {
		if pid != id {
			others = append(others, pid)
		}
	}
	a := store.ProjectAccess{ToolsMode: req.ToolsMode, AllowedTools: req.AllowedTools, AllowedProjects: others}
	if err := s.db.SetProjectAccess(id, a); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, accessDTO{a.ToolsMode, a.AllowedTools, a.AllowedProjects})
}

// markSessionSeen records that the user has viewed the session's latest output.
func (s *Server) markSessionSeen(w http.ResponseWriter, r *http.Request) {
	if err := s.db.MarkSessionSeen(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
