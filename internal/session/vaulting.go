package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"system/internal/store"
	"system/internal/vault"
)

// ErrVaultUnavailable is returned by the vault operations when no VaultDir is
// configured (the safe deposit box is disabled).
var ErrVaultUnavailable = errors.New("project vault is not configured")

// VaultProject is the second stage of the safe deletion workflow: it packs an
// (already-archived) project into a .tar.gz in the vault — working tree,
// journals and per-account memory plus a manifest — and ONLY once that archive
// is safely written does it destroy the on-disk project (DB rows, journals, RAG
// collection, working dir, memory). Nothing is erased before the archive exists.
// Refuses a protected project, or one with a still-live session.
func (m *Manager) VaultProject(ctx context.Context, projectID string) (vault.Entry, error) {
	if m.vault == nil {
		return vault.Entry{}, ErrVaultUnavailable
	}
	p, err := m.db.GetProject(projectID)
	if err != nil {
		return vault.Entry{}, err
	}
	if p.Protected {
		return vault.Entry{}, fmt.Errorf("%w: %s", ErrProjectProtected, projectID)
	}
	sessions, err := m.db.ListSessionsByProject(projectID)
	if err != nil {
		return vault.Entry{}, err
	}
	for _, s := range sessions {
		if s.State == store.StateRunning || s.State == store.StateStarting {
			return vault.Entry{}, fmt.Errorf("%w: stop session %s before deleting its project", ErrSessionLive, s.ID)
		}
	}

	manifest := vault.Manifest{
		Project: vault.ProjectMeta{
			ID: p.ID, Name: p.Name, Slug: p.Slug, Dir: p.Dir,
			Category: p.Category, Starred: p.Starred, CreatedAt: p.CreatedAt,
		},
		DeletedAt: time.Now().UTC(),
	}
	if pr, err := m.db.GetProjectRag(projectID); err == nil {
		manifest.Rag = &vault.RagMeta{
			Enabled: pr.Enabled, AutoOnChange: pr.AutoOnChange, IntervalMinutes: pr.IntervalMinutes,
		}
	}

	var sources []vault.Source
	if isDir(p.Dir) {
		sources = append(sources, vault.Source{ArchivePath: "working", FSPath: p.Dir})
	}
	for _, s := range sessions {
		sm := vault.SessionMeta{
			ID: s.ID, State: s.State, PermissionMode: s.PermissionMode,
			CreatedAt: s.CreatedAt, Comment: s.Comment, PrURL: s.PrURL,
			ClaudeAccountID: s.ClaudeAccountID,
		}
		if s.JournalPath != "" && fileExists(s.JournalPath) {
			base := filepath.Base(s.JournalPath)
			sm.JournalFile = base
			sources = append(sources, vault.Source{ArchivePath: "journals/" + base, FSPath: s.JournalPath})
		}
		manifest.Sessions = append(manifest.Sessions, sm)
	}
	// Per-account project memory (best-effort; absent dirs are simply skipped).
	memDirs := map[string]string{} // accountID -> on-disk memory dir, for cleanup
	if accts, err := m.Accounts(); err == nil {
		for _, a := range accts {
			md := projectMemoryDir(a.ConfigDir, p.Dir)
			if isDir(md) {
				sources = append(sources, vault.Source{ArchivePath: "memory/" + a.ID, FSPath: md})
				manifest.Accounts = append(manifest.Accounts, a.ID)
				memDirs[a.ID] = md
			}
		}
	}

	entry, err := m.vault.Create(p.Slug, manifest, sources)
	if err != nil {
		return vault.Entry{}, fmt.Errorf("write vault archive: %w", err)
	}

	// The archive is committed; now it is safe to destroy the live copy. Reuse
	// DeleteProject for DB rows + journals + RAG collection, then wipe the working
	// dir and memory (which DeleteProject deliberately leaves alone).
	if err := m.DeleteProject(projectID); err != nil {
		return vault.Entry{}, fmt.Errorf("archive written (%s) but cleanup failed: %w", entry.Name, err)
	}
	if safeToRemoveDir(p.Dir) {
		_ = os.RemoveAll(p.Dir)
	}
	for _, md := range memDirs {
		_ = os.RemoveAll(md)
	}
	return entry, nil
}

// ListVault returns the safe deposit box contents (most-recently-deleted first).
func (m *Manager) ListVault() ([]vault.Entry, error) {
	if m.vault == nil {
		return nil, ErrVaultUnavailable
	}
	return m.vault.List()
}

// DestroyVault permanently erases a vault archive — the single, irreversible
// destruction action of the whole workflow.
func (m *Manager) DestroyVault(name string) error {
	if m.vault == nil {
		return ErrVaultUnavailable
	}
	return m.vault.Delete(name)
}

// RestoreFromVault rebuilds a deleted project from its vault archive and brings
// it back to the active board: working tree, project + session rows, journals
// and per-account memory. On success the archive is removed (the project is no
// longer "deleted"). RAG re-indexes from scratch via the normal scheduler.
func (m *Manager) RestoreFromVault(ctx context.Context, name string) (store.Project, error) {
	if m.vault == nil {
		return store.Project{}, ErrVaultUnavailable
	}
	if _, err := m.vault.Get(name); err != nil {
		return store.Project{}, err
	}
	tmp, err := os.MkdirTemp(m.cfg.DataDir, "vault-restore-")
	if err != nil {
		return store.Project{}, err
	}
	defer os.RemoveAll(tmp)

	manifest, err := m.vault.Extract(name, tmp)
	if err != nil {
		return store.Project{}, fmt.Errorf("extract archive: %w", err)
	}
	pm := manifest.Project

	// Pick a destination dir: the original if free, else <ProjectsDir>/<slug>.
	targetDir := pm.Dir
	if targetDir == "" || pathExists(targetDir) {
		targetDir = filepath.Join(m.cfg.ProjectsDir, m.uniqueSlug(pm.Name))
	}
	workSrc := filepath.Join(tmp, "working")
	if isDir(workSrc) {
		if err := os.MkdirAll(filepath.Dir(targetDir), 0o755); err != nil {
			return store.Project{}, err
		}
		if err := moveTree(workSrc, targetDir); err != nil {
			return store.Project{}, fmt.Errorf("restore working dir: %w", err)
		}
	} else if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return store.Project{}, err
	}

	// Reuse the original project id if free, else mint a new one.
	id := pm.ID
	if id == "" {
		id = uuid.NewString()
	} else if _, err := m.db.GetProject(id); err == nil {
		id = uuid.NewString()
	}
	p := store.Project{
		ID: id, Name: pm.Name, Slug: m.uniqueSlug(pm.Name), Dir: targetDir,
		Category: pm.Category, Starred: pm.Starred, CreatedAt: pm.CreatedAt,
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	if err := m.db.CreateProject(p); err != nil {
		return store.Project{}, fmt.Errorf("recreate project row: %w", err)
	}

	// Restore session rows + journals. A live state is coerced to stopped (there
	// is no runner). Session ids are uuids, so collisions are not expected; on the
	// off chance one exists, skip it rather than clobber a live session.
	for _, sm := range manifest.Sessions {
		if _, err := m.db.GetSession(sm.ID); err == nil {
			continue
		}
		journalPath := m.cfg.journalPath(sm.ID)
		if sm.JournalFile != "" {
			src := filepath.Join(tmp, "journals", sm.JournalFile)
			if fileExists(src) {
				if err := copyFile(src, journalPath); err != nil {
					return store.Project{}, fmt.Errorf("restore journal %s: %w", sm.ID, err)
				}
			}
		}
		state := sm.State
		if state == store.StateRunning || state == store.StateStarting {
			state = store.StateStopped
		}
		sess := store.Session{
			ID: sm.ID, ProjectID: id, JournalPath: journalPath,
			SocketPath: m.cfg.socketPath(sm.ID), State: state,
			PermissionMode: sm.PermissionMode, CreatedAt: sm.CreatedAt,
			ClaudeAccountID: sm.ClaudeAccountID,
		}
		if err := m.db.CreateSession(sess); err != nil {
			return store.Project{}, fmt.Errorf("recreate session %s: %w", sm.ID, err)
		}
		if sm.Comment != "" {
			_ = m.db.SetSessionComment(sm.ID, sm.Comment)
		}
		if sm.PrURL != "" {
			_ = m.db.SetSessionPrURL(sm.ID, sm.PrURL)
		}
	}

	// Restore per-account memory, keyed to the (possibly new) working dir.
	if len(manifest.Accounts) > 0 {
		accts, _ := m.Accounts()
		cfgByID := map[string]string{}
		for _, a := range accts {
			cfgByID[a.ID] = a.ConfigDir
		}
		for _, accID := range manifest.Accounts {
			cfgDir, ok := cfgByID[accID]
			if !ok {
				continue
			}
			memSrc := filepath.Join(tmp, "memory", accID)
			if !isDir(memSrc) {
				continue
			}
			dest := projectMemoryDir(cfgDir, targetDir)
			if pathExists(dest) {
				continue // never clobber existing memory
			}
			_ = os.MkdirAll(filepath.Dir(dest), 0o755)
			_ = moveTree(memSrc, dest)
		}
	}

	// The project is restored and active again — drop the archive.
	_ = m.vault.Delete(name)
	return p, nil
}

// --- filesystem helpers ---

// projectMemoryDir maps a project's absolute working dir to the Claude CLI's
// per-project memory folder under an account config dir: the CLI encodes the
// path by replacing every "/" with "-" (e.g. /home/claude/projects/x ->
// -home-claude-projects-x).
func projectMemoryDir(configDir, projectDir string) string {
	encoded := strings.ReplaceAll(projectDir, "/", "-")
	return filepath.Join(configDir, "projects", encoded)
}

// safeToRemoveDir guards the destructive RemoveAll against obviously wrong
// targets (root, empty, non-absolute). Protected projects are filtered earlier.
func safeToRemoveDir(dir string) bool {
	return dir != "" && dir != "/" && filepath.IsAbs(dir)
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// moveTree moves src to dst, falling back to copy+remove across filesystems
// (the vault temp dir and the projects dir may live on different mounts).
func moveTree(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyTree(src, dst); err != nil {
		return err
	}
	return os.RemoveAll(src)
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case fi.IsDir():
			return os.MkdirAll(target, fi.Mode().Perm())
		case fi.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			os.Remove(target)
			return os.Symlink(link, target)
		default:
			return copyFile(p, target)
		}
	})
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fi.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
