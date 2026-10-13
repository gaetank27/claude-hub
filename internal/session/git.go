package session

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// planBranch is the branch an approved plan executes on, isolating autonomous
// work as a safety net. Per-step commits and the PR come in slice 4.
func planBranch(sessionID string) string { return "plan/" + sessionID }

// createPlanBranch checks out a dedicated branch for an approved plan. It is a
// no-op when dir is not a git repository (not every project is versioned yet).
// `checkout -B` makes re-approving the same session idempotent.
func createPlanBranch(ctx context.Context, dir, sessionID string) error {
	if !isGitRepo(ctx, dir) {
		return nil
	}
	out, err := gitCmd(ctx, dir, "checkout", "-B", planBranch(sessionID)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("create plan branch: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ensureRemote makes sure the project repo has an `origin` to push to, creating a
// GitHub repository with `gh` when none is configured. It is a no-op outside a git
// repo or when origin already exists, so it is safe to call on every approval.
// Best-effort: failures (no gh, not authenticated) must not block execution —
// the worst case is autonomous work that commits locally but cannot open a PR.
//
// Must run BEFORE the plan/<id> branch is checked out: on a freshly created repo
// it also pushes the current (base) branch, so the pull request claude later
// opens from plan/<id> has a base to target — an empty remote has none.
func ensureRemote(ctx context.Context, dir, name string) error {
	if !isGitRepo(ctx, dir) {
		return nil
	}
	if err := gitCmd(ctx, dir, "remote", "get-url", "origin").Run(); err == nil {
		return nil // origin already configured
	}
	// `--source` adds the directory as a git repo on GitHub and wires up the
	// `origin` remote; private by default. It does not push anything.
	out, err := exec.CommandContext(ctx, "gh", "repo", "create", name,
		"--private", "--source", dir, "--remote", "origin").CombinedOutput()
	if err != nil {
		return fmt.Errorf("gh repo create: %w: %s", err, strings.TrimSpace(string(out)))
	}
	// Establish the base branch on the empty remote so a later PR can target it.
	if branch := currentBranch(ctx, dir); branch != "" {
		if out, err := gitCmd(ctx, dir, "push", "-u", "origin", branch).CombinedOutput(); err != nil {
			return fmt.Errorf("push base branch %s: %w: %s", branch, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// hasUncommittedChanges reports whether the working tree in dir has staged,
// unstaged, or untracked changes. Used by the backend git fallback to decide
// whether claude left work uncommitted.
func hasUncommittedChanges(ctx context.Context, dir string) bool {
	out, err := gitCmd(ctx, dir, "status", "--porcelain").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// commitAll stages everything and commits with msg. An empty index (nothing
// staged) is a benign no-op so the fallback stays idempotent. The empty-index
// check uses the staged-diff exit code rather than matching git's message, which
// is locale-dependent.
func commitAll(ctx context.Context, dir, msg string) error {
	if out, err := gitCmd(ctx, dir, "add", "-A").CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if gitCmd(ctx, dir, "diff", "--cached", "--quiet").Run() == nil {
		return nil // nothing staged
	}
	out, err := gitCmd(ctx, dir, "commit", "-m", msg).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git commit: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// commitsAhead returns how many commits branch has beyond base, or -1 when the
// count cannot be determined (e.g. base ref missing). The caller treats -1 as
// "unknown, proceed best-effort" rather than "nothing to ship".
func commitsAhead(ctx context.Context, dir, base, branch string) int {
	out, err := gitCmd(ctx, dir, "rev-list", "--count", base+".."+branch).Output()
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return -1
	}
	return n
}

// hasUnpushedCommits reports whether branch has commits not on origin/<branch>.
// A missing upstream ref (the branch was never pushed) reads as unpushed.
func hasUnpushedCommits(ctx context.Context, dir, branch string) bool {
	out, err := gitCmd(ctx, dir, "rev-list", "--count", "origin/"+branch+".."+branch).Output()
	if err != nil {
		return true
	}
	return strings.TrimSpace(string(out)) != "0"
}

// pushBranch pushes branch to origin, setting upstream so later status checks
// have a tracking ref to compare against.
func pushBranch(ctx context.Context, dir, branch string) error {
	out, err := gitCmd(ctx, dir, "push", "-u", "origin", branch).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push %s: %w: %s", branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// defaultBranch returns the repository's default branch via gh, falling back to
// "main" when gh is unavailable or the repo records no default.
func defaultBranch(ctx context.Context, dir string) string {
	out, err := ghCmd(ctx, dir, "repo", "view", "--json", "defaultBranchRef", "-q", ".defaultBranchRef.name").Output()
	if err != nil {
		return "main"
	}
	if b := strings.TrimSpace(string(out)); b != "" {
		return b
	}
	return "main"
}

// createPR opens a pull request from head into base and returns the PR URL,
// parsed from gh's output. It errors when gh fails (including when a PR already
// exists for the branch); the caller recovers an existing URL via existingPRURL.
func createPR(ctx context.Context, dir, base, head, title, body string) (string, error) {
	out, err := ghCmd(ctx, dir, "pr", "create",
		"--base", base, "--head", head, "--title", title, "--body", body).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gh pr create: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if url := prURLRe.Find(out); url != nil {
		return string(url), nil
	}
	return "", fmt.Errorf("gh pr create: no PR URL in output: %s", strings.TrimSpace(string(out)))
}

// existingPRURL returns the URL of the open PR for head, or "" when none exists
// or the lookup fails. Used to recover a URL claude opened but never reported.
func existingPRURL(ctx context.Context, dir, head string) string {
	out, err := ghCmd(ctx, dir, "pr", "view", head, "--json", "url", "-q", ".url").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func currentBranch(ctx context.Context, dir string) string {
	out, _ := gitCmd(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	return strings.TrimSpace(string(out))
}

func isGitRepo(ctx context.Context, dir string) bool {
	out, _ := gitCmd(ctx, dir, "rev-parse", "--is-inside-work-tree").Output()
	return strings.TrimSpace(string(out)) == "true"
}

// gitInit initializes a new git repository in dir, defaulting the initial branch
// to `main` to match the hub convention. No-op if dir is already a repo.
func gitInit(ctx context.Context, dir string) error {
	if isGitRepo(ctx, dir) {
		return nil
	}
	out, err := gitCmd(ctx, dir, "init", "-b", "main").CombinedOutput()
	if err != nil {
		return fmt.Errorf("git init: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func gitCmd(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	return cmd
}

// ghCmd builds a gh command rooted in dir so it resolves the right repository
// from the working directory (gh has no per-repo flag for most subcommands).
func ghCmd(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	return cmd
}
