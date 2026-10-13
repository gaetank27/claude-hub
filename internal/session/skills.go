package session

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"system/internal/store"
)

// SkillsProjectID is the stable id of the hidden project hosting skill-authoring
// sessions (seeded by bootstrap.EnsureSkillsProject). Its working directory is
// the canonical skills dir, so a session launched in it can write SKILL.md files
// directly. Mirrors bootstrap.SkillsProjectID — kept as a local constant so the
// session package need not import bootstrap.
const SkillsProjectID = "skills"

// ListSkills returns all skills, newest first.
func (m *Manager) ListSkills() ([]store.Skill, error) { return m.db.ListSkills() }

// GetSkill returns one skill by id.
func (m *Manager) GetSkill(id string) (store.Skill, error) { return m.db.GetSkill(id) }

// CreateSkill registers a new skill and launches a dedicated background Claude
// session to author it. The session runs in the hidden skills workspace
// (cwd = SkillsDir) and is briefed to write <slug>/SKILL.md. When discover is
// true the brief instead asks Claude to find an EXISTING Claude Code skill on the
// web (GitHub, community/official collections) and install it — the "intelligent
// search" of an already-existing skill. The skill stays in `draft` until the user
// validates it, at which point Claude loads it in every session via the shared
// skills/ dir.
func (m *Manager) CreateSkill(ctx context.Context, name, description string, discover bool) (store.Skill, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return store.Skill{}, fmt.Errorf("skill name is required")
	}
	root := m.SkillsDir()
	if root == "" {
		return store.Skill{}, fmt.Errorf("skills directory unavailable (no credentials path configured)")
	}
	slug := m.uniqueSkillSlug(name)
	if err := os.MkdirAll(filepath.Join(root, slug), 0o755); err != nil {
		return store.Skill{}, fmt.Errorf("create skill dir: %w", err)
	}
	// The skills/ dir may not have existed when accounts were last synced; ensure
	// every account now sees it (symlink) so the skill is globally visible.
	if err := m.SyncAllAccounts(); err != nil {
		log.Printf("sync accounts after skill create: %v", err)
	}

	// Launch the authoring session first: if it fails (e.g. no active account)
	// we leave only an empty dir, no orphan skill row.
	sess, err := m.LaunchSession(ctx, SkillsProjectID, ModeBypass, "", "", "")
	if err != nil {
		return store.Skill{}, err
	}

	sk := store.Skill{
		ID:          uuid.NewString(),
		Name:        name,
		Slug:        slug,
		Description: strings.TrimSpace(description),
		Status:      store.SkillDraft,
		SessionID:   sess.ID,
		CreatedAt:   time.Now().UTC(),
	}
	if err := m.db.CreateSkill(sk); err != nil {
		_ = m.StopSession(ctx, sess.ID)
		return store.Skill{}, err
	}
	// Brief the session with the task (best-effort; the user can also just talk to
	// it in the UI). Discovery searches the web for an existing skill; design
	// authors a new one from scratch.
	brief := skillBrief(slug, name, sk.Description)
	if discover {
		brief = skillDiscoverBrief(slug, name, sk.Description)
	}
	if err := m.SendUserMessage(sess.ID, brief); err != nil {
		log.Printf("brief skill session %s: %v", sess.ID, err)
	}
	return sk, nil
}

// UpdateSkill edits a skill's user-facing metadata.
func (m *Manager) UpdateSkill(id, name, description, keywords string) (store.Skill, error) {
	sk, err := m.db.GetSkill(id)
	if err != nil {
		return store.Skill{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = sk.Name
	}
	if err := m.db.UpdateSkillMeta(id, name, strings.TrimSpace(description), strings.TrimSpace(keywords)); err != nil {
		return store.Skill{}, err
	}
	return m.db.GetSkill(id)
}

// SetSkillStatus validates (enabled), disables, or reverts a skill. Enabling a
// skill makes Claude load it; the definition is already on disk and shared, so
// this only flips the index flag. Validating or disabling also closes the editing
// session — a skill's session is an ephemeral tool, never left running idle.
func (m *Manager) SetSkillStatus(ctx context.Context, id, status string) (store.Skill, error) {
	switch status {
	case store.SkillDraft, store.SkillEnabled, store.SkillDisabled:
	default:
		return store.Skill{}, fmt.Errorf("invalid skill status %q", status)
	}
	sk, err := m.db.GetSkill(id)
	if err != nil {
		return store.Skill{}, err
	}
	if err := m.db.SetSkillStatus(id, status); err != nil {
		return store.Skill{}, err
	}
	// Reaching a terminal state (validated/disabled) ends the editing work.
	if status == store.SkillEnabled || status == store.SkillDisabled {
		m.stopSkillSession(ctx, sk)
	}
	return m.db.GetSkill(id)
}

// CloseSkillSession stops a skill's editing session without changing its status,
// so the user can dismiss it without leaving a Claude Code session running.
func (m *Manager) CloseSkillSession(ctx context.Context, id string) (store.Skill, error) {
	sk, err := m.db.GetSkill(id)
	if err != nil {
		return store.Skill{}, err
	}
	m.stopSkillSession(ctx, sk)
	return m.db.GetSkill(id)
}

// EvolveSkill (re)opens an editing session on an existing skill so it can be
// modified. A still-live session is reused; otherwise a fresh one is launched in
// the skills workspace and briefed to read and improve the existing SKILL.md. The
// skill's status is preserved (an enabled skill keeps being loaded while edited).
func (m *Manager) EvolveSkill(ctx context.Context, id string) (store.Skill, error) {
	sk, err := m.db.GetSkill(id)
	if err != nil {
		return store.Skill{}, err
	}
	if sk.Slug == "" {
		return store.Skill{}, fmt.Errorf("skill has no on-disk definition")
	}
	// Reuse a session that is still live.
	if sk.SessionID != "" {
		if s, err := m.db.GetSession(sk.SessionID); err == nil &&
			(s.State == store.StateRunning || s.State == store.StateStarting) {
			return sk, nil
		}
	}
	sess, err := m.LaunchSession(ctx, SkillsProjectID, ModeBypass, "", "", "")
	if err != nil {
		return store.Skill{}, err
	}
	if err := m.db.SetSkillSession(id, sess.ID); err != nil {
		_ = m.StopSession(ctx, sess.ID)
		return store.Skill{}, err
	}
	if err := m.SendUserMessage(sess.ID, skillEvolveBrief(sk.Slug, sk.Name, sk.Description)); err != nil {
		log.Printf("brief evolve session %s: %v", sess.ID, err)
	}
	return m.db.GetSkill(id)
}

// stopSkillSession stops a skill's editing session if it is still live. The
// journal is kept, so the UI can still replay the work read-only.
func (m *Manager) stopSkillSession(ctx context.Context, sk store.Skill) {
	if sk.SessionID == "" {
		return
	}
	if s, err := m.db.GetSession(sk.SessionID); err == nil &&
		(s.State == store.StateRunning || s.State == store.StateStarting) {
		_ = m.StopSession(ctx, sk.SessionID)
	}
}

// DeleteSkill removes a skill: stops its authoring session if still live, drops
// the on-disk definition, and deletes the index row.
func (m *Manager) DeleteSkill(ctx context.Context, id string) error {
	sk, err := m.db.GetSkill(id)
	if err != nil {
		return err
	}
	if sk.SessionID != "" {
		if s, err := m.db.GetSession(sk.SessionID); err == nil &&
			(s.State == store.StateRunning || s.State == store.StateStarting) {
			_ = m.StopSession(ctx, sk.SessionID)
		}
	}
	if root := m.SkillsDir(); root != "" && sk.Slug != "" {
		if err := os.RemoveAll(filepath.Join(root, sk.Slug)); err != nil {
			return fmt.Errorf("remove skill dir: %w", err)
		}
	}
	return m.db.DeleteSkill(id)
}

// uniqueSkillSlug derives a filesystem- and URL-safe slug unique across skills.
func (m *Manager) uniqueSkillSlug(name string) string {
	base := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if base == "" {
		base = "skill"
	}
	slug := base
	for i := 2; ; i++ {
		if _, err := m.db.GetSkillBySlug(slug); err == store.ErrNotFound {
			return slug
		}
		slug = fmt.Sprintf("%s-%d", base, i)
	}
}

// skillBrief is the opening instruction handed to the authoring session.
func skillBrief(slug, name, description string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are designing a new Claude Code skill named %q.\n", name)
	if description != "" {
		fmt.Fprintf(&b, "Goal: %s\n", description)
	}
	fmt.Fprintf(&b, "\nCreate the skill at `%s/SKILL.md` (relative to the current "+
		"working directory). Follow the SKILL.md convention: YAML frontmatter with "+
		"`name` and a precise `description` (the trigger), then the instructions in "+
		"Markdown. Add any helper scripts or templates in the same `%s/` directory. "+
		"Keep everything in English. When done, briefly summarize what the skill does "+
		"and how it triggers.", slug, slug)
	return b.String()
}

// skillDiscoverBrief instructs the session to find an EXISTING Claude Code skill
// on the web and install it locally, rather than authoring one from scratch.
func skillDiscoverBrief(slug, name, query string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Find an EXISTING, already-published Claude Code skill that "+
		"solves this need: %q.\n", name)
	if query != "" {
		fmt.Fprintf(&b, "More detail: %s\n", query)
	}
	fmt.Fprint(&b, "\nUse web search and fetch to look for well-regarded existing "+
		"skills — Anthropic's official skill collections, the anthropics/claude-code "+
		"and awesome-claude-code repositories, and reputable GitHub projects. Compare "+
		"the candidates and pick the best match (prefer popular, maintained, "+
		"permissively-licensed ones).\n")
	fmt.Fprintf(&b, "\nInstall the chosen skill into the `%s/` directory: write its "+
		"`%s/SKILL.md` (adapting the frontmatter `name`/`description` if needed) and "+
		"copy any helper scripts/templates it ships. Add a short comment recording the "+
		"source URL and license for attribution. Everything in English.\n", slug, slug)
	fmt.Fprint(&b, "\nIf no suitable existing skill exists, say so clearly and do NOT "+
		"invent one. When done, summarize which skill you installed, its source URL, "+
		"its license, and how it triggers.")
	return b.String()
}

// skillEvolveBrief opens an editing session on an existing skill: read the current
// definition, then apply the changes the user is about to describe.
func skillEvolveBrief(slug, name, description string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are evolving the existing Claude Code skill %q, defined at "+
		"`%s/SKILL.md` (relative to the current working directory).\n", name, slug)
	if description != "" {
		fmt.Fprintf(&b, "Its purpose: %s\n", description)
	}
	fmt.Fprintf(&b, "\nFirst read `%s/SKILL.md` and any helper files in `%s/` to "+
		"understand the current behavior. Then apply the changes the user describes "+
		"next, editing the files in place and preserving the SKILL.md convention "+
		"(YAML frontmatter `name`/`description`, then Markdown instructions). Keep "+
		"everything in English. Summarize what you changed when done.", slug, slug)
	return b.String()
}
