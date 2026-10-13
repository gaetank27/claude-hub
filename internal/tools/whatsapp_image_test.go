package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"system/internal/imgsafe"
	"system/internal/whatsapp"
)

// whatsapp_send_image never reaches WhatsApp for a path the policy refuses, and
// refuses everything while no guard is configured.
func TestWhatsAppSendImageRefusals(t *testing.T) {
	oldMgr, oldGuard := waManager, imgGuard
	t.Cleanup(func() { waManager, imgGuard = oldMgr, oldGuard })
	waManager = &whatsapp.Manager{}
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	_ = os.MkdirAll(projects, 0o755)
	secret := filepath.Join(root, "secret.png")
	_ = os.WriteFile(secret, []byte("\x89PNG\r\n\x1a\n"), 0o644)
	cfg := map[string]string{"_tool_id": "t1"}
	call := func(path string) error {
		_, err := waConnector{}.Call(context.Background(), cfg, "whatsapp_send_image",
			map[string]any{"chat_jid": "33612345678", "path": path, "caption": "x"})
		return err
	}
	imgGuard = nil
	if err := call(filepath.Join(projects, "a.png")); err == nil {
		t.Fatal("no guard: must refuse")
	}
	imgGuard = imgsafe.NewGuard(imgsafe.DefaultPolicy(projects, "", filepath.Join(root, "data")))
	for _, p := range []string{secret, "/etc/passwd", filepath.Join(projects, "..", "secret.png"), filepath.Join(projects, "missing.png")} {
		if err := call(p); err == nil {
			t.Errorf("%s must be refused", p)
		}
	}
	found := false
	for _, s := range (waConnector{}).Tools() {
		found = found || s.Name == "whatsapp_send_image"
	}
	if !found {
		t.Fatal("tool not declared")
	}
}
