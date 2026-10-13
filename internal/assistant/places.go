package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"system/internal/store"
	"system/internal/tools"
)

const placeEvery = 30 * time.Second

// Person returns the Home Assistant entity whose zone is tracked ("" = none).
func (s *Service) Person() string {
	v, _ := s.db.GetAppMeta(personMetaKey)
	return v
}

// SetPerson chooses the tracked entity and forgets the previous baseline.
func (s *Service) SetPerson(entity string) error {
	s.mu.Lock()
	s.haState = ""
	s.mu.Unlock()
	return s.db.SetAppMeta(personMetaKey, strings.TrimSpace(entity))
}

// callTool runs a connector action on the first enabled tool of that type.
func (s *Service) callTool(ctx context.Context, typ, action string, args map[string]any) (string, error) {
	all, err := s.db.ListTools()
	if err != nil {
		return "", err
	}
	conn, ok := tools.Get(typ)
	if !ok {
		return "", fmt.Errorf("no %s connector", typ)
	}
	for _, t := range all {
		if t.Type != typ || !t.Enabled {
			continue
		}
		b, err := s.box.Decrypt(t.ConfigEnc)
		if err != nil {
			return "", err
		}
		cfg := map[string]string{}
		_ = json.Unmarshal(b, &cfg)
		cfg["_tool_id"] = t.ID
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		return conn.Call(cctx, cfg, action, args)
	}
	return "", fmt.Errorf("no enabled %s tool", typ)
}

// Entity is a Home Assistant entity offered in the place settings.
type Entity struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Name  string `json:"name"`
}

// Entities lists the Home Assistant entities of one domain (person, zone).
func (s *Service) Entities(ctx context.Context, domain string) ([]Entity, error) {
	out, err := s.callTool(ctx, "homeassistant", "ha_list_entities", map[string]any{"domain": domain})
	if err != nil {
		return nil, err
	}
	list := []Entity{}
	for _, line := range strings.Split(out, "\n") {
		if e, ok := parseEntityLine(line); ok {
			list = append(list, e)
		}
	}
	return list, nil
}

// parseEntityLine reads "- person.x = home (Name)" or "person.x = home (Name)".
func parseEntityLine(line string) (Entity, bool) {
	line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
	id, rest, ok := strings.Cut(line, " = ")
	if !ok || !strings.Contains(id, ".") {
		return Entity{}, false
	}
	e := Entity{ID: id, State: rest}
	if i := strings.LastIndex(rest, " ("); i >= 0 && strings.HasSuffix(rest, ")") {
		e.State, e.Name = rest[:i], rest[i+2:len(rest)-1]
	}
	return e, true
}

// placeLoop watches the tracked person's zone and fires matching triggers.
func (s *Service) placeLoop(ctx context.Context) {
	t := time.NewTicker(placeEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.checkPlace(ctx)
		}
	}
}

func (s *Service) checkPlace(ctx context.Context) {
	person := s.Person()
	if person == "" {
		return
	}
	out, err := s.callTool(ctx, "homeassistant", "ha_get_state", map[string]any{"entity_id": person})
	if err != nil {
		return
	}
	first, _, _ := strings.Cut(out, "\n")
	e, ok := parseEntityLine(first)
	if !ok {
		return
	}
	now := strings.ToLower(strings.TrimSpace(e.State))
	if now == "" || now == "unknown" || now == "unavailable" {
		return // keep the last known place
	}
	s.mu.Lock()
	prev := s.haState
	s.haState = now
	s.mu.Unlock()
	if prev == "" || prev == now {
		return // first reading is the baseline, not an event
	}
	triggers, err := s.db.ListAssistantTriggers()
	if err != nil {
		return
	}
	for _, tr := range triggers {
		if !tr.Enabled {
			continue
		}
		place := strings.ToLower(strings.TrimSpace(tr.Place))
		if (tr.Event == "enter" && now == place) || (tr.Event == "leave" && prev == place) {
			s.fire(ctx, tr)
		}
	}
}

func (s *Service) fire(ctx context.Context, tr store.AssistantTrigger) {
	log.Printf("assistant: trigger %q fired", tr.Name)
	_ = s.db.MarkAssistantTriggerFired(tr.ID)
	label := "Déclencheur : " + tr.Name
	if err := s.Send(ctx, "place", "", "", label, tr.Prompt, nil); err != nil {
		log.Printf("assistant: trigger %q: %v", tr.Name, err)
	}
}

// FireTrigger runs a trigger now (the "test" button).
func (s *Service) FireTrigger(ctx context.Context, id string) error {
	all, err := s.db.ListAssistantTriggers()
	if err != nil {
		return err
	}
	for _, tr := range all {
		if tr.ID == id {
			s.fire(ctx, tr)
			return nil
		}
	}
	return fmt.Errorf("unknown trigger")
}

// FireTask runs a scheduled task's prompt through the assistant.
func (s *Service) FireTask(ctx context.Context, name, prompt string) error {
	return s.Send(ctx, "task", "", "", "Tâche : "+name, prompt, nil)
}
