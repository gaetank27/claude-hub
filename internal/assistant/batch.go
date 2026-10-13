package assistant

import (
	"fmt"
	"strconv"
	"time"
)

// Grouped wake-ups. When SEVERAL delegated requests are running, the assistant is
// not woken for each end: the ends are retained and handed over together, in one
// wake-up (one <session_deleguee> section per session), so the user gets a single
// recap instead of a burst of notifications.
//
//   - "Several requests running" = at least 2 watched sessions (registry entries,
//     ended or not) at the moment an end is seen. With one request the wake-up is
//     immediate, as before.
//   - Window = from the FIRST retained end (not from the last send): the whole
//     batch is handed over once that end is Interval old. Simpler than a sliding
//     timer and it bounds the delay of every retained session to Interval.
//   - Early release: when no other session is still working, there is nothing left
//     to group, so the batch goes at once (a wait would only delay the recap).
//   - Never retained: a session waiting for the user's answer (ask_user). It is
//     reported immediately, and carries any retained ends along with it.
//   - Retained ends survive a restart (watched.Held in the registry) and are
//     released at the next pass once due. They go through the usual path
//     afterwards, so the night mode still holds the assistant's answer.
//   - Scope: one wake-up per answer route (web / each WhatsApp contact), because
//     the route decides where the recap is sent.
const (
	batchOnKey      = "assistant_batch"         // "0" = off (default on)
	batchMinutesKey = "assistant_batch_minutes" // window, minutes

	DefaultBatchMinutes = 10
	maxBatchMinutes     = 120
)

// BatchSettings are the stored settings of the grouped wake-ups.
type BatchSettings struct {
	Enabled bool
	Minutes int
}

// Interval is the window length.
func (b BatchSettings) Interval() time.Duration { return time.Duration(b.Minutes) * time.Minute }

// Batch returns the settings, defaults filled in.
func (s *Service) Batch() BatchSettings {
	min := DefaultBatchMinutes
	if n, err := strconv.Atoi(s.metaOr(batchMinutesKey, "")); err == nil && n > 0 && n <= maxBatchMinutes {
		min = n
	}
	v, _ := s.db.GetAppMeta(batchOnKey)
	return BatchSettings{Enabled: v != "0", Minutes: min}
}

// SetBatch validates and stores the changed settings (nil = unchanged).
func (s *Service) SetBatch(enabled *bool, minutes *int) error {
	b := s.Batch()
	if enabled != nil {
		b.Enabled = *enabled
	}
	if minutes != nil {
		b.Minutes = *minutes
	}
	if b.Minutes < 1 || b.Minutes > maxBatchMinutes {
		return fmt.Errorf("grouping delay must be between 1 and %d minutes", maxBatchMinutes)
	}
	flag := "1"
	if !b.Enabled {
		flag = "0"
	}
	if err := s.db.SetAppMeta(batchOnKey, flag); err != nil {
		return err
	}
	return s.db.SetAppMeta(batchMinutesKey, strconv.Itoa(b.Minutes))
}

// BatchHeld is the number of ended sessions currently retained.
func (s *Service) BatchHeld() int {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	n := 0
	for _, w := range s.loadWatch() {
		if w.Held > 0 && w.Woken == 0 {
			n++
		}
	}
	return n
}

// retainEnds decides which of the settled ends are handed over now. total is the
// number of watched sessions, pending how many of them are still working or
// settling, hasWaits whether a session waiting for the user is reported in the
// same pass.
func (s *Service) retainEnds(ends []wake, total, pending int, hasWaits bool, now time.Time) []wake {
	b := s.Batch()
	if !b.Enabled || total < 2 || len(ends) == 0 {
		return ends
	}
	nowMs := now.UnixMilli()
	oldest := nowMs
	for i := range ends {
		if ends[i].w.Held == 0 {
			ends[i].w.Held = nowMs
			s.watchUpdate(ends[i].id, func(w *watched) { w.Held = nowMs })
		}
		if ends[i].w.Held < oldest {
			oldest = ends[i].w.Held
		}
	}
	if hasWaits || pending == 0 || now.Sub(time.UnixMilli(oldest)) >= b.Interval() {
		return ends
	}
	return nil
}
