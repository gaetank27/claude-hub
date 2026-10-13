package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// truncationScriptContent is the PostToolUse hook that truncates large Bash
// outputs before they reach Claude, saving session-window tokens. It is written
// verbatim to <DataDir>/hooks/truncate-output.py at startup and referenced from
// every account's settings.json.
const truncationScriptContent = `#!/usr/bin/env python3
"""
PostToolUse hook — truncates large Bash outputs to save Claude Code tokens.
Registered automatically by Claude Hub in ~/.claude/settings.json.
"""
import json, sys

BASH_MAX_BYTES  = 40_000   # truncate Bash output above this size
BASH_HEAD_LINES = 120      # lines to keep at the top
BASH_TAIL_LINES = 60       # lines to keep at the bottom

try:
    data = json.load(sys.stdin)
except Exception:
    sys.exit(0)

tool   = data.get("tool_name", "")
resp   = data.get("tool_response", {})
output = resp.get("output", "")

if not isinstance(output, str) or not output:
    sys.exit(0)

def truncate(text, max_bytes, head, tail):
    if len(text.encode("utf-8", errors="replace")) <= max_bytes:
        return text, False
    lines = text.splitlines()
    total = len(lines)
    if total <= head + tail:
        return text, False
    top     = lines[:head]
    bot     = lines[total - tail:]
    skipped = total - head - tail
    parts   = ["\n".join(top)]
    parts.append(
        f"\n[... {skipped} lines truncated"
        f" ({len(text):,} bytes total — saved ~{len(text)//4:,} tokens) ...]\n"
    )
    parts.append("\n".join(bot))
    return "\n".join(parts), True

if tool == "Bash":
    new_out, changed = truncate(output, BASH_MAX_BYTES, BASH_HEAD_LINES, BASH_TAIL_LINES)
    if changed:
        data["tool_response"]["output"] = new_out
        json.dump(data, sys.stdout)

sys.exit(0)
`

// hubHookID is a stable identifier embedded as a comment in the hook entry so
// the manager can find and remove its own entry without touching user-added hooks.
const hubHookID = "claude-hub-truncation"

// truncationScriptPath returns the canonical path for the truncation script
// inside the hub's data dir.
func truncationScriptPath(dataDir string) string {
	return filepath.Join(dataDir, "hooks", "truncate-output.py")
}

// EnsureTruncationScript writes (or overwrites) the truncation hook script to
// <dataDir>/hooks/truncate-output.py and makes it executable.
func EnsureTruncationScript(dataDir string) (string, error) {
	dir := filepath.Join(dataDir, "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create hooks dir: %w", err)
	}
	p := truncationScriptPath(dataDir)
	if err := os.WriteFile(p, []byte(truncationScriptContent), 0o755); err != nil {
		return "", fmt.Errorf("write truncation script: %w", err)
	}
	return p, nil
}

// ApplyOutputTruncation enables or disables the truncation PostToolUse hook in
// the given configDir's settings.json. It reads/merges so other settings
// (permissions, other hooks) are preserved. The settings.json is created if it
// does not exist.
func ApplyOutputTruncation(configDir, scriptPath string, enabled bool) error {
	settingsPath := filepath.Join(configDir, "settings.json")

	// Read existing settings or start fresh.
	var doc map[string]json.RawMessage
	if b, err := os.ReadFile(settingsPath); err == nil {
		_ = json.Unmarshal(b, &doc)
	}
	if doc == nil {
		doc = make(map[string]json.RawMessage)
	}

	// Decode the "hooks" object (keep unrelated hook types intact).
	var hooks map[string]json.RawMessage
	if h, ok := doc["hooks"]; ok {
		_ = json.Unmarshal(h, &hooks)
	}
	if hooks == nil {
		hooks = make(map[string]json.RawMessage)
	}

	// Decode the PostToolUse array (keep entries we didn't add).
	type hookEntry struct {
		Matcher string          `json:"matcher"`
		Hooks   json.RawMessage `json:"hooks"`
		// _id is our internal tag; Claude Code ignores unknown fields.
		ID string `json:"_id,omitempty"`
	}
	var postToolUse []hookEntry
	if raw, ok := hooks["PostToolUse"]; ok {
		_ = json.Unmarshal(raw, &postToolUse)
	}

	// Remove any pre-existing hub truncation entry.
	filtered := postToolUse[:0]
	for _, e := range postToolUse {
		if e.ID != hubHookID {
			filtered = append(filtered, e)
		}
	}

	if enabled {
		// Append our entry.
		cmdJSON, _ := json.Marshal([]map[string]string{
			{"type": "command", "command": scriptPath},
		})
		filtered = append(filtered, hookEntry{
			Matcher: "Bash",
			Hooks:   cmdJSON,
			ID:      hubHookID,
		})
	}

	if len(filtered) == 0 {
		delete(hooks, "PostToolUse")
	} else {
		raw, _ := json.Marshal(filtered)
		hooks["PostToolUse"] = raw
	}

	if len(hooks) == 0 {
		delete(doc, "hooks")
	} else {
		raw, _ := json.Marshal(hooks)
		doc["hooks"] = raw
	}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	if err := os.WriteFile(settingsPath, append(out, '\n'), 0o644); err != nil {
		return fmt.Errorf("write settings.json: %w", err)
	}
	return nil
}

// SyncOutputTruncation writes the truncation script (if enabled) and patches
// every connected account's settings.json. Called after boot and on toggle.
func (m *Manager) SyncOutputTruncation(enabled bool) error {
	scriptPath := truncationScriptPath(m.cfg.DataDir)

	if enabled {
		var err error
		scriptPath, err = EnsureTruncationScript(m.cfg.DataDir)
		if err != nil {
			return err
		}
	}

	// Apply to the shared root (symlinked to non-default accounts via SyncAllAccounts).
	root := m.sharedRoot()
	if root != "" {
		if err := ApplyOutputTruncation(root, scriptPath, enabled); err != nil {
			return fmt.Errorf("patch shared root settings.json: %w", err)
		}
		// Ensure every non-default account has a symlink to the shared settings.json.
		if err := m.SyncAllAccounts(); err != nil {
			return fmt.Errorf("sync account symlinks: %w", err)
		}
	}

	// Also apply directly to any account whose configDir is not symlinked
	// (i.e. accounts whose configDir != sharedRoot but already has a real settings.json).
	accts, err := m.db.ListClaudeAccounts()
	if err != nil {
		return err
	}
	for _, a := range accts {
		if a.ConfigDir == "" || filepath.Clean(a.ConfigDir) == filepath.Clean(root) {
			continue // default account already handled via sharedRoot
		}
		dest := filepath.Join(a.ConfigDir, "settings.json")
		fi, err := os.Lstat(dest)
		if err != nil {
			continue // doesn't exist — symlink will be created by SyncAllAccounts
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			continue // symlink → already handled via sharedRoot
		}
		// Real file that is NOT the sharedRoot — patch it directly.
		if err := ApplyOutputTruncation(a.ConfigDir, scriptPath, enabled); err != nil {
			return fmt.Errorf("patch account %s settings.json: %w", a.ID, err)
		}
	}
	return nil
}
