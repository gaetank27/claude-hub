package session

import (
	"context"
	"log"
	"time"

	"system/internal/runner"
	"system/internal/store"
)

// defaultReconcileInterval is how often the runtime reconciler sweeps running
// sessions to detect runners that died on their own.
const defaultReconcileInterval = 30 * time.Second

// RunReconciler periodically reconciles stored session state with reality until
// ctx is cancelled. It exists because nothing else observes a runner that dies
// between backend restarts: Recover only runs at boot. Without it, a crashed or
// OOM-killed runner leaves its session stuck on "running" forever, with no unit
// left to stop (transient units are --collect'd on exit).
//
// Unlike Recover — which resumes a dead session at boot, assuming the death was
// a reboot — a mid-flight death is unexpected (crash, OOM, runner exit), so the
// reconciler marks the session errored rather than relaunching it. Relaunching
// here would crash-loop a runner that is failing on startup.
func (m *Manager) RunReconciler(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = defaultReconcileInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.reconcileOnce(ctx)
		}
	}
}

func (m *Manager) reconcileOnce(ctx context.Context) {
	sessions, err := m.db.ListSessionsByState(store.StateRunning)
	if err != nil {
		log.Printf("reconcile: list running sessions: %v", err)
		return
	}
	for _, s := range sessions {
		// Checking transitioning BEFORE unitActive is what closes the race with a
		// deliberate stop+relaunch (DecidePlan): that path sets transitioning
		// before it stops the unit, so a unit seen absent here without the flag
		// set can only be an unexpected death.
		m.mu.Lock()
		skip := m.transitioning[s.ID]
		m.mu.Unlock()
		if skip || unitActive(ctx, s.ID) {
			continue
		}
		// The unit is gone. A clean claude exit recorded by the runner means the
		// session ended normally (stopped); anything else (crash, OOM, kill, or no
		// marker at all) is an unexpected death (error).
		m.stopSupervisor(s.ID)
		state := store.StateError
		if runner.CleanExit(s.JournalPath) {
			state = store.StateStopped
			// The supervisor is already stopped above, so no goroutine races git on
			// this repo: finish the autonomous git workflow if claude left it
			// incomplete. Failures must not block the state write below.
			if isAutonomousJournal(s.JournalPath) {
				m.ApplyGitFallback(ctx, s)
			}
		}
		log.Printf("reconcile: session %s runner is gone, marking %s", s.ID, state)
		if err := m.db.SetSessionState(s.ID, state); err != nil {
			log.Printf("reconcile: mark session %s %s: %v", s.ID, state, err)
		}
	}
}
