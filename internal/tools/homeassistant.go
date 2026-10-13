package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

func init() { register(homeassistant{}) }

// homeassistant talks to a Home Assistant instance over its REST API using a
// Long-Lived Access Token (Bearer). It exposes read (list/get state) and write
// (call service / turn on-off) actions so a session can supervise and drive the
// home. HA on a LAN is commonly plain HTTP; when fronted by TLS with a self-signed
// cert the verify_tls opt-out mirrors the Proxmox connector.
type homeassistant struct{}

func (homeassistant) Type() string  { return "homeassistant" }
func (homeassistant) Label() string { return "Home Assistant" }

func (homeassistant) Fields() []Field {
	return []Field{
		{Key: "base_url", Label: "URL de l'instance", Type: FieldURL, Required: true,
			Placeholder: "http://homeassistant.local:8123"},
		{Key: "token", Label: "Long-Lived Access Token", Type: FieldPassword, Secret: true, Required: true},
		{Key: "verify_tls", Label: "Vérifier le certificat TLS", Type: FieldBool},
	}
}

func (homeassistant) Test(ctx context.Context, cfg map[string]string) error {
	if err := missing(cfg, "base_url", "token"); err != nil {
		return err
	}
	// GET /api/ returns {"message":"API running."} on a healthy, authenticated call.
	var res struct {
		Message string `json:"message"`
	}
	if err := haDo(ctx, cfg, http.MethodGet, "/api/", nil, &res); err != nil {
		return err
	}
	if !strings.Contains(res.Message, "running") {
		return fmt.Errorf("unexpected API response: %q", res.Message)
	}
	return nil
}

func (homeassistant) Tools() []ToolSpec {
	entity := ArgSpec{Name: "entity_id", Type: "string", Description: "Entity id, e.g. light.kitchen", Required: true}
	return []ToolSpec{
		{Name: "ha_list_entities", Description: "List Home Assistant entities with their state and friendly name. Optional domain filter (e.g. light, switch, sensor, climate, cover).",
			Args: []ArgSpec{{Name: "domain", Type: "string", Description: "Filter to one domain, e.g. 'light'. Omit for all entities."}}},
		{Name: "ha_get_state", Description: "Get the current state and attributes of one entity.", Args: []ArgSpec{entity}},
		{Name: "ha_call_service", Description: "Call a Home Assistant service, e.g. domain=light service=turn_on entity_id=light.kitchen. Use data_json for extra parameters.",
			Args: []ArgSpec{
				{Name: "domain", Type: "string", Description: "Service domain, e.g. 'light', 'climate', 'scene'.", Required: true},
				{Name: "service", Type: "string", Description: "Service name, e.g. 'turn_on', 'set_temperature', 'toggle'.", Required: true},
				{Name: "entity_id", Type: "string", Description: "Target entity id (optional for services that don't take one)."},
				{Name: "data_json", Type: "string", Description: `Optional extra service data as a JSON object, e.g. {"brightness_pct":40}.`},
			}},
		{Name: "ha_turn_on", Description: "Turn an entity on (works across light/switch/etc via homeassistant.turn_on).", Args: []ArgSpec{entity}},
		{Name: "ha_turn_off", Description: "Turn an entity off (via homeassistant.turn_off).", Args: []ArgSpec{entity}},
	}
}

func (homeassistant) Call(ctx context.Context, cfg map[string]string, action string, args map[string]any) (string, error) {
	switch action {
	case "ha_list_entities":
		return haListEntities(ctx, cfg, argString(args, "domain", ""))
	case "ha_get_state":
		id := argString(args, "entity_id", "")
		if id == "" {
			return "", fmt.Errorf("entity_id is required")
		}
		return haGetState(ctx, cfg, id)
	case "ha_call_service":
		domain := argString(args, "domain", "")
		service := argString(args, "service", "")
		if domain == "" || service == "" {
			return "", fmt.Errorf("domain and service are required")
		}
		body := map[string]any{}
		if raw := argString(args, "data_json", ""); raw != "" {
			if err := json.Unmarshal([]byte(raw), &body); err != nil {
				return "", fmt.Errorf("data_json is not a valid JSON object: %w", err)
			}
		}
		if id := argString(args, "entity_id", ""); id != "" {
			body["entity_id"] = id
		}
		return haCallService(ctx, cfg, domain, service, body)
	case "ha_turn_on", "ha_turn_off":
		id := argString(args, "entity_id", "")
		if id == "" {
			return "", fmt.Errorf("entity_id is required")
		}
		service := "turn_on"
		if action == "ha_turn_off" {
			service = "turn_off"
		}
		return haCallService(ctx, cfg, "homeassistant", service, map[string]any{"entity_id": id})
	default:
		return "", fmt.Errorf("unknown action %q", action)
	}
}

// haEntity is one entry of GET /api/states.
type haEntity struct {
	EntityID   string         `json:"entity_id"`
	State      string         `json:"state"`
	Attributes map[string]any `json:"attributes"`
}

func (e haEntity) friendly() string {
	if n, ok := e.Attributes["friendly_name"].(string); ok && n != "" {
		return n
	}
	return e.EntityID
}

func haListEntities(ctx context.Context, cfg map[string]string, domain string) (string, error) {
	var states []haEntity
	if err := haDo(ctx, cfg, http.MethodGet, "/api/states", nil, &states); err != nil {
		return "", err
	}
	prefix := ""
	if domain != "" {
		prefix = domain + "."
	}
	sort.Slice(states, func(i, j int) bool { return states[i].EntityID < states[j].EntityID })
	var b strings.Builder
	n := 0
	for _, e := range states {
		if prefix != "" && !strings.HasPrefix(e.EntityID, prefix) {
			continue
		}
		fmt.Fprintf(&b, "- %s = %s (%s)\n", e.EntityID, e.State, e.friendly())
		n++
	}
	if n == 0 {
		if domain != "" {
			return fmt.Sprintf("No entities in domain %q.", domain), nil
		}
		return "No entities found.", nil
	}
	return strings.TrimSpace(b.String()), nil
}

func haGetState(ctx context.Context, cfg map[string]string, entityID string) (string, error) {
	var e haEntity
	if err := haDo(ctx, cfg, http.MethodGet, "/api/states/"+entityID, nil, &e); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s = %s (%s)\n", e.EntityID, e.State, e.friendly())
	keys := make([]string, 0, len(e.Attributes))
	for k := range e.Attributes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "  %s: %v\n", k, e.Attributes[k])
	}
	return strings.TrimSpace(b.String()), nil
}

func haCallService(ctx context.Context, cfg map[string]string, domain, service string, body map[string]any) (string, error) {
	// POST /api/services/<domain>/<service> returns the states it changed.
	var changed []haEntity
	if err := haDo(ctx, cfg, http.MethodPost, "/api/services/"+domain+"/"+service, body, &changed); err != nil {
		return "", err
	}
	if len(changed) == 0 {
		return fmt.Sprintf("Called %s.%s (no state change reported).", domain, service), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Called %s.%s — %d entit(y/ies) changed:\n", domain, service, len(changed))
	for _, e := range changed {
		fmt.Fprintf(&b, "- %s = %s\n", e.EntityID, e.State)
	}
	return strings.TrimSpace(b.String()), nil
}

func haDo(ctx context.Context, cfg map[string]string, method, path string, in any, out any) error {
	base := strings.TrimRight(cfg["base_url"], "/")
	var bodyReader *bytes.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(raw)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg["token"])
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient(cfg["verify_tls"] != "true").Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("home assistant %s %s: %s", method, path, resp.Status)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
