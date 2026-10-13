package tools

import "system/internal/store"

// defaultCategory maps a connector type to the key of its default category.
// A connector type not listed (a new one) falls into "other" until classified.
var defaultCategory = map[string]string{
	"homeassistant": "home",
	"telegram":      "messaging",
	"whatsapp":      "messaging",
	"email":         "mail",
	"imap":          "mail",
	"proton":        "mail",
	"proxmox":       "infra",
	"synology":      "infra",
	"npm":           "infra",
	"infomaniak":    "infra",
	"cloudflare":    "infra",
	"github":        "infra",
	"s3":            "infra",
	"hub":           "assistant", // also carries the script_* actions (status, commands)
}

// DefaultCategory returns the default category id of a connector type.
func DefaultCategory(connectorType string) string {
	if c, ok := defaultCategory[connectorType]; ok {
		return c
	}
	return store.OtherToolCategory
}

// EffectiveCategory resolves the category a tool is shown in: its explicit
// choice when that category still exists, else the default of its type when
// present, else "other".
func EffectiveCategory(explicit, connectorType string, exists func(id string) bool) (id string, auto bool) {
	if explicit != "" && exists(explicit) {
		return explicit, false
	}
	if d := DefaultCategory(connectorType); exists(d) {
		return d, true
	}
	return store.OtherToolCategory, true
}
