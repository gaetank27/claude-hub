package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// unitName is the transient systemd unit hosting a session's runner.
func unitName(sessionID string) string {
	return "system-session-" + sessionID + ".service"
}

// launchRunnerUnit starts the session-runner as a transient user systemd
// service. As a service (not a --scope), it runs in the background under the
// user manager and is fully decoupled from the backend: the backend can restart
// or be recompiled without affecting it.
//
// Resource caps (MemoryMax/CPUQuota) protect the host — and `system` itself —
// from a runaway session. They are best-effort: empty values are skipped.
func launchRunnerUnit(ctx context.Context, cfg LaunchSpec) error {
	args := []string{
		"--user",
		"--unit", unitName(cfg.SessionID),
		"--collect", // garbage-collect the unit once it exits
		"--setenv=PATH=" + envOrDefault("PATH", "/usr/local/bin:/usr/bin:/bin"),
		"--setenv=HOME=" + envOrDefault("HOME", "/home/claude"),
	}
	// Isolate the session's Claude subscription: CLAUDE_CONFIG_DIR relocates the
	// CLI's whole config (incl. .credentials.json) so accounts never overwrite
	// each other; ANTHROPIC_API_KEY switches an apikey account to key-based auth.
	if cfg.ConfigDir != "" {
		args = append(args, "--setenv=CLAUDE_CONFIG_DIR="+cfg.ConfigDir)
	}
	if cfg.APIKey != "" {
		args = append(args, "--setenv=ANTHROPIC_API_KEY="+cfg.APIKey)
	}
	if cfg.MemoryMax != "" {
		args = append(args, "--property=MemoryMax="+cfg.MemoryMax)
	}
	if cfg.CPUQuota != "" {
		args = append(args, "--property=CPUQuota="+cfg.CPUQuota)
	}

	args = append(args, "--",
		cfg.RunnerBin,
		"--session-id", cfg.SessionID,
		"--project-dir", cfg.ProjectDir,
		"--journal", cfg.JournalPath,
		"--socket", cfg.SocketPath,
		"--permission-mode", cfg.PermissionMode,
		"--claude-bin", cfg.ClaudeBin,
	)
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	if cfg.MCPConfigPath != "" {
		args = append(args, "--mcp-config", cfg.MCPConfigPath)
	}
	if cfg.AskViaMCP {
		args = append(args, "--ask-via-mcp")
	}
	if cfg.ContextFilePath != "" {
		args = append(args, "--context-file", cfg.ContextFilePath)
	}
	if cfg.Resume {
		args = append(args, "--resume")
	}

	out, err := exec.CommandContext(ctx, "systemd-run", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemd-run: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// stopRunnerUnit stops the transient unit (which terminates claude). It is
// idempotent: transient units are --collect'd on exit, so a runner that already
// died leaves no unit and `systemctl stop` exits 5 ("unit not loaded"). That is
// the desired post-stop state, so we treat it as success — otherwise the caller
// never marks the session stopped and the UI stays stuck on "running".
func stopRunnerUnit(ctx context.Context, sessionID string) error {
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "stop", unitName(sessionID)).CombinedOutput()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 5 {
			return nil
		}
		return fmt.Errorf("systemctl stop: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// unitActive reports whether the session's unit is currently active.
func unitActive(ctx context.Context, sessionID string) bool {
	out, _ := exec.CommandContext(ctx, "systemctl", "--user", "is-active", unitName(sessionID)).Output()
	return strings.TrimSpace(string(out)) == "active"
}

// unitResources reads a running session unit's current memory and cumulative CPU
// usage from systemd. ok is false when the property is unavailable (unit gone,
// or systemd reports "[not set]"), so callers can omit the metric rather than
// show a bogus zero.
func unitResources(ctx context.Context, sessionID string) (memBytes int64, cpuNSec int64, ok bool) {
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "show",
		unitName(sessionID), "--property=MemoryCurrent,CPUUsageNSec").Output()
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		key, val, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found {
			continue
		}
		n, perr := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
		if perr != nil {
			continue // "[not set]" / empty when the unit is gone or unaccounted
		}
		switch key {
		case "MemoryCurrent":
			memBytes, ok = n, true
		case "CPUUsageNSec":
			cpuNSec = n
		}
	}
	return memBytes, cpuNSec, ok
}

// waitForUnitInactive blocks until the session's transient unit is no longer
// active, so its name is free to be reused by a resume relaunch (the unit is
// --collect'd on exit).
func waitForUnitInactive(ctx context.Context, sessionID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !unitActive(ctx, sessionID) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("unit %s still active after %s", unitName(sessionID), timeout)
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
