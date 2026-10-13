package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"system/internal/assistant"
	"system/internal/session"
	"system/internal/store"
)

// --- DTOs (stable JSON shape for the frontend) ---

type projectDTO struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	Dir       string    `json:"dir"`
	IsSystem  bool      `json:"isSystem"`
	Protected bool      `json:"protected"`
	CreatedAt time.Time `json:"createdAt"`
	Starred   bool      `json:"starred"`
	Category  string    `json:"category"`
	Archived  bool      `json:"archived"`
	Comment   string    `json:"comment"`
	// ClaudeAccountID is the subscription this project's new sessions run under;
	// empty means it inherits the default account.
	ClaudeAccountID string `json:"claudeAccountId"`
	// Model is the Claude model this project's new sessions run on; empty means
	// it inherits the global runtime-settings model.
	Model string `json:"model"`
	// ShowAutoSessions: show this project's automatic bot sessions (WhatsApp, API
	// key) in the Dashboard and recent bar; hidden by default.
	ShowAutoSessions bool `json:"showAutoSessions"`
	// ContextResources are the shared Hub resources visible to this project's
	// sessions (Sandbox, enabled tool connectors, roadmap items), powering the
	// "context injected" indicator. Mirrors what the session system prompt is told.
	ContextResources []session.ContextResource `json:"contextResources"`
}

type sessionDTO struct {
	ID              string               `json:"id"`
	ProjectID       string               `json:"projectId"`
	State           string               `json:"state"`
	PermissionMode  string               `json:"permissionMode"`
	Autonomous      bool                 `json:"autonomous"`
	Attention       session.Attention    `json:"attention"`
	CreatedAt       time.Time            `json:"createdAt"`
	LastEventAt     *time.Time           `json:"lastEventAt,omitempty"`
	LastMessageAt   *time.Time           `json:"lastMessageAt,omitempty"`
	Comment         string               `json:"comment"`
	Branch          string               `json:"branch,omitempty"`
	PrURL           string               `json:"prUrl,omitempty"`
	ClaudeAccountID string               `json:"claudeAccountId,omitempty"`
	Model           string               `json:"model,omitempty"`
	Usage           session.SessionUsage `json:"usage"`
	Source          string               `json:"source,omitempty"`
	Archived        bool                 `json:"archived,omitempty"`
	// Unread is true when the session produced output the user has not looked at yet.
	Unread bool `json:"unread,omitempty"`
	// Asking: an ask_user question is waiting for the user's answer.
	Asking bool `json:"asking"`
	// Turn is the outcome of the latest turn: working (a turn is in progress, or
	// the session waits for a background task), done, or error. A live session
	// whose last turn is over is "running" (process alive) with turn "done".
	Turn string `json:"turn"`
	// Brief is the one-line brief (300 chars) of a session delegated by the
	// assistant (source "assistant-task").
	Brief string `json:"brief,omitempty"`
	// Summary is the short (3-8 words) summary of a delegated request, given by
	// the assistant (or derived from the brief); "" for older sessions.
	Summary string `json:"summary,omitempty"`
}

// journalMtime returns the mtime of the journal file, which is updated on every
// write — including after the last assistant turn — giving an accurate "last
// message" timestamp without parsing the JSONL content.
func journalMtime(path string) *time.Time {
	if path == "" {
		return nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	t := fi.ModTime().UTC()
	return &t
}

func (s *Server) toProjectDTO(p store.Project) projectDTO {
	return projectDTO{
		ID: p.ID, Name: p.Name, Slug: p.Slug, Dir: p.Dir,
		IsSystem: p.IsSystem, Protected: p.Protected, CreatedAt: p.CreatedAt,
		Starred: p.Starred, Category: p.Category, Archived: p.Archived, Comment: p.Comment,
		ClaudeAccountID:  p.ClaudeAccountID,
		Model:            p.Model,
		ShowAutoSessions: p.ShowAutoSessions,
		ContextResources: s.mgr.BuildProjectContext(p).Resources,
	}
}

func lastOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Now()
	}
	return *t
}

// toSessionDTO enriches the stored session with live, in-memory hand-back state
// from the manager (autonomy + attention), which is not persisted.
func (s *Server) toSessionDTO(sess store.Session) sessionDTO {
	last := journalMtime(sess.JournalPath)
	unread := false
	// Bot/gateway sessions (WhatsApp, API-key clients) are driven from outside the
	// Hub: their replies are never "unread" here.
	// Only a real assistant text message counts, not thinking or tool calls.
	if last != nil && sess.Source != "whatsapp" && sess.Source != "api" && sess.Source != "assistant" {
		if text := journalLastText(sess.JournalPath); text != nil {
			seen, ok := s.db.SessionSeenAt(sess.ID)
			unread = !ok || text.After(seen)
		}
	}
	live := sess.State == store.StateRunning || sess.State == store.StateStarting
	turn := assistant.TurnDone
	if sess.State == store.StateError {
		turn = assistant.TurnError
	}
	if live {
		turn = assistant.ReadTurn(sess.JournalPath, time.Since(lastOrZero(last))).Outcome
	}
	brief, summary := "", ""
	if sess.Source == assistant.SourceTask && s.assistant != nil {
		brief = s.assistant.Brief(sess.ID)
		summary = s.assistant.Summary(sess.ID)
		if summary == "" {
			summary = assistant.FallbackSummary(brief)
		}
	}
	return sessionDTO{
		Asking:          live && s.mgr.Asks().Pending(sess.ID),
		Turn:            turn,
		Brief:           brief,
		Summary:         summary,
		Unread:          unread,
		ID:              sess.ID,
		ProjectID:       sess.ProjectID,
		State:           sess.State,
		PermissionMode:  sess.PermissionMode,
		Autonomous:      s.mgr.IsAutonomous(sess.ID),
		Attention:       s.mgr.Attention(sess.ID),
		CreatedAt:       sess.CreatedAt,
		LastEventAt:     sess.LastEventAt,
		LastMessageAt:   journalMtime(sess.JournalPath),
		Comment:         sess.Comment,
		Branch:          s.mgr.PlanBranch(sess.ID),
		PrURL:           sess.PrURL,
		ClaudeAccountID: sess.ClaudeAccountID,
		Model:           sess.Model,
		Usage:           s.mgr.SessionUsage(sess.ID),
		Source:          sess.Source,
		Archived:        sess.Archived,
	}
}

// activeAccountID returns the active subscription's id, or "" when none is
// active. Used to scope the cross-project stats to the selected account.
func (s *Server) activeAccountID() string {
	if a, err := s.mgr.ActiveAccount(); err == nil {
		return a.ID
	}
	return ""
}

// listAllSessions returns every session across all projects AND all Claude
// subscriptions, enriched like the per-project list. Powers the observability
// dashboard: operational visibility (live sessions, hand-back attention) must be
// account-agnostic — a session running under a non-active subscription is still
// real work the user needs to see. Each DTO carries its claudeAccountId so the
// UI can tag which subscription it belongs to. (The plan-usage meter and history
// stay account-scoped; this list does not.)
func (s *Server) listAllSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.db.ListActiveSessions()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]sessionDTO, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, s.toSessionDTO(sess))
	}
	writeJSON(w, http.StatusOK, out)
}

// --- handlers ---

// listProjects returns the active board by default, or the archived projects when
// called with ?archived=true (powers the Archivage page).
func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	archived := r.URL.Query().Get("archived")
	lister := s.db.ListProjects
	if archived == "true" || archived == "1" {
		lister = s.db.ListArchivedProjects
	}
	projects, err := lister()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]projectDTO, 0, len(projects))
	for _, p := range projects {
		out = append(out, s.toProjectDTO(p))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Dir      string `json:"dir"`      // optional: empty derives <ProjectsDir>/<slug>
		Category string `json:"category"` // optional grouping label
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, errors.New("name is required"))
		return
	}
	p, err := s.mgr.CreateProject(r.Context(), req.Name, req.Dir, req.Category)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.toProjectDTO(p))
}

// updateProject sets user-editable project metadata (starred, category, comment).
// PATCH is partial: only the fields present in the body are changed, the rest
// keep their stored value — so toggling the star never wipes the comment, and
// editing the comment never wipes the category.
func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Starred          *bool   `json:"starred"`
		Category         *string `json:"category"`
		Comment          *string `json:"comment"`
		ClaudeAccountID  *string `json:"claudeAccountId"`
		Model            *string `json:"model"`
		ShowAutoSessions *bool   `json:"showAutoSessions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	id := r.PathValue("id")
	cur, err := s.db.GetProject(id)
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	starred, category, comment := cur.Starred, cur.Category, cur.Comment
	if req.Starred != nil {
		starred = *req.Starred
	}
	if req.Category != nil {
		category = *req.Category
	}
	if req.Comment != nil {
		comment = *req.Comment
	}
	if err := s.mgr.UpdateProjectMeta(id, starred, category, comment); err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	// The subscription assignment has its own setter (validates the account and
	// '' => inherit the default), kept out of UpdateProjectMeta.
	if req.ClaudeAccountID != nil {
		if err := s.mgr.SetProjectClaudeAccount(id, *req.ClaudeAccountID); err != nil {
			writeAccountErr(w, err)
			return
		}
	}
	// The per-project model override ('' => inherit the global default) has its own
	// setter, kept out of UpdateProjectMeta like the subscription assignment.
	if req.Model != nil {
		if err := s.mgr.SetProjectModel(id, *req.Model); err != nil {
			writeNotFoundOr500(w, err)
			return
		}
	}
	if req.ShowAutoSessions != nil {
		if err := s.db.SetProjectShowAutoSessions(id, *req.ShowAutoSessions); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// archiveProject soft-archives a project (reversible). Refuses a protected
// project with 409; otherwise stops its live sessions and hides it from the board.
func (s *Server) archiveProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Archiving puts the project to rest: stop its Docker containers (resumable
	// from the Environments page after a restore) before suspending it.
	if p, err := s.db.GetProject(id); err == nil {
		s.stopProjectContainers(r.Context(), p.Slug)
	}
	err := s.mgr.ArchiveProject(r.Context(), id)
	if errors.Is(err, session.ErrProjectProtected) {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// unarchiveProject restores an archived project to the active board.
func (s *Server) unarchiveProject(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.UnarchiveProject(r.PathValue("id")); err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.db.GetProject(r.PathValue("id"))
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.toProjectDTO(p))
}

// deleteProject permanently removes a project and all its sessions. Refuses a
// protected project or one with a live session with 409.
func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	err := s.mgr.DeleteProject(r.PathValue("id"))
	if errors.Is(err, session.ErrProjectProtected) || errors.Is(err, session.ErrSessionLive) {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("id")
	var sessions []store.Session
	var err error
	if r.URL.Query().Get("archived") == "true" {
		sessions, err = s.db.ListArchivedSessionsByProject(pid)
	} else {
		sessions, err = s.db.ListSessionsByProject(pid)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]sessionDTO, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, s.toSessionDTO(sess))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) launchSession(w http.ResponseWriter, r *http.Request) {
	// Body is optional: an empty body launches with the configured default mode
	// and the resolved default model; {"permissionMode":"plan"} starts an
	// autonomous session, {"model":"opus"} overrides the model for this session
	// only (empty => project default, then global default).
	var req struct {
		PermissionMode string `json:"permissionMode"`
		Model          string `json:"model"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	// Machine clients (API key, e.g. the ha-ia gateway) are tagged "api" so the
	// UI can keep their automatic sessions out of the recent-sessions views.
	source := ""
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer "+store.APIKeyPrefix) {
		source = "api"
	}
	sess, err := s.mgr.LaunchSession(r.Context(), r.PathValue("id"), req.PermissionMode, req.Model, "", source)
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.toSessionDTO(sess))
}

func (s *Server) decidePlan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Decision string `json:"decision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var approve bool
	switch req.Decision {
	case "approve":
		approve = true
	case "reject":
		approve = false
	default:
		writeErr(w, http.StatusBadRequest, errors.New(`decision must be "approve" or "reject"`))
		return
	}
	if err := s.mgr.DecidePlan(r.Context(), r.PathValue("id"), approve); err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// answerQuestion resolves a session's pending mcp__ask__ask_user question with
// the user's choice. It unblocks the ask-mcp call that is holding the session,
// which then returns the answer as the tool_result so claude resumes. Returns 409
// when no question is currently awaiting an answer (already answered, superseded,
// or the session moved on).
func (s *Server) answerQuestion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Answer string `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Answer == "" {
		writeErr(w, http.StatusBadRequest, errors.New("answer must not be empty"))
		return
	}
	if !s.mgr.Asks().Answer(r.PathValue("id"), req.Answer) {
		writeErr(w, http.StatusConflict, errors.New("no question is awaiting an answer for this session"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.db.GetSession(r.PathValue("id"))
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.toSessionDTO(sess))
}

// setSessionComment updates a session's free-text note (a human label). The note
// is stored in the DB index, not the journal, so it is editable for any session
// regardless of state.
func (s *Server) setSessionComment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Comment string `json:"comment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if _, err := s.db.GetSession(r.PathValue("id")); err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	if err := s.db.SetSessionComment(r.PathValue("id"), req.Comment); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// getJournal serves a session's recorded stream-json transcript, used to replay
// a finished session (stopped/error) that no longer has a live runner socket.
func (s *Server) getJournal(w http.ResponseWriter, r *http.Request) {
	data, err := s.mgr.Journal(r.PathValue("id"))
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Write(data)
}

// archiveSession soft-archives a session: hides it from the normal list while
// keeping its journal on disk. Refuses a live session with 409.
func (s *Server) archiveSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.db.GetSession(id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if sess.State == "running" || sess.State == "starting" {
		writeErr(w, http.StatusConflict, session.ErrSessionLive)
		return
	}
	if err := s.db.SetSessionArchived(id, true); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// unarchiveSession restores a session to the normal view.
func (s *Server) unarchiveSession(w http.ResponseWriter, r *http.Request) {
	if err := s.db.SetSessionArchived(r.PathValue("id"), false); err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteSession permanently removes a finished session and its journal. Refuses
// a live session with 409 — it must be stopped first.
func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	err := s.mgr.DeleteSession(r.PathValue("id"))
	if errors.Is(err, session.ErrSessionLive) {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) stopSession(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.StopSession(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeNotFoundOr500(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeErr(w, http.StatusInternalServerError, err)
}
