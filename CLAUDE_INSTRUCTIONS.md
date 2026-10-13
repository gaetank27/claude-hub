# CLAUDE_INSTRUCTIONS — mandatory working method for this repository

**Any Claude instance opening this directory MUST read this file and apply the
rules below.** This is enforced by pointing at it from `CLAUDE.md` (the only file
the harness auto-loads); this standalone file is the canonical statement of the
method so it stays explicit and shareable.

## Rule 1 — `CONTEXT_MEMORY.md` is part of the Definition of Done

`CONTEXT_MEMORY.md` is the single live source of truth for project context. It is
**not** optional documentation — keeping it current is part of finishing the work.

Whenever you **change code, fix a bug, or add a feature**, you MUST update
`CONTEXT_MEMORY.md` in the **same change set / commit**, autonomously and
transparently. Do not ask the user to validate this update — do it automatically
as soon as a change is made.

Concretely, on every meaningful change reflect it in the relevant section:
- **État Actuel & Fonctionnalités** — when behavior/features change.
- **Architecture & Flux de Données** — when components, APIs or data flow change.
- **Décisions Techniques Majeures (ADR)** — when a non-obvious choice is made
  (record *why*, not just *what*).
- **Dette Technique & Améliorations Futures** — when something is deferred,
  worked around, or newly discovered.
- **Prochaines Étapes Immédiates** — update the checklist so the next session
  resumes instantly.

## Rule 2 — keep the resume path clean

- `CONTEXT_MEMORY.md` is the entry point. `README.md` covers install/architecture
  for humans. `PROJECT_CONTEXT.md` and `DECISIONS.md` are **frozen archives** — do
  not update them; fold any still-relevant content into `CONTEXT_MEMORY.md`.
- Prefer updating the existing section over appending duplicates. One fact, one
  place.

## Rule 3 — project-specific hygiene (carried from hard-won lessons)

- **UI changes:** edit → `npm run build` → **verify the served artifact** (grep
  the embedded JS, or curl prod with a minted `hub_session` cookie) → commit.
  Never batch edit+build+commit+push in one parallel block — it has repeatedly
  left stale bundles / unregistered routes while reporting success.
- **New API handler:** register its route in `internal/api/server.go`, else it
  falls through to the SPA catch-all and returns `index.html`.
- **DB:** no `sqlite3` CLI on the box; use the idempotent in-code `migrate()` or a
  one-shot Go program importing `internal/store`.
- **`go` is not on PATH:** `export PATH=$PATH:/usr/local/go/bin` first.

## Rule 4 — keep the user documentation alive (Aide + PDF manual)

The in-app **Aide** section is the user-facing manual; it must never drift from
the product. Whenever a feature is **developed, modified, or deployed** in Claude
Hub, you have the absolute obligation, **in the same change set**, to:

1. **Update the matching chapter** in the Help content (`web/src/lib/help.ts`) —
   add, edit or remove sections so the documentation matches the new behavior.
2. **Regenerate the screenshot** for any page whose UI changed: run
   `cmd/docgen -mode screenshots` (against the running instance, forging a session
   cookie from `SYSTEM_SESSION_SECRET` when OIDC is on), then `npm run build` to
   re-embed the fresh PNGs.
3. **Rebuild the PDF manual** with `cmd/docgen -mode pdf` (full flow:
   `make docs DOCS_BASE=… DOCS_SECRET=…`), and email it via the global Email
   connector (`email_send`) when a delivered manual is expected.

Do this autonomously, without asking for validation — same as Rule 1. The Help
section, its screenshots and the PDF are part of the Definition of Done.

These rules are permanent and apply to every session on this repository.
