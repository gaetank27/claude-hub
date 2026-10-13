package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"system/internal/store"
)

const (
	pollEvery = 2 * time.Second
	// maxPayload keeps the JSON well under the WebPush body limit.
	maxPayload = 3000
	maxText    = 1200
	// maxCatchUp bounds how much unread journal one pass parses; past it (a
	// backend that was down for a long turn) the older events are skipped.
	maxCatchUp = 32 << 20

	askTool  = "mcp__ask__ask_user"
	planTool = "ExitPlanMode"
)

// Payload is the message a phone receives. The Android app decodes this shape.
type Payload struct {
	SessionID string            `json:"sessionId"`
	Kind      string            `json:"kind"` // question | plan | reply
	Project   string            `json:"project"`
	Text      string            `json:"text,omitempty"`
	Questions []json.RawMessage `json:"questions,omitempty"`
}

// Notifier watches the running sessions and pushes the requests that wait for
// the user's answer: questions and plans. Nothing else is worth interrupting
// them for — a finished turn or a flagged session shows in the app.
type Notifier struct {
	db      *store.DB
	client  *http.Client
	started time.Time
	tails   map[string]*tail
	// send delivers one payload; replaced in tests.
	send func(Payload)
	gate assistantGate
}

// tail is the notifier's reading position in one session's journal.
type tail struct {
	offset int64
}

func New(db *store.DB) *Notifier {
	n := &Notifier{
		db:      db,
		client:  &http.Client{Timeout: 15 * time.Second},
		started: time.Now(),
		tails:   make(map[string]*tail),
	}
	n.send = n.deliver
	return n
}

// Run polls until ctx is cancelled.
func (n *Notifier) Run(ctx context.Context) {
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	for {
		n.poll()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (n *Notifier) poll() {
	sessions, err := n.db.ListSessionsByState(store.StateRunning)
	if err != nil {
		return
	}
	running := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		// WhatsApp sessions talk to their own correspondent, not to the Hub's user.
		if s.Source == "whatsapp" || s.Source == "assistant" {
			continue
		}
		running[s.ID] = true
		n.watch(s)
	}
	for id := range n.tails {
		if !running[id] {
			delete(n.tails, id)
		}
	}
}

func (n *Notifier) watch(s store.Session) {
	t, known := n.tails[s.ID]
	if !known {
		t = &tail{}
		// A session that predates this process (or is being resumed) has a past
		// that was already reported, or is too old to matter: start at the end.
		if s.CreatedAt.Before(n.started) {
			if fi, err := os.Stat(s.JournalPath); err == nil {
				t.offset = fi.Size()
			}
		}
		n.tails[s.ID] = t
	}

	for _, line := range readNew(s.JournalPath, t) {
		n.observe(s, line)
	}

}

// readNew returns the complete journal lines written since the last pass.
func readNew(path string, t *tail) [][]byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() <= t.offset {
		if err == nil && fi.Size() < t.offset {
			t.offset = fi.Size() // journal replaced: start over from its end
		}
		return nil
	}
	if fi.Size()-t.offset > maxCatchUp {
		t.offset = fi.Size() - maxCatchUp
	}
	data := make([]byte, fi.Size()-t.offset)
	if _, err := io.ReadFull(io.NewSectionReader(f, t.offset, int64(len(data))), data); err != nil {
		return nil
	}
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		return nil // a line still being written
	}
	t.offset += int64(end) + 1
	return bytes.Split(data[:end], []byte("\n"))
}

type journalEvent struct {
	Type    string `json:"type"`
	Message struct {
		Content []struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

// observe pushes the question or plan a journal line carries, if any.
func (n *Notifier) observe(s store.Session, line []byte) {
	// Cheap test first: stream deltas and tool results are the bulk of a journal.
	if !bytes.Contains(line, []byte(`"type":"assistant"`)) || !bytes.Contains(line, []byte(`"type":"tool_use"`)) {
		return
	}
	var ev journalEvent
	if json.Unmarshal(line, &ev) != nil || ev.Type != "assistant" {
		return
	}
	for _, c := range ev.Message.Content {
		switch {
		case c.Type == "tool_use" && c.Name == askTool:
			var in struct {
				Questions []json.RawMessage `json:"questions"`
			}
			_ = json.Unmarshal(c.Input, &in)
			n.send(Payload{SessionID: s.ID, Kind: "question", Questions: in.Questions})
		case c.Type == "tool_use" && c.Name == planTool:
			var in struct {
				Plan string `json:"plan"`
			}
			_ = json.Unmarshal(c.Input, &in)
			n.send(Payload{SessionID: s.ID, Kind: "plan", Text: in.Plan})
		}
	}
}

// deliver posts p to every registered phone, off the polling loop.
func (n *Notifier) deliver(p Payload) {
	endpoints, err := n.db.ListPushEndpoints()
	if err != nil || len(endpoints) == 0 {
		return
	}
	if s, err := n.db.GetSession(p.SessionID); err == nil {
		if proj, err := n.db.GetProject(s.ProjectID); err == nil {
			p.Project = proj.Name
		}
	}
	body := encode(p)
	for _, e := range endpoints {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			err := Send(ctx, n.client, e.Endpoint, e.P256dh, e.Auth, body)
			if errors.Is(err, ErrGone) {
				_ = n.db.DeletePushEndpoint(e.Endpoint)
			} else if err != nil {
				log.Printf("push: %s (%s): %v", p.Kind, e.Device, err)
			}
		}()
	}
}

// encode serializes p, trimming it until it fits a push message: first the
// text, then the options' descriptions, then the options themselves.
func encode(p Payload) []byte {
	p.Text = truncate(p.Text, maxText)
	out, _ := json.Marshal(p)
	for step := 0; len(out) > maxPayload && step < 3; step++ {
		switch step {
		case 0:
			p.Questions = mapQuestions(p.Questions, func(q map[string]any) {
				opts, _ := q["options"].([]any)
				for _, o := range opts {
					if m, ok := o.(map[string]any); ok {
						delete(m, "description")
					}
				}
			})
		case 1:
			p.Questions = mapQuestions(p.Questions, func(q map[string]any) { delete(q, "options") })
		case 2:
			// Still too long: the tap opens the session, where the question is whole.
			p.Questions = nil
		}
		out, _ = json.Marshal(p)
	}
	return out
}

func mapQuestions(qs []json.RawMessage, edit func(map[string]any)) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(qs))
	for _, raw := range qs {
		var q map[string]any
		if json.Unmarshal(raw, &q) != nil {
			continue
		}
		edit(q)
		if b, err := json.Marshal(q); err == nil {
			out = append(out, b)
		}
	}
	return out
}

// truncate cuts s to at most max bytes on a character boundary.
func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
