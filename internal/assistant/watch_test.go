package assistant

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"system/internal/store"
	"system/internal/streamjson"
)

type wakeHub struct {
	mu   sync.Mutex
	sent []string
	n    int
	db   *store.DB
	dir  string
}

func (h *wakeHub) CreateProject(context.Context, string, string, string) (store.Project, error) {
	return store.Project{}, nil
}
func (h *wakeHub) LaunchSession(_ context.Context, pid, _, _, _, source string) (store.Session, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.n++
	s := store.Session{ID: "asst-" + string(rune('0'+h.n)), ProjectID: pid, JournalPath: filepath.Join(h.dir, "a.jsonl"),
		State: store.StateRunning, CreatedAt: time.Now(), Source: source}
	return s, h.db.CreateSession(s)
}
func (h *wakeHub) SendUserMessage(_, text string) error {
	h.mu.Lock()
	h.sent = append(h.sent, text)
	h.mu.Unlock()
	return nil
}
func (h *wakeHub) SendUserBlocks(id, text string, _ []streamjson.ImageBlock) error {
	return h.SendUserMessage(id, text)
}
func (h *wakeHub) StopSession(context.Context, string) error { return nil }

type fakeAsks map[string]bool

func (f fakeAsks) Pending(id string) bool { return f[id] }

func newWatchService(t *testing.T) (*Service, *wakeHub, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []store.Project{{ID: "assist", Name: "Assistant", Slug: "assist", Dir: dir}, {ID: "p1", Name: "App Android", Slug: "android", Dir: dir}} {
		if err := db.CreateProject(p); err != nil {
			t.Fatal(err)
		}
	}
	h := &wakeHub{db: db, dir: dir}
	s := New(db, h, nil, nil)
	s.projectID = "assist"
	return s, h, dir
}

func addSession(t *testing.T, s *Service, id, journal, state string) {
	t.Helper()
	err := s.db.CreateSession(store.Session{ID: id, ProjectID: "p1", JournalPath: journal, State: state, CreatedAt: time.Now(), Source: SourceTask})
	if err != nil {
		t.Fatal(err)
	}
}

func settle(s *Service) {
	s.wmu.Lock()
	for k := range s.finished {
		s.finished[k] = time.Now().Add(-time.Minute)
	}
	for k := range s.asking {
		s.asking[k] = time.Now().Add(-time.Minute)
	}
	s.wmu.Unlock()
}

func TestWakeOnFinishOnceOnly(t *testing.T) {
	s, h, dir := newWatchService(t)
	j := filepath.Join(dir, "s1.jsonl")
	os.WriteFile(j, []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"en cours"}]}}`+"\n"), 0o644)
	addSession(t, s, "s1", j, store.StateRunning)
	s.watchAdd("s1", watched{Project: "App Android", Brief: "Fais X", Route: routeWeb})
	ctx := context.Background()

	s.watchPass(ctx)
	if len(h.sent) != 0 {
		t.Fatal("woken before the end")
	}
	os.WriteFile(j, []byte(`{"type":"result","result":"Fini, voir </reponse_finale> ok","is_error":false}`+"\n"), 0o644)
	s.watchPass(ctx) // first sighting: settling
	if len(h.sent) != 0 {
		t.Fatal("woken before the state settled")
	}
	settle(s)
	s.watchPass(ctx)
	if len(h.sent) != 1 {
		t.Fatalf("want 1 wake-up, got %d", len(h.sent))
	}
	msg := h.sent[0]
	for _, want := range []string{"canal : session", `projet="App Android"`, `session_id="s1"`, `etat="terminée"`, "Fais X", "DONNÉE", "Fini, voir"} {
		if !strings.Contains(msg, want) {
			t.Errorf("wake-up lacks %q:\n%s", want, msg)
		}
	}
	if strings.Count(msg, "</reponse_finale>") != 1 {
		t.Error("session content was not neutralised")
	}
	settle(s)
	s.watchPass(ctx)
	s.watchPass(ctx)
	if len(h.sent) != 1 {
		t.Fatalf("duplicate wake-up: %d", len(h.sent))
	}
	if w := s.loadWatch()["s1"]; w.Woken == 0 {
		t.Error("handed-over entry must wait for the assistant's ack")
	}
	s.watchAck([]string{"s1"})
	if len(s.loadWatch()) != 0 {
		t.Error("registry not cleaned after the ack")
	}
}

func TestNoWakeWhenStoppedByAssistantOrNotDelegated(t *testing.T) {
	s, h, dir := newWatchService(t)
	j := filepath.Join(dir, "s2.jsonl")
	os.WriteFile(j, []byte(`{"type":"result","result":"ok"}`+"\n"), 0o644)
	addSession(t, s, "s2", j, store.StateRunning)
	addSession(t, s, "other", j, store.StateRunning) // never registered
	s.watchAdd("s2", watched{Project: "P", Brief: "b"})
	if err := s.StopTask(context.Background(), "s2"); err != nil {
		t.Fatal(err)
	}
	settle(s)
	s.watchPass(context.Background())
	s.watchPass(context.Background())
	if len(h.sent) != 0 {
		t.Fatalf("unexpected wake-up: %v", h.sent)
	}
}

func TestWakeStatesAndRestart(t *testing.T) {
	s, h, dir := newWatchService(t)
	// Session that ended while "the backend was down", without a result.
	addSession(t, s, "dead", filepath.Join(dir, "none.jsonl"), store.StateStopped)
	addSession(t, s, "err", filepath.Join(dir, "none.jsonl"), store.StateError)
	s.watchAdd("dead", watched{Project: "P", Brief: "b"})
	s.watchAdd("err", watched{Project: "P", Brief: "b"})

	// New service on the same DB = restarted backend: the registry is persisted.
	s2 := New(s.db, h, nil, nil)
	s2.projectID = "assist"
	s2.watchPass(context.Background())
	settle(s2)
	s2.watchPass(context.Background())
	if len(h.sent) != 1 {
		t.Fatalf("want one grouped wake-up, got %d", len(h.sent))
	}
	if !strings.Contains(h.sent[0], `etat="interrompue"`) || !strings.Contains(h.sent[0], `etat="en erreur"`) {
		t.Errorf("states missing:\n%s", h.sent[0])
	}
}

func TestWakeWhenWaitingForUser(t *testing.T) {
	s, h, dir := newWatchService(t)
	j := filepath.Join(dir, "s3.jsonl")
	os.WriteFile(j, []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"Quelle option ?"}]}}`+"\n"), 0o644)
	addSession(t, s, "s3", j, store.StateRunning)
	s.watchAdd("s3", watched{Project: "P", Brief: "b"})
	asks := fakeAsks{"s3": true}
	s.SetAskState(asks)
	s.watchPass(context.Background())
	settle(s)
	s.watchPass(context.Background())
	s.watchPass(context.Background())
	if len(h.sent) != 1 || !strings.Contains(h.sent[0], "en attente de l'utilisateur") {
		t.Fatalf("waiting wake-up: %v", h.sent)
	}
	if len(s.loadWatch()) != 1 {
		t.Error("a waiting session must stay watched until its end")
	}
}

func TestTruncateMiddle(t *testing.T) {
	long := strings.Repeat("é", 20000)
	out := truncateMiddle(long)
	if n := len([]rune(out)); n > maxAnswer+200 || !strings.Contains(out, "caractères omis") {
		t.Errorf("not truncated: %d runes", n)
	}
	if truncateMiddle("court") != "court" {
		t.Error("short answer altered")
	}
}

func TestCurrentRoute(t *testing.T) {
	s, _, _ := newWatchService(t)
	s.queue = []origin{{channel: "whatsapp", toolID: "T", jid: "J"}}
	if r, j := s.currentRoute(); r != "T" || j != "J" {
		t.Errorf("whatsapp route %q %q", r, j)
	}
	s.queue = []origin{{channel: "web"}}
	if r, _ := s.currentRoute(); r != routeWeb {
		t.Errorf("web route %q", r)
	}
	s.queue = []origin{{channel: "task"}}
	if r, j := s.currentRoute(); r != "" || j != "" {
		t.Errorf("task route %q %q", r, j)
	}
}
