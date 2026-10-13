package session

import (
	"context"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// autoNameRe matches the placeholder names LaunchSession hands out ("Session #3").
// Only sessions still carrying one are renamed; a name set by a human or the
// scheduler is never overwritten.
var autoNameRe = regexp.MustCompile(`^Session #\d+$`)

const (
	autoNameModel   = "haiku"
	autoNameTimeout = 45 * time.Second
	autoNameMaxIn   = 2000 // runes of the first message sent to Haiku
	autoNameMaxOut  = 60   // runes kept from Haiku's answer
)

// MaybeAutoName names a session after its first user message by asking Haiku for
// a short title. It is cheap to call on every user input: it returns immediately
// unless the session still has its placeholder name and has not been tried yet,
// and the Haiku call itself runs in a goroutine. Best-effort — on any failure the
// "Session #N" name simply stays.
func (m *Manager) MaybeAutoName(sessionID, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if _, loaded := m.autoNamed.LoadOrStore(sessionID, struct{}{}); loaded {
		return
	}
	s, err := m.db.GetSession(sessionID)
	if err != nil || !autoNameRe.MatchString(s.Comment) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), autoNameTimeout)
		defer cancel()
		title, err := m.haikuTitle(ctx, s.ClaudeAccountID, text)
		if err != nil || title == "" {
			if err != nil {
				log.Printf("session %s: auto-name: %v", sessionID, err)
			}
			return
		}
		// Re-check: the user may have renamed the session while Haiku was thinking.
		cur, err := m.db.GetSession(sessionID)
		if err != nil || !autoNameRe.MatchString(cur.Comment) {
			return
		}
		if err := m.db.SetSessionComment(sessionID, title); err != nil {
			log.Printf("session %s: auto-name: %v", sessionID, err)
		}
	}()
}

// haikuTitle runs a one-shot, tool-less `claude -p` on Haiku under the session's
// own subscription and returns a cleaned-up short title for the message.
func (m *Manager) haikuTitle(ctx context.Context, accountID, text string) (string, error) {
	if r := []rune(text); len(r) > autoNameMaxIn {
		text = string(r[:autoNameMaxIn])
	}
	prompt := "Summarize the user message below into a short session title: 3 to 6 words, " +
		"same language as the message, no quotes, no trailing punctuation. " +
		"Reply with the title only.\n\n<message>\n" + text + "\n</message>"

	cmd := exec.CommandContext(ctx, m.cfg.ClaudeBin, "-p", prompt,
		"--model", autoNameModel,
		"--tools", "",
		"--disable-slash-commands",
		"--no-session-persistence")
	cmd.Dir = os.TempDir() // keep project CLAUDE.md / settings out of a naming call
	cmd.Env = os.Environ()
	if a, err := m.db.GetClaudeAccount(accountID); err == nil {
		cmd.Env = append(cmd.Env, "CLAUDE_CONFIG_DIR="+a.ConfigDir)
		if key, err := os.ReadFile(apiKeyPath(a)); err == nil && strings.TrimSpace(string(key)) != "" {
			cmd.Env = append(cmd.Env, "ANTHROPIC_API_KEY="+strings.TrimSpace(string(key)))
		}
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return cleanTitle(string(out)), nil
}

// cleanTitle keeps the first non-empty line, strips quotes/markdown/trailing
// punctuation and caps the length.
func cleanTitle(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		line = strings.Trim(line, "\"'`*#« »“”")
		line = strings.TrimRight(line, ".:;!")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > autoNameMaxOut {
			line = strings.TrimSpace(string(r[:autoNameMaxOut]))
		}
		return line
	}
	return ""
}
