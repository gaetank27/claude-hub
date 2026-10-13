package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestToolCategories(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	cats, err := db.ListToolCategories()
	if err != nil || len(cats) != len(DefaultToolCategoryKeys) {
		t.Fatalf("seed: %v %v", cats, err)
	}
	if cats[0].Key != "home" || cats[len(cats)-1].ID != OtherToolCategory {
		t.Fatalf("order: %+v", cats)
	}

	// a tool created before categories existed has no category (automatic)
	if err := db.CreateTool(Tool{ID: "t1", Type: "telegram", Name: "TG", Status: ToolDisconnected, Enabled: true, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetTool("t1"); got.CategoryID != "" {
		t.Fatalf("new tool category = %q", got.CategoryID)
	}

	mine, err := db.CreateToolCategory("  Perso ")
	if err != nil || mine.Name != "Perso" {
		t.Fatalf("create: %+v %v", mine, err)
	}
	if _, err := db.CreateToolCategory("  "); err == nil {
		t.Fatal("empty name accepted")
	}
	if err := db.SetToolCategory("t1", mine.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetTool("t1"); got.CategoryID != mine.ID {
		t.Fatalf("move: %q", got.CategoryID)
	}
	if err := db.SetToolCategory("t1", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown category: %v", err)
	}
	if err := db.SetToolCategory("ghost", mine.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown tool: %v", err)
	}

	// rename: default may return to its localised label, a custom one may not be blank
	if err := db.RenameToolCategory("home", "Domotique"); err != nil {
		t.Fatal(err)
	}
	if err := db.RenameToolCategory("home", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.RenameToolCategory(mine.ID, ""); err == nil {
		t.Fatal("blank custom name accepted")
	}

	// reorder: listed ids first, the rest keep their order
	if err := db.ReorderToolCategories([]string{mine.ID, "infra", "ghost"}); err != nil {
		t.Fatal(err)
	}
	cats, _ = db.ListToolCategories()
	if cats[0].ID != mine.ID || cats[1].ID != "infra" || cats[2].ID != "home" {
		t.Fatalf("reorder: %+v", cats)
	}

	// delete: tools fall back to automatic; "other" is protected; no re-seed after delete
	if err := db.DeleteToolCategory(OtherToolCategory); !errors.Is(err, ErrProtectedCategory) {
		t.Fatalf("delete other: %v", err)
	}
	if err := db.DeleteToolCategory(mine.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetTool("t1"); got.CategoryID != "" {
		t.Fatalf("after delete: %q", got.CategoryID)
	}
	if err := db.DeleteToolCategory("home"); err != nil {
		t.Fatal(err)
	}
	cats, _ = db.ListToolCategories()
	for _, c := range cats {
		if c.ID == "home" {
			t.Fatal("deleted default was re-seeded")
		}
	}
}
