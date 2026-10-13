package assistant

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

// maxCatchUp bounds how much unread journal one pass parses.
const maxCatchUp = 32 << 20

// readNew returns the complete journal lines written after offset and the new
// offset. A journal that shrank (replaced) restarts from its end.
func readNew(path string, offset int64) ([][]byte, int64) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, offset
	}
	if fi.Size() < offset {
		return nil, fi.Size()
	}
	if fi.Size() == offset {
		return nil, offset
	}
	if fi.Size()-offset > maxCatchUp {
		offset = fi.Size() - maxCatchUp
	}
	data := make([]byte, fi.Size()-offset)
	if _, err := io.ReadFull(io.NewSectionReader(f, offset, int64(len(data))), data); err != nil {
		return nil, offset
	}
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		return nil, offset // a line still being written
	}
	return bytes.Split(data[:end], []byte("\n")), offset + int64(end) + 1
}

// Turn outcomes of a session, as exposed by the API (`turn`).
const (
	TurnWorking = "working"
	TurnDone    = "done"
	TurnError   = "error"
)

// bgGrace bounds how long a finished turn is held "working" because a background
// task (Gradle, a dev server…) is still registered: Claude resumes by itself when
// a task ends, but a task that never ends must not keep the session "working"
// forever.
const bgGrace = 30 * time.Minute

// Turn is where a session's journal stands.
type Turn struct {
	Text    string // final answer, or the latest assistant text of a turn in progress
	Outcome string // TurnWorking | TurnDone | TurnError
	// Background: the last turn ended (result) but a background task is still
	// registered or just completed, so Claude is about to resume: not final.
	Background bool
}

// Finished reports whether the last turn is over (done or error).
func (t Turn) Finished() bool { return t.Outcome != TurnWorking }

// ReadTurn reports the state of the latest turn of a journal. A `result` event
// ends a turn, but is NOT the end of the work when background tasks are pending:
// Claude Code emits a result when the model stops talking, then starts a new turn
// by itself when a background task completes. Any activity after the result
// (new assistant output, a user message, a task notification) reopens the turn.
// idle is how long the journal has been silent (0 = unknown): once a background
// wait exceeds bgGrace the turn counts as finished again.
func ReadTurn(path string, idle time.Duration) Turn {
	t := cachedTurn(path)
	if t.Background && idle >= bgGrace {
		// Waited too long for a task that never reports: call the turn over.
		t.Outcome = TurnDone
	}
	return t
}

type turnKey struct {
	size  int64
	mtime int64
}

var (
	turnMu    sync.Mutex
	turnCache = map[string]struct {
		k turnKey
		t Turn
	}{}
)

// cachedTurn parses a journal only when it changed (the session list is polled
// often and journals are large).
func cachedTurn(path string) Turn {
	fi, err := os.Stat(path)
	if err != nil {
		return Turn{Outcome: TurnWorking}
	}
	k := turnKey{fi.Size(), fi.ModTime().UnixNano()}
	turnMu.Lock()
	if c, ok := turnCache[path]; ok && c.k == k {
		turnMu.Unlock()
		return c.t
	}
	turnMu.Unlock()
	t := parseTurn(path)
	turnMu.Lock()
	if len(turnCache) > 256 {
		turnCache = map[string]struct {
			k turnKey
			t Turn
		}{}
	}
	turnCache[path] = struct {
		k turnKey
		t Turn
	}{k, t}
	turnMu.Unlock()
	return t
}

func parseTurn(path string) Turn {
	f, err := os.Open(path)
	if err != nil {
		return Turn{Outcome: TurnWorking}
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Turn{Outcome: TurnWorking}
	}
	const tail = 4 << 20
	start := int64(0)
	if fi.Size() > tail {
		start = fi.Size() - tail
	}
	data, err := io.ReadAll(io.NewSectionReader(f, start, fi.Size()-start))
	if err != nil {
		return Turn{Outcome: TurnWorking}
	}
	t := Turn{Outcome: TurnWorking}
	bg := map[string]bool{} // registered background tasks
	afterResult := false    // a result is the latest turn boundary
	reopen := func() { afterResult, t.Outcome = false, TurnWorking }
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		switch {
		case bytes.Contains(line, []byte(`"type":"result"`)):
			var ev struct {
				Type    string `json:"type"`
				Result  string `json:"result"`
				IsError bool   `json:"is_error"`
			}
			if json.Unmarshal(line, &ev) == nil && ev.Type == "result" {
				t.Text, afterResult, t.Background = ev.Result, true, false
				t.Outcome = TurnDone
				if ev.IsError {
					t.Outcome = TurnError
				}
			}
		case bytes.Contains(line, []byte(`"type":"system"`)):
			var ev struct {
				Type    string `json:"type"`
				Subtype string `json:"subtype"`
				TaskID  string `json:"task_id"`
				Status  string `json:"status"`
				BG      bool   `json:"is_backgrounded"`
				Tasks   []struct {
					TaskID string `json:"task_id"`
				} `json:"tasks"`
			}
			if json.Unmarshal(line, &ev) != nil || ev.Type != "system" {
				continue
			}
			switch ev.Subtype {
			case "background_tasks_changed":
				bg = map[string]bool{}
				for _, k := range ev.Tasks {
					bg[k.TaskID] = true
				}
			case "task_started":
				if ev.BG && ev.TaskID != "" {
					bg[ev.TaskID] = true
				}
			case "task_notification":
				delete(bg, ev.TaskID)
				if afterResult {
					// The task that outlived the turn is done: Claude resumes.
					t.Background = true
					t.Outcome = TurnWorking
				}
			case "init", "status":
				if afterResult {
					reopen()
				}
			}
		case bytes.Contains(line, []byte(`"type":"assistant"`)):
			var ev struct {
				Type    string `json:"type"`
				Message struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &ev) != nil || ev.Type != "assistant" {
				continue
			}
			if afterResult {
				reopen()
			}
			for _, c := range ev.Message.Content {
				if c.Type == "text" && c.Text != "" {
					t.Text = c.Text
				}
			}
			t.Outcome = TurnWorking
		case bytes.Contains(line, []byte(`"type":"user"`)), bytes.Contains(line, []byte(`"type":"stream_event"`)):
			if afterResult {
				reopen()
			}
		}
	}
	// A result with background work pending is not the end.
	if afterResult && len(bg) > 0 {
		t.Background, t.Outcome = true, TurnWorking
	}
	return t
}

// lastAnswer returns the final text of the latest finished turn of a journal,
// falling back to the latest assistant text block of a turn still running.
func lastAnswer(path string) (text string, finished bool) {
	text, finished, _ = lastOutcome(path)
	return text, finished
}

// lastOutcome is lastAnswer that also reports whether the finished turn ended in
// error (a result event with is_error). A turn that ended while a background task
// is still running (or just completed) is not finished.
func lastOutcome(path string) (text string, finished, isError bool) {
	idle := time.Duration(0)
	if fi, err := os.Stat(path); err == nil {
		idle = time.Since(fi.ModTime())
	}
	t := ReadTurn(path, idle)
	return t.Text, t.Finished(), t.Outcome == TurnError
}
