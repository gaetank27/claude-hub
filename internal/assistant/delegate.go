package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"time"

	"system/internal/store"
)

// ProjectInfo is a project the assistant may delegate to.
type ProjectInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListProjects returns the projects work can be delegated to (everything but the
// assistant's own project and archived ones).
func (s *Service) ListProjects() ([]ProjectInfo, error) {
	all, err := s.db.ListProjects()
	if err != nil {
		return nil, err
	}
	own := s.ProjectID()
	out := []ProjectInfo{}
	for _, p := range all {
		if p.ID == own || p.Archived || p.Hidden {
			continue
		}
		out = append(out, ProjectInfo{ID: p.ID, Name: p.Name})
	}
	return out, nil
}

// StartTask launches a session in another project and gives it the brief.
func (s *Service) StartTask(ctx context.Context, project, brief, summary string) (string, error) {
	if strings.TrimSpace(brief) == "" {
		return "", fmt.Errorf("brief is required")
	}
	projects, err := s.ListProjects()
	if err != nil {
		return "", err
	}
	var target string
	for _, p := range projects {
		if p.ID == project || strings.EqualFold(p.Name, project) {
			target = p.ID
			break
		}
	}
	if target == "" {
		return "", fmt.Errorf("unknown project %q (see hub_list_projects)", project)
	}
	summary = CleanSummary(summary)
	if summary == "" {
		summary = FallbackSummary(brief)
	}
	sess, err := s.hub.LaunchSession(ctx, target, "", "", "Assistant · "+oneLine(brief, 40), SourceTask)
	if err != nil {
		return "", err
	}
	route, jid := s.currentRoute()
	s.rememberDelegation(sess.ID, oneLine(brief, 300), summary)
	s.watchAdd(sess.ID, watched{Project: projectName(projects, target), Brief: oneLine(brief, 300), Summary: summary, Route: route, JID: jid, Started: time.Now().UnixMilli()})
	if err := s.hub.SendUserMessage(sess.ID, brief); err != nil {
		s.watchRemove(sess.ID)
		return sess.ID, fmt.Errorf("session %s started but the brief was not delivered: %w", sess.ID, err)
	}
	return sess.ID, nil
}

func projectName(ps []ProjectInfo, id string) string {
	for _, p := range ps {
		if p.ID == id {
			return p.Name
		}
	}
	return id
}

// TaskResult reports a delegated session's state and latest answer. state is
// working (a turn is in progress, or the session waits for a background task),
// idle (the last turn is over: the process is alive but the report is ready),
// stopped or error; finished is true unless the session is working.
func (s *Service) TaskResult(sessionID string) (state, text string, finished bool, err error) {
	sess, err := s.db.GetSession(sessionID)
	if err != nil {
		return "", "", false, fmt.Errorf("unknown session %q", sessionID)
	}
	turn := ReadTurn(sess.JournalPath, journalIdle(sess.JournalPath))
	return TaskState(sess.State, turn), turn.Text, turn.Finished(), nil
}

// TaskState folds the stored process state and the journal's turn into the one
// state the assistant reads: a live process that finished its turn is idle, not
// "running" forever.
func TaskState(procState string, t Turn) string {
	switch procState {
	case store.StateRunning, store.StateStarting:
		if t.Finished() {
			return "idle"
		}
		return "working"
	}
	return procState
}

// StopTask closes a delegated session and archives it. Only sessions the
// assistant started can be closed this way.
func (s *Service) StopTask(ctx context.Context, sessionID string) error {
	sess, err := s.db.GetSession(sessionID)
	if err != nil {
		return fmt.Errorf("unknown session %q", sessionID)
	}
	if sess.Source != SourceTask {
		return fmt.Errorf("session %s was not started by the assistant", sessionID)
	}
	s.watchRemove(sessionID) // closed by the assistant: no wake-up
	if sess.State == store.StateRunning || sess.State == store.StateStarting {
		if err := s.hub.StopSession(ctx, sessionID); err != nil {
			return err
		}
	}
	return s.db.SetSessionArchived(sessionID, true)
}

// ListProjectsJSON is ListProjects serialized for the hub connector.
func (s *Service) ListProjectsJSON() (string, error) {
	p, err := s.ListProjects()
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(p)
	return string(b), err
}

const briefsMetaKey = "assistant_briefs"

// briefKeep is how long the one-line brief of a delegation stays readable (the
// registry entry is gone once the assistant took the wake-up).
const briefKeep = 30 * 24 * time.Hour

type briefEntry struct {
	Brief   string `json:"b"`
	Summary string `json:"s,omitempty"`
	At      int64  `json:"t"`
}

// rememberBrief keeps a delegation's one-line brief for the session API, past the
// life of its registry entry; old ones are dropped.
func (s *Service) rememberBrief(id, brief string) { s.rememberDelegation(id, brief, "") }

// rememberDelegation is rememberBrief plus the short summary given by the assistant.
func (s *Service) rememberDelegation(id, brief, summary string) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	m := map[string]briefEntry{}
	raw, _ := s.db.GetAppMeta(briefsMetaKey)
	_ = json.Unmarshal([]byte(raw), &m)
	cut := time.Now().Add(-briefKeep).UnixMilli()
	for k, e := range m {
		if e.At < cut {
			delete(m, k)
		}
	}
	m[id] = briefEntry{brief, summary, time.Now().UnixMilli()}
	b, _ := json.Marshal(m)
	_ = s.db.SetAppMeta(briefsMetaKey, string(b))
}

// Brief is the one-line brief (300 runes max) a delegated session was started
// with, "" when unknown (not delegated, or older than briefKeep).
func (s *Service) Brief(id string) string {
	raw, _ := s.db.GetAppMeta(briefsMetaKey)
	if raw == "" {
		return ""
	}
	m := map[string]briefEntry{}
	if json.Unmarshal([]byte(raw), &m) != nil {
		return ""
	}
	if e, ok := m[id]; ok {
		return e.Brief
	}
	// Delegations started before the brief store: the registry still has them.
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.loadWatch()[id].Brief
}

const summaryMax = 60

// CleanSummary normalises a summary given by the assistant: plain text on one
// line, no control characters, cut at a word boundary to summaryMax runes.
func CleanSummary(t string) string {
	t = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, t)
	t = strings.Join(strings.Fields(t), " ")
	r := []rune(t)
	if len(r) <= summaryMax {
		return t
	}
	cut := string(r[:summaryMax])
	if i := strings.LastIndex(cut, " "); i > summaryMax/2 && r[summaryMax] != ' ' {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:.-–—") + "…"
}

// FallbackSummary derives a summary from the brief: first sentence, without the
// "Demande de l'utilisateur … :" preamble.
func FallbackSummary(brief string) string {
	t := strings.Join(strings.Fields(brief), " ")
	if m := summaryPreamble.FindString(t); m != "" {
		t = strings.TrimSpace(t[len(m):])
	}
	if i := strings.IndexAny(t, ".!?\n"); i > 0 {
		t = t[:i]
	}
	return CleanSummary(t)
}

var summaryPreamble = regexp.MustCompile(`(?i)^demande (de l['’]utilisateur|d['’][^:]{1,60}|de [^:]{1,60})[^:]{0,80}:\s*`)

// Summary is the short summary of a delegated session, "" when unknown.
func (s *Service) Summary(id string) string {
	raw, _ := s.db.GetAppMeta(briefsMetaKey)
	m := map[string]briefEntry{}
	if raw != "" && json.Unmarshal([]byte(raw), &m) == nil {
		if e, ok := m[id]; ok && e.Summary != "" {
			return e.Summary
		}
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.loadWatch()[id].Summary
}
