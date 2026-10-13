// Package bootstrap seeds mandatory state on first run.
package bootstrap

import (
	"time"

	"system/internal/config"
	"system/internal/store"
)

// EnsureSystemProject guarantees the permanent, protected `system` project
// exists, rooted at /home/claude. It is idempotent: a no-op once present.
// The protected flag is what later slices check to forbid archiving/deletion.
func EnsureSystemProject(db *store.DB) error {
	if _, err := db.GetProjectBySlug("system"); err == nil {
		return nil // already present
	} else if err != store.ErrNotFound {
		return err
	}
	return db.CreateProject(store.Project{
		ID:        "system", // stable, well-known id for the root project
		Name:      "system",
		Slug:      "system",
		Dir:       config.SystemDir,
		IsSystem:  true,
		Protected: true,
		CreatedAt: time.Now().UTC(),
	})
}
