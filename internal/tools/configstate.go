package tools

import (
	"encoding/json"
	"sync"

	"system/internal/secretbox"
	"system/internal/store"
)

// ConfigUpdater rewrites the stored (encrypted) config of a saved tool. Stateful
// connectors use it to persist what they learn at runtime, such as a rotated
// session token. mutate receives the decrypted config and edits it in place.
type ConfigUpdater func(toolID string, mutate func(cfg map[string]string)) error

var configUpdater ConfigUpdater

// SetConfigUpdater wires the config persistence into the connectors.
func SetConfigUpdater(u ConfigUpdater) { configUpdater = u }

// updateToolConfig is a no-op when no updater is wired (unit tests, CLI tools).
func updateToolConfig(toolID string, mutate func(cfg map[string]string)) error {
	if configUpdater == nil || toolID == "" {
		return nil
	}
	return configUpdater(toolID, mutate)
}

type configStore interface {
	GetTool(id string) (store.Tool, error)
	UpdateTool(id, name, configEnc string, enabled bool) error
}

// NewConfigUpdater persists tool config changes through the store, encrypted
// with the Hub's secret box like every other connector secret.
func NewConfigUpdater(db configStore, box *secretbox.Box) ConfigUpdater {
	var mu sync.Mutex // read-modify-write of one JSON blob: serialise
	return func(toolID string, mutate func(cfg map[string]string)) error {
		mu.Lock()
		defer mu.Unlock()
		t, err := db.GetTool(toolID)
		if err != nil {
			return err
		}
		cfg, err := decryptConfig(box, t.ConfigEnc)
		if err != nil {
			return err
		}
		mutate(cfg)
		raw, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		enc, err := box.Encrypt(raw)
		if err != nil {
			return err
		}
		return db.UpdateTool(t.ID, t.Name, enc, t.Enabled)
	}
}
