package api

import (
	"encoding/json"
	"net/http"

	"system/internal/session"
)

// getSettings returns the current effective runtime settings.
func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mgr.Settings())
}

// updateSettings validates and applies new runtime settings (persisted + applied
// to new sessions). Invalid values are rejected with 400.
func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	var v session.SettingsView
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	prev := s.mgr.Settings()
	if err := s.mgr.UpdateSettings(v); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// Sync the PostToolUse hook to all accounts when the truncation toggle changes.
	if v.OutputTruncation != prev.OutputTruncation {
		if err := s.mgr.SyncOutputTruncation(v.OutputTruncation); err != nil {
			// Best-effort: log but don't fail the settings update.
			_ = err
		}
	}
	writeJSON(w, http.StatusOK, s.mgr.Settings())
}
