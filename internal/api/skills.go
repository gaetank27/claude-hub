package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"system/internal/store"
)

// skillDTO is the stable JSON shape of a skill for the frontend.
type skillDTO struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Description string    `json:"description"`
	Keywords    string    `json:"keywords"`
	Status      string    `json:"status"`
	SessionID   string    `json:"sessionId,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

func toSkillDTO(sk store.Skill) skillDTO {
	return skillDTO{
		ID: sk.ID, Name: sk.Name, Slug: sk.Slug, Description: sk.Description,
		Keywords: sk.Keywords, Status: sk.Status, SessionID: sk.SessionID,
		CreatedAt: sk.CreatedAt,
	}
}

// listSkills returns all skills, optionally filtered by a free-text query ?q=
// matched against name, slug, description and keywords (the library search).
func (s *Server) listSkills(w http.ResponseWriter, r *http.Request) {
	skills, err := s.mgr.ListSkills()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	out := make([]skillDTO, 0, len(skills))
	for _, sk := range skills {
		if q != "" && !skillMatches(sk, q) {
			continue
		}
		out = append(out, toSkillDTO(sk))
	}
	writeJSON(w, http.StatusOK, out)
}

// skillMatches reports whether a skill matches a lowercased search query.
func skillMatches(sk store.Skill, q string) bool {
	hay := strings.ToLower(sk.Name + " " + sk.Slug + " " + sk.Description + " " + sk.Keywords)
	return strings.Contains(hay, q)
}

// createSkill registers a skill and spins up its background authoring session.
// mode "discover" briefs the session to find an existing Claude Code skill on the
// web and install it; any other value (default) authors a new skill from scratch.
func (s *Server) createSkill(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Mode        string `json:"mode"` // "design" (default) | "discover"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("name is required"))
		return
	}
	sk, err := s.mgr.CreateSkill(r.Context(), req.Name, req.Description, req.Mode == "discover")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, toSkillDTO(sk))
}

func (s *Server) getSkill(w http.ResponseWriter, r *http.Request) {
	sk, err := s.mgr.GetSkill(r.PathValue("id"))
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSkillDTO(sk))
}

// updateSkill edits metadata and/or flips the status (validate/disable). Fields
// are optional (pointers): only those present are applied, so a partial PATCH
// never clobbers the others.
func (s *Server) updateSkill(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Keywords    *string `json:"keywords"`
		Status      *string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	id := r.PathValue("id")
	cur, err := s.mgr.GetSkill(id)
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	if req.Name != nil || req.Description != nil || req.Keywords != nil {
		name := derefOr(req.Name, cur.Name)
		desc := derefOr(req.Description, cur.Description)
		kw := derefOr(req.Keywords, cur.Keywords)
		if cur, err = s.mgr.UpdateSkill(id, name, desc, kw); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if req.Status != nil {
		if cur, err = s.mgr.SetSkillStatus(r.Context(), id, *req.Status); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, toSkillDTO(cur))
}

// evolveSkill (re)opens an editing session on an existing skill so it can be
// modified, streamed via the existing /api/sessions routes.
func (s *Server) evolveSkill(w http.ResponseWriter, r *http.Request) {
	sk, err := s.mgr.EvolveSkill(r.Context(), r.PathValue("id"))
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSkillDTO(sk))
}

// closeSkillSession stops a skill's editing session without changing its status,
// so no Claude Code session is left running idle.
func (s *Server) closeSkillSession(w http.ResponseWriter, r *http.Request) {
	sk, err := s.mgr.CloseSkillSession(r.Context(), r.PathValue("id"))
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSkillDTO(sk))
}

func (s *Server) deleteSkill(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.DeleteSkill(r.Context(), r.PathValue("id")); err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// derefOr returns *p when set, else def.
func derefOr(p *string, def string) string {
	if p != nil {
		return *p
	}
	return def
}
