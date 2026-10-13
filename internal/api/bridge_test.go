package api

import (
	"path/filepath"
	"strings"
	"testing"

	"system/internal/secretbox"
	"system/internal/store"
	"system/internal/tools"
)

func bridgeTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	box, err := secretbox.Open(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	return &Server{db: db, secrets: box}
}

// The managed IMAP instance: created once, refreshed in place afterwards, with the
// Bridge password encrypted at rest and tls disabled on loopback.
func TestBridgeUpsertToolCreatesThenUpdates(t *testing.T) {
	s := bridgeTestServer(t)
	id, err := s.bridgeUpsertTool(tools.BridgeConnectResult{Status: "added", Email: "me@proton.test", IMAPPort: 1143, IMAPPassword: "pw-one-0123456"})
	if err != nil {
		t.Fatal(err)
	}
	all, _ := s.db.ListTools()
	if len(all) != 1 || all[0].ID != id || all[0].Type != "imap" || all[0].Name != "Proton Bridge" || !all[0].Enabled {
		t.Fatalf("unexpected tools: %+v", all)
	}
	if strings.Contains(all[0].ConfigEnc, "pw-one-0123456") || strings.Contains(all[0].ConfigEnc, "me@proton.test") {
		t.Fatal("config must be encrypted at rest")
	}
	cfg, _ := s.decryptToolConfig(all[0].ConfigEnc)
	if cfg["host"] != "127.0.0.1" || cfg["port"] != "1143" || cfg["username"] != "me@proton.test" || cfg["password"] != "pw-one-0123456" || cfg["tls"] != "false" {
		t.Fatalf("unexpected config: %v", cfg)
	}

	id2, err := s.bridgeUpsertTool(tools.BridgeConnectResult{Status: "added", Email: "me@proton.test", IMAPPort: 1144, IMAPPassword: "pw-two-0123456"})
	if err != nil || id2 != id {
		t.Fatalf("second connect must update the same instance: %v %v", id2, err)
	}
	all, _ = s.db.ListTools()
	cfg, _ = s.decryptToolConfig(all[0].ConfigEnc)
	if len(all) != 1 || cfg["port"] != "1144" || cfg["password"] != "pw-two-0123456" {
		t.Fatalf("not refreshed: %d %v", len(all), cfg)
	}

	// A hand-made instance (cmd/addbridge) is adopted rather than duplicated.
	s2 := bridgeTestServer(t)
	enc, _ := s2.encryptToolConfig(map[string]string{"host": "127.0.0.1", "port": "1143", "username": "me@proton.test", "password": "x", "tls": "false"})
	_ = s2.db.CreateTool(store.Tool{ID: "legacy", Type: "imap", Name: "Proton Bridge", Enabled: true, ConfigEnc: enc})
	if got, err := s2.bridgeUpsertTool(tools.BridgeConnectResult{Email: "me@proton.test", IMAPPort: 1143, IMAPPassword: "new-pw-0123456"}); err != nil || got != "legacy" {
		t.Fatalf("adopt legacy: %v %v", got, err)
	}
}
