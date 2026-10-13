package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func init() { register(github{}) }

type github struct{}

func (github) Type() string  { return "github" }
func (github) Label() string { return "GitHub" }

func (github) Fields() []Field {
	return []Field{
		{Key: "token", Label: "Personal Access Token", Type: FieldPassword, Secret: true, Required: true,
			Placeholder: "ghp_xxxxxxxxxxxxxxxxxxxx"},
		{Key: "username", Label: "Utilisateur / organisation", Type: FieldText, Required: true,
			Placeholder: "mon-username"},
	}
}

func (github) Test(ctx context.Context, cfg map[string]string) error {
	if err := missing(cfg, "token"); err != nil {
		return err
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := ghGet(ctx, cfg["token"], "/user", &user); err != nil {
		return err
	}
	if user.Login == "" {
		return fmt.Errorf("github: token valid but no user returned")
	}
	return nil
}

func (github) Tools() []ToolSpec {
	return []ToolSpec{
		{
			Name:        "github_list_repos",
			Description: "List GitHub repositories for the authenticated user.",
			Args: []ArgSpec{
				{Name: "limit", Type: "integer", Description: "Max number of repos to return (default 30)"},
			},
		},
		{
			Name:        "github_create_repo",
			Description: "Create a new GitHub repository.",
			Args: []ArgSpec{
				{Name: "name", Type: "string", Description: "Repository name", Required: true},
				{Name: "private", Type: "boolean", Description: "Private repository (default true)"},
				{Name: "description", Type: "string", Description: "Repository description"},
			},
		},
		{
			Name: "github_push_backup",
			Description: "Commit and push a local directory to a GitHub repository. " +
				"Creates the repo if it does not exist. Useful for automated project backups.",
			Args: []ArgSpec{
				{Name: "local_path", Type: "string", Description: "Absolute path to the local directory to back up", Required: true},
				{Name: "repo_name", Type: "string", Description: "Target GitHub repository name", Required: true},
				{Name: "private", Type: "boolean", Description: "Private when auto-created (default true)"},
				{Name: "branch", Type: "string", Description: "Branch to push to (default: main)"},
				{Name: "message", Type: "string", Description: "Commit message (default: backup)"},
				{Name: "create_if_missing", Type: "boolean", Description: "Create the repo if it does not exist (default true)"},
			},
		},
	}
}

func (g github) Call(ctx context.Context, cfg map[string]string, action string, args map[string]any) (string, error) {
	if err := missing(cfg, "token"); err != nil {
		return "", err
	}
	switch action {
	case "github_list_repos":
		return g.listRepos(ctx, cfg, args)
	case "github_create_repo":
		return g.createRepo(ctx, cfg, args)
	case "github_push_backup":
		return g.pushBackup(ctx, cfg, args)
	default:
		return "", fmt.Errorf("unknown action %q", action)
	}
}

func (github) listRepos(ctx context.Context, cfg map[string]string, args map[string]any) (string, error) {
	limit := 30
	if l, ok := argInt(args, "limit"); ok && l > 0 {
		limit = l
	}
	var repos []struct {
		Name        string `json:"name"`
		Private     bool   `json:"private"`
		Description string `json:"description"`
		HTMLURL     string `json:"html_url"`
		UpdatedAt   string `json:"updated_at"`
	}
	if err := ghGet(ctx, cfg["token"], fmt.Sprintf("/user/repos?per_page=%d&sort=updated", limit), &repos); err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, r := range repos {
		vis := "public"
		if r.Private {
			vis = "private"
		}
		fmt.Fprintf(&sb, "%s [%s] %s\n", r.Name, vis, r.HTMLURL)
		if r.Description != "" {
			fmt.Fprintf(&sb, "  %s\n", r.Description)
		}
	}
	if sb.Len() == 0 {
		return "No repositories found.", nil
	}
	return sb.String(), nil
}

func (github) createRepo(ctx context.Context, cfg map[string]string, args map[string]any) (string, error) {
	name := argString(args, "name", "")
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	private := !argBool(args, "public") // default private unless explicitly public
	if _, ok := args["private"]; ok {
		private = argBool(args, "private")
	}
	body, _ := json.Marshal(map[string]any{
		"name":        name,
		"private":     private,
		"description": argString(args, "description", ""),
		"auto_init":   false,
	})
	var repo struct {
		HTMLURL string `json:"html_url"`
	}
	if err := ghPost(ctx, cfg["token"], "/user/repos", body, &repo); err != nil {
		return "", err
	}
	vis := "public"
	if private {
		vis = "private"
	}
	return fmt.Sprintf("Repository created (%s): %s", vis, repo.HTMLURL), nil
}

func (g github) pushBackup(ctx context.Context, cfg map[string]string, args map[string]any) (string, error) {
	localPath := argString(args, "local_path", "")
	repoName := argString(args, "repo_name", "")
	if localPath == "" || repoName == "" {
		return "", fmt.Errorf("local_path and repo_name are required")
	}
	absPath, err := filepath.Abs(localPath)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	if _, err := os.Stat(absPath); err != nil {
		return "", fmt.Errorf("local_path does not exist: %w", err)
	}

	private := true
	if _, ok := args["private"]; ok {
		private = argBool(args, "private")
	}
	branch := argString(args, "branch", "main")
	message := argString(args, "message", "backup")
	createIfMissing := true
	if _, ok := args["create_if_missing"]; ok {
		createIfMissing = argBool(args, "create_if_missing")
	}

	token := cfg["token"]
	username := cfg["username"]
	remoteURL := fmt.Sprintf("https://%s@github.com/%s/%s.git", token, username, repoName)

	// Ensure the remote repo exists.
	var existing struct{ Name string `json:"name"` }
	if err := ghGet(ctx, token, fmt.Sprintf("/repos/%s/%s", username, repoName), &existing); err != nil {
		if !createIfMissing {
			return "", fmt.Errorf("repo %s/%s not found and create_if_missing is false", username, repoName)
		}
		body, _ := json.Marshal(map[string]any{"name": repoName, "private": private})
		var newRepo struct{ HTMLURL string `json:"html_url"` }
		if err2 := ghPost(ctx, token, "/user/repos", body, &newRepo); err2 != nil {
			return "", fmt.Errorf("create repo: %w", err2)
		}
	}

	// git init if needed.
	if _, err := os.Stat(filepath.Join(absPath, ".git")); os.IsNotExist(err) {
		if out, err := gitRun(absPath, "init"); err != nil {
			return "", fmt.Errorf("git init: %s: %w", out, err)
		}
		// Set default branch name for older git versions.
		gitRun(absPath, "checkout", "-b", branch) //nolint:errcheck
	}

	// Configure identity (needed for commits in CI-like environments).
	gitRun(absPath, "config", "user.email", "hub@example.com") //nolint:errcheck
	gitRun(absPath, "config", "user.name", "Claude Hub")    //nolint:errcheck

	// Set remote.
	if _, err := gitRun(absPath, "remote", "get-url", "origin"); err == nil {
		gitRun(absPath, "remote", "set-url", "origin", remoteURL) //nolint:errcheck
	} else {
		if out, err := gitRun(absPath, "remote", "add", "origin", remoteURL); err != nil {
			return "", fmt.Errorf("git remote add: %s: %w", out, err)
		}
	}

	// Stage everything (respects .gitignore).
	if out, err := gitRun(absPath, "add", "-A"); err != nil {
		return "", fmt.Errorf("git add: %s: %w", out, err)
	}

	// Commit only if something changed.
	status, _ := gitRun(absPath, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		if out, err := gitRun(absPath, "commit", "-m", message); err != nil {
			return "", fmt.Errorf("git commit: %s: %w", out, err)
		}
	}

	// Push (force to keep remote in sync with local backup state).
	out, err := gitRun(absPath, "push", "origin", fmt.Sprintf("HEAD:refs/heads/%s", branch), "--force")
	if err != nil {
		return "", fmt.Errorf("git push: %s: %w", out, err)
	}
	return fmt.Sprintf("Pushed to github.com/%s/%s (branch: %s)", username, repoName, branch), nil
}

// gitRun runs a git command in dir and returns combined output.
func gitRun(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// ghGet performs an authenticated GET against the GitHub REST API.
func ghGet(ctx context.Context, token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com"+path, nil)
	if err != nil {
		return err
	}
	ghHeaders(req, token)
	return ghDo(req, out)
}

// ghPost performs an authenticated POST against the GitHub REST API.
func ghPost(ctx context.Context, token, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.github.com"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	ghHeaders(req, token)
	req.Header.Set("Content-Type", "application/json")
	return ghDo(req, out)
}

func ghHeaders(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
}

func ghDo(req *http.Request, out any) error {
	resp, err := httpClient(false).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var ghErr struct {
			Message string `json:"message"`
		}
		json.NewDecoder(resp.Body).Decode(&ghErr)
		if ghErr.Message != "" {
			return fmt.Errorf("github: %s (HTTP %d)", ghErr.Message, resp.StatusCode)
		}
		return fmt.Errorf("github: HTTP %d", resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
