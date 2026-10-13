package vault

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRoundtrip packs a small project tree, lists it, extracts it and checks the
// manifest and file contents survive create→list→extract intact.
func TestRoundtrip(t *testing.T) {
	root := t.TempDir()
	vaultDir := filepath.Join(root, "vault")
	work := filepath.Join(root, "work")
	mustWrite(t, filepath.Join(work, "README.md"), "hello")
	mustWrite(t, filepath.Join(work, "src", "main.go"), "package main")
	journal := filepath.Join(root, "j", "sess1.jsonl")
	mustWrite(t, journal, `{"type":"x"}`)

	s, err := New(vaultDir)
	if err != nil {
		t.Fatal(err)
	}
	deletedAt := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	m := Manifest{
		Project:   ProjectMeta{ID: "p1", Name: "Demo", Slug: "demo", Dir: work, CreatedAt: deletedAt},
		Rag:       &RagMeta{Enabled: true, IntervalMinutes: 30},
		Sessions:  []SessionMeta{{ID: "sess1", State: "stopped", JournalFile: "sess1.jsonl"}},
		DeletedAt: deletedAt,
	}
	entry, err := s.Create("demo", m, []Source{
		{ArchivePath: "working", FSPath: work},
		{ArchivePath: "journals/sess1.jsonl", FSPath: journal},
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.Size == 0 {
		t.Fatal("archive size is zero")
	}

	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Manifest.Project.Slug != "demo" {
		t.Fatalf("list = %+v", list)
	}

	dest := filepath.Join(root, "out")
	got, err := s.Extract(entry.Name, dest)
	if err != nil {
		t.Fatal(err)
	}
	if got.Project.ID != "p1" || got.Rag == nil || got.Rag.IntervalMinutes != 30 {
		t.Fatalf("manifest mismatch: %+v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "working", "src", "main.go")); string(b) != "package main" {
		t.Fatalf("working file mismatch: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "journals", "sess1.jsonl")); string(b) != `{"type":"x"}` {
		t.Fatalf("journal mismatch: %q", b)
	}

	if err := s.Delete(entry.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(entry.Name); err != ErrNotFound {
		t.Fatalf("Get after delete = %v, want ErrNotFound", err)
	}
}

// TestNameTraversal rejects malicious archive names.
func TestNameTraversal(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../etc/passwd", "a/b.tar.gz", "..", "x.tar.gz/.."} {
		if _, err := s.Get(bad); err == nil {
			t.Fatalf("Get(%q) should have failed", bad)
		}
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
