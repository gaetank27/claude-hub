package api

import (
	"errors"
	"net/http"
)

// getHostMetrics returns a live snapshot of the physical host (CPU, RAM, network
// and disk I/O rates, disk space) for the real-time gauges banner. It reads /proc
// directly via the server's own collector — independent of the RRD sampler — so
// rates are computed over the UI's poll interval and gauges work even without
// rrdtool.
func (s *Server) getHostMetrics(w http.ResponseWriter, r *http.Request) {
	sample, err := s.hostMetrics.Snapshot()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, sample)
}

// validHostRange is the set of chart ranges the RRD exposes.
var validHostRange = map[string]bool{"hour": true, "day": true, "week": true, "month": true}

// getHostHistory returns the RRD-backed time series for a chart range
// (?range=hour|day|week|month). When rrdtool/RRD is unavailable it returns
// {available:false} rather than erroring, so the UI degrades cleanly.
func (s *Server) getHostHistory(w http.ResponseWriter, r *http.Request) {
	rng := r.URL.Query().Get("range")
	if rng == "" {
		rng = "hour"
	}
	if !validHostRange[rng] {
		writeErr(w, http.StatusBadRequest, errors.New("range must be hour, day, week or month"))
		return
	}
	if s.hostRRD == nil || !s.hostRRD.Available() {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "range": rng})
		return
	}
	hist, err := s.hostRRD.Export(r.Context(), rng)
	if err != nil {
		// The RRD may not exist yet (no samples) — report unavailable, not 5xx.
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "range": rng, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, hist)
}
