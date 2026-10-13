package bootstrap

import (
	"system/internal/rag"
	"system/internal/store"
)

// EnsureProjectRag guarantees every user-facing project (including `system`) has a
// private RAG collection + a project_rag config row at boot. The actual indexing is
// driven by the scheduler, not here, so this stays cheap. Delegates to the shared
// implementation in the rag package (also used by the runtime scheduler).
func EnsureProjectRag(db *store.DB) error {
	return rag.EnsureProjectCollections(db)
}
