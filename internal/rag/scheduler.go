package rag

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"

	"system/internal/store"
)

// ReconcileEmbedder detects a change of embedding model since the last run and
// forces a full re-embed when it happens — vectors from a different model have a
// different dimension and cannot be compared against the new one. Idempotent: a
// no-op on first run or when the model is unchanged. Call once at boot, before the
// worker starts draining.
func (ix *Indexer) ReconcileEmbedder() {
	if ix.emb == nil {
		return
	}
	cur := ix.emb.Name()
	settings, _ := ix.db.GetSettings()
	if prev := settings["rag_embedder"]; prev != "" && prev != cur {
		log.Printf("rag: embedder changed (%s -> %s); re-embedding all documents", prev, cur)
		if err := ix.db.ResetAllRagVectors(); err != nil {
			log.Printf("rag: reset vectors: %v", err)
		}
	}
	_ = ix.db.SetSetting("rag_embedder", cur)
}

// EnsureProjectCollections guarantees every non-hidden project has a private RAG
// collection + a project_rag config row. Idempotent. Shared by the boot sequence
// and the scheduler, so a project created at runtime gets its index initialized on
// the next scheduler tick without coupling the session manager to the rag package.
func EnsureProjectCollections(db *store.DB) error {
	projects, err := db.ListProjects()
	if err != nil {
		return err
	}
	for _, p := range projects {
		if _, err := db.GetProjectRag(p.ID); err == nil {
			continue
		} else if err != store.ErrNotFound {
			return err
		}
		col := store.RagCollection{
			ID:          uuid.NewString(),
			Name:        "Code · " + p.Name,
			Slug:        "proj-" + p.Slug,
			Description: "Index RAG privé du projet " + p.Name,
			ProjectID:   p.ID,
			CreatedAt:   time.Now().UTC(),
		}
		if err := db.CreateRagCollection(col); err != nil {
			return err
		}
		if err := db.CreateProjectRag(store.ProjectRag{
			ProjectID: p.ID, CollectionID: col.ID, Enabled: true, AutoOnChange: true,
		}); err != nil {
			return err
		}
	}
	return nil
}

// RunProjectScheduler drives per-project (re)indexing for the process lifetime: it
// ensures every project has an index, finalizes settled index passes, and triggers
// a reindex when one is due (never-indexed, git HEAD changed, or interval elapsed).
// All heavy work is async (the embedding runs in the worker), so a tick is cheap.
func (ix *Indexer) RunProjectScheduler(ctx context.Context, tick time.Duration) {
	t := time.NewTimer(10 * time.Second) // first pass shortly after boot
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			ix.scheduleOnce(ctx)
			t.Reset(tick)
		}
	}
}

func (ix *Indexer) tryReindex(ctx context.Context, projectID string) {
	if err := ix.Reindex(ctx, projectID); err != nil {
		log.Printf("rag: reindex %s: %v", projectID, err)
	}
}

func (ix *Indexer) scheduleOnce(ctx context.Context) {
	if err := EnsureProjectCollections(ix.db); err != nil {
		log.Printf("rag: ensure project collections: %v", err)
	}
	prs, err := ix.db.ListProjectRag()
	if err != nil {
		return
	}
	for _, pr := range prs {
		if !pr.Enabled {
			continue
		}
		switch pr.Status {
		case store.ProjectRagIndexing:
			ix.finalizeIfSettled(pr)
		case store.ProjectRagIdle:
			if ix.dueForReindex(ctx, pr) {
				ix.tryReindex(ctx, pr.ProjectID)
			}
		case store.ProjectRagError:
			// Auto-recover once an embedder is available (e.g. it was missing at the
			// first scan): retry when due, but never churn while still unavailable.
			if ix.Available() && ix.dueForReindex(ctx, pr) {
				ix.tryReindex(ctx, pr.ProjectID)
			}
		}
	}
}

// finalizeIfSettled flips an indexing project back to idle once the async worker
// has drained every pending/processing document, recording the final stats.
func (ix *Indexer) finalizeIfSettled(pr store.ProjectRag) {
	if !ix.Available() {
		return // no embedder: documents stay pending, nothing to finalize
	}
	st, err := ix.CollectionStats(pr.CollectionID)
	if err != nil || st.Pending > 0 {
		return
	}
	_ = ix.db.SetProjectRagIndexed(pr.ProjectID, pr.LastHeadSHA, st.Files, st.Chunks, st.Bytes, time.Now().UTC())
}

func (ix *Indexer) dueForReindex(ctx context.Context, pr store.ProjectRag) bool {
	proj, err := ix.db.GetProject(pr.ProjectID)
	if err != nil {
		return false
	}
	if pr.LastIndexedAt == nil {
		return true // never indexed
	}
	if pr.AutoOnChange {
		if h := gitHead(ctx, proj.Dir); h != "" && h != pr.LastHeadSHA {
			return true
		}
	}
	if pr.IntervalMinutes > 0 && time.Since(*pr.LastIndexedAt) >= time.Duration(pr.IntervalMinutes)*time.Minute {
		return true
	}
	return false
}

// Reindex scans a project's files into its collection and enqueues the changed
// ones. Status flips to `indexing` (finalized later by the scheduler) or `error`
// when no embeddings provider is configured. Safe to call manually (API) or from
// the scheduler.
func (ix *Indexer) Reindex(ctx context.Context, projectID string) error {
	pr, err := ix.db.GetProjectRag(projectID)
	if err != nil {
		return err
	}
	proj, err := ix.db.GetProject(projectID)
	if err != nil {
		return err
	}
	_ = ix.db.SetProjectRagStatus(projectID, store.ProjectRagIndexing, "")
	files, bytesTotal, head, err := ix.ScanProject(ctx, proj.Dir, pr.CollectionID)
	if err != nil {
		_ = ix.db.SetProjectRagStatus(projectID, store.ProjectRagError, err.Error())
		return err
	}
	_ = ix.db.SetProjectRagScan(projectID, head, files, bytesTotal)
	if !ix.Available() {
		_ = ix.db.SetProjectRagStatus(projectID, store.ProjectRagError,
			"fournisseur d'embeddings non configuré (VOYAGE_API_KEY) — indexation en attente")
	}
	return nil
}
