package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"system/internal/store"
)

// TestSaveAttachment verifies an uploaded file lands in the project working dir
// under .hub-uploads/<batch>/, the returned path is relative + slash-separated,
// the filename is reduced to its base name (no traversal), and the upload subdir
// is added to the project's .gitignore.
func TestSaveAttachment(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	projDir := t.TempDir()
	if err := db.CreateProject(store.Project{ID: "p", Name: "p", Slug: "p", Dir: projDir, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := db.CreateSession(store.Session{ID: "s", ProjectID: "p", State: store.StateRunning, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	m := &Manager{db: db}

	// A name with a leading path is reduced to its base name (anti-traversal).
	rel, err := m.SaveAttachment("s", "1700000000", "../../etc/evil.tgz", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("SaveAttachment: %v", err)
	}
	if rel != ".hub-uploads/1700000000/evil.tgz" {
		t.Fatalf("rel = %q; want .hub-uploads/1700000000/evil.tgz", rel)
	}
	got, err := os.ReadFile(filepath.Join(projDir, rel))
	if err != nil || string(got) != "payload" {
		t.Fatalf("file content = %q,%v; want payload", got, err)
	}

	// The upload subdir is gitignored exactly once across repeated uploads.
	if _, err := m.SaveAttachment("s", "1700000001", "second.zip", strings.NewReader("x")); err != nil {
		t.Fatalf("second SaveAttachment: %v", err)
	}
	gi, err := os.ReadFile(filepath.Join(projDir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if n := strings.Count(string(gi), ".hub-uploads/"); n != 1 {
		t.Fatalf(".gitignore has %d .hub-uploads/ entries; want 1 (%q)", n, gi)
	}

	// An unknown session is a not-found, not a panic.
	if _, err := m.SaveAttachment("missing", "1700000002", "a.txt", strings.NewReader("x")); err == nil {
		t.Fatal("SaveAttachment on missing session: want error")
	}
}
