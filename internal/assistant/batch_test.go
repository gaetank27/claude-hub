package assistant

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"system/internal/store"
)

const (
	evInit     = `{"type":"system","subtype":"init"}` + "\n"
	evText     = `{"type":"assistant","message":{"content":[{"type":"text","text":"%s"}]}}` + "\n"
	evResult   = `{"type":"result","result":"%s","is_error":false}` + "\n"
	evBGStart  = `{"type":"system","subtype":"task_started","task_id":"t1","is_backgrounded":true}` + "\n"
	evBGList   = `{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"t1"}]}` + "\n"
	evBGEmpty  = `{"type":"system","subtype":"background_tasks_changed","tasks":[]}` + "\n"
	evBGNotify = `{"type":"system","subtype":"task_notification","task_id":"t1","status":"completed"}` + "\n"
	evStatus   = `{"type":"system","subtype":"status","status":"requesting"}` + "\n"
)

func writeJournal(t *testing.T, path string, parts ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(parts, "")), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sprintf1(f, a string) string { return strings.Replace(f, "%s", a, 1) }

// The Gradle case: a result written while a background task runs is not the end.
func TestReadTurnBackgroundTask(t *testing.T) {
	j := filepath.Join(t.TempDir(), "j.jsonl")
	writeJournal(t, j, evInit, sprintf1(evText, "je lance gradle"), evBGStart, evBGList, sprintf1(evResult, "Gradle tourne, je reviens"))
	if tr := ReadTurn(j, time.Minute); tr.Finished() || !tr.Background || tr.Text != "Gradle tourne, je reviens" {
		t.Fatalf("result with a background task pending must not be final: %+v", tr)
	}
	// The task ends: Claude is about to resume, still not final.
	writeJournal(t, j, evInit, evBGStart, evBGList, sprintf1(evResult, "Gradle tourne"), evBGEmpty, evBGNotify)
	if tr := ReadTurn(j, time.Minute); tr.Finished() {
		t.Fatalf("task completed after the result: a new turn follows: %+v", tr)
	}
	// New turn starts, then ends with the real report.
	writeJournal(t, j, evInit, evBGStart, evBGList, sprintf1(evResult, "Gradle tourne"), evBGEmpty, evBGNotify, evStatus, sprintf1(evText, "tests ok"), sprintf1(evResult, "Rapport final"))
	tr := ReadTurn(j, time.Minute)
	if !tr.Finished() || tr.Outcome != TurnDone || tr.Text != "Rapport final" || tr.Background {
		t.Fatalf("final report: %+v", tr)
	}
	// A task that never reports does not keep the session working forever.
	writeJournal(t, j, evInit, evBGStart, evBGList, sprintf1(evResult, "serveur lancé"))
	if tr := ReadTurn(j, bgGrace+time.Minute); !tr.Finished() || tr.Text != "serveur lancé" {
		t.Fatalf("stale background wait: %+v", tr)
	}
	// Plain turn, and an error turn.
	writeJournal(t, j, evInit, sprintf1(evResult, "ok"))
	if tr := ReadTurn(j, 0); tr.Outcome != TurnDone {
		t.Fatalf("plain: %+v", tr)
	}
	writeJournal(t, j, evInit, `{"type":"result","result":"boom","is_error":true}`+"\n")
	if tr := ReadTurn(j, 0); tr.Outcome != TurnError {
		t.Fatalf("error: %+v", tr)
	}
}

func TestNoWakeWhileBackgroundTaskPending(t *testing.T) {
	s, h, dir := newWatchService(t)
	j := filepath.Join(dir, "g.jsonl")
	writeJournal(t, j, evInit, evBGStart, evBGList, sprintf1(evResult, "Gradle tourne"))
	addSession(t, s, "g", j, store.StateRunning)
	s.watchAdd("g", watched{Project: "App Android", Brief: "b", Route: routeWeb})
	ctx := context.Background()
	s.watchPass(ctx)
	settle(s)
	s.watchPass(ctx)
	if len(h.sent) != 0 {
		t.Fatalf("woken for an intermediate turn: %v", h.sent)
	}
	if state, _, fin, _ := s.TaskResult("g"); fin || state != "working" {
		t.Errorf("TaskResult = %s %v, want working/false", state, fin)
	}
	writeJournal(t, j, evInit, evBGStart, evBGList, sprintf1(evResult, "Gradle tourne"), evBGEmpty, evBGNotify, evStatus, sprintf1(evResult, "Fini"))
	s.watchPass(ctx)
	settle(s)
	s.watchPass(ctx)
	if len(h.sent) != 1 || !strings.Contains(h.sent[0], "Fini") {
		t.Fatalf("final wake-up: %v", h.sent)
	}
	if state, _, fin, _ := s.TaskResult("g"); !fin || state != "idle" {
		t.Errorf("TaskResult = %s %v, want idle/true (live process, turn over)", state, fin)
	}
}

func twoSessions(t *testing.T) (*Service, *wakeHub, string, string) {
	s, h, dir := newWatchService(t)
	j1, j2 := filepath.Join(dir, "a.jsonl"), filepath.Join(dir, "b.jsonl")
	writeJournal(t, j1, evInit, sprintf1(evText, "en cours"))
	writeJournal(t, j2, evInit, sprintf1(evText, "en cours"))
	addSession(t, s, "a", j1, store.StateRunning)
	addSession(t, s, "b", j2, store.StateRunning)
	s.watchAdd("a", watched{Project: "P1", Brief: "A", Route: routeWeb, Started: 1})
	s.watchAdd("b", watched{Project: "P2", Brief: "B", Route: routeWeb, Started: 2})
	return s, h, j1, j2
}

func TestBatchedWakeUps(t *testing.T) {
	s, h, j1, j2 := twoSessions(t)
	ctx := context.Background()
	pass := func() { s.watchPass(ctx); settle(s); s.watchPass(ctx) }

	// a ends while b still works: retained.
	writeJournal(t, j1, evInit, sprintf1(evResult, "rapport A"))
	pass()
	if len(h.sent) != 0 {
		t.Fatalf("an end must be retained while another request runs: %v", h.sent)
	}
	if s.BatchHeld() != 1 {
		t.Fatalf("BatchHeld = %d", s.BatchHeld())
	}
	pass()
	if len(h.sent) != 0 {
		t.Fatal("released before the window")
	}
	// The window (from the first retained end) elapses: released alone.
	s.watchUpdate("a", func(w *watched) { w.Held = time.Now().Add(-11 * time.Minute).UnixMilli() })
	pass()
	if len(h.sent) != 1 || !strings.Contains(h.sent[0], `session_id="a"`) || strings.Contains(h.sent[0], `session_id="b"`) {
		t.Fatalf("window release: %v", h.sent)
	}
	s.watchAck([]string{"a"})

	// Back to one request: immediate.
	writeJournal(t, j2, evInit, sprintf1(evResult, "rapport B"))
	pass()
	if len(h.sent) != 2 || !strings.Contains(h.sent[1], "rapport B") {
		t.Fatalf("single request must wake at once: %v", h.sent)
	}
}

func TestBatchGroupsAllEndsInOneMessage(t *testing.T) {
	s, h, j1, j2 := twoSessions(t)
	ctx := context.Background()
	pass := func() { s.watchPass(ctx); settle(s); s.watchPass(ctx) }
	writeJournal(t, j1, evInit, sprintf1(evResult, "rapport A"))
	pass()
	if len(h.sent) != 0 {
		t.Fatal("retained expected")
	}
	// The last running session ends: nothing left to group, one message for both.
	writeJournal(t, j2, evInit, sprintf1(evResult, "rapport B"))
	pass()
	if len(h.sent) != 1 {
		t.Fatalf("want ONE grouped wake-up, got %d", len(h.sent))
	}
	m := h.sent[0]
	for _, want := range []string{`session_id="a"`, `session_id="b"`, "rapport A", "rapport B", "UN SEUL récapitulatif", "2 sessions"} {
		if !strings.Contains(m, want) {
			t.Errorf("grouped wake-up lacks %q:\n%s", want, m)
		}
	}
}

func TestBatchWaitingForUserIsImmediateAndCarriesHeldEnds(t *testing.T) {
	s, h, j1, j2 := twoSessions(t)
	s.SetAskState(fakeAsks{})
	ctx := context.Background()
	pass := func() { s.watchPass(ctx); settle(s); s.watchPass(ctx) }
	writeJournal(t, j1, evInit, sprintf1(evResult, "rapport A"))
	pass()
	if len(h.sent) != 0 {
		t.Fatal("retained expected")
	}
	s.SetAskState(fakeAsks{"b": true})
	pass()
	if len(h.sent) != 1 {
		t.Fatalf("want one wake-up, got %d", len(h.sent))
	}
	if m := h.sent[0]; !strings.Contains(m, "en attente de l'utilisateur") || !strings.Contains(m, "rapport A") {
		t.Errorf("waiting wake-up must carry the retained end:\n%s", m)
	}
	_ = j2
}

func TestBatchDisabledAndSettings(t *testing.T) {
	s, h, j1, _ := twoSessions(t)
	if b := s.Batch(); !b.Enabled || b.Minutes != 10 {
		t.Fatalf("defaults: %+v", b)
	}
	off := false
	if err := s.SetBatch(&off, nil); err != nil {
		t.Fatal(err)
	}
	bad := 0
	if err := s.SetBatch(nil, &bad); err == nil {
		t.Error("0 minutes accepted")
	}
	writeJournal(t, j1, evInit, sprintf1(evResult, "rapport A"))
	s.watchPass(context.Background())
	settle(s)
	s.watchPass(context.Background())
	if len(h.sent) != 1 {
		t.Fatalf("disabled grouping must wake at once: %v", h.sent)
	}
	on, five := true, 5
	if err := s.SetBatch(&on, &five); err != nil || s.Batch().Minutes != 5 || !s.Batch().Enabled {
		t.Errorf("SetBatch: %+v %v", s.Batch(), err)
	}
}

func TestBatchSurvivesRestart(t *testing.T) {
	s, h, j1, _ := twoSessions(t)
	ctx := context.Background()
	writeJournal(t, j1, evInit, sprintf1(evResult, "rapport A"))
	s.watchPass(ctx)
	settle(s)
	s.watchPass(ctx)
	if len(h.sent) != 0 || s.BatchHeld() != 1 {
		t.Fatal("retained expected")
	}
	s.watchUpdate("a", func(w *watched) { w.Held = time.Now().Add(-time.Hour).UnixMilli() })
	s2 := New(s.db, h, nil, nil) // restarted Hub
	s2.projectID = "assist"
	s2.watchPass(ctx)
	settle(s2)
	s2.watchPass(ctx)
	if len(h.sent) != 1 || !strings.Contains(h.sent[0], "rapport A") {
		t.Fatalf("overdue retained end must be released after a restart: %v", h.sent)
	}
}

func TestLostWakeIsHandedOverAgain(t *testing.T) {
	s, h, dir := newWatchService(t)
	j := filepath.Join(dir, "x.jsonl")
	writeJournal(t, j, evInit, sprintf1(evResult, "Fini"))
	addSession(t, s, "x", j, store.StateRunning)
	s.watchAdd("x", watched{Project: "P", Brief: "b", Route: routeWeb})
	ctx := context.Background()
	s.watchPass(ctx)
	settle(s)
	s.watchPass(ctx)
	if len(h.sent) != 1 {
		t.Fatalf("first wake-up: %d", len(h.sent))
	}
	s.watchPass(ctx)
	if len(h.sent) != 1 {
		t.Fatal("re-sent before the loss delay")
	}
	if len(s.queue) != 1 || len(s.queue[0].wake) != 1 {
		t.Fatalf("queue slot of the wake-up: %+v", s.queue)
	}
	// The assistant never consumed it (journal silent): after the delay, again.
	s.watchUpdate("x", func(w *watched) { w.Woken = time.Now().Add(-2 * time.Minute).UnixMilli() })
	s.watchPass(ctx)
	settle(s)
	s.watchPass(ctx)
	if len(h.sent) != 2 {
		t.Fatalf("lost wake-up not handed over again: %d", len(h.sent))
	}
	if len(s.queue) != 1 {
		t.Errorf("stale answer slot kept: %+v", s.queue)
	}
	// Echo of the message in the assistant's journal = ack.
	line := `{"type":"user","message":{"role":"user","content":"[2026-10-11 18:17 UTC · canal : session]\nRéveil automatique du Hub\n<session_deleguee projet=\"P\" session_id=\"x\" etat=\"terminée\">"}}`
	if ids := wakeAcks([]byte(line)); len(ids) != 1 || ids[0] != "x" {
		t.Fatalf("wakeAcks = %v", ids)
	}
	s.watchAck(wakeAcks([]byte(line)))
	if len(s.loadWatch()) != 0 {
		t.Error("acked entry kept")
	}
	if wakeAcks([]byte(`{"type":"user","message":{"content":"bonjour"}}`)) != nil {
		t.Error("ordinary user line parsed as an ack")
	}
}

func TestLostWakeGivesUp(t *testing.T) {
	s, h, dir := newWatchService(t)
	j := filepath.Join(dir, "y.jsonl")
	writeJournal(t, j, evInit, sprintf1(evResult, "Fini"))
	addSession(t, s, "y", j, store.StateRunning)
	s.watchAdd("y", watched{Project: "P", Brief: "b", Route: routeWeb, Woken: time.Now().Add(-time.Hour).UnixMilli(), Tries: wakeMaxTries})
	s.watchPass(context.Background())
	if len(s.loadWatch()) != 0 || len(h.sent) != 0 {
		t.Errorf("endless retries: %v %v", s.loadWatch(), h.sent)
	}
}

func TestTaskState(t *testing.T) {
	done, work := Turn{Outcome: TurnDone}, Turn{Outcome: TurnWorking}
	cases := []struct {
		proc string
		t    Turn
		want string
	}{
		{store.StateRunning, work, "working"}, {store.StateRunning, done, "idle"},
		{store.StateStopped, done, store.StateStopped}, {store.StateError, work, store.StateError},
	}
	for _, c := range cases {
		if got := TaskState(c.proc, c.t); got != c.want {
			t.Errorf("TaskState(%s,%v) = %s, want %s", c.proc, c.t.Outcome, got, c.want)
		}
	}
}

func TestBriefKept(t *testing.T) {
	s, _, _ := newWatchService(t)
	s.rememberBrief("z", "Fais le truc")
	if s.Brief("z") != "Fais le truc" || s.Brief("nope") != "" {
		t.Error("brief store")
	}
}

func TestSummary(t *testing.T) {
	if got := CleanSummary("  Migration\ndes VM\x00 \t "); got != "Migration des VM" {
		t.Fatalf("clean: %q", got)
	}
	long := CleanSummary("Migration des machines virtuelles vers le tout nouveau stockage partagé du cluster")
	if r := []rune(long); len(r) > 61 || !strings.HasSuffix(long, "…") {
		t.Fatalf("truncate: %q", long)
	}
	if got := FallbackSummary("Demande de l'utilisateur (Alice, via l'assistant) : migre les VM. Puis le reste."); got != "migre les VM" {
		t.Fatalf("fallback: %q", got)
	}
	if FallbackSummary("") != "" {
		t.Fatal("empty fallback")
	}
	s, _, _ := newWatchService(t)
	s.rememberDelegation("q", "brief", "Alertes Zaptec")
	if s.Summary("q") != "Alertes Zaptec" || s.Summary("nope") != "" {
		t.Fatal("summary store")
	}
	if !strings.Contains(wakeText([]wake{{id: "q", state: "terminée", w: watched{Project: "P", Summary: "Alertes Zaptec"}}}), `resume="Alertes Zaptec"`) {
		t.Fatal("wake attr")
	}
}
