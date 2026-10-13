package session

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"time"

	"system/internal/store"
)

// autoPauseMarkerPath is a sidecar file recording that the session guard paused a
// session because its subscription's 5-hour window hit the limit. It carries the
// window's reset time so the guard can resume the session at the next reset even
// if the usage endpoint is unreachable then. Kept next to the journal so it
// survives a backend restart (the resume pass re-discovers it at any later tick).
func autoPauseMarkerPath(journalPath string) string { return journalPath + ".auto-paused" }

// autoPause is the content of an auto-pause marker.
type autoPause struct {
	AccountID string    `json:"accountId"`
	ResetsAt  time.Time `json:"resetsAt"`
	PausedAt  time.Time `json:"pausedAt"`
}

func writeAutoPauseMarker(journalPath string, mk autoPause) {
	b, err := json.Marshal(mk)
	if err != nil {
		return
	}
	_ = os.WriteFile(autoPauseMarkerPath(journalPath), b, 0o600)
}

func clearAutoPauseMarker(journalPath string) { _ = os.Remove(autoPauseMarkerPath(journalPath)) }

// readAutoPauseMarker returns the marker and true when one exists and parses.
func readAutoPauseMarker(journalPath string) (autoPause, bool) {
	b, err := os.ReadFile(autoPauseMarkerPath(journalPath))
	if err != nil {
		return autoPause{}, false
	}
	var mk autoPause
	if json.Unmarshal(b, &mk) != nil {
		return autoPause{}, false
	}
	return mk, true
}

// PauseAccountSessions cleanly stops every live session running under accountID,
// tagging each with an auto-pause marker so the guard resumes it at resetsAt.
// Called when the account's 5-hour window crosses the pause threshold. Idempotent:
// already-stopped sessions are skipped, so re-running it while the window stays
// over the limit is a no-op. The marker is written BEFORE the stop so a crash in
// between still leaves the session resumable (the resume pass only ever acts on
// sessions that are actually stopped, so a marker on a still-running session is
// harmless — the next tick re-stops it).
func (m *Manager) PauseAccountSessions(ctx context.Context, accountID string, resetsAt time.Time) {
	sessions, err := m.db.ListSessions()
	if err != nil {
		log.Printf("guard: list sessions: %v", err)
		return
	}
	// No reset time from upstream → fall back to one window length out, so a paused
	// session is never stranded waiting for a reset we never learned.
	if resetsAt.IsZero() {
		resetsAt = time.Now().Add(5 * time.Hour)
	}
	for _, s := range sessions {
		if s.ClaudeAccountID != accountID {
			continue
		}
		if s.State != store.StateRunning && s.State != store.StateStarting {
			continue
		}
		writeAutoPauseMarker(s.JournalPath, autoPause{
			AccountID: accountID, ResetsAt: resetsAt, PausedAt: time.Now().UTC(),
		})
		if err := m.StopSession(ctx, s.ID); err != nil {
			log.Printf("guard: pause session %s: %v", s.ID, err)
			continue
		}
		log.Printf("guard: paused session %s (account %s at limit; resumes at %s)",
			s.ID, accountID, resetsAt.Format(time.RFC3339))
	}
}

// ResumeDueSessions resumes every guard-paused session whose subscription window
// has reset (now >= the marker's resetsAt). It runs on each guard tick and needs
// no network, so paused work restarts at the reset even if the usage endpoint is
// down. Only sessions still in the stopped state are touched.
func (m *Manager) ResumeDueSessions(ctx context.Context) {
	sessions, err := m.db.ListSessionsByState(store.StateStopped)
	if err != nil {
		log.Printf("guard: list stopped sessions: %v", err)
		return
	}
	now := time.Now()
	for _, s := range sessions {
		mk, ok := readAutoPauseMarker(s.JournalPath)
		if !ok || now.Before(mk.ResetsAt) {
			continue
		}
		proj, err := m.db.GetProject(s.ProjectID)
		if err != nil {
			log.Printf("guard: resume session %s: get project: %v", s.ID, err)
			continue
		}
		if err := m.resumeStopped(ctx, proj, s); err != nil {
			log.Printf("guard: resume session %s: %v", s.ID, err)
			continue // keep the marker so a later tick retries
		}
		clearAutoPauseMarker(s.JournalPath)
		log.Printf("guard: resumed session %s after window reset", s.ID)
	}
}

// resumeStopped relaunches a stopped session with --resume so claude continues
// the same journal, mirroring the boot-time Recover path: shield the relaunch from
// the reconciler, wait for the runner socket, mark it running, and re-arm the
// hand-back supervisor for autonomous sessions (autonomy is inferred from the
// journal, which survives the later switch to bypassPermissions).
func (m *Manager) resumeStopped(ctx context.Context, proj store.Project, s store.Session) error {
	m.beginTransition(s.ID)
	defer m.endTransition(s.ID)
	// The transient unit name must be free before the resume relaunch reuses it.
	if err := waitForUnitInactive(ctx, s.ID, 10*time.Second); err != nil {
		return err
	}
	if err := m.launch(ctx, proj, s, true); err != nil {
		m.db.SetSessionState(s.ID, store.StateError)
		return err
	}
	if err := m.waitForSocket(s.SocketPath, 15*time.Second); err != nil {
		m.db.SetSessionState(s.ID, store.StateError)
		return err
	}
	if err := m.db.SetSessionState(s.ID, store.StateRunning); err != nil {
		return err
	}
	if isAutonomousJournal(s.JournalPath) {
		m.superviseAutonomous(s)
	}
	return nil
}
