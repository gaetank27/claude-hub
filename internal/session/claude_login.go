package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/creack/pty"

	"system/internal/store"
)

// pendingLogin is an in-flight `claude auth login` running under a PTY, awaiting
// the auth code the user pastes back from the browser. Held per account between
// StartLogin and FinishLogin.
type pendingLogin struct {
	cmd    *exec.Cmd
	ptmx   *os.File
	cancel context.CancelFunc
}

// LoginInstructions is what StartLogin hands the UI: the OAuth authorize URL the
// user opens in a browser to obtain the code they then paste back.
type LoginInstructions struct {
	AuthURL   string `json:"authUrl"`
	ConfigDir string `json:"configDir,omitempty"`
}

// authURLRe matches the Claude OAuth authorize URL the CLI prints. The PTY is
// sized wide enough (below) that the URL is never wrapped, so it stays on one
// line; the char class stops at whitespace / control bytes.
var authURLRe = regexp.MustCompile(`https?://[a-zA-Z0-9.-]*claude\.com/[^\s\x00-\x1f"'<>]+`)

// StartLogin launches `claude auth login --claudeai` under a PTY (the CLI login
// is an interactive TUI that only renders — and only prints the auth URL — on a
// real terminal), scoped to the account's CLAUDE_CONFIG_DIR, and returns the
// authorize URL scraped from its output. The process stays alive awaiting the
// code (FinishLogin).
func (m *Manager) StartLogin(id string) (LoginInstructions, error) {
	a, err := m.db.GetClaudeAccount(id)
	if err != nil {
		return LoginInstructions{}, err
	}
	if a.Kind != store.AccountKindOAuth {
		return LoginInstructions{}, fmt.Errorf("login flow applies to oauth accounts only")
	}
	m.cancelLogin(id) // drop any stale attempt

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	cmd := exec.CommandContext(ctx, m.cfg.ClaudeBin, "auth", "login", "--claudeai")
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+a.ConfigDir)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		cancel()
		return LoginInstructions{}, fmt.Errorf("start login: %w", err)
	}
	// Wide PTY so the long authorize URL is emitted on a single unwrapped line.
	_ = pty.Setsize(ptmx, &pty.Winsize{Rows: 50, Cols: 1000})

	m.loginMu.Lock()
	m.logins[id] = &pendingLogin{cmd: cmd, ptmx: ptmx, cancel: cancel}
	m.loginMu.Unlock()

	// Drain the PTY continuously (so the child never blocks on a full buffer) and
	// signal as soon as the authorize URL appears.
	urlCh := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		var acc []byte
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				acc = append(acc, buf[:n]...)
				if len(acc) > 1<<16 {
					acc = acc[len(acc)-(1<<16):]
				}
				if u := authURLRe.FindString(string(acc)); u != "" {
					select {
					case urlCh <- u:
					default:
					}
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	select {
	case u := <-urlCh:
		return LoginInstructions{AuthURL: u, ConfigDir: a.ConfigDir}, nil
	case <-time.After(25 * time.Second):
		m.cancelLogin(id)
		return LoginInstructions{}, fmt.Errorf("login did not surface an auth URL (CLI may have changed)")
	}
}

// FinishLogin writes the pasted auth code to the waiting login process, waits for
// it to complete, then validates via `claude auth status` (scoped to the config
// dir), enriches the account's identity, marks it connected and activates it.
func (m *Manager) FinishLogin(id, code string) (store.ClaudeAccount, error) {
	a, err := m.db.GetClaudeAccount(id)
	if err != nil {
		return store.ClaudeAccount{}, err
	}

	m.loginMu.Lock()
	pend := m.logins[id]
	delete(m.logins, id)
	m.loginMu.Unlock()

	if pend != nil {
		if c := strings.TrimSpace(code); c != "" {
			_, _ = pend.ptmx.Write([]byte(c + "\n"))
		}
		done := make(chan struct{})
		go func() { _ = pend.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
		}
		pend.ptmx.Close()
		pend.cancel()
	}

	st, serr := m.claudeAuthStatus(a.ConfigDir)
	if serr != nil || !st.LoggedIn {
		return store.ClaudeAccount{}, fmt.Errorf("login not completed — check the code and try again")
	}
	_ = m.db.UpdateClaudeAccountMeta(id, st.OrgID, st.Email)
	if err := m.db.SetClaudeAccountStatus(id, store.AccountConnected); err != nil {
		return store.ClaudeAccount{}, err
	}
	if err := m.db.SetActiveClaudeAccount(id); err != nil {
		return store.ClaudeAccount{}, err
	}
	a.Status, a.Active, a.OrgID, a.AccountEmail = store.AccountConnected, true, st.OrgID, st.Email
	// Ensure the freshly-logged-in account sees the shared Claude context. The
	// login may have created the config dir only now, so (re)link here too.
	if err := m.SyncSharedContext(a); err != nil {
		log.Printf("share claude context for %s: %v", id, err)
	}
	return a, nil
}

// cancelLogin kills and forgets any in-flight login for an account.
func (m *Manager) cancelLogin(id string) {
	m.loginMu.Lock()
	pend := m.logins[id]
	delete(m.logins, id)
	m.loginMu.Unlock()
	if pend != nil {
		pend.cancel()
		if pend.ptmx != nil {
			pend.ptmx.Close()
		}
	}
}

// authStatus mirrors the JSON of `claude auth status`.
type authStatus struct {
	LoggedIn         bool   `json:"loggedIn"`
	AuthMethod       string `json:"authMethod"`
	Email            string `json:"email"`
	OrgID            string `json:"orgId"`
	OrgName          string `json:"orgName"`
	SubscriptionType string `json:"subscriptionType"`
}

// claudeAuthStatus runs `claude auth status` scoped to a config dir and parses
// its JSON. Used to validate a login and to enrich an account's identity.
func (m *Manager) claudeAuthStatus(configDir string) (authStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.cfg.ClaudeBin, "auth", "status")
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+configDir)
	out, err := cmd.Output()
	if err != nil {
		return authStatus{}, err
	}
	s := strings.TrimSpace(string(out))
	if i := strings.IndexByte(s, '{'); i > 0 {
		s = s[i:]
	}
	var st authStatus
	if err := json.Unmarshal([]byte(s), &st); err != nil {
		return authStatus{}, err
	}
	return st, nil
}
