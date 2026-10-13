package tools

import "testing"

func TestEffectiveCategory(t *testing.T) {
	have := func(ids ...string) func(string) bool {
		return func(id string) bool {
			for _, x := range ids {
				if x == id {
					return true
				}
			}
			return false
		}
	}
	for name, c := range map[string]struct {
		explicit, typ string
		exists        func(string) bool
		want          string
		auto          bool
	}{
		"explicit":               {"mine", "telegram", have("mine", "messaging", "other"), "mine", false},
		"type default":           {"", "telegram", have("messaging", "other"), "messaging", true},
		"explicit deleted":       {"gone", "proxmox", have("infra", "other"), "infra", true},
		"default deleted":        {"", "proxmox", have("other"), "other", true},
		"unknown connector":      {"", "brand-new", have("home", "other"), "other", true},
		"hub in assistant":       {"", "hub", have("assistant", "other"), "assistant", true},
		"explicit beats unknown": {"home", "brand-new", have("home", "other"), "home", false},
	} {
		got, auto := EffectiveCategory(c.explicit, c.typ, c.exists)
		if got != c.want || auto != c.auto {
			t.Errorf("%s: got %s,%v", name, got, auto)
		}
	}
	// every registered connector has a deliberate default (not silently "other")
	for _, c := range All() {
		if _, ok := defaultCategory[c.Type()]; !ok {
			t.Errorf("connector %q has no default category", c.Type())
		}
	}
}
