package scheduler

import (
	"bytes"
	"context"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"

	"system/internal/store"
)

const (
	commandTimeout = time.Hour
	// outputTail is how much of a script's combined output is kept in the DB.
	outputTail = 4000
)

// runningCommands guards against overlapping runs of the same script task
// (a slow script on a tight schedule, or "run now" during a scheduled run).
var runningCommands sync.Map

// RunCommand executes a script task's shell command (bash -c) and records its
// outcome (ok/error + output tail) on the task. It blocks until the command
// exits or commandTimeout elapses. The working directory is the task's project
// directory, or the home directory for global tasks.
func RunCommand(db *store.DB, t store.ScheduledTask) {
	if _, busy := runningCommands.LoadOrStore(t.ID, struct{}{}); busy {
		log.Printf("scheduler: task %s (%s): previous run still in progress, skipping", t.ID, t.Name)
		return
	}
	defer runningCommands.Delete(t.ID)

	dir, _ := os.UserHomeDir()
	if t.ProjectID != "" {
		if p, err := db.GetProject(t.ProjectID); err == nil && p.Dir != "" {
			dir = p.Dir
		}
	}

	_ = db.SetTaskResult(t.ID, "running", "")

	// Deliberately not tied to the server context: a script should not be killed
	// mid-run just because the scheduler loop is shutting down.
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "bash", "-c", t.Command)
	cmd.Dir = dir
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()

	status := "ok"
	if err != nil {
		status = "error"
		out.WriteString("\n[" + err.Error() + "]")
	}
	b := out.Bytes()
	if len(b) > outputTail {
		b = b[len(b)-outputTail:]
	}
	if err := db.SetTaskResult(t.ID, status, string(bytes.ToValidUTF8(b, nil))); err != nil {
		log.Printf("scheduler: task %s (%s): persist result: %v", t.ID, t.Name, err)
	}
	log.Printf("scheduler: ran script task %s (%s): %s", t.ID, t.Name, status)
}
