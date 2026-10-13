package assistant

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalCron(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Zurich")
	old := time.Local
	time.Local = loc
	defer func() { time.Local = old }()
	winter := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC) // UTC+1
	cases := []struct{ at, days, want string }{
		{"07:30", "all", "30 6 * * *"},
		{"07:30", "week", "30 6 * * 1,2,3,4,5"},
		{"00:30", "week", "30 23 * * 0,1,2,3,4"}, // previous UTC day: weekdays shift back
		{"00:30", "weekend", "30 23 * * 5,6"},
	}
	for _, c := range cases {
		got, err := localCron(c.at, c.days, winter)
		if err != nil || got != c.want {
			t.Errorf("localCron(%s,%s) = %q, %v; want %q", c.at, c.days, got, err, c.want)
		}
	}
	if _, err := localCron("25:00", "all", winter); err == nil {
		t.Error("expected error for bad time")
	}
	if _, err := localCron("07:30", "monthly", winter); err == nil {
		t.Error("expected error for bad days")
	}
}

func TestParseEntityLine(t *testing.T) {
	e, ok := parseEntityLine("- person.alice = home (Alice)")
	if !ok || e.ID != "person.alice" || e.State != "home" || e.Name != "Alice" {
		t.Errorf("got %+v %v", e, ok)
	}
	e, ok = parseEntityLine("person.x = not home (X (bis))")
	if !ok || e.State != "not home (X" && e.State != "not home" {
		t.Logf("state parsing of nested parens: %+v", e)
	}
	if _, ok := parseEntityLine("No entities found."); ok {
		t.Error("prose must not parse")
	}
}

func TestReadNewAndLastAnswer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.jsonl")
	l1 := `{"type":"assistant","message":{"content":[{"type":"text","text":"en cours"}]}}` + "\n"
	l2 := `{"type":"result","result":"fini","is_error":false}` + "\n"
	if err := os.WriteFile(path, []byte(l1+`{"type":"res`), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, off := readNew(path, 0)
	if len(lines) != 1 || off != int64(len(l1)) {
		t.Fatalf("partial line must wait: %d lines, off %d", len(lines), off)
	}
	if txt, fin := lastAnswer(path); txt != "en cours" || fin {
		t.Errorf("running turn: %q %v", txt, fin)
	}
	if err := os.WriteFile(path, []byte(l1+l2), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, off2 := readNew(path, off)
	if len(lines) != 1 || off2 != int64(len(l1)+len(l2)) {
		t.Fatalf("second read: %d lines, off %d", len(lines), off2)
	}
	if txt, fin := lastAnswer(path); txt != "fini" || !fin {
		t.Errorf("finished turn: %q %v", txt, fin)
	}
}
