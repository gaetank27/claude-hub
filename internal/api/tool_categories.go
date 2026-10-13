package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"system/internal/store"
)

// Tool categories group the configured tools on the Tools page. They are a pure
// presentation layer: the category of a tool never changes what it can do.

func (s *Server) listToolCategories(w http.ResponseWriter, r *http.Request) {
	cats, err := s.db.ListToolCategories()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, cats)
}

func (s *Server) createToolCategory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	c, err := s.db.CreateToolCategory(req.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) renameToolCategory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.db.RenameToolCategory(r.PathValue("id"), req.Name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.listToolCategories(w, r)
}

func (s *Server) deleteToolCategory(w http.ResponseWriter, r *http.Request) {
	err := s.db.DeleteToolCategory(r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, err)
	case errors.Is(err, store.ErrProtectedCategory):
		writeErr(w, http.StatusBadRequest, err)
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// reorderToolCategories persists the display order of the sections.
func (s *Server) reorderToolCategories(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.db.ReorderToolCategories(req.IDs); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.listToolCategories(w, r)
}
