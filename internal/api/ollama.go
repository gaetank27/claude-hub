package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"system/internal/ollama"
)

// getOllama reports the local Ollama supervision state in one call: reachability,
// version, installed models and the models currently loaded in memory.
func (s *Server) getOllama(w http.ResponseWriter, r *http.Request) {
	if s.ollama == nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "disabled"})
		return
	}
	ctx := r.Context()
	version, err := s.ollama.Version(ctx)
	if err != nil {
		// Unreachable (service down) is a normal state, not an API error.
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "unreachable",
			"host":   s.ollama.Host(),
			"error":  err.Error(),
		})
		return
	}
	models, mErr := s.ollama.Models(ctx)
	running, rErr := s.ollama.Running(ctx)
	status := "active"
	var errMsg string
	if mErr != nil || rErr != nil {
		status = "error"
		if mErr != nil {
			errMsg = mErr.Error()
		} else {
			errMsg = rErr.Error()
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  status,
		"host":    s.ollama.Host(),
		"version": version,
		"models":  defaultModels(models),
		"running": defaultRunning(running),
		"error":   errMsg,
	})
}

// unloadOllamaModel evicts a model from memory to free RAM.
func (s *Server) unloadOllamaModel(w http.ResponseWriter, r *http.Request) {
	if s.ollama == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("ollama not configured"))
		return
	}
	var req struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.ollama.Unload(r.Context(), req.Model); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func defaultModels(m []ollama.Model) []ollama.Model {
	if m == nil {
		return []ollama.Model{}
	}
	return m
}

func defaultRunning(r []ollama.Running) []ollama.Running {
	if r == nil {
		return []ollama.Running{}
	}
	return r
}
