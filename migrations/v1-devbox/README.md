# V1 (devbox) → Claude Hub V2 migration

Tooling and report for importing the user projects from the V1 "devbox" archive
into Claude Hub V2. Generated 2026-06-01.

## Files
- `MIGRATION_REPORT.md` — full analysis: archive inventory, project classification
  (imported / excluded / flagged), category mapping, and the V1↔V2 context-system
  comparison + improvement proposals.
- `import-manifest.json` — the 22 user projects to recreate (name, V2 category,
  source path, V1 metadata preserved as the project comment).
- `importer/main.go` — throwaway one-shot that performs the import over the V2
  `store`/`session` packages. Dry-run by default.

## Safety model
- The archive is a full V1 home-directory dump and contains secrets (`.ssh`
  private keys, `*.env`, mail creds). They were **never extracted** and are **not**
  referenced here. Only `projects/` + root context files were extracted to the
  quarantine at `/home/claude/archives/v1-devbox/`.
- The V1 **system** project (`proj` orchestrator + `projbot`) is **excluded** —
  no V1 backend logic is reused in V2.
- No existing V2 system source is modified; this directory is purely additive.

## How to run the import
The importer reuses the production config (`config.Load()`), so it targets the
same DB (`~/.local/share/system/system.db`) and `ProjectsDir` (`/home/claude/projects`)
as the backend.

```sh
export PATH=$PATH:/usr/local/go/bin

# 1. Dry-run (no writes) — lists what would be created
go run ./migrations/v1-devbox/importer

# 2. Real import — STOP the backend first to avoid SQLite write contention
systemctl --user stop system        # adjust unit name if different
go run ./migrations/v1-devbox/importer -apply
systemctl --user start system
```

For each project the importer: creates it via `Manager.CreateProject` (unique
slug, `<ProjectsDir>/<slug>`, fresh `git init`), copies the V1 tree **excluding
the old `.git`** (clean history), makes one import commit, and writes the V1
metadata into the project's comment field.

Override the staging root with `-staging <dir>` if the quarantine moved.
