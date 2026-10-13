package assistant

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"system/internal/store"
)

type fakeWA struct{ sent int }

func (f *fakeWA) SendMessage(ctx context.Context, toolID, jid, text string) error {
	f.sent++
	return nil
}

func (f *fakeWA) SendImage(ctx context.Context, toolID, jid string, data []byte, mime, caption string) error {
	f.sent++
	return nil
}

func TestReplyPhoneNotification(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	wa := &fakeWA{}
	s := New(db, nil, wa, nil)
	s.now = func() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC) } // outside the night mode
	var pushed []string
	s.SetPhoneNotifier(func(text string) { pushed = append(pushed, text) })
	web := origin{channel: "web"}
	onWA := origin{channel: "whatsapp", toolID: "t", jid: "j"}

	s.reply(web, "salut")              // pushed
	s.reply(web, "[silence]")          // nothing
	s.reply(onWA, "déjà sur WhatsApp") // WhatsApp only
	if len(pushed) != 1 || pushed[0] != "salut" || wa.sent != 1 {
		t.Fatalf("defaults: pushed %v, wa %d", pushed, wa.sent)
	}
	s.SetPush(true, true)
	s.reply(onWA, "les deux")
	if len(pushed) != 2 || wa.sent != 2 {
		t.Fatalf("also-whatsapp: pushed %v, wa %d", pushed, wa.sent)
	}
	s.SetPush(false, true)
	s.reply(web, "muet")
	if len(pushed) != 2 {
		t.Fatalf("disabled must not push: %v", pushed)
	}
	// An automatic run with no WhatsApp contact stays on the app.
	s.SetPush(true, false)
	s.reply(origin{channel: "task"}, "rappel")
	if len(pushed) != 3 || pushed[2] != "rappel" {
		t.Fatalf("task without contact: %v", pushed)
	}
}
