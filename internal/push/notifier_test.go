package push

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"system/internal/store"
)

func newTestNotifier(t *testing.T) (*Notifier, store.Session, *[]Payload) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	n := New(db)
	var sent []Payload
	n.send = func(p Payload) { sent = append(sent, p) }
	s := store.Session{ID: "s1", JournalPath: filepath.Join(dir, "s1.jsonl"), CreatedAt: time.Now().Add(time.Minute)}
	return n, s, &sent
}

func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.WriteString(strings.Join(lines, "\n") + "\n")
}

func assistant(ts int64, content string) string {
	return `{"_ts":` + jsonInt(ts) + `,"type":"assistant","message":{"content":[` + content + `]},"parent_tool_use_id":null}`
}

func jsonInt(v int64) string { b, _ := json.Marshal(v); return string(b) }

func TestQuestionAndPlanArePushedWithTheirContent(t *testing.T) {
	n, s, sent := newTestNotifier(t)
	appendLines(t, s.JournalPath,
		// The streamed start of the call carries no input yet: not a question.
		`{"type":"stream_event","event":{"content_block":{"type":"tool_use","name":"mcp__ask__ask_user","input":{}}}}`,
		assistant(1, `{"type":"text","text":"Let me ask."},{"type":"tool_use","name":"mcp__ask__ask_user","input":{"questions":[{"question":"Which one?","header":"Pick","options":[{"label":"A"},{"label":"B"}]}]}}`),
		assistant(2, `{"type":"tool_use","name":"ExitPlanMode","input":{"plan":"1. do it"}}`),
		`{"_ts":3,"type":"result","is_error":false}`,
	)
	n.watch(s)
	if len(*sent) != 2 {
		t.Fatalf("want 2 pushes, got %+v", *sent)
	}
	q, p := (*sent)[0], (*sent)[1]
	if q.Kind != "question" || len(q.Questions) != 1 || !strings.Contains(string(q.Questions[0]), `"Which one?"`) {
		t.Errorf("question push: %+v", q)
	}
	if p.Kind != "plan" || p.Text != "1. do it" {
		t.Errorf("plan push: %+v", p)
	}
}

// A finished turn does not wait for an answer: the app shows it as unread.
func TestRepliesAreNotPushed(t *testing.T) {
	n, s, sent := newTestNotifier(t)
	appendLines(t, s.JournalPath,
		assistant(1, `{"type":"text","text":"All done."}`),
		`{"_ts":2,"type":"result"}`,
	)
	n.watch(s)
	if len(*sent) != 0 {
		t.Fatalf("want no push, got %+v", *sent)
	}
}

func TestOlderSessionStartsAtJournalEnd(t *testing.T) {
	n, s, sent := newTestNotifier(t)
	s.CreatedAt = time.Now().Add(-time.Hour)
	appendLines(t, s.JournalPath, assistant(1, `{"type":"tool_use","name":"ExitPlanMode","input":{"plan":"old"}}`))
	n.watch(s)
	appendLines(t, s.JournalPath, assistant(2, `{"type":"tool_use","name":"ExitPlanMode","input":{"plan":"new"}}`))
	n.watch(s)
	if len(*sent) != 1 || (*sent)[0].Text != "new" {
		t.Fatalf("want only the new plan, got %+v", *sent)
	}
}

func TestEncodeFitsAPushMessage(t *testing.T) {
	long := strings.Repeat("é", 4000)
	q, _ := json.Marshal(map[string]any{"question": "Q?", "options": []map[string]string{{"label": "A", "description": long}}})
	out := encode(Payload{SessionID: "s", Kind: "question", Text: long, Questions: []json.RawMessage{q}})
	if len(out) > maxPayload {
		t.Fatalf("payload is %d bytes", len(out))
	}
	var p Payload
	if err := json.Unmarshal(out, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Questions) != 1 || !strings.Contains(string(p.Questions[0]), `"label":"A"`) {
		t.Errorf("the option should survive without its description: %s", out)
	}
}

func TestExcerptStripsMarkdown(t *testing.T) {
	in := "## Titre\n- **Gras** et _italique_ avec [un lien](https://x.y/z)\n```go\ncode()\n```\n| a | b |\n|---|---|\nFin `inline`."
	got := Excerpt(in, 200)
	want := "Titre Gras et italique avec un lien a b Fin inline."
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if long := Excerpt(strings.Repeat("é", 300), 10); long != strings.Repeat("é", 10)+"…" {
		t.Fatalf("cut: %q", long)
	}
}

func TestAssistantPushesAreSpacedAndMerged(t *testing.T) {
	n, _, sent := newTestNotifier(t)
	var mu sync.Mutex
	n.send = func(p Payload) { mu.Lock(); *sent = append(*sent, p); mu.Unlock() }
	n.NotifyAssistant("**un**")
	n.NotifyAssistant("deux")
	n.NotifyAssistant("trois")
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(*sent) != 1 {
		t.Fatalf("burst should send one push at once, got %d", len(*sent))
	}
	p := (*sent)[0]
	if p.SessionID != "assistant" || p.Kind != "reply" || p.Project != "Assistant" || p.Text != "un" {
		t.Fatalf("payload %+v", p)
	}
	if n.gate.waiting != "trois" || n.gate.timer == nil {
		t.Fatalf("latest text should wait for the window: %+v", n.gate.waiting)
	}
	n.gate.timer.Stop()
}
