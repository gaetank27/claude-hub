package assistant

import (
	"path/filepath"
	"testing"

	"system/internal/store"
)

func TestModelSetting(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(db, nil, nil, nil)
	if got := s.Model(); got != "sonnet" {
		t.Fatalf("default = %q, want sonnet", got)
	}
	if err := s.SetModel("gpt"); err == nil {
		t.Fatal("unknown model accepted")
	}
	for _, m := range []string{"opus", "", "haiku"} {
		if err := s.SetModel(m); err != nil || s.Model() != m {
			t.Fatalf("SetModel(%q): %v, got %q", m, err, s.Model())
		}
	}
	if eff, pending := s.ModelState(); eff != "" || pending {
		t.Fatalf("no session: %q %v", eff, pending)
	}
}
