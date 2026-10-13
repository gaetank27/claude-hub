package api

import (
	"errors"
	"net/http"
	"time"

	"system/internal/claudemetrics"
)

// claudeHistoryResp is the account-scoped (or aggregated) Claude usage history:
// RRD series (per-bucket token/cost totals + active-session gauge) plus the plan
// usage windows from the snapshot table for a specific account.
type claudeHistoryResp struct {
	Available bool                          `json:"available"`
	Range     string                        `json:"range"`
	Account   string                        `json:"account"` // "all" or an account id
	From      int64                         `json:"from"`
	To        int64                         `json:"to"`
	Step      int64                         `json:"step"`
	Series    map[string][]claudemetrics.Point `json:"series"`
	Plan      []planPoint                   `json:"plan"`
}

// getClaudeHistory returns the RRD-backed Claude usage history for a range
// (?range=hour|day|week|month) scoped to one account (?account=<id>) or summed
// across all (?account=all, the default). Plan-usage windows are included only for
// a specific account (a quota percentage can't be summed across accounts).
func (s *Server) getClaudeHistory(w http.ResponseWriter, r *http.Request) {
	rng := r.URL.Query().Get("range")
	if rng == "" {
		rng = "hour"
	}
	if !validHostRange[rng] {
		writeErr(w, http.StatusBadRequest, errors.New("range must be hour, day, week or month"))
		return
	}
	account := r.URL.Query().Get("account")
	if account == "" {
		account = "all"
	}
	if s.claudeRRD == nil || !s.claudeRRD.Available() {
		writeJSON(w, http.StatusOK, claudeHistoryResp{Available: false, Range: rng, Account: account})
		return
	}

	var hist claudemetrics.History
	if account == "all" {
		ids := s.accountIDs()
		hist, _ = s.claudeRRD.ExportAggregate(r.Context(), ids, rng)
	} else {
		h, err := s.claudeRRD.Export(r.Context(), account, rng)
		if err != nil {
			// No samples for this account yet — empty, not an error.
			h = claudemetrics.History{Available: true, Range: rng, Series: map[string][]claudemetrics.Point{}}
		}
		hist = h
	}

	resp := claudeHistoryResp{
		Available: true, Range: rng, Account: account,
		From: hist.From, To: hist.To, Step: hist.Step, Series: hist.Series, Plan: []planPoint{},
	}
	// Plan usage: only meaningful per-account, and only when we have a window.
	if account != "all" && hist.From > 0 {
		from := time.Unix(hist.From, 0)
		to := time.Unix(hist.To, 0)
		if snaps, err := s.db.ListUsageSnapshots(from, to, account); err == nil {
			for _, sn := range snaps {
				resp.Plan = append(resp.Plan, planPoint{
					TS: sn.TS, Session: sn.Session, Week: sn.Week,
					WeekOpus: sn.WeekOpus, WeekSonnet: sn.WeekSonnet,
				})
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// accountIDs lists every Claude account id (for the aggregated "Tous" view).
func (s *Server) accountIDs() []string {
	accts, err := s.db.ListClaudeAccounts()
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(accts))
	for _, a := range accts {
		ids = append(ids, a.ID)
	}
	return ids
}
