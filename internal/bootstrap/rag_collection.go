package bootstrap

import (
	"time"

	"system/internal/store"
)

// EnsureDefaultRagCollection seeds the permanent "default" knowledge base on
// first run and adopts any pre-collections documents into it. Idempotent: a no-op
// once any collection exists.
func EnsureDefaultRagCollection(db *store.DB) error {
	n, err := db.CountRagCollections()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if err := db.CreateRagCollection(store.RagCollection{
		ID:          store.DefaultRagCollectionID,
		Name:        "Général",
		Slug:        "general",
		Description: "Base de connaissances par défaut",
		CreatedAt:   time.Now().UTC(),
	}); err != nil {
		return err
	}
	// Documents created before collections existed have an empty collection_id.
	return db.BackfillRagCollection(store.DefaultRagCollectionID)
}
