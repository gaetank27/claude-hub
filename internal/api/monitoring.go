package api

import (
	"net/http"

	"system/internal/session"
	"system/internal/store"
)

// monitoringRow is a running session enriched with live resource metrics.
type monitoringRow struct {
	ID        string                   `json:"id"`
	ProjectID string                   `json:"projectId"`
	Resources session.SessionResources `json:"resources"`
}

// getMonitoring reports live resource use (memory, CPU, journal size) for every
// running session. It probes systemd per session, so it is a dedicated endpoint
// kept off the hot path of the general session list.
func (s *Server) getMonitoring(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.db.ListSessionsByState(store.StateRunning)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]monitoringRow, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, monitoringRow{
			ID:        sess.ID,
			ProjectID: sess.ProjectID,
			Resources: s.mgr.SessionResources(r.Context(), sess.ID),
		})
	}
	writeJSON(w, http.StatusOK, out)
}
