package assistant

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"system/internal/scheduler"
	"system/internal/store"
)

// localCron turns a local "HH:MM" and a day set (all | week | weekend) into the
// UTC 5-field cron the scheduler runs on.
func localCron(hhmm, days string, now time.Time) (string, error) {
	t, err := time.ParseInLocation("15:04", hhmm, time.Local)
	if err != nil {
		return "", fmt.Errorf("time must be HH:MM, got %q", hhmm)
	}
	local := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, time.Local)
	u := local.UTC()
	shift := int(time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC).
		Sub(time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)).Hours() / 24)
	var base []int
	switch strings.ToLower(days) {
	case "", "all":
		return fmt.Sprintf("%d %d * * *", u.Minute(), u.Hour()), nil
	case "week":
		base = []int{1, 2, 3, 4, 5}
	case "weekend":
		base = []int{0, 6}
	default:
		return "", fmt.Errorf("days must be all, week or weekend, got %q", days)
	}
	shifted := make([]int, len(base))
	for i, d := range base {
		shifted[i] = ((d+shift)%7 + 7) % 7
	}
	sort.Ints(shifted)
	dow := make([]string, len(shifted))
	for i, d := range shifted {
		dow[i] = fmt.Sprint(d)
	}
	return fmt.Sprintf("%d %d * * %s", u.Minute(), u.Hour(), strings.Join(dow, ",")), nil
}

// AddSchedule creates a task that runs prompt through the assistant. at is a
// local "HH:MM" (recurring, on days) or a local date-time "YYYY-MM-DD HH:MM"
// (one-shot reminder).
func (s *Service) AddSchedule(name, prompt, at, days string) (string, error) {
	pid := s.ProjectID()
	if pid == "" {
		return "", fmt.Errorf("assistant not ready")
	}
	name, prompt = strings.TrimSpace(name), strings.TrimSpace(prompt)
	if name == "" || prompt == "" {
		return "", fmt.Errorf("name and prompt are required")
	}
	t := store.ScheduledTask{
		ID: uuid.NewString(), Name: name, ProjectID: pid, Prompt: prompt, Enabled: true,
		PermissionMode: "bypassPermissions", CreatedAt: time.Now().UTC(),
	}
	if when, err := time.ParseInLocation("2006-01-02 15:04", strings.TrimSpace(at), time.Local); err == nil {
		if !when.After(time.Now()) {
			return "", fmt.Errorf("%s is in the past", at)
		}
		u := when.UTC()
		t.OneShot, t.NextRunAt = true, u
		t.Schedule = fmt.Sprintf("%d %d %d %d *", u.Minute(), u.Hour(), u.Day(), int(u.Month()))
	} else {
		cron, err := localCron(strings.TrimSpace(at), days, time.Now())
		if err != nil {
			return "", err
		}
		next, err := scheduler.Next(cron, time.Now().UTC())
		if err != nil {
			return "", err
		}
		t.Schedule, t.NextRunAt = cron, next
	}
	if err := s.db.CreateScheduledTask(t); err != nil {
		return "", err
	}
	return fmt.Sprintf("id: %s — next run %s", t.ID, t.NextRunAt.In(time.Local).Format("2006-01-02 15:04 MST")), nil
}

// AddPlace creates a place trigger.
func (s *Service) AddPlace(name, place, event, prompt string) (string, error) {
	if event != "enter" && event != "leave" {
		return "", fmt.Errorf("event must be enter or leave")
	}
	tr := store.AssistantTrigger{
		ID: uuid.NewString(), Name: strings.TrimSpace(name), Place: strings.TrimSpace(place),
		Event: event, Prompt: strings.TrimSpace(prompt), Enabled: true, CreatedAt: time.Now().UnixMilli(),
	}
	if tr.Name == "" || tr.Place == "" || tr.Prompt == "" {
		return "", fmt.Errorf("name, place and prompt are required")
	}
	if err := s.db.CreateAssistantTrigger(tr); err != nil {
		return "", err
	}
	note := ""
	if s.Person() == "" {
		note = " — warning: no tracked person is set yet (Assistant page > Automations), it will not fire"
	}
	return "id: " + tr.ID + note, nil
}

type automation struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"` // schedule | place | message
	Name    string `json:"name"`
	When    string `json:"when"`
	Prompt  string `json:"prompt"`
	Enabled bool   `json:"enabled"`
}

// ListAutomations returns the assistant's tasks, place and message triggers as JSON.
func (s *Service) ListAutomations() (string, error) {
	out := []automation{}
	if pid := s.ProjectID(); pid != "" {
		tasks, err := s.db.ListScheduledTasksByProject(pid)
		if err != nil {
			return "", err
		}
		for _, t := range tasks {
			when := t.Schedule + " (UTC cron)"
			if t.OneShot {
				when = "once, " + t.NextRunAt.In(time.Local).Format("2006-01-02 15:04 MST")
			}
			out = append(out, automation{t.ID, "schedule", t.Name, when, t.Prompt, t.Enabled})
		}
	}
	trs, err := s.db.ListAssistantTriggers()
	if err != nil {
		return "", err
	}
	for _, tr := range trs {
		if tr.Event == eventMessage {
			from := tr.Place
			if from == "" {
				from = "anyone"
			}
			out = append(out, automation{tr.ID, "message", tr.Name, "WhatsApp message received from " + from, tr.Prompt, tr.Enabled})
			continue
		}
		verb := map[string]string{"enter": "arrive at", "leave": "leave"}[tr.Event]
		out = append(out, automation{tr.ID, "place", tr.Name, verb + " " + tr.Place, tr.Prompt, tr.Enabled})
	}
	b, err := json.Marshal(out)
	return string(b), err
}

// RemoveAutomation deletes a task or a place/message trigger by id.
func (s *Service) RemoveAutomation(id string) error {
	if t, err := s.db.GetScheduledTask(id); err == nil {
		if t.ProjectID != s.ProjectID() {
			return fmt.Errorf("%s is not one of the assistant's tasks", id)
		}
		return s.db.DeleteScheduledTask(id)
	}
	trs, err := s.db.ListAssistantTriggers()
	if err != nil {
		return err
	}
	for _, tr := range trs {
		if tr.ID == id {
			return s.db.DeleteAssistantTrigger(id)
		}
	}
	return fmt.Errorf("unknown automation %q", id)
}
