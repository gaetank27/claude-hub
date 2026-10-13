package bootstrap

import (
	"path/filepath"
	"time"

	"system/internal/session"
	"system/internal/store"
)

// EnsureDefaultClaudeAccount seeds the permanent default subscription on first
// run, pointing at the pre-existing CLAUDE_CONFIG_DIR (the dir holding the
// credentials file, i.e. ~/.claude). It is idempotent: a no-op once any account
// exists. On the very first run over a legacy DB it also rebinds existing
// sessions and usage samples to this account so their stats are attributed
// instead of orphaned.
func EnsureDefaultClaudeAccount(db *store.DB, credentialsPath string) error {
	n, err := db.CountClaudeAccounts()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil // already initialized
	}
	acct := store.ClaudeAccount{
		ID:        session.DefaultAccountID, // stable, protected id
		Label:     "default",
		ConfigDir: filepath.Dir(credentialsPath),
		Kind:      store.AccountKindOAuth, // the existing ~/.claude is a subscription login
		Status:    store.AccountConnected,
		Active:    true,
		CreatedAt: time.Now().UTC(),
	}
	if err := db.CreateClaudeAccount(acct); err != nil {
		return err
	}
	if err := db.AttachOrphanSessions(acct.ID); err != nil {
		return err
	}
	return db.AttachOrphanSnapshots(acct.ID)
}
