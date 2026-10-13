package assistant

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"system/internal/imgsafe"
	"system/internal/store"
)

type imgEnv struct {
	*nightEnv
	proj string // allowed "projects" dir
	out  string // a dir outside the allow-list
}

func newImgEnv(t *testing.T, at time.Time) *imgEnv {
	t.Helper()
	e := &imgEnv{nightEnv: newNight(t, filepath.Join(t.TempDir(), "h.db"), at)}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	e.proj = filepath.Join(root, "projects")
	e.out = filepath.Join(root, "elsewhere")
	for _, d := range []string{e.proj, e.out, filepath.Join(e.proj, "assistant")} {
		_ = os.MkdirAll(d, 0o755)
	}
	if err := e.db.CreateProject(store.Project{ID: "p1", Name: "Assistant", Slug: "assistant", Dir: filepath.Join(e.proj, "assistant"), CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	e.s.projectID = "p1"
	e.s.SetImageGuard(imgsafe.NewGuard(imgsafe.DefaultPolicy(e.proj, filepath.Join(root, "home"), filepath.Join(root, "data"))))
	return e
}

func (e *imgEnv) png(t *testing.T, dir, name string) string {
	t.Helper()
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 64, 32)))
	p := filepath.Join(dir, name)
	_ = os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// ask queues a turn the way send() does.
func (e *imgEnv) ask(o origin) {
	e.s.mu.Lock()
	e.s.queue = append(e.s.queue, o)
	e.s.mu.Unlock()
}

// answer takes the head of the queue and replies, as pump() does.
func (e *imgEnv) answer(text string) {
	e.s.mu.Lock()
	o := e.s.queue[0]
	e.s.queue = e.s.queue[1:]
	e.s.mu.Unlock()
	e.s.reply(o, text)
}

func TestSendImageWebThread(t *testing.T) {
	e := newImgEnv(t, local(8, 12, 0))
	if _, err := e.s.SendImage(e.png(t, e.proj, "a.png"), "x"); err == nil {
		t.Fatal("no turn in progress: must refuse")
	}
	e.ask(origin{channel: "web"})
	res, err := e.s.SendImage(e.png(t, filepath.Join(e.proj, "vac"), "carte.png"), "Carte de l'aspirateur\nligne 2")
	if err != nil || !strings.Contains(res, "fil de l'assistant") || strings.Contains(res, "WhatsApp") {
		t.Fatalf("%q %v", res, err)
	}
	e.answer("Voici la carte.")
	if len(e.wa.sent) != 0 || len(e.wa.images) != 0 {
		t.Fatalf("web turn must not reach WhatsApp: %v %v", e.wa.sent, e.wa.images)
	}
	msgs, _ := e.db.ListAssistantMessages(0, 10)
	if len(msgs) != 1 || len(msgs[0].Attachments) != 1 {
		t.Fatalf("thread: %+v", msgs)
	}
	a := msgs[0].Attachments[0]
	if a.Kind != "image" || a.Direction != "out" || a.Mime != "image/png" || a.Width != 64 || a.Height != 32 ||
		a.Caption != "Carte de l'aspirateur ligne 2" || a.URL != "/api/assistant/attachments/"+a.ID || a.Expired {
		t.Fatalf("attachment: %+v", a)
	}
	full, _ := e.s.AttachmentFile(a)
	if !strings.Contains(full, outgoingDir) || strings.Contains(filepath.Base(full), "carte") {
		t.Fatalf("stored path %s (random name expected, outgoing store)", full)
	}
	if fi, err := os.Stat(full); err != nil || fi.Size() != a.Size {
		t.Fatalf("file: %v", err)
	}
}

func TestSendImageWhatsAppTurn(t *testing.T) {
	e := newImgEnv(t, local(8, 12, 0))
	e.ask(origin{channel: "whatsapp", toolID: "tool", jid: "jid"})
	res, err := e.s.SendImage(e.png(t, e.proj, "a.png"), "Légende")
	if err != nil || !strings.Contains(res, "WhatsApp") {
		t.Fatalf("%q %v", res, err)
	}
	e.answer("Voilà")
	if len(e.wa.sent) != 1 || len(e.wa.images) != 1 || e.wa.images[0] != "Légende" {
		t.Fatalf("whatsapp: %v %v", e.wa.sent, e.wa.images)
	}
}

func TestSendImageSilenceSendsNothing(t *testing.T) {
	e := newImgEnv(t, local(8, 12, 0))
	e.ask(origin{channel: "task"})
	if _, err := e.s.SendImage(e.png(t, e.proj, "a.png"), ""); err != nil {
		t.Fatal(err)
	}
	e.answer("[silence]")
	msgs, _ := e.db.ListAssistantMessages(0, 10)
	if len(msgs) != 0 || len(e.wa.images) != 0 {
		t.Fatalf("silence: %+v %v", msgs, e.wa.images)
	}
	root, _ := e.s.projectDir()
	if ents, _ := os.ReadDir(filepath.Join(root, outgoingDir)); len(ents) != 0 {
		t.Fatalf("files left: %d", len(ents))
	}
}

func TestSendImageImageOnlyAnswer(t *testing.T) {
	e := newImgEnv(t, local(8, 12, 0))
	e.ask(origin{channel: "whatsapp", toolID: "tool", jid: "jid"})
	_, _ = e.s.SendImage(e.png(t, e.proj, "a.png"), "Seule")
	e.answer("")
	msgs, _ := e.db.ListAssistantMessages(0, 10)
	if len(msgs) != 1 || msgs[0].Text != "" || len(msgs[0].Attachments) != 1 || len(e.wa.images) != 1 || len(e.wa.sent) != 0 {
		t.Fatalf("%+v %v %v", msgs, e.wa.sent, e.wa.images)
	}
}

func TestSendImageNightHeldThenDigest(t *testing.T) {
	e := newImgEnv(t, local(8, 23, 0))
	e.ask(origin{channel: "task"})
	if _, err := e.s.SendImage(e.png(t, e.proj, "a.png"), "Nuit"); err != nil {
		t.Fatal(err)
	}
	e.answer("Alerte caméra")
	if len(e.wa.sent) != 0 || len(e.wa.images) != 0 || len(e.pushed) != 0 {
		t.Fatalf("held at night: %v %v %v", e.wa.sent, e.wa.images, e.pushed)
	}
	msgs, _ := e.db.ListAssistantMessages(0, 10)
	if len(msgs) != 1 || !msgs[0].Held || len(msgs[0].Attachments) != 1 {
		t.Fatalf("held thread: %+v", msgs)
	}
	e.clock = local(9, 7, 30)
	e.s.sweepHeld()
	if len(e.wa.sent) != 1 || !strings.Contains(e.wa.sent[0], "📷 1 image") || len(e.wa.images) != 1 || e.wa.images[0] != "Nuit" {
		t.Fatalf("digest: %v / %v", e.wa.sent, e.wa.images)
	}
}

func TestSendImageRefusals(t *testing.T) {
	e := newImgEnv(t, local(8, 12, 0))
	e.ask(origin{channel: "web"})
	good := e.png(t, e.proj, "a.png")
	outside := e.png(t, e.out, "b.png")
	link := filepath.Join(e.proj, "l.png")
	_ = os.Symlink(outside, link)
	bad := filepath.Join(e.proj, "fake.png")
	_ = os.WriteFile(bad, []byte("<svg/>"), 0o644)
	big := filepath.Join(e.proj, "big.png")
	_ = os.WriteFile(big, append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 11<<20)...), 0o644)
	for name, p := range map[string]string{
		"outside": outside, "symlink": link, "traversal": filepath.Join(e.proj, "..", "elsewhere", "b.png"),
		"fake": bad, "big": big, "etc": "/etc/passwd", "hidden": e.png(t, filepath.Join(e.proj, ".ssh"), "k.png"),
	} {
		if _, err := e.s.SendImage(p, ""); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
	e.s.mu.Lock()
	n := len(e.s.queue[0].images)
	e.s.mu.Unlock()
	if n != 0 {
		t.Fatalf("refused images attached: %d", n)
	}
	for i := 0; i < maxImagesPerReply; i++ {
		if _, err := e.s.SendImage(good, ""); err != nil {
			t.Fatalf("image %d: %v", i, err)
		}
	}
	if _, err := e.s.SendImage(good, ""); err == nil {
		t.Fatal("too many images in one reply")
	}
}

func TestSendImageRateLimit(t *testing.T) {
	e := newImgEnv(t, local(8, 12, 0))
	g := imgsafe.NewGuard(e.s.imageGuard().Policy)
	g.Limiter = imgsafe.NewLimiter(2)
	e.s.SetImageGuard(g)
	f := e.png(t, e.proj, "a.png")
	for i := 0; i < 3; i++ {
		e.ask(origin{channel: "web"})
		_, err := e.s.SendImage(f, "")
		if (i < 2) != (err == nil) {
			t.Fatalf("send %d: %v", i, err)
		}
		e.answer("ok")
	}
}

func TestSendImageNotConfigured(t *testing.T) {
	e := newImgEnv(t, local(8, 12, 0))
	e.s.SetImageGuard(nil)
	e.ask(origin{channel: "web"})
	if _, err := e.s.SendImage(e.png(t, e.proj, "a.png"), ""); err == nil {
		t.Fatal("must refuse without a guard")
	}
}

func TestOutgoingImagesPurged(t *testing.T) {
	e := newImgEnv(t, local(8, 12, 0))
	e.ask(origin{channel: "web"})
	_, _ = e.s.SendImage(e.png(t, e.proj, "a.png"), "")
	e.answer("ok")
	msgs, _ := e.db.ListAssistantMessages(0, 10)
	a := msgs[0].Attachments[0]
	full, _ := e.s.AttachmentFile(a)
	e.s.SetRetention(time.Nanosecond)
	time.Sleep(5 * time.Millisecond)
	if n := e.s.PurgeAttachments(); n != 1 {
		t.Fatalf("purged %d", n)
	}
	if _, err := os.Stat(full); !os.IsNotExist(err) {
		t.Fatalf("file kept: %v", err)
	}
	msgs, _ = e.db.ListAssistantMessages(0, 10)
	if !msgs[0].Attachments[0].Expired {
		t.Fatal("record must stay, flagged expired")
	}
}
