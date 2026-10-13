package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"system/internal/store"
)

// SkillsProjectID is the stable, well-known id of the hidden project that hosts
// skill-authoring sessions. It is excluded from the user-facing project list
// (projects.hidden) but reuses the whole session machinery so a skill is designed
// by a real background Claude session writing into the shared skills dir.
const SkillsProjectID = "skills"

// EnsureSkillsProject guarantees the hidden, protected `skills` workspace exists,
// rooted at the canonical skills dir (~/.claude/skills, the shared source of
// truth). Idempotent: a no-op once present. credentialsPath locates ~/.claude
// (its parent), mirroring the manager's sharedRoot.
func EnsureSkillsProject(db *store.DB, credentialsPath string) error {
	if _, err := db.GetProjectBySlug("skills"); err == nil {
		return nil // already present
	} else if err != store.ErrNotFound {
		return err
	}
	if credentialsPath == "" {
		return fmt.Errorf("skills project: no credentials path configured")
	}
	dir := filepath.Join(filepath.Dir(credentialsPath), "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("skills project: create dir %s: %w", dir, err)
	}
	return db.CreateProject(store.Project{
		ID:        SkillsProjectID,
		Name:      "Skills",
		Slug:      "skills",
		Dir:       dir,
		Protected: true, // never deletable via the projects API
		Hidden:    true, // excluded from the projects list
		CreatedAt: time.Now().UTC(),
	})
}
