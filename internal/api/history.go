package api

import (
	"net/http"
	"strconv"
	"time"

	"system/internal/store"
)

// Number of buckets shown per granularity (the visible window width).
var bucketSpan = map[string]int{
	"hour": 24, // last 24 hours
	"day":  30, // last 30 days
	"week": 12, // last 12 weeks
}

// historyBucket is one time bucket of session-derived activity. Token/cost/
// compute totals and the started count are attributed to the bucket containing
// each session's creation time; active is the number of sessions whose lifespan
// overlaps the bucket (a rough concurrency/utilization measure).
type historyBucket struct {
	TS              time.Time `json:"ts"`
	InputTokens     int       `json:"inputTokens"`
	OutputTokens    int       `json:"outputTokens"`
	CostUSD         float64   `json:"costUSD"`
	ComputeMS       int       `json:"computeMs"`
	SessionsStarted int       `json:"sessionsStarted"`
	SessionsActive  int       `json:"sessionsActive"`
}

// planPoint is one sampled reading of the subscription usage windows.
type planPoint struct {
	TS         time.Time `json:"ts"`
	Session    *float64  `json:"session,omitempty"`
	Week       *float64  `json:"week,omitempty"`
	WeekOpus   *float64  `json:"weekOpus,omitempty"`
	WeekSonnet *float64  `json:"weekSonnet,omitempty"`
}

type historyResp struct {
	Bucket   string          `json:"bucket"`
	Offset   int             `json:"offset"`
	From     time.Time       `json:"from"`
	To       time.Time       `json:"to"`
	HasNewer bool            `json:"hasNewer"` // a more recent window exists (offset>0)
	Buckets  []historyBucket `json:"buckets"`
	Plan     []planPoint     `json:"plan"`
}

// getHistory returns activity bucketed by hour/day/week for a sliding window.
// `offset` pages the window into the past (0 = most recent, 1 = the window
// before that, …). Session activity is derived live from the journals (so it
// covers all existing history retroactively); plan usage comes from the sampled
// snapshots (which only exist from when sampling began).
func (s *Server) getHistory(w http.ResponseWriter, r *http.Request) {
	bucket := r.URL.Query().Get("bucket")
	span, ok := bucketSpan[bucket]
	if !ok {
		bucket, span = "day", bucketSpan["day"]
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}

	// The most recent bucket is the one containing now; page back by offset
	// whole windows so successive pages tile without gaps or overlap.
	now := time.Now()
	anchor := bucketStart(bucket, now)
	for i := 0; i < offset*span; i++ {
		anchor = bucketAdd(bucket, anchor, -1)
	}

	// Build the span bucket starts ending at `anchor`.
	starts := make([]time.Time, span)
	cur := anchor
	for i := span - 1; i >= 0; i-- {
		starts[i] = cur
		cur = bucketAdd(bucket, cur, -1)
	}
	from := starts[0]
	to := bucketAdd(bucket, starts[span-1], 1)

	buckets := make([]historyBucket, span)
	index := make(map[int64]int, span) // bucket-start unix → slice index
	for i, st := range starts {
		buckets[i].TS = st
		index[st.Unix()] = i
	}

	sessions, err := s.db.ListSessions()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Scope history to the active subscription so charts reflect only the selected
	// account's consumption (matching the dashboard).
	activeID := s.activeAccountID()
	for _, sess := range sessions {
		if sess.ClaudeAccountID != activeID {
			continue
		}
		// Attribute usage to the bucket of the session's start time.
		if bi, ok := index[bucketStart(bucket, sess.CreatedAt).Unix()]; ok {
			u := s.mgr.SessionUsage(sess.ID)
			b := &buckets[bi]
			b.InputTokens += u.InputTokens
			b.OutputTokens += u.OutputTokens
			b.CostUSD += u.CostUSD
			b.ComputeMS += u.ComputeMS
			b.SessionsStarted++
		}
		// Count the session as active in every bucket its lifespan overlaps.
		start, end := sess.CreatedAt, sessionEnd(sess, now)
		if end.Before(from) || !start.Before(to) {
			continue
		}
		for i := range buckets {
			bStart := starts[i]
			bEnd := bucketAdd(bucket, bStart, 1)
			if start.Before(bEnd) && !end.Before(bStart) {
				buckets[i].SessionsActive++
			}
		}
	}

	plan := make([]planPoint, 0)
	if snaps, err := s.db.ListUsageSnapshots(from, to, activeID); err == nil && activeID != "" {
		for _, sn := range snaps {
			plan = append(plan, planPoint{
				TS: sn.TS, Session: sn.Session, Week: sn.Week,
				WeekOpus: sn.WeekOpus, WeekSonnet: sn.WeekSonnet,
			})
		}
	}

	writeJSON(w, http.StatusOK, historyResp{
		Bucket: bucket, Offset: offset, From: from, To: to,
		HasNewer: offset > 0, Buckets: buckets, Plan: plan,
	})
}

// sessionEnd is the instant a session stopped producing activity: its last
// event, or now if still live, or its creation time as a last resort.
func sessionEnd(s store.Session, now time.Time) time.Time {
	if s.LastEventAt != nil {
		return *s.LastEventAt
	}
	if s.State == store.StateRunning || s.State == store.StateStarting {
		return now
	}
	return s.CreatedAt
}

// bucketStart truncates t to the start of its bucket in local time.
func bucketStart(bucket string, t time.Time) time.Time {
	t = t.Local()
	y, m, d := t.Date()
	switch bucket {
	case "hour":
		return time.Date(y, m, d, t.Hour(), 0, 0, 0, t.Location())
	case "week":
		// ISO weeks start Monday; Go's Sunday=0 → shift so Monday=0.
		day := time.Date(y, m, d, 0, 0, 0, 0, t.Location())
		return day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
	default: // day
		return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
	}
}

// bucketAdd advances t by n buckets (n may be negative). It uses calendar math
// (AddDate) for day/week so the boundary stays at local midnight across DST.
func bucketAdd(bucket string, t time.Time, n int) time.Time {
	switch bucket {
	case "hour":
		return t.Add(time.Duration(n) * time.Hour)
	case "week":
		return t.AddDate(0, 0, 7*n)
	default: // day
		return t.AddDate(0, 0, n)
	}
}
