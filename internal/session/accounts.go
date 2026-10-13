package session

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"system/internal/store"
)

// Account lifecycle errors, surfaced as 4xx by the API layer.
var (
	ErrNoActiveAccount  = errors.New("no active Claude account: connect one first")
	ErrAccountLoggedOut = errors.New("account is logged out: connect it first")
	ErrAccountActive    = errors.New("account is active: switch to another account first")
)

// DefaultAccountID is the stable id of the seeded account that points at the
// pre-existing ~/.claude config dir, so legacy sessions keep working and it can
// never be deleted (mirrors the protected `system` project).
const DefaultAccountID = "default"

func (c Config) accountsDir() string { return filepath.Join(c.DataDir, "claude-accounts") }

func validAccountKind(k string) bool {
	return k == store.AccountKindOAuth || k == store.AccountKindAPIKey
}

// credentialsPath / apiKeyPath locate an account's secrets inside its isolated
// config dir. The credentials file is what the usage meter reads; the api_key
// file is injected as ANTHROPIC_API_KEY at launch for apikey accounts.
func credentialsPath(a store.ClaudeAccount) string {
	return filepath.Join(a.ConfigDir, ".credentials.json")
}
func apiKeyPath(a store.ClaudeAccount) string {
	return filepath.Join(a.ConfigDir, "api_key")
}

// ActiveAccount returns the currently-selected subscription.
func (m *Manager) ActiveAccount() (store.ClaudeAccount, error) {
	return m.db.GetActiveClaudeAccount()
}

// ActiveCredentialsPath is the active account's .credentials.json, consumed by
// the usage meter. Empty when no account is active (the meter then hides).
func (m *Manager) ActiveCredentialsPath() string {
	a, err := m.db.GetActiveClaudeAccount()
	if err != nil {
		return ""
	}
	return credentialsPath(a)
}

// Accounts lists all connected/known subscriptions for the settings UI.
func (m *Manager) Accounts() ([]store.ClaudeAccount, error) {
	return m.db.ListClaudeAccounts()
}

// AccountCredentialsPath is an account's .credentials.json (the file the usage
// meter reads). Unlike ActiveCredentialsPath it works for any account, so the
// session guard can poll each connected subscription's 5-hour window.
func (m *Manager) AccountCredentialsPath(a store.ClaudeAccount) string {
	return credentialsPath(a)
}

// EnrichActiveAccount fills the active account's org/email from `claude auth
// status` when they are still blank (e.g. the seeded default). Best-effort: any
// failure (CLI missing, not logged in) is ignored. Called once at boot.
func (m *Manager) EnrichActiveAccount() {
	a, err := m.db.GetActiveClaudeAccount()
	if err != nil || a.OrgID != "" || a.AccountEmail != "" {
		return
	}
	if st, err := m.claudeAuthStatus(a.ConfigDir); err == nil && st.LoggedIn {
		_ = m.db.UpdateClaudeAccountMeta(a.ID, st.OrgID, st.Email)
	}
}

// Account returns a single subscription by id (used by the usage meter to scope
// itself to a project's account).
func (m *Manager) Account(id string) (store.ClaudeAccount, error) {
	return m.db.GetClaudeAccount(id)
}

// accountForProject resolves the subscription a project's NEW sessions run under:
// the project's assigned account when set and connected, otherwise the default
// (active) account. ok is false when neither resolves (no default to fall back
// on), in which case launch refuses rather than running on ambient credentials.
func (m *Manager) accountForProject(proj store.Project) (store.ClaudeAccount, bool) {
	if proj.ClaudeAccountID != "" {
		if a, err := m.db.GetClaudeAccount(proj.ClaudeAccountID); err == nil && a.Status == store.AccountConnected {
			return a, true
		}
	}
	if a, err := m.db.GetActiveClaudeAccount(); err == nil {
		return a, true
	}
	return store.ClaudeAccount{}, false
}

// SetProjectClaudeAccount assigns a project's subscription ('' => inherit the
// default account). A non-empty id must reference a connected account. New
// sessions follow it; in-flight sessions keep their original account.
func (m *Manager) SetProjectClaudeAccount(projectID, accountID string) error {
	if _, err := m.db.GetProject(projectID); err != nil {
		return err
	}
	if accountID != "" {
		a, err := m.db.GetClaudeAccount(accountID)
		if err != nil {
			return err
		}
		if a.Status != store.AccountConnected {
			return ErrAccountLoggedOut
		}
	}
	return m.db.SetProjectClaudeAccount(projectID, accountID)
}

// SetProjectModel assigns the Claude model a project's new sessions run on
// ('' => inherit the global runtime-settings model). New sessions follow it;
// in-flight sessions keep the model they were stamped with at launch.
func (m *Manager) SetProjectModel(projectID, model string) error {
	if _, err := m.db.GetProject(projectID); err != nil {
		return err
	}
	return m.db.SetProjectModel(projectID, model)
}

// accountForSession resolves the subscription a session runs under: the one it
// was stamped with (so a resume reattaches under its original account), else the
// active account. ok is false when neither resolves.
func (m *Manager) accountForSession(s store.Session) (store.ClaudeAccount, bool) {
	if s.ClaudeAccountID != "" {
		if a, err := m.db.GetClaudeAccount(s.ClaudeAccountID); err == nil {
			return a, true
		}
	}
	if a, err := m.db.GetActiveClaudeAccount(); err == nil {
		return a, true
	}
	return store.ClaudeAccount{}, false
}

func (m *Manager) readAPIKey(a store.ClaudeAccount) string {
	b, err := os.ReadFile(apiKeyPath(a))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ConnectAccount registers a new subscription in its own CLAUDE_CONFIG_DIR.
// An apikey account stores the key and is immediately connected; an oauth account
// is created logged-out and completed by StartLogin/FinishLogin.
func (m *Manager) ConnectAccount(label, kind, apiKey string) (store.ClaudeAccount, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return store.ClaudeAccount{}, fmt.Errorf("label is required")
	}
	if !validAccountKind(kind) {
		return store.ClaudeAccount{}, fmt.Errorf("invalid account kind %q", kind)
	}
	id := uuid.NewString()
	dir := filepath.Join(m.cfg.accountsDir(), id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return store.ClaudeAccount{}, fmt.Errorf("create config dir: %w", err)
	}
	acct := store.ClaudeAccount{
		ID:        id,
		Label:     label,
		ConfigDir: dir,
		Kind:      kind,
		Status:    store.AccountLoggedOut,
		CreatedAt: time.Now().UTC(),
	}
	if kind == store.AccountKindAPIKey {
		if strings.TrimSpace(apiKey) == "" {
			return store.ClaudeAccount{}, fmt.Errorf("api key is required")
		}
		if err := os.WriteFile(apiKeyPath(acct), []byte(strings.TrimSpace(apiKey)), 0o600); err != nil {
			return store.ClaudeAccount{}, fmt.Errorf("store api key: %w", err)
		}
		acct.Status = store.AccountConnected
	}
	if err := m.db.CreateClaudeAccount(acct); err != nil {
		return store.ClaudeAccount{}, err
	}
	// Wire the new account to the shared Claude context (memory, skills, commands,
	// …) so it starts with the same knowledge as every other account.
	if err := m.SyncSharedContext(acct); err != nil {
		log.Printf("share claude context for %s: %v", acct.ID, err)
	}
	return acct, nil
}

// SwitchAccount makes id the active subscription. It refuses a logged-out account.
// In-flight sessions keep their original account (their runners are immutable);
// only new sessions and the usage meter follow the switch.
func (m *Manager) SwitchAccount(id string) error {
	a, err := m.db.GetClaudeAccount(id)
	if err != nil {
		return err
	}
	if a.Status != store.AccountConnected {
		return ErrAccountLoggedOut
	}
	return m.db.SetActiveClaudeAccount(id)
}

// LogoutAccount stops the account's live sessions, archives its credentials
// (reversible) and marks it logged out. When the active account is logged out,
// another connected account is activated if one exists.
func (m *Manager) LogoutAccount(ctx context.Context, id string) error {
	a, err := m.db.GetClaudeAccount(id)
	if err != nil {
		return err
	}
	sessions, err := m.db.ListSessions()
	if err != nil {
		return err
	}
	for _, s := range sessions {
		if s.ClaudeAccountID == id && (s.State == store.StateRunning || s.State == store.StateStarting) {
			_ = m.StopSession(ctx, s.ID)
		}
	}
	ts := time.Now().Unix()
	archiveFile(credentialsPath(a), ts)
	archiveFile(apiKeyPath(a), ts)
	if err := m.db.SetClaudeAccountStatus(id, store.AccountLoggedOut); err != nil {
		return err
	}
	if a.Active {
		m.activateFallback(id)
	}
	return nil
}

// activateFallback picks any other connected account as the new active one, or
// clears the active flag when none remains (the hub then refuses to launch until
// an account is reconnected).
func (m *Manager) activateFallback(excludeID string) {
	accts, err := m.db.ListClaudeAccounts()
	if err == nil {
		for _, a := range accts {
			if a.ID != excludeID && a.Status == store.AccountConnected {
				_ = m.db.SetActiveClaudeAccount(a.ID)
				return
			}
		}
	}
	_ = m.db.ClearActiveClaudeAccount()
}

// DeleteAccount removes a non-active account and its config dir. Any account can
// be deleted, including the seeded default (the user may not want it): when the
// DB ends up empty, bootstrap re-seeds default from ~/.claude on next start.
// Live sessions of the account are stopped first (they can't outlive it); unlike
// logout, no credentials are archived, so deleting default never mutates the
// shared ~/.claude. Finished sessions keep their now-dangling account id, which
// resolves to the active account on resume.
func (m *Manager) DeleteAccount(ctx context.Context, id string) error {
	a, err := m.db.GetClaudeAccount(id)
	if err != nil {
		return err
	}
	if a.Active {
		return ErrAccountActive
	}
	sessions, err := m.db.ListSessions()
	if err != nil {
		return err
	}
	for _, s := range sessions {
		if s.ClaudeAccountID == id && (s.State == store.StateRunning || s.State == store.StateStarting) {
			_ = m.StopSession(ctx, s.ID)
		}
	}
	// Only remove a dedicated dir under our accounts root — never a shared
	// ~/.claude that the default account points at.
	if strings.HasPrefix(a.ConfigDir, m.cfg.accountsDir()+string(os.PathSeparator)) {
		_ = os.RemoveAll(a.ConfigDir)
	}
	return m.db.DeleteClaudeAccount(id)
}

// archiveFile renames path to "path.bak-<ts>" when it exists, so a logout is a
// reversible archive rather than a destructive delete.
func archiveFile(path string, ts int64) {
	if _, err := os.Stat(path); err == nil {
		_ = os.Rename(path, fmt.Sprintf("%s.bak-%d", path, ts))
	}
}
