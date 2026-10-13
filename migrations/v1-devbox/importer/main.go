// Command v1import recreates user projects exported from the V1 "devbox" into
// Claude Hub V2. It is a throwaway one-shot over the V2 store/session packages
// (the sanctioned pattern, since there is no sqlite3 CLI and no bulk-import API).
//
// It reads an import manifest (see migrations/v1-devbox/import-manifest.json),
// and for each entry: creates the project via the normal Manager.CreateProject
// path (which derives a unique slug, makes <ProjectsDir>/<slug>, and git-inits a
// fresh repo), copies the V1 working tree into it EXCLUDING the old .git (clean
// history per the migration plan), makes one import commit, and stores the V1
// metadata in the project's comment field. If the copy/commit fails, the
// half-created project is rolled back so a re-run stays clean (no -2 duplicates).
//
// Safety: dry-run by default. Pass -apply to actually write. It reuses the exact
// production config via config.Load(), so it targets the same DB and ProjectsDir
// as the backend — STOP the backend before running with -apply to avoid SQLite
// write contention, then restart it.
//
// Usage:
//
//	go run ./migrations/v1-devbox/importer                # dry-run
//	go run ./migrations/v1-devbox/importer -apply         # real import
//	go run ./migrations/v1-devbox/importer -cleanup       # remove empty-tree imports (manifest names only)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"system/internal/config"
	"system/internal/session"
	"system/internal/store"
)

type manifest struct {
	StagingRootDefault string `json:"stagingRootDefault"`
	Projects           []struct {
		Name     string `json:"name"`
		Category string `json:"category"`
		Src      string `json:"src"`
		Comment  string `json:"comment"`
	} `json:"projects"`
}

func main() {
	var manifestPath, staging string
	var apply, cleanup bool
	flag.StringVar(&manifestPath, "manifest", "migrations/v1-devbox/import-manifest.json", "path to the import manifest")
	flag.StringVar(&staging, "staging", "", "staging root holding extracted V1 projects (default: manifest stagingRootDefault)")
	flag.BoolVar(&apply, "apply", false, "actually create projects and copy files (default: dry-run, no writes)")
	flag.BoolVar(&cleanup, "cleanup", false, "delete projects matching manifest names whose working tree is empty (rolls back a failed import)")
	flag.Parse()

	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		fatal("read manifest: %v", err)
	}
	var mf manifest
	if err := json.Unmarshal(raw, &mf); err != nil {
		fatal("parse manifest: %v", err)
	}
	if staging == "" {
		staging = mf.StagingRootDefault
	}

	cfg := config.Load()
	fmt.Printf("DB=%s\nProjectsDir=%s\nstaging=%s\napply=%v cleanup=%v\nprojects=%d\n\n",
		cfg.DBPath, cfg.ProjectsDir, staging, apply, cleanup, len(mf.Projects))

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		fatal("open store: %v", err)
	}
	mgr, err := session.NewManager(db, session.Config{DataDir: cfg.DataDir, ProjectsDir: cfg.ProjectsDir})
	if err != nil {
		fatal("new manager: %v", err)
	}
	ctx := context.Background()

	if cleanup {
		runCleanup(db, mf)
		return
	}

	var ok, skipped, failed int
	for _, p := range mf.Projects {
		srcAbs := filepath.Join(staging, p.Src)
		if fi, statErr := os.Stat(srcAbs); statErr != nil || !fi.IsDir() {
			fmt.Printf("SKIP %-30s source missing: %s\n", p.Name, srcAbs)
			skipped++
			continue
		}
		if !apply {
			fmt.Printf("[dry-run] import %-30s category=%-9s <- %s\n", p.Name, p.Category, srcAbs)
			ok++
			continue
		}

		proj, err := mgr.CreateProject(ctx, p.Name, "", p.Category)
		if err != nil {
			fmt.Printf("FAIL %-30s create: %v\n", p.Name, err)
			failed++
			continue
		}
		// Roll back the half-created project if population fails, so a re-run is clean.
		if err := copyTree(srcAbs, proj.Dir); err != nil {
			rollback(db, proj)
			fmt.Printf("FAIL %-30s copy (rolled back): %v\n", p.Name, err)
			failed++
			continue
		}
		if err := gitCommitAll(ctx, proj.Dir, "Import from V1 devbox archive"); err != nil {
			fmt.Printf("WARN %-30s commit: %v\n", p.Name, err)
		}
		if err := mgr.UpdateProjectMeta(proj.ID, false, p.Category, p.Comment); err != nil {
			fmt.Printf("WARN %-30s metadata: %v\n", p.Name, err)
		}
		fmt.Printf("OK   %-30s slug=%-32s dir=%s\n", p.Name, proj.Slug, proj.Dir)
		ok++
	}

	fmt.Printf("\nDone. ok=%d skipped=%d failed=%d (apply=%v)\n", ok, skipped, failed, apply)
	if failed > 0 {
		os.Exit(1)
	}
}

// runCleanup removes projects whose name matches a manifest entry AND whose
// working tree is empty (only a fresh .git, no files). This safely undoes a
// failed import without ever touching a populated project.
func runCleanup(db *store.DB, mf manifest) {
	names := map[string]bool{}
	for _, p := range mf.Projects {
		names[p.Name] = true
	}
	all, err := db.ListProjects()
	if err != nil {
		fatal("list projects: %v", err)
	}
	archived, _ := db.ListArchivedProjects()
	all = append(all, archived...)

	var removed, kept int
	for _, p := range all {
		if p.IsSystem || !names[p.Name] {
			continue
		}
		if !emptyTree(p.Dir) {
			fmt.Printf("KEEP %-30s non-empty tree: %s\n", p.Name, p.Dir)
			kept++
			continue
		}
		rollback(db, p)
		fmt.Printf("DEL  %-30s %s\n", p.Name, p.Dir)
		removed++
	}
	fmt.Printf("\nCleanup done. removed=%d kept=%d\n", removed, kept)
}

// rollback drops the project row and removes its working directory.
func rollback(db *store.DB, p store.Project) {
	if err := db.DeleteProject(p.ID); err != nil {
		fmt.Printf("WARN rollback row %s: %v\n", p.Name, err)
	}
	if p.Dir != "" {
		if err := os.RemoveAll(p.Dir); err != nil {
			fmt.Printf("WARN rollback dir %s: %v\n", p.Dir, err)
		}
	}
}

// emptyTree reports whether dir contains nothing but a .git entry (i.e. a
// freshly-initialised, never-populated project).
func emptyTree(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Name() != ".git" {
			return false
		}
	}
	return true
}

// copyTree mirrors src into dst, dropping the V1 .git so CreateProject's fresh
// repo owns history. Trailing slashes make rsync copy contents, not the dir.
func copyTree(src, dst string) error {
	cmd := exec.Command("rsync", "-a", "--exclude", ".git/", src+"/", dst+"/")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// gitCommitAll stages everything and records a single import commit. An empty
// tree (nothing to commit) is tolerated by the caller as a warning.
func gitCommitAll(ctx context.Context, dir, msg string) error {
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", msg, "--no-gpg-sign"}} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
	}
	return nil
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "v1import: "+format+"\n", args...)
	os.Exit(1)
}
