package session

import (
	"context"
	"log"
	"os"
	"time"

	"system/internal/store"
)

const (
	// botIdleStop: a bot session with no journal activity for this long is stopped.
	botIdleStop = 12 * time.Hour
	// botClosedArchive: a stopped bot session is archived after this long closed.
	botClosedArchive = 12 * time.Hour
	autoArchiveEvery = 10 * time.Minute
)

// RunAutoArchive periodically stops idle bot sessions (source "whatsapp"/"api",
// driven from outside the Hub) and archives the ones that stayed closed, until
// ctx is cancelled.
func (m *Manager) RunAutoArchive(ctx context.Context) {
	t := time.NewTicker(autoArchiveEvery)
	defer t.Stop()
	m.autoArchiveOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.autoArchiveOnce(ctx)
		}
	}
}

func (m *Manager) autoArchiveOnce(ctx context.Context) {
	sessions, err := m.db.ListActiveSessions()
	if err != nil {
		log.Printf("autoarchive: %v", err)
		return
	}
	now := time.Now()
	for _, s := range sessions {
		if s.Source != "whatsapp" && s.Source != "api" && s.Source != "assistant" && s.Source != "assistant-task" {
			continue
		}
		// Last activity: journal mtime, else last event, else creation.
		last := s.CreatedAt
		if s.LastEventAt != nil && s.LastEventAt.After(last) {
			last = *s.LastEventAt
		}
		if fi, err := os.Stat(s.JournalPath); err == nil && fi.ModTime().After(last) {
			last = fi.ModTime()
		}
		switch s.State {
		case store.StateRunning, store.StateStarting:
			if now.Sub(last) >= botIdleStop {
				if err := m.StopSession(ctx, s.ID); err != nil {
					log.Printf("autoarchive: stop %s: %v", s.ID, err)
				}
			}
		default:
			closed := last
			if at, ok := m.db.SessionStoppedAt(s.ID); ok {
				closed = at
			}
			if now.Sub(closed) >= botClosedArchive {
				if err := m.db.SetSessionArchived(s.ID, true); err != nil {
					log.Printf("autoarchive: archive %s: %v", s.ID, err)
				}
			}
		}
	}
}
