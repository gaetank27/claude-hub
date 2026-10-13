package scheduler

import (
	"context"
	"log"
	"time"

	"system/internal/store"
)

// SessionLauncher abstracts the session.Manager methods the scheduler needs.
type SessionLauncher interface {
	LaunchSession(ctx context.Context, projectID, permissionMode, model, comment, source string) (store.Session, error)
	SendUserMessage(sessionID, text string) error
}

// Run starts the task scheduler, ticking every minute. It blocks until ctx is
// cancelled. Call it in a goroutine from main.
func Run(ctx context.Context, db *store.DB, mgr SessionLauncher, systemProjectID string) {
	// Align to the next whole minute so fires land predictably at :00 seconds.
	now := time.Now()
	delay := time.Until(now.Truncate(time.Minute).Add(time.Minute))
	select {
	case <-ctx.Done():
		return
	case <-time.After(delay):
	}

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		runDue(ctx, db, mgr, systemProjectID)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func runDue(ctx context.Context, db *store.DB, mgr SessionLauncher, systemProjectID string) {
	// Truncated to the second: next_run_at is stored as text, and "…:00Z" sorts after
	// "…:00.123Z", so a tick a few ms past the minute would postpone the task by one.
	tasks, err := db.ListDueTasks(time.Now().UTC().Truncate(time.Second))
	if err != nil {
		log.Printf("scheduler: list due tasks: %v", err)
		return
	}
	for _, t := range tasks {
		go fireTask(ctx, db, mgr, t, systemProjectID)
	}
}

func fireTask(ctx context.Context, db *store.DB, mgr SessionLauncher, t store.ScheduledTask, systemProjectID string) {
	// Script tasks run a shell command directly: no session, no tokens.
	if t.Command != "" {
		markFired(db, t, "")
		RunCommand(db, t)
		return
	}

	if TryAssistant(ctx, t) {
		markFired(db, t, "")
		return
	}

	projectID := t.ProjectID
	if projectID == "" {
		projectID = systemProjectID
	}

	mode := t.PermissionMode
	if mode == "" {
		mode = "default"
	}

	// Reuse the task's existing session if it is still alive (running/starting).
	// Create a new one only on the first run or when the prior session is gone.
	sessID := ""
	if t.LastSessionID != "" {
		if s, err := db.GetSession(t.LastSessionID); err == nil &&
			(s.State == "running" || s.State == "starting") {
			sessID = s.ID
			log.Printf("scheduler: task %s (%s): reusing session %s", t.ID, t.Name, sessID)
		}
	}

	if sessID == "" {
		sess, err := mgr.LaunchSession(ctx, projectID, mode, t.Model, t.Name, "scheduler")
		if err != nil {
			log.Printf("scheduler: task %s (%s): launch session: %v", t.ID, t.Name, err)
			advanceNextRun(db, t)
			return
		}
		sessID = sess.ID
	}

	if t.Prompt != "" {
		if err := mgr.SendUserMessage(sessID, t.Prompt); err != nil {
			log.Printf("scheduler: task %s (%s): send prompt: %v", t.ID, t.Name, err)
		}
	}

	markFired(db, t, sessID)
}

// markFired records a fire and schedules the next one (or disables a one-shot).
func markFired(db *store.DB, t store.ScheduledTask, sessID string) {
	now := time.Now().UTC()

	// One-shot tasks disable themselves after the first successful fire.
	if t.OneShot {
		if err := db.SetTaskEnabled(t.ID, false); err != nil {
			log.Printf("scheduler: task %s (%s): disable one-shot: %v", t.ID, t.Name, err)
		}
		_ = db.SetTaskLastRun(t.ID, sessID, now, now.AddDate(10, 0, 0))
		log.Printf("scheduler: fired one-shot task %s (%s) → session %q", t.ID, t.Name, sessID)
		return
	}

	next, err := Next(t.Schedule, now)
	if err != nil {
		log.Printf("scheduler: task %s (%s): compute next run: %v", t.ID, t.Name, err)
		next = now.Add(time.Hour) // fallback: retry in 1 hour
	}
	if err := db.SetTaskLastRun(t.ID, sessID, now, next); err != nil {
		log.Printf("scheduler: task %s (%s): persist last run: %v", t.ID, t.Name, err)
	}
	log.Printf("scheduler: fired task %s (%s) → session %q, next %s", t.ID, t.Name, sessID, next.Format(time.RFC3339))
}

// advanceNextRun pushes next_run_at forward even when a launch fails, so the
// scheduler doesn't retry the same minute forever.
func advanceNextRun(db *store.DB, t store.ScheduledTask) {
	next, err := Next(t.Schedule, time.Now().UTC())
	if err != nil {
		next = time.Now().UTC().Add(time.Hour)
	}
	_ = db.SetTaskLastRun(t.ID, t.LastSessionID, time.Now().UTC(), next)
}
