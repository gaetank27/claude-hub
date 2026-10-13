package assistant

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"system/internal/store"
)

type recWA struct {
	mu     sync.Mutex
	sent   []string
	images []string // captions of the images sent
}

func (r *recWA) SendImage(ctx context.Context, toolID, jid string, data []byte, mime, caption string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.images = append(r.images, caption)
	return nil
}

func (r *recWA) SendMessage(ctx context.Context, toolID, jid, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, text)
	return nil
}

type nightEnv struct {
	s      *Service
	db     *store.DB
	wa     *recWA
	pushed []string
	clock  time.Time
	path   string
}

var zurich, _ = time.LoadLocation("Europe/Zurich")

func newNight(t *testing.T, path string, at time.Time) *nightEnv {
	t.Helper()
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e := &nightEnv{db: db, wa: &recWA{}, clock: at, path: path}
	e.s = New(db, nil, e.wa, nil)
	e.s.now = func() time.Time { return e.clock }
	e.s.SetPhoneNotifier(func(text string) { e.pushed = append(e.pushed, text) })
	_ = db.SetAppMeta(waToolMetaKey, "tool")
	_ = db.SetAppMeta(waJIDMetaKey, "jid")
	return e
}

func local(day, hh, mm int) time.Time { return time.Date(2026, 10, day, hh, mm, 0, 0, zurich) }

// userWrote records a user message at the simulated time.
func (e *nightEnv) userWrote(at time.Time) {
	m, _ := e.db.AddAssistantMessage("web", "user", "salut")
	_ = e.db.SetAssistantMessageTS(m.ID, at.UnixMilli())
}

func TestNightRangeBoundaries(t *testing.T) {
	e := newNight(t, filepath.Join(t.TempDir(), "h.db"), local(8, 12, 0))
	n := e.s.Night()
	cases := []struct {
		h, m int
		want bool
	}{{21, 59, false}, {22, 0, true}, {23, 59, true}, {0, 0, true}, {6, 59, true}, {7, 0, false}, {7, 29, false}, {12, 0, false}}
	for _, c := range cases {
		if got := n.InQuiet(local(8, c.h, c.m)); got != c.want {
			t.Errorf("%02d:%02d quiet = %v, want %v", c.h, c.m, got, c.want)
		}
	}
}

func TestNightHoldAndMorningDelivery(t *testing.T) {
	e := newNight(t, filepath.Join(t.TempDir(), "h.db"), local(8, 23, 0))
	task := origin{channel: "task"}
	e.s.reply(task, "premier")
	e.clock = local(9, 2, 30)
	e.s.reply(origin{channel: "place"}, "second")
	if len(e.wa.sent) != 0 || len(e.pushed) != 0 {
		t.Fatalf("held messages must not be sent: wa %v push %v", e.wa.sent, e.pushed)
	}
	if e.s.HeldCount() != 2 {
		t.Fatalf("held = %d", e.s.HeldCount())
	}
	msgs, _ := e.db.ListAssistantMessages(0, 10)
	if len(msgs) != 2 || !msgs[0].Held || msgs[0].HeldUntil != local(9, 7, 30).UnixMilli() {
		t.Fatalf("thread marks: %+v", msgs)
	}
	e.clock = local(9, 7, 29)
	e.s.sweepHeld()
	if len(e.wa.sent) != 0 {
		t.Fatal("delivered before 07:30")
	}
	e.clock = local(9, 7, 30)
	e.s.sweepHeld()
	e.s.sweepHeld() // no duplicate
	if len(e.wa.sent) != 1 || len(e.pushed) != 0 {
		t.Fatalf("one WhatsApp digest expected: %v / push %v", e.wa.sent, e.pushed)
	}
	d := e.wa.sent[0]
	if !strings.Contains(d, "23:00 — premier") || !strings.Contains(d, "02:30 — second") ||
		strings.Index(d, "premier") > strings.Index(d, "second") {
		t.Fatalf("digest: %q", d)
	}
	if e.s.HeldCount() != 0 {
		t.Fatal("still held after delivery")
	}
}

func TestNightIdleWindow(t *testing.T) {
	for _, c := range []struct {
		ago  time.Duration
		held bool
	}{{19 * time.Minute, false}, {21 * time.Minute, true}} {
		e := newNight(t, filepath.Join(t.TempDir(), "h.db"), local(8, 23, 30))
		e.userWrote(e.clock.Add(-c.ago))
		e.s.reply(origin{channel: "task"}, "rappel")
		if (e.s.HeldCount() == 1) != c.held {
			t.Errorf("user wrote %v ago: held = %d, want held=%v", c.ago, e.s.HeldCount(), c.held)
		}
		if !c.held && len(e.wa.sent) != 1 {
			t.Errorf("awake user: expected immediate send, got %v", e.wa.sent)
		}
	}
}

func TestNightDirectReplyNeverHeld(t *testing.T) {
	e := newNight(t, filepath.Join(t.TempDir(), "h.db"), local(8, 23, 30))
	e.s.reply(origin{channel: "whatsapp", toolID: "tool", jid: "jid"}, "réponse")
	e.s.reply(origin{channel: "web"}, "réponse web")
	if e.s.HeldCount() != 0 || len(e.wa.sent) != 1 || len(e.pushed) != 1 {
		t.Fatalf("held %d wa %v push %v", e.s.HeldCount(), e.wa.sent, e.pushed)
	}
}

func TestNightDayAndBoundaryDelivery(t *testing.T) {
	// 21:59 sends, 22:00 holds, 06:59 holds (until 07:30 same day), 07:00 sends.
	for _, c := range []struct {
		at   time.Time
		held bool
	}{{local(8, 21, 59), false}, {local(8, 22, 0), true}, {local(9, 6, 59), true}, {local(9, 7, 0), false}} {
		e := newNight(t, filepath.Join(t.TempDir(), "h.db"), c.at)
		e.s.reply(origin{channel: "task"}, "x")
		if (e.s.HeldCount() == 1) != c.held {
			t.Errorf("%s: held=%d want %v", c.at.Format("15:04"), e.s.HeldCount(), c.held)
		}
	}
	e := newNight(t, filepath.Join(t.TempDir(), "h.db"), local(9, 6, 59))
	e.s.reply(origin{channel: "task"}, "x")
	held, _ := e.db.HeldAssistantMessages()
	if held[0].HeldUntil != local(9, 7, 30).UnixMilli() {
		t.Errorf("06:59 must be held until 07:30 the same day, got %v", time.UnixMilli(held[0].HeldUntil).In(zurich))
	}
}

func TestNightCatchUpOnUserMessage(t *testing.T) {
	e := newNight(t, filepath.Join(t.TempDir(), "h.db"), local(8, 23, 0))
	e.s.reply(origin{channel: "task"}, "retenu 1")
	e.s.reply(origin{channel: "session", toolID: routeWeb}, "retenu web")
	e.clock = local(9, 1, 0)
	e.userWrote(e.clock)
	e.s.flushHeld("catchup") // what send() does for a user message
	if len(e.wa.sent) != 1 || !strings.Contains(e.wa.sent[0], "Rattrapage") {
		t.Fatalf("catch-up on WhatsApp: %v", e.wa.sent)
	}
	if len(e.pushed) != 1 { // the web-routed one has no WhatsApp target: push
		t.Fatalf("web-routed message goes to the phone: %v", e.pushed)
	}
	e.clock = local(9, 7, 30)
	e.s.sweepHeld()
	if len(e.wa.sent) != 1 || len(e.pushed) != 1 {
		t.Fatalf("not re-sent at 07:30: %v %v", e.wa.sent, e.pushed)
	}
	// Awake user: a new spontaneous message passes at once.
	e.clock = local(9, 1, 5)
	e.s.reply(origin{channel: "task"}, "frais")
	if len(e.wa.sent) != 2 || e.s.HeldCount() != 0 {
		t.Fatalf("awake: %v", e.wa.sent)
	}
}

func TestNightRestartCatchesUpDelivery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.db")
	e := newNight(t, path, local(8, 23, 0))
	e.s.reply(origin{channel: "task"}, "dort")
	e.db.Close()
	// Backend restarted at 09:15, after the delivery time.
	e2 := newNight(t, path, local(9, 9, 15))
	if e2.s.HeldCount() != 1 {
		t.Fatalf("held message lost across restart: %d", e2.s.HeldCount())
	}
	e2.s.sweepHeld()
	if len(e2.wa.sent) != 1 || !strings.Contains(e2.wa.sent[0], "dort") {
		t.Fatalf("late delivery: %v", e2.wa.sent)
	}
	e2.s.sweepHeld()
	if len(e2.wa.sent) != 1 {
		t.Fatal("duplicate")
	}
}

func TestNightDisabled(t *testing.T) {
	e := newNight(t, filepath.Join(t.TempDir(), "h.db"), local(8, 23, 0))
	e.s.reply(origin{channel: "task"}, "retenu")
	off := false
	if err := e.s.SetNight(NightPatch{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	e.s.sweepHeld() // switching off releases what was held
	if len(e.wa.sent) != 1 || e.s.HeldCount() != 0 {
		t.Fatalf("release on disable: %v", e.wa.sent)
	}
	e.s.reply(origin{channel: "task"}, "direct")
	if len(e.wa.sent) != 2 {
		t.Fatalf("disabled mode must not hold: %v", e.wa.sent)
	}
}

func TestNightDSTChange2026(t *testing.T) {
	// 25 Oct 2026: clocks go back at 03:00 CEST → 02:00 CET (a 25 h night).
	e := newNight(t, filepath.Join(t.TempDir(), "h.db"), time.Date(2026, 10, 24, 22, 30, 0, 0, zurich))
	n := e.s.Night()
	// 02:30 happens twice; both are quiet.
	first := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)  // 02:30 CEST
	second := time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC) // 02:30 CET
	if !n.InQuiet(first) || !n.InQuiet(second) {
		t.Fatal("both 02:30 are inside the range")
	}
	// 06:59 CET = 05:59 UTC, 07:00 CET = 06:00 UTC.
	if !n.InQuiet(time.Date(2026, 10, 25, 5, 59, 0, 0, time.UTC)) || n.InQuiet(time.Date(2026, 10, 25, 6, 0, 0, 0, time.UTC)) {
		t.Fatal("range end follows the local wall clock after the change")
	}
	// Held at 23:00 on the 24th → delivered at 07:30 CET = 06:30 UTC (not 05:30).
	e.clock = time.Date(2026, 10, 24, 23, 0, 0, 0, zurich)
	e.s.reply(origin{channel: "task"}, "nuit longue")
	held, _ := e.db.HeldAssistantMessages()
	want := time.Date(2026, 10, 25, 6, 30, 0, 0, time.UTC).UnixMilli()
	if held[0].HeldUntil != want {
		t.Fatalf("delivery = %v, want 07:30 CET", time.UnixMilli(held[0].HeldUntil).UTC())
	}
	e.clock = time.Date(2026, 10, 25, 6, 29, 0, 0, time.UTC)
	e.s.sweepHeld()
	if len(e.wa.sent) != 0 {
		t.Fatal("delivered at 07:29 local")
	}
	e.clock = time.Date(2026, 10, 25, 6, 30, 0, 0, time.UTC)
	e.s.sweepHeld()
	if len(e.wa.sent) != 1 {
		t.Fatal("not delivered at 07:30 local")
	}
	// The spring change (29 Mar 2026) too: 22:00 local is 20:00 UTC after it.
	if !n.InQuiet(time.Date(2026, 3, 28, 21, 30, 0, 0, time.UTC)) || n.InQuiet(time.Date(2026, 3, 28, 20, 30, 0, 0, time.UTC)) {
		t.Fatal("before the spring change, 22:00 local = 21:00 UTC")
	}
}

func TestNightSettingsValidation(t *testing.T) {
	e := newNight(t, filepath.Join(t.TempDir(), "h.db"), local(8, 12, 0))
	n := e.s.Night()
	if !n.Enabled || n.Start != "22:00" || n.End != "07:00" || n.Deliver != "07:30" || n.IdleMinutes != 20 || n.Timezone != "Europe/Zurich" {
		t.Fatalf("defaults: %+v", n)
	}
	bad, tz, idle := "25:00", "Mars/Olympus", 0
	inside := "23:00"
	for _, p := range []NightPatch{{Start: &bad}, {Timezone: &tz}, {IdleMinutes: &idle}, {Deliver: &inside}} {
		if e.s.SetNight(p) == nil {
			t.Errorf("accepted invalid %+v", p)
		}
	}
	end, deliver := "06:30", "06:45"
	if err := e.s.SetNight(NightPatch{End: &end, Deliver: &deliver}); err != nil {
		t.Fatal(err)
	}
}

func TestDigestFormatting(t *testing.T) {
	long := strings.Repeat("mot ", 200)
	items := []heldItem{{1, local(8, 23, 0).UnixMilli(), long}}
	for i := 0; i < 40; i++ {
		items = append(items, heldItem{int64(i + 2), local(9, 1, 0).UnixMilli(), "message numéro court " + strings.Repeat("x", 100)})
	}
	d := digestText("morning", items, zurich, local(9, 7, 30))
	if len([]rune(d)) > digestMaxRunes+300 {
		t.Fatalf("digest too long: %d", len([]rune(d)))
	}
	if !strings.Contains(d, "voir le fil") || !strings.Contains(d, "…") {
		t.Fatalf("expected truncation notes: %q", d)
	}
}
