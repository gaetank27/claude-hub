package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestProjectArchiveLifecycle verifies the archived column migration plus the
// list/set queries: archiving removes a project from the active board, surfaces
// it in the archived list, and unarchiving restores it.
func TestProjectArchiveLifecycle(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	mk := func(id, slug string) {
		if err := db.CreateProject(Project{ID: id, Name: id, Slug: slug, Dir: "/tmp/" + slug, CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	mk("a", "alpha")
	mk("b", "beta")

	active, err := db.ListProjects()
	if err != nil || len(active) != 2 {
		t.Fatalf("ListProjects = %d,%v; want 2", len(active), err)
	}

	if err := db.SetProjectArchived("a", true); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if active, _ = db.ListProjects(); len(active) != 1 || active[0].ID != "b" {
		t.Fatalf("after archive, active board = %+v; want only b", active)
	}
	arch, err := db.ListArchivedProjects()
	if err != nil || len(arch) != 1 || arch[0].ID != "a" || !arch[0].Archived {
		t.Fatalf("ListArchivedProjects = %+v,%v; want [a archived]", arch, err)
	}
	if p, _ := db.GetProject("a"); !p.Archived {
		t.Fatalf("GetProject(a).Archived = false; want true")
	}

	if err := db.SetProjectArchived("a", false); err != nil {
		t.Fatalf("unarchive: %v", err)
	}
	if active, _ = db.ListProjects(); len(active) != 2 {
		t.Fatalf("after unarchive, active board = %d; want 2", len(active))
	}
	if arch, _ = db.ListArchivedProjects(); len(arch) != 0 {
		t.Fatalf("after unarchive, archived = %d; want 0", len(arch))
	}
}

// TestDeleteProjectClearsProjectRagFK reproduces the foreign-key failure that hit
// archived-project deletion: project_rag references projects(id), so dropping the
// project while its project_rag row remains violates the FK. Removing the
// project_rag row first (as the manager now does) unblocks the delete.
func TestDeleteProjectClearsProjectRagFK(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if err := db.CreateProject(Project{ID: "p", Name: "p", Slug: "p", Dir: "/tmp/p", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := db.CreateProjectRag(ProjectRag{ProjectID: "p", CollectionID: "c"}); err != nil {
		t.Fatalf("create project_rag: %v", err)
	}

	// Direct delete must fail on the FK while the project_rag row is present.
	if err := db.DeleteProject("p"); err == nil || !strings.Contains(strings.ToUpper(err.Error()), "FOREIGN KEY") {
		t.Fatalf("DeleteProject with dangling project_rag = %v; want a FOREIGN KEY error", err)
	}

	// Clearing project_rag first (the manager's fix) lets the delete succeed.
	if err := db.DeleteProjectRag("p"); err != nil {
		t.Fatalf("delete project_rag: %v", err)
	}
	if err := db.DeleteProject("p"); err != nil {
		t.Fatalf("DeleteProject after cleanup = %v; want nil", err)
	}
}
