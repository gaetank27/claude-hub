package mcpremote

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Class is how the remote endpoint treats a tool.
type Class string

const (
	ClassRead      Class = "read"      // read-only
	ClassSafe      Class = "safe"      // harmless action (lights)
	ClassSensitive Class = "sensitive" // writes / sends / delegates: off by default, confirmation required
	ClassBlocked   Class = "blocked"   // infrastructure administration: never exposed remotely
)

type entry struct {
	class     Class
	defaultOn bool
}

func ro(on bool) entry { return entry{ClassRead, on} }

// catalog classifies every known tool. A tool NOT listed here (a connector added
// later) is treated as sensitive and off: the endpoint fails closed.
var catalog = map[string]entry{
	// Read-only, on by default.
	"imap_list_mailboxes": ro(true), "imap_list_messages": ro(true), "imap_search_messages": ro(true), "imap_get_message": ro(true),
	"proton_list_calendars": ro(true), "proton_list_events": ro(true),
	"ha_list_entities": ro(true), "ha_get_state": ro(true),
	"whatsapp_get_chats": ro(true), "whatsapp_get_messages": ro(true),
	"assistant_list_automations": ro(true), "hub_list_projects": ro(true), "hub_session_result": ro(true),
	"script_status_list": ro(true), "script_status_get": ro(true), "script_command_list": ro(true),
	"proxmox_backup_report": ro(true),
	// Read-only infrastructure listings: available but off until enabled.
	"proxmox_list_vms": ro(false), "proxmox_list_snapshots": ro(false), "proxmox_list_storages": ro(false),
	"synology_info": ro(false), "synology_list_volumes": ro(false), "synology_list_shares": ro(false), "synology_list_backup_tasks": ro(false),
	"npm_list_proxy_hosts": ro(false), "npm_list_certificates": ro(false), "npm_list_access_lists": ro(false),
	"infomaniak_list_dns_records": ro(false),
	"cloudflare_list_zones": ro(false), "cloudflare_list_dns_records": ro(false), "cloudflare_list_tunnels": ro(false),
	"cloudflare_get_tunnel_routes": ro(false), "cloudflare_list_access_apps": ro(false), "cloudflare_list_access_idps": ro(false),
	"cloudflare_get_zone_status": ro(false), "cloudflare_get_dnssec": ro(false), "github_list_repos": ro(false), "telegram_get_updates": ro(false),
	// Harmless actions, on by default (lights only, enforced in validate).
	"ha_turn_on": {ClassSafe, true}, "ha_turn_off": {ClassSafe, true},
	// Sensitive: writes, sends, delegation. Off by default.
	"email_send": {ClassSensitive, false}, "whatsapp_send_message": {ClassSensitive, false}, "whatsapp_send_image": {ClassSensitive, false},
	"assistant_send_image": {ClassSensitive, false}, "telegram_send_message": {ClassSensitive, false}, "telegram_send_photo": {ClassSensitive, false},
	"proton_create_event": {ClassSensitive, false}, "proton_update_event": {ClassSensitive, false}, "proton_delete_event": {ClassSensitive, false},
	"ha_call_service": {ClassSensitive, false}, "script_command_send": {ClassSensitive, false}, "script_command_cancel": {ClassSensitive, false},
	"hub_start_session": {ClassSensitive, false}, "hub_stop_session": {ClassSensitive, false},
	"assistant_add_schedule": {ClassSensitive, false}, "assistant_add_place_trigger": {ClassSensitive, false},
	"assistant_add_message_trigger": {ClassSensitive, false}, "assistant_remove_automation": {ClassSensitive, false},
	// Blocked: destructive infrastructure administration.
	"proxmox_start_vm": {ClassBlocked, false}, "proxmox_stop_vm": {ClassBlocked, false}, "proxmox_backup_vm": {ClassBlocked, false},
	"proxmox_snapshot_create": {ClassBlocked, false}, "proxmox_snapshot_rollback": {ClassBlocked, false}, "proxmox_snapshot_delete": {ClassBlocked, false},
	"synology_run_backup_task": {ClassBlocked, false},
	"npm_create_proxy_host":    {ClassBlocked, false}, "npm_delete_proxy_host": {ClassBlocked, false},
	"npm_enable_proxy_host": {ClassBlocked, false}, "npm_disable_proxy_host": {ClassBlocked, false},
	"infomaniak_create_dns_record": {ClassBlocked, false}, "infomaniak_update_dns_record": {ClassBlocked, false}, "infomaniak_delete_dns_record": {ClassBlocked, false},
	"cloudflare_create_dns_record": {ClassBlocked, false}, "cloudflare_update_dns_record": {ClassBlocked, false}, "cloudflare_delete_dns_record": {ClassBlocked, false},
	"cloudflare_create_tunnel": {ClassBlocked, false}, "cloudflare_delete_tunnel": {ClassBlocked, false}, "cloudflare_set_tunnel_routes": {ClassBlocked, false},
	"cloudflare_create_access_app": {ClassBlocked, false}, "cloudflare_update_access_app": {ClassBlocked, false}, "cloudflare_delete_access_app": {ClassBlocked, false},
	"cloudflare_create_zone": {ClassBlocked, false}, "cloudflare_enable_dnssec": {ClassBlocked, false}, "cloudflare_disable_dnssec": {ClassBlocked, false},
	"github_create_repo": {ClassBlocked, false}, "github_push_backup": {ClassBlocked, false},
}

func lookup(name string) entry {
	if e, ok := catalog[name]; ok {
		return e
	}
	return entry{ClassSensitive, false}
}

// untrusted lists tools whose output carries third-party text (mail, chat
// messages, script reports, calendar invites) and is wrapped as data.
func untrusted(name string) bool {
	return strings.HasPrefix(name, "imap_") || strings.HasPrefix(name, "whatsapp_get_") ||
		strings.HasPrefix(name, "script_") || strings.HasPrefix(name, "cloudflare_") || name == "hub_session_result" ||
		name == "proton_list_events" || name == "telegram_get_updates"
}

// needsConfirm: every sensitive tool carries a mandatory user_confirmed flag.
func needsConfirm(name string) bool { return lookup(name).class == ClassSensitive }

var lightRe = regexp.MustCompile(`^light\.[a-z0-9_]+$`)

// validate applies per-tool argument restrictions for the remote surface.
func validate(name string, args map[string]any) error {
	switch name {
	case "ha_turn_on", "ha_turn_off":
		id, _ := args["entity_id"].(string)
		if !lightRe.MatchString(id) {
			return fmt.Errorf("à distance, seuls les éclairages (light.xxx) peuvent être allumés/éteints")
		}
	}
	return nil
}

// settings are persisted in app_meta.
type settings struct {
	Enabled   bool
	PublicURL string
	Overrides map[string]bool
}

const (
	metaEnabled = "mcp_remote_enabled"
	metaURL     = "mcp_remote_public_url"
	metaTools   = "mcp_remote_tools"
)

func (s *Service) load() settings {
	st := settings{Overrides: map[string]bool{}}
	if v, _ := s.db.GetAppMeta(metaEnabled); v == "1" {
		st.Enabled = true
	}
	st.PublicURL, _ = s.db.GetAppMeta(metaURL)
	if raw, _ := s.db.GetAppMeta(metaTools); raw != "" {
		_ = json.Unmarshal([]byte(raw), &st.Overrides)
	}
	return st
}

// effective reports whether a tool may be served remotely right now.
func (st settings) effective(name string) bool {
	e := lookup(name)
	if e.class == ClassBlocked {
		return false
	}
	if v, ok := st.Overrides[name]; ok {
		return v
	}
	return e.defaultOn
}
