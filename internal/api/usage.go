package api

import (
	"net/http"
	"time"

	"system/internal/usage"
)

// usageWindow / usageResp are the JSON shape of the meter. stale=true means the
// values come from the last stored snapshot (a transient live-fetch failure),
// so the UI can show them dimmed rather than hiding the meter entirely.
type usageWindow struct {
	Utilization float64    `json:"utilization"`
	ResetsAt    *time.Time `json:"resetsAt,omitempty"`
}

type usageResp struct {
	Session    *usageWindow `json:"session,omitempty"`
	Week       *usageWindow `json:"week,omitempty"`
	WeekOpus   *usageWindow `json:"weekOpus,omitempty"`
	WeekSonnet *usageWindow `json:"weekSonnet,omitempty"`
	Stale      bool         `json:"stale,omitempty"`
}

// getUsage reports the Claude subscription usage (session + weekly windows) for
// the top-of-page meter. A live-fetch failure (token missing/expired, upstream
// down, backend just restarted) falls back to the last stored snapshot so the
// meter survives transient blips; only when there is nothing to show does it 503
// (the SPA then hides the meter).
func (s *Server) getUsage(w http.ResponseWriter, r *http.Request) {
	if s.usage == nil {
		http.Error(w, "usage unavailable", http.StatusServiceUnavailable)
		return
	}
	// Optional ?account=<id> scopes the meter to a specific subscription — the
	// project's account when the UI is inside a project. Empty => the default
	// (active) account, i.e. the page-wide meter shown outside any project.
	if accountID := r.URL.Query().Get("account"); accountID != "" {
		acct, err := s.mgr.Account(accountID)
		if err != nil {
			http.Error(w, "usage unavailable", http.StatusServiceUnavailable)
			return
		}
		if u, err := s.usage.GetCachedFor(r.Context(), s.mgr.AccountCredentialsPath(acct)); err == nil {
			writeJSON(w, http.StatusOK, usageRespFromUsage(u))
			return
		}
		if resp, ok := s.snapshotFor(acct.ID); ok {
			writeJSON(w, http.StatusOK, resp)
			return
		}
		http.Error(w, "usage unavailable", http.StatusServiceUnavailable)
		return
	}
	if u, err := s.usage.Get(r.Context()); err == nil {
		writeJSON(w, http.StatusOK, usageRespFromUsage(u))
		return
	}
	if resp, ok := s.lastUsageSnapshot(); ok {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	http.Error(w, "usage unavailable", http.StatusServiceUnavailable)
}

func usageRespFromUsage(u usage.Usage) usageResp {
	return usageResp{
		Session:    winFromUsage(u.Session),
		Week:       winFromUsage(u.Week),
		WeekOpus:   winFromUsage(u.WeekOpus),
		WeekSonnet: winFromUsage(u.WeekSonnet),
	}
}

// lastUsageSnapshot builds a stale meter from the active account's most recent
// stored sample. Reset times are persisted in the snapshot, so the countdown
// survives a live-fetch failure (only the utilization is slightly stale).
func (s *Server) lastUsageSnapshot() (usageResp, bool) {
	acct, err := s.mgr.ActiveAccount()
	if err != nil {
		return usageResp{}, false
	}
	return s.snapshotFor(acct.ID)
}

// snapshotFor builds a stale meter from an explicit account's latest stored
// sample (the per-project meter's fallback when the live fetch fails).
func (s *Server) snapshotFor(accountID string) (usageResp, bool) {
	snap, err := s.db.LatestUsageSnapshot(accountID)
	if err != nil {
		return usageResp{}, false
	}
	return usageResp{
		Session:    winFromSnap(snap.Session, snap.SessionReset),
		Week:       winFromSnap(snap.Week, snap.WeekReset),
		WeekOpus:   winFromSnap(snap.WeekOpus, snap.WeekOpusReset),
		WeekSonnet: winFromSnap(snap.WeekSonnet, snap.WeekSonnetReset),
		Stale:      true,
	}, true
}

func winFromUsage(w *usage.Window) *usageWindow {
	if w == nil {
		return nil
	}
	return &usageWindow{Utilization: w.Utilization, ResetsAt: w.ResetsAt}
}

func winFromSnap(p *float64, reset *time.Time) *usageWindow {
	if p == nil {
		return nil
	}
	return &usageWindow{Utilization: *p, ResetsAt: reset}
}
