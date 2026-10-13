package auth

import (
	"errors"
	"net/http"
	"time"

	"system/internal/store"
)

// deviceDTO is one signed-in device as the Configuration page lists it.
type deviceDTO struct {
	ID         string     `json:"id"`
	Email      string     `json:"email"`
	Method     string     `json:"method"`
	UserAgent  string     `json:"userAgent"`
	IP         string     `json:"ip"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastSeenAt time.Time  `json:"lastSeenAt"`
	ExpiresAt  *time.Time `json:"expiresAt"` // null => stays signed in
	Current    bool       `json:"current"`
}

// DevicesHandler lists the live sign-ins (GET /api/devices), flagging the one
// making the request.
func (a *Authenticator) DevicesHandler(w http.ResponseWriter, r *http.Request) {
	sc, _ := a.session(r)
	list, err := a.cfg.DB.ListLoginSessions()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list devices")
		return
	}
	out := make([]deviceDTO, len(list))
	for i, s := range list {
		out[i] = deviceDTO{
			ID: s.ID, Email: s.Email, Method: s.Method, UserAgent: s.UserAgent, IP: s.IP,
			CreatedAt: s.CreatedAt, LastSeenAt: s.LastSeenAt, ExpiresAt: s.ExpiresAt,
			Current: s.ID == sc.Sid,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// RevokeDeviceHandler signs one device out (DELETE /api/devices/{id}). Its
// cookie stops working on its very next request.
func (a *Authenticator) RevokeDeviceHandler(w http.ResponseWriter, r *http.Request) {
	err := a.cfg.DB.DeleteLoginSession(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not revoke device")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RevokeOtherDevicesHandler signs out every device but the caller's
// (DELETE /api/devices).
func (a *Authenticator) RevokeOtherDevicesHandler(w http.ResponseWriter, r *http.Request) {
	sc, ok := a.session(r)
	if !ok || sc.Sid == "" {
		writeErr(w, http.StatusConflict, "sign in again to manage devices")
		return
	}
	if err := a.cfg.DB.DeleteOtherLoginSessions(sc.Sid); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not revoke devices")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
