package session

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// run executes a git command in dir and fails the test on error.
func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// initRepo creates a git repo in a fresh temp dir with a deterministic identity
// and an initial commit on `main`, returning its path.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "-b", "main")
	run(t, dir, "config", "user.email", "test@example.com")
	run(t, dir, "config", "user.name", "test")
	run(t, dir, "config", "commit.gpgsign", "false")
	writeFile(t, dir, "README.md", "init\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-m", "init")
	return dir
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestHasUncommittedChanges(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)

	if hasUncommittedChanges(ctx, dir) {
		t.Fatal("clean tree reported as dirty")
	}
	writeFile(t, dir, "new.txt", "work\n")
	if !hasUncommittedChanges(ctx, dir) {
		t.Fatal("untracked file not detected")
	}
}

func TestCommitAll(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)

	writeFile(t, dir, "a.txt", "a\n")
	if err := commitAll(ctx, dir, "add a"); err != nil {
		t.Fatalf("commitAll: %v", err)
	}
	if hasUncommittedChanges(ctx, dir) {
		t.Fatal("tree still dirty after commitAll")
	}
	// A second commit with nothing staged must be a benign no-op.
	if err := commitAll(ctx, dir, "noop"); err != nil {
		t.Fatalf("commitAll on clean tree should be a no-op, got: %v", err)
	}
}

func TestCommitsAhead(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	branch := "plan/x"
	run(t, dir, "checkout", "-B", branch)

	if n := commitsAhead(ctx, dir, "main", branch); n != 0 {
		t.Fatalf("fresh branch: want 0 ahead, got %d", n)
	}
	writeFile(t, dir, "b.txt", "b\n")
	if err := commitAll(ctx, dir, "add b"); err != nil {
		t.Fatalf("commitAll: %v", err)
	}
	if n := commitsAhead(ctx, dir, "main", branch); n != 1 {
		t.Fatalf("after one commit: want 1 ahead, got %d", n)
	}
}

func TestHasUnpushedCommits(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)

	// A bare repo acts as origin.
	remote := t.TempDir()
	run(t, remote, "init", "--bare", "-b", "main")
	run(t, dir, "remote", "add", "origin", remote)
	run(t, dir, "push", "-u", "origin", "main")

	branch := "plan/x"
	run(t, dir, "checkout", "-B", branch)
	writeFile(t, dir, "c.txt", "c\n")
	if err := commitAll(ctx, dir, "add c"); err != nil {
		t.Fatalf("commitAll: %v", err)
	}
	// Branch never pushed → unpushed.
	if !hasUnpushedCommits(ctx, dir, branch) {
		t.Fatal("never-pushed branch reported as pushed")
	}
	if err := pushBranch(ctx, dir, branch); err != nil {
		t.Fatalf("pushBranch: %v", err)
	}
	if hasUnpushedCommits(ctx, dir, branch) {
		t.Fatal("pushed branch reported as unpushed")
	}
}
