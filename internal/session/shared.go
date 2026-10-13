package session

import (
	"fmt"
	"os"
	"path/filepath"

	"system/internal/store"
)

// sharedContextEntries are the Claude Code config-dir entries that must be the
// SAME across every subscription: global instructions, knowledge and tooling.
// Only the per-account secrets (.credentials.json, api_key) stay isolated, so
// switching accounts never loses memory, skills, commands, agents or settings.
//
// Entries are linked at their natural granularity (a whole dir, or a single
// file). Missing sources are skipped, so the list is a superset: an entry that
// does not exist in the shared root is simply not shared.
var sharedContextEntries = []string{
	"CLAUDE.md",        // global preferences / instructions
	"settings.json",    // harness configuration (hooks, permissions, env)
	"skills",           // reusable skills (the Skills module writes here)
	"commands",         // slash commands
	"agents",           // subagent definitions
	"templates",        // file templates referenced from memory
	"keybindings.json", // custom keybindings
}

// sharedRoot is the canonical config dir holding the shared context: the
// pre-existing ~/.claude (parent of the credentials file). It is the default
// account's dir and the source every other account's symlinks point at. Empty
// when no credentials path is configured (sharing then becomes a no-op).
func (m *Manager) sharedRoot() string {
	if m.cfg.CredentialsPath == "" {
		return ""
	}
	return filepath.Dir(m.cfg.CredentialsPath)
}

// SkillsDir is the canonical on-disk skills directory (the shared root's
// skills/), the single source of truth for skill definitions. Every account
// sees it through the symlink installed by SyncSharedContext.
func (m *Manager) SkillsDir() string {
	root := m.sharedRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(root, "skills")
}

// SyncSharedContext makes a non-default account see the shared Claude context by
// symlinking each existing shared entry from sharedRoot into the account's config
// dir. It is idempotent and non-destructive: a real (non-symlink) file already in
// place is left untouched rather than clobbered, and the per-account secrets are
// never touched (they are not in the allowlist).
func (m *Manager) SyncSharedContext(a store.ClaudeAccount) error {
	root := m.sharedRoot()
	if root == "" || a.ConfigDir == "" {
		return nil
	}
	// The shared root is itself the source; nothing to do for the account that
	// already lives there (the default).
	if filepath.Clean(a.ConfigDir) == filepath.Clean(root) {
		return nil
	}
	if err := os.MkdirAll(a.ConfigDir, 0o700); err != nil {
		return fmt.Errorf("ensure config dir %s: %w", a.ConfigDir, err)
	}
	var firstErr error
	for _, entry := range sharedContextEntries {
		src := filepath.Join(root, entry)
		if _, err := os.Lstat(src); err != nil {
			continue // nothing to share for this entry
		}
		dst := filepath.Join(a.ConfigDir, entry)
		if err := linkShared(src, dst); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// linkShared ensures dst is a symlink to src. An existing correct symlink is a
// no-op; a stale symlink is repointed; a real file/dir is preserved (skipped) so
// account-local data is never destroyed.
func linkShared(src, dst string) error {
	if fi, err := os.Lstat(dst); err == nil {
		if fi.Mode()&os.ModeSymlink == 0 {
			return nil // real file/dir already there — never clobber it
		}
		if cur, lerr := os.Readlink(dst); lerr == nil && cur == src {
			return nil // already correct
		}
		if err := os.Remove(dst); err != nil {
			return fmt.Errorf("replace stale link %s: %w", dst, err)
		}
	}
	if err := os.Symlink(src, dst); err != nil {
		return fmt.Errorf("link %s -> %s: %w", dst, src, err)
	}
	return nil
}

// SyncAllAccounts reconciles the shared context for every known account. Called
// at boot so accounts created before this feature (and any drift, e.g. a skills/
// dir that appeared after the account was connected) are repaired.
func (m *Manager) SyncAllAccounts() error {
	accts, err := m.db.ListClaudeAccounts()
	if err != nil {
		return err
	}
	var firstErr error
	for _, a := range accts {
		if err := m.SyncSharedContext(a); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
