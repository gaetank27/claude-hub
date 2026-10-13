package session

import (
	"context"
	"os"

	"system/internal/store"
)

// SessionResources is a live snapshot of a running session's resource use.
type SessionResources struct {
	MemoryBytes  int64 `json:"memoryBytes"`  // current RSS-ish, from systemd MemoryCurrent
	CPUNSec      int64 `json:"cpuNSec"`      // cumulative CPU time, from systemd CPUUsageNSec
	JournalBytes int64 `json:"journalBytes"` // on-disk journal size
	HasMetrics   bool  `json:"hasMetrics"`   // false when systemd had no accounting for the unit
}

// SessionResources probes a session's live memory/CPU from systemd and its
// journal size from disk. It only queries systemd for running sessions (a
// finished session has no unit); journal size is always read. Probing is one
// exec per call, so callers should limit it to running sessions and a modest
// poll cadence (the monitoring page).
func (m *Manager) SessionResources(ctx context.Context, sessionID string) SessionResources {
	var r SessionResources
	s, err := m.db.GetSession(sessionID)
	if err != nil {
		return r
	}
	if fi, err := os.Stat(s.JournalPath); err == nil {
		r.JournalBytes = fi.Size()
	}
	if s.State == store.StateRunning {
		if mem, cpu, ok := unitResources(ctx, sessionID); ok {
			r.MemoryBytes, r.CPUNSec, r.HasMetrics = mem, cpu, true
		}
	}
	return r
}
