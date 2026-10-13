package scriptstatus

import (
	"context"
	"log"
	"time"
)

// CheckWake wakes the assistant when a script's effective state is error or muet
// and it has not been told yet; a return to another state re-arms it. Called
// after each report and every minute by Run (silence has no incoming request).
func (s *Service) CheckWake(script string) {
	rep, ok, err := s.Get(script)
	if err != nil || !ok {
		return
	}
	var notified string
	if err := s.db.QueryRow(`SELECT notified FROM script_status WHERE script=?`, script).Scan(&notified); err != nil {
		return
	}
	want := ""
	if rep.Effective == "error" || rep.Effective == "muet" {
		want = rep.Effective
	}
	if want == notified {
		return
	}
	if want != "" {
		keys, _ := keysForScript(s.db, script)
		if _, wake := silenceFor(keys); wake && s.wake != nil {
			if err := s.wake(script, want); err != nil {
				log.Printf("script-status: wake %s: %v", script, err)
				return // retried next minute
			}
		}
	}
	_, _ = s.db.Exec(`UPDATE script_status SET notified=? WHERE script=?`, want, script)
}

// Run checks every script for silence once a minute until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.PurgeImages()
			rows, err := s.db.Query(`SELECT script FROM script_status`)
			if err != nil {
				continue
			}
			var names []string
			for rows.Next() {
				var n string
				if rows.Scan(&n) == nil {
					names = append(names, n)
				}
			}
			rows.Close()
			for _, n := range names {
				s.CheckWake(n)
			}
		}
	}
}
