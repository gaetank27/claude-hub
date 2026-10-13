package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"system/internal/store"
)

// newTestManager builds a Manager backed by a fresh on-disk store under a temp
// DataDir, exercising the real NewManager wiring (it MkdirAll's journal/socket).
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	m, err := NewManager(db, Config{
		DataDir:       dir,
		ProjectsDir:   dir,
		PublicBaseURL: "https://hub.example.com",
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return m
}

func TestBuildProjectContext_ResourcesAndPrompt(t *testing.T) {
	m := newTestManager(t)

	// An enabled telegram tool should surface as an available resource and its MCP
	// actions should be named in the prompt; a disabled tool must be ignored.
	if err := m.db.CreateTool(store.Tool{
		ID: "t1", Type: "telegram", Name: "bot", Status: store.ToolConnected, Enabled: true,
	}); err != nil {
		t.Fatalf("create enabled tool: %v", err)
	}
	if err := m.db.CreateTool(store.Tool{
		ID: "t2", Type: "proxmox", Name: "pve", Status: store.ToolDisconnected, Enabled: false,
	}); err != nil {
		t.Fatalf("create disabled tool: %v", err)
	}
	if err := m.db.CreateTool(store.Tool{
		ID: "t3", Type: "s3", Name: "store", Status: store.ToolConnected, Enabled: true,
	}); err != nil {
		t.Fatalf("create enabled s3 tool: %v", err)
	}

	pc := m.BuildProjectContext(store.Project{Slug: "myproj"})

	keys := map[string]ContextResource{}
	for _, r := range pc.Resources {
		keys[r.Key] = r
	}
	if r, ok := keys["sandbox"]; !ok || !r.Available {
		t.Errorf("sandbox resource should be present and available, got %+v", r)
	}
	if r, ok := keys["telegram"]; !ok || !r.Available {
		t.Errorf("enabled telegram should be an available resource, got %+v (%v)", r, ok)
	}
	if _, ok := keys["proxmox"]; ok {
		t.Errorf("disabled proxmox must not be advertised")
	}
	if r, ok := keys["s3"]; !ok || !r.Available {
		t.Errorf("enabled s3 should be an available resource, got %+v (%v)", r, ok)
	}

	// Prompt must teach the real exposure mechanism, not Nginx, and carry the slug
	// and public URL plus the enabled connector's actions.
	for _, want := range []string{
		"claudehub.project=myproj",
		"https://hub.example.com/x/<container-name>/",
		"telegram_send_message",
		"s3_list_buckets",
		"Do NOT",
	} {
		if !strings.Contains(pc.Prompt, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, pc.Prompt)
		}
	}
	if strings.Contains(pc.Prompt, "proxmox_list_vms") {
		t.Errorf("prompt must not mention a disabled connector's actions")
	}
}

func TestWriteContextFile(t *testing.T) {
	m := newTestManager(t)
	path := m.writeContextFile(store.Project{Slug: "p"}, "sess-123")
	if path == "" {
		t.Fatal("writeContextFile returned empty path")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read context file: %v", err)
	}
	if !strings.Contains(string(b), "Claude Hub") {
		t.Errorf("context file missing expected content, got %q", string(b))
	}
	if filepath.Base(path) != "sess-123.md" {
		t.Errorf("unexpected context file name: %s", path)
	}
}
