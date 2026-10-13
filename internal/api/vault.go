package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"system/internal/docker"
	"system/internal/session"
	"system/internal/vault"
)

// vaultEntryDTO is the safe-deposit-box listing shape for the frontend.
type vaultEntryDTO struct {
	Name         string    `json:"name"`
	ProjectName  string    `json:"projectName"`
	Slug         string    `json:"slug"`
	OriginalDir  string    `json:"originalDir"`
	DeletedAt    time.Time `json:"deletedAt"`
	Size         int64     `json:"size"`
	SessionCount int       `json:"sessionCount"`
}

func toVaultDTO(e vault.Entry) vaultEntryDTO {
	return vaultEntryDTO{
		Name:         e.Name,
		ProjectName:  e.Manifest.Project.Name,
		Slug:         e.Manifest.Project.Slug,
		OriginalDir:  e.Manifest.Project.Dir,
		DeletedAt:    e.Manifest.DeletedAt,
		Size:         e.Size,
		SessionCount: len(e.Manifest.Sessions),
	}
}

// vaultProject is the second deletion stage: it force-removes the project's
// Docker containers (their working tree is about to be wiped), then packs the
// project into the vault and destroys the live copy. Called from the Archivage
// page's "Supprimer" — it never erases without first writing the archive.
func (s *Server) vaultProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if p, err := s.db.GetProject(id); err == nil {
		s.removeProjectContainers(r.Context(), p.Slug)
	}
	entry, err := s.mgr.VaultProject(r.Context(), id)
	switch {
	case errors.Is(err, session.ErrVaultUnavailable):
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	case errors.Is(err, session.ErrProjectProtected), errors.Is(err, session.ErrSessionLive):
		writeErr(w, http.StatusConflict, err)
		return
	case err != nil:
		writeNotFoundOr500(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toVaultDTO(entry))
}

// listVault returns the safe deposit box contents (most-recently-deleted first).
func (s *Server) listVault(w http.ResponseWriter, r *http.Request) {
	entries, err := s.mgr.ListVault()
	if errors.Is(err, session.ErrVaultUnavailable) {
		// Vault disabled: report an empty box rather than an error so the UI
		// section renders cleanly.
		writeJSON(w, http.StatusOK, []vaultEntryDTO{})
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]vaultEntryDTO, 0, len(entries))
	for _, e := range entries {
		out = append(out, toVaultDTO(e))
	}
	writeJSON(w, http.StatusOK, out)
}

// restoreVault rebuilds a deleted project from its archive and reactivates it.
func (s *Server) restoreVault(w http.ResponseWriter, r *http.Request) {
	p, err := s.mgr.RestoreFromVault(r.Context(), r.PathValue("name"))
	switch {
	case errors.Is(err, session.ErrVaultUnavailable):
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	case errors.Is(err, vault.ErrNotFound):
		writeErr(w, http.StatusNotFound, err)
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.toProjectDTO(p))
}

// destroyVault permanently erases a vault archive — the only irreversible
// destruction in the whole lifecycle.
func (s *Server) destroyVault(w http.ResponseWriter, r *http.Request) {
	err := s.mgr.DestroyVault(r.PathValue("name"))
	switch {
	case errors.Is(err, session.ErrVaultUnavailable):
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	case errors.Is(err, vault.ErrNotFound):
		writeErr(w, http.StatusNotFound, err)
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// stopProjectContainers stops (without removing) every hub-managed container of
// a project — used when archiving. Best-effort: a missing docker daemon or a
// stop error must not block the archive.
func (s *Server) stopProjectContainers(ctx context.Context, slug string) {
	for _, c := range s.projectContainers(ctx, slug) {
		if c.State == "running" {
			sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			_ = s.docker.Stop(sctx, c.ID)
			cancel()
		}
	}
}

// removeProjectContainers force-removes every hub-managed container of a project
// — used when vaulting, since the working tree they mount is about to be wiped.
func (s *Server) removeProjectContainers(ctx context.Context, slug string) {
	for _, c := range s.projectContainers(ctx, slug) {
		rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		_ = s.docker.Remove(rctx, c.ID)
		cancel()
	}
}

func (s *Server) projectContainers(ctx context.Context, slug string) []docker.Container {
	if s.docker == nil || slug == "" {
		return nil
	}
	lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cs, err := s.docker.List(lctx, docker.LabelProject+"="+slug)
	if err != nil {
		return nil
	}
	return cs
}
