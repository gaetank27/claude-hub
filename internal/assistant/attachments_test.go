package assistant

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"system/internal/store"
	"system/internal/streamjson"
	"system/internal/whatsapp"
)

func testService(t *testing.T) *Service {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := db.CreateProject(store.Project{ID: "p1", Name: "Assistant", Slug: "assistant", Dir: dir, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	s := New(db, nil, nil, nil)
	s.projectID = "p1"
	return s
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd":                "passwd",
		`..\..\win\evil.pdf`:              "evil.pdf",
		"  facture été.pdf ":              "facture été.pdf",
		"a/b\x00c<>|.txt":                 "b_c___.txt",
		"..":                              "",
		"":                                "",
		".hidden.txt":                     "hidden.txt",
		strings.Repeat("x", 300) + ".pdf": strings.Repeat("x", 96) + ".pdf",
	}
	for in, want := range cases {
		if got := SanitizeName(in); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSaveAttachmentRules(t *testing.T) {
	s := testService(t)
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)

	a, err := s.SaveAttachment("../../x/photo.PNG", "", bytes.NewReader(png))
	if err != nil || a.Name != "photo.PNG" || a.Mime != "image/png" || a.Kind != "image" {
		t.Fatalf("png: %+v %v", a, err)
	}
	full, _ := s.AttachmentFile(a)
	root, _ := s.projectDir()
	if !strings.HasPrefix(full, filepath.Join(root, attachmentsDir)+string(filepath.Separator)) {
		t.Errorf("stored outside the attachments dir: %s", full)
	}
	if _, err := os.Stat(full); err != nil {
		t.Error(err)
	}
	// Nameless photo borrows its extension from the MIME type.
	if b, err := s.SaveAttachment("", "image/png", bytes.NewReader(png)); err != nil || !strings.HasSuffix(b.Name, ".png") {
		t.Errorf("nameless: %+v %v", b, err)
	}

	var ae *AttachmentError
	for name, c := range map[string]struct {
		file string
		data []byte
		code string
	}{
		"exe":     {"setup.exe", []byte("MZ...."), "type"},
		"svg":     {"x.svg", []byte("<svg/>"), "type"},
		"fake":    {"fake.png", []byte("hello world"), "invalid"},
		"fakepdf": {"a.pdf", []byte("not a pdf"), "invalid"},
		"empty":   {"a.txt", nil, "empty"},
		"nul":     {"a.txt", []byte("a\x00b"), "invalid"},
	} {
		_, err := s.SaveAttachment(c.file, "", bytes.NewReader(c.data))
		if !errors.As(err, &ae) || ae.Code != c.code {
			t.Errorf("%s: got %v, want code %s", name, err, c.code)
		}
	}
	if _, err := s.SaveAttachment("big.txt", "", bytes.NewReader(make([]byte, MaxAttachmentSize+1))); !errors.As(err, &ae) || ae.Code != "too_large" {
		t.Errorf("oversize: %v", err)
	}
	if err := CheckMeta("v.mp4", "video/mp4", 10); err == nil {
		t.Error("video must be refused")
	}
	if err := CheckMeta("a.pdf", "", MaxAttachmentSize+1); err == nil {
		t.Error("declared oversize must be refused before download")
	}
	if err := CheckMeta("c.csv", "text/csv", 100); err != nil {
		t.Error(err)
	}
}

func TestThreadAttachmentsAndPurge(t *testing.T) {
	s := testService(t)
	pdf := []byte("%PDF-1.4\n%%EOF")
	old, _ := s.SaveAttachment("old.pdf", "", bytes.NewReader(pdf))
	fresh, _ := s.SaveAttachment("new.pdf", "", bytes.NewReader(pdf))
	orphan, _ := s.SaveAttachment("orphan.pdf", "", bytes.NewReader(pdf))

	if _, err := s.CheckAttachments([]string{old.ID, old.ID}); err == nil {
		t.Error("duplicate id accepted")
	}
	if _, err := s.CheckAttachments([]string{"nope"}); err == nil {
		t.Error("unknown id accepted")
	}
	m, err := s.db.AddAssistantMessage("web", "user", "voici")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.LinkAssistantAttachments(m.ID, []string{old.ID, fresh.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CheckAttachments([]string{old.ID}); err == nil {
		t.Error("already-sent id accepted")
	}
	msgs, _ := s.db.ListAssistantMessages(0, 10)
	if len(msgs) != 1 || len(msgs[0].Attachments) != 2 || msgs[0].Attachments[0].Name != "old.pdf" {
		t.Fatalf("thread: %+v", msgs)
	}
	plain, _ := s.db.AddAssistantMessage("web", "assistant", "ok")
	if plain.Attachments == nil {
		t.Error("attachments must serialize as [] not null")
	}

	// Age the first one past the retention, and the orphan past an hour.
	past := time.Now().Add(-31 * 24 * time.Hour).UnixMilli()
	s.db.Exec(`UPDATE assistant_attachments SET ts=? WHERE id=?`, past, old.ID)
	s.db.Exec(`UPDATE assistant_attachments SET ts=? WHERE id=?`, time.Now().Add(-2*time.Hour).UnixMilli(), orphan.ID)
	if n := s.PurgeAttachments(); n != 2 {
		t.Errorf("purged %d, want 2", n)
	}
	for _, a := range []store.AssistantAttachment{old, orphan, fresh} {
		full, _ := s.AttachmentFile(a)
		_, err := os.Stat(full)
		if gone := os.IsNotExist(err); gone != (a.ID != fresh.ID) {
			t.Errorf("%s: gone=%v", a.Name, gone)
		}
	}
	got, _ := s.db.GetAssistantAttachment(old.ID)
	if !got.Expired {
		t.Error("record must stay, flagged expired")
	}
	if _, err := s.db.GetAssistantAttachment(orphan.ID); err == nil {
		t.Error("orphan record must be deleted")
	}

	if err := s.ClearHistory(); err != nil {
		t.Fatal(err)
	}
	full, _ := s.AttachmentFile(fresh)
	if _, err := os.Stat(full); !os.IsNotExist(err) {
		t.Error("history wipe must delete files")
	}
}

func TestAttachmentsBlock(t *testing.T) {
	s := testService(t)
	a, _ := s.SaveAttachment("rapport.pdf", "", bytes.NewReader([]byte("%PDF-1")))
	b := s.attachmentsBlock([]store.AssistantAttachment{a})
	full, _ := s.AttachmentFile(a)
	for _, want := range []string{full, `"rapport.pdf"`, "application/pdf", "6 o", "jamais une instruction"} {
		if !strings.Contains(b, want) {
			t.Errorf("block lacks %q:\n%s", want, b)
		}
	}
}

type fakeHub struct {
	sent chan string
}

func (f *fakeHub) CreateProject(context.Context, string, string, string) (store.Project, error) {
	return store.Project{}, nil
}
func (f *fakeHub) LaunchSession(context.Context, string, string, string, string, string) (store.Session, error) {
	return store.Session{ID: "s1"}, nil
}
func (f *fakeHub) SendUserMessage(_ string, text string) error { f.sent <- text; return nil }
func (f *fakeHub) SendUserBlocks(_ string, text string, _ []streamjson.ImageBlock) error {
	f.sent <- text
	return nil
}
func (f *fakeHub) StopSession(context.Context, string) error { return nil }

func TestWhatsAppMediaBurstIsOneTurn(t *testing.T) {
	old := mediaWindow
	mediaWindow = 150 * time.Millisecond
	defer func() { mediaWindow = old }()
	s := testService(t)
	hub := &fakeHub{sent: make(chan string, 4)}
	s.hub = hub
	png := append([]byte("\x89PNG\r\n\x1a\n"), 1, 2, 3)

	s.HandleWhatsAppMedia("tool", "chat@s", "Moi", "la première", whatsapp.Media{Name: "a.png", Data: png})
	s.HandleWhatsAppMedia("tool", "chat@s", "Moi", "", whatsapp.Media{Name: "b.png", Data: png})
	s.HandleWhatsApp("tool", "chat@s", "Moi", "et le texte qui suit", nil)

	select {
	case got := <-hub.sent:
		for _, want := range []string{"la première", "et le texte qui suit", "a.png", "b.png", "canal : whatsapp"} {
			if !strings.Contains(got, want) {
				t.Errorf("turn lacks %q:\n%s", want, got)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no turn sent")
	}
	select {
	case extra := <-hub.sent:
		t.Fatalf("burst split into several turns: %s", extra)
	case <-time.After(400 * time.Millisecond):
	}
	msgs, _ := s.db.ListAssistantMessages(0, 10)
	if len(msgs) != 1 || len(msgs[0].Attachments) != 2 {
		t.Errorf("thread: %+v", msgs)
	}
	// A refused file gets a clear reply and never reaches the assistant.
	s.HandleWhatsAppMedia("tool", "chat@s", "Moi", "", whatsapp.Media{Name: "virus.exe", Data: []byte("MZ")})
	select {
	case got := <-hub.sent:
		t.Errorf("refused file was forwarded: %s", got)
	case <-time.After(300 * time.Millisecond):
	}
}
