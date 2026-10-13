package session

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// attachmentsSubdir is where uploaded files land inside a project's working dir.
// The runner sets the claude process cwd + --add-dir to the project dir, so a
// file written here is directly reachable by claude's tools (unzip, tar, Read).
const attachmentsSubdir = ".hub-uploads"

// SaveAttachment streams an uploaded file into the session's project working
// directory under .hub-uploads/<batch>/<name>, returning the path relative to
// the project dir (slash-separated) for referencing in a prompt. The filename
// and batch are reduced to their base names to prevent path traversal. The
// upload subdir is best-effort added to the project's .gitignore so autonomous
// git workflows do not commit transient attachments.
func (m *Manager) SaveAttachment(sessionID, batch, filename string, r io.Reader) (string, error) {
	s, err := m.db.GetSession(sessionID)
	if err != nil {
		return "", err
	}
	proj, err := m.db.GetProject(s.ProjectID)
	if err != nil {
		return "", err
	}
	name := sanitizeFilename(filename)
	if name == "" {
		return "", fmt.Errorf("invalid filename %q", filename)
	}
	batch = sanitizeFilename(batch)
	if batch == "" {
		return "", fmt.Errorf("invalid batch %q", batch)
	}
	dir := filepath.Join(proj.Dir, attachmentsSubdir, batch)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	ensureGitignored(proj.Dir, attachmentsSubdir+"/")
	return filepath.ToSlash(filepath.Join(attachmentsSubdir, batch, name)), nil
}

// sanitizeFilename reduces a user-supplied name to a safe base name, rejecting
// empty/dot/traversal inputs.
func sanitizeFilename(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "." || name == ".." || name == string(filepath.Separator) {
		return ""
	}
	return name
}

// ensureGitignored appends entry to the project's .gitignore unless it is already
// present. Best-effort: failures (no repo, unreadable file) are silently ignored.
func ensureGitignored(projectDir, entry string) {
	path := filepath.Join(projectDir, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == strings.TrimSpace(entry) {
			return
		}
	}
	f, ferr := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if ferr != nil {
		return
	}
	defer f.Close()
	prefix := ""
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		prefix = "\n"
	}
	f.WriteString(prefix + entry + "\n")
}
