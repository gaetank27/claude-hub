package runner

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// The exit marker is a sidecar file next to the journal recording how the runner's
// claude process terminated. It exists because session units are launched with
// --collect: once the runner exits, systemd garbage-collects the unit and its exit
// metadata, so the backend reconciler can no longer ask systemd how it died. The
// runner therefore records its own outcome here, while it still can.
//
// Semantics, read by the backend when it finds a running session whose unit is gone:
//   - content "0"        => claude exited cleanly        => session stopped
//   - content non-"0"    => claude exited with an error  => session error
//   - file absent        => runner died before it could write (SIGKILL/OOM/reboot)
//                           => session error
//
// The marker is kept out of the journal on purpose: the journal is the immutable
// claude stream, replayed verbatim to clients, and must not carry runner-internal
// events.

// ExitMarkerPath is the sidecar path for a session's journal.
func ExitMarkerPath(journalPath string) string {
	return journalPath + ".exit"
}

// clearExitMarker removes any stale marker from a previous run, so a resumed
// session is not judged by the outcome of its prior process.
func clearExitMarker(journalPath string) {
	_ = os.Remove(ExitMarkerPath(journalPath))
}

// writeExitMarker records the claude process's exit status. A process terminated
// by a signal reports a negative code via ExitError, which reads as non-clean.
func writeExitMarker(journalPath string, waitErr error) {
	code := 0
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	_ = os.WriteFile(ExitMarkerPath(journalPath), []byte(strconv.Itoa(code)), 0o600)
}

// CleanExit reports whether the runner recorded a clean (status 0) claude exit.
// Absent or non-zero marker => not clean.
func CleanExit(journalPath string) bool {
	b, err := os.ReadFile(ExitMarkerPath(journalPath))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(b)) == "0"
}
