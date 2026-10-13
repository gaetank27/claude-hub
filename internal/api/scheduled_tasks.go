package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"system/internal/scheduler"
	"system/internal/store"
)

type taskDTO struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	ProjectID      string     `json:"projectId"`
	Prompt         string     `json:"prompt"`
	Schedule       string     `json:"schedule"`
	Enabled        bool       `json:"enabled"`
	PermissionMode string     `json:"permissionMode"`
	Model          string     `json:"model"`
	LastRunAt      *time.Time `json:"lastRunAt,omitempty"`
	NextRunAt      time.Time  `json:"nextRunAt"`
	LastSessionID  string     `json:"lastSessionId,omitempty"`
	OneShot        bool       `json:"oneShot"`
	CreatedAt      time.Time  `json:"createdAt"`
	Command        string     `json:"command"`
	LastStatus     string     `json:"lastStatus,omitempty"`
	LastOutput     string     `json:"lastOutput,omitempty"`
}

func toTaskDTO(t store.ScheduledTask) taskDTO {
	return taskDTO{
		ID: t.ID, Name: t.Name, ProjectID: t.ProjectID, Prompt: t.Prompt,
		Schedule: t.Schedule, Enabled: t.Enabled, PermissionMode: t.PermissionMode,
		Model: t.Model, LastRunAt: t.LastRunAt, NextRunAt: t.NextRunAt,
		LastSessionID: t.LastSessionID, OneShot: t.OneShot, CreatedAt: t.CreatedAt,
		Command: t.Command, LastStatus: t.LastStatus, LastOutput: t.LastOutput,
	}
}

type taskInput struct {
	Name           string `json:"name"`
	ProjectID      string `json:"projectId"`
	Prompt         string `json:"prompt"`
	Schedule       string `json:"schedule"`
	Enabled        bool   `json:"enabled"`
	PermissionMode string `json:"permissionMode"`
	Model          string `json:"model"`
	OneShot        bool   `json:"oneShot"`
	// Command makes this a script task: the shell command is run on schedule
	// instead of launching a Claude session (prompt is then ignored).
	Command string `json:"command"`
	// LaunchAt overrides next_run_at for one-shot tasks (ISO 8601). When set and
	// oneShot is true, schedule may be omitted (a dummy cron is generated).
	LaunchAt string `json:"launchAt"`
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("project")
	var tasks []store.ScheduledTask
	var err error
	if projectID != "" {
		tasks, err = s.db.ListScheduledTasksByProject(projectID)
	} else {
		tasks, err = s.db.ListScheduledTasks()
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]taskDTO, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, toTaskDTO(t))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var req taskInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, errors.New("name is required"))
		return
	}
	// One-shot tasks launched via launchAt don't need a prompt (just start a session).
	// Regular recurring tasks require a cron schedule and a prompt (or a command
	// for script tasks).
	if !req.OneShot && ((req.Prompt == "" && req.Command == "") || req.Schedule == "") {
		writeErr(w, http.StatusBadRequest, errors.New("schedule and prompt (or command) are required for recurring tasks"))
		return
	}

	mode := req.PermissionMode
	if mode == "" {
		mode = "default"
	}

	var next time.Time
	schedule := req.Schedule

	if req.OneShot && req.LaunchAt != "" {
		at, err := time.Parse(time.RFC3339, req.LaunchAt)
		if err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid launchAt: %w", err))
			return
		}
		next = at.UTC()
		// Generate a cron that matches the launch time exactly. The task will be
		// disabled by the scheduler after the first fire anyway (one_shot = true).
		schedule = fmt.Sprintf("%d %d %d %d *", at.UTC().Minute(), at.UTC().Hour(), at.UTC().Day(), int(at.UTC().Month()))
	} else {
		if err := scheduler.Validate(schedule); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		next, _ = scheduler.Next(schedule, time.Now().UTC())
	}

	t := store.ScheduledTask{
		ID:             uuid.NewString(),
		Name:           req.Name,
		ProjectID:      req.ProjectID,
		Prompt:         req.Prompt,
		Command:        req.Command,
		Schedule:       schedule,
		Enabled:        req.Enabled,
		PermissionMode: mode,
		Model:          req.Model,
		OneShot:        req.OneShot,
		NextRunAt:      next,
		CreatedAt:      time.Now().UTC(),
	}
	if err := s.db.CreateScheduledTask(t); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, toTaskDTO(t))
}

func (s *Server) updateTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := s.db.GetScheduledTask(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	var req taskInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Schedule != existing.Schedule {
		if err := scheduler.Validate(req.Schedule); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		existing.NextRunAt, _ = scheduler.Next(req.Schedule, time.Now().UTC())
	}
	existing.Name = req.Name
	existing.ProjectID = req.ProjectID
	existing.Prompt = req.Prompt
	existing.Command = req.Command
	existing.Schedule = req.Schedule
	existing.Enabled = req.Enabled
	existing.PermissionMode = req.PermissionMode
	existing.Model = req.Model
	existing.OneShot = req.OneShot
	if err := s.db.UpdateScheduledTask(existing); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, toTaskDTO(existing))
}

func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request) {
	if err := s.db.DeleteScheduledTask(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// runTaskNow fires a scheduled task immediately (outside its schedule). It
// launches a session and injects the prompt, just like the scheduler does.
func (s *Server) runTaskNow(w http.ResponseWriter, r *http.Request) {
	t, err := s.db.GetScheduledTask(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if t.Command != "" {
		now := time.Now().UTC()
		next, _ := scheduler.Next(t.Schedule, now)
		_ = s.db.SetTaskLastRun(t.ID, "", now, next)
		go scheduler.RunCommand(s.db, t)
		writeJSON(w, http.StatusOK, map[string]string{"sessionId": ""})
		return
	}
	if scheduler.TryAssistant(r.Context(), t) {
		now := time.Now().UTC()
		next, _ := scheduler.Next(t.Schedule, now)
		_ = s.db.SetTaskLastRun(t.ID, "", now, next)
		writeJSON(w, http.StatusOK, map[string]string{"sessionId": ""})
		return
	}
	projectID := t.ProjectID
	if projectID == "" {
		projectID = "system"
	}
	mode := t.PermissionMode
	if mode == "" {
		mode = "default"
	}
	sessionName := fmt.Sprintf("%s · %s", t.Name, time.Now().Format("02/01 15:04"))
	sess, err := s.mgr.LaunchSession(r.Context(), projectID, mode, t.Model, sessionName, "scheduler")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.mgr.SendUserMessage(sess.ID, t.Prompt); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	now := time.Now().UTC()
	next, _ := scheduler.Next(t.Schedule, now)
	_ = s.db.SetTaskLastRun(t.ID, sess.ID, now, next)
	writeJSON(w, http.StatusOK, map[string]string{"sessionId": sess.ID})
}
