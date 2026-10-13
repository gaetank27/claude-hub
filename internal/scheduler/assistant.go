package scheduler

import (
	"context"
	"log"
	"sync"

	"system/internal/store"
)

// Dispatcher is the personal assistant: tasks of its project are answered in its
// conversation instead of opening a session of their own.
type Dispatcher interface {
	ProjectID() string
	FireTask(ctx context.Context, name, prompt string) error
}

var (
	dispMu     sync.RWMutex
	dispatcher Dispatcher
)

// SetAssistant routes the assistant project's prompt tasks to d.
func SetAssistant(d Dispatcher) {
	dispMu.Lock()
	dispatcher = d
	dispMu.Unlock()
}

// TryAssistant runs t through the assistant when it belongs to the assistant
// project; it reports whether it did.
func TryAssistant(ctx context.Context, t store.ScheduledTask) bool {
	dispMu.RLock()
	d := dispatcher
	dispMu.RUnlock()
	if d == nil || t.Command != "" || t.ProjectID == "" || t.ProjectID != d.ProjectID() {
		return false
	}
	if err := d.FireTask(ctx, t.Name, t.Prompt); err != nil {
		log.Printf("scheduler: task %s (%s): assistant: %v", t.ID, t.Name, err)
	}
	return true
}
