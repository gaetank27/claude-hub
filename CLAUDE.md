# CLAUDE.md — system hub ("Claude Hub")

Project-level instructions. Auto-loaded at the start of every session in this
repository. Read this, then read `CONTEXT_MEMORY.md`.

## Mandatory working method

**Before coding:** read `CONTEXT_MEMORY.md` — the single live source of truth for
project state, architecture, decisions, tech debt and next steps.

**While coding (Definition of Done):** every code change, bug fix or feature MUST
update `CONTEXT_MEMORY.md` in the same change set, autonomously — do not ask for
validation. The full method is in `CLAUDE_INSTRUCTIONS.md`; read and apply it.

## Quick reference

- **Architecture & install:** `README.md`. **Live context:** `CONTEXT_MEMORY.md`.
- `PROJECT_CONTEXT.md` and `DECISIONS.md` are **frozen archives** (history up to
  slice 8) — do not update them; `CONTEXT_MEMORY.md` supersedes them.
- `go` is not on PATH: `export PATH=$PATH:/usr/local/go/bin`.
- UI: edit → `npm run build` → verify the embedded artifact → commit. Never batch
  edit+build+commit+push in one parallel block.
- New API handler ⇒ register its route in `internal/api/server.go`.
- No `sqlite3` CLI: use `migrate()` or a one-shot Go program over `internal/store`.
- No full test suite; verify by building + exercising the app.
- **Doc obligatoire** (Rule 4): toute évolution UI/feature ⇒ mettre à jour le
  chapitre Aide (`web/src/lib/help.ts`), régénérer la capture (`cmd/docgen`) et
  rebâtir le PDF (`make docs`), dans le même change set.
