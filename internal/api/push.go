package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"system/internal/push"
	"system/internal/store"
)

// registerPushEndpoint records a phone's WebPush subscription (upsert on the
// endpoint). The app re-posts it at every sign-in.
func (s *Server) registerPushEndpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Endpoint string `json:"endpoint"`
		P256dh   string `json:"p256dh"`
		Auth     string `json:"auth"`
		Device   string `json:"device"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// The Hub will POST to this URL: only a real web address is acceptable.
	if u, err := url.Parse(req.Endpoint); err != nil || u.Scheme != "https" || u.Host == "" {
		writeErr(w, http.StatusBadRequest, errors.New("endpoint must be an https URL"))
		return
	}
	// Refuse keys the Hub could not encrypt for, rather than fail at every push.
	if _, err := push.Encrypt(nil, req.P256dh, req.Auth); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	e := store.PushEndpoint{Endpoint: req.Endpoint, P256dh: req.P256dh, Auth: req.Auth, Device: req.Device}
	if err := s.db.UpsertPushEndpoint(e); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"registered": true})
}

func (s *Server) unregisterPushEndpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.db.DeletePushEndpoint(req.Endpoint); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
