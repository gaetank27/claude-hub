package assistant

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"system/internal/store"
)

// Night mode ("ne pas déranger") : during the quiet range, a message the assistant
// writes on its own initiative (task, place, message trigger, delegated-session
// wake-up) is kept in the thread, marked as held, but neither sent on WhatsApp nor
// pushed — unless the user wrote to the assistant within the idle delay. Held
// messages are delivered as one digest at the delivery time, or at once as a
// catch-up when the user writes. Direct replies are never held.
const (
	nightOnKey      = "assistant_night"         // "0" = off (default on)
	nightStartKey   = "assistant_night_start"   // HH:MM local
	nightEndKey     = "assistant_night_end"     // HH:MM local
	nightDeliverKey = "assistant_night_deliver" // HH:MM local
	nightIdleKey    = "assistant_night_idle"    // minutes
	nightTZKey      = "assistant_night_tz"

	DefaultNightStart   = "22:00"
	DefaultNightEnd     = "07:00"
	DefaultNightDeliver = "07:30"
	DefaultNightIdle    = 20
	DefaultNightTZ      = "Europe/Zurich"

	digestItemRunes = 300  // longest excerpt of one held message in a digest
	digestMaxRunes  = 3000 // longest digest text; the rest is referred to the thread
)

// NightSettings are the stored settings of the mode.
type NightSettings struct {
	Enabled     bool
	Start, End  string // HH:MM
	Deliver     string // HH:MM
	IdleMinutes int
	Timezone    string
}

// clock is the current time (replaced in tests).
func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Service) metaOr(key, def string) string {
	if v, _ := s.db.GetAppMeta(key); v != "" {
		return v
	}
	return def
}

// Night returns the settings, defaults filled in.
func (s *Service) Night() NightSettings {
	idle := DefaultNightIdle
	if n, err := strconv.Atoi(s.metaOr(nightIdleKey, "")); err == nil && n > 0 {
		idle = n
	}
	v, _ := s.db.GetAppMeta(nightOnKey)
	return NightSettings{
		Enabled: v != "0", Start: s.metaOr(nightStartKey, DefaultNightStart), End: s.metaOr(nightEndKey, DefaultNightEnd),
		Deliver: s.metaOr(nightDeliverKey, DefaultNightDeliver), IdleMinutes: idle, Timezone: s.metaOr(nightTZKey, DefaultNightTZ),
	}
}

// NightPatch carries the optional fields of a settings update.
type NightPatch struct {
	Enabled     *bool
	Start, End  *string
	Deliver     *string
	IdleMinutes *int
	Timezone    *string
}

// SetNight validates and stores the changed settings; held messages are
// rescheduled to the new delivery time.
func (s *Service) SetNight(p NightPatch) error {
	n := s.Night()
	if p.Enabled != nil {
		n.Enabled = *p.Enabled
	}
	if p.Start != nil {
		n.Start = *p.Start
	}
	if p.End != nil {
		n.End = *p.End
	}
	if p.Deliver != nil {
		n.Deliver = *p.Deliver
	}
	if p.IdleMinutes != nil {
		n.IdleMinutes = *p.IdleMinutes
	}
	if p.Timezone != nil {
		n.Timezone = *p.Timezone
	}
	if err := n.validate(); err != nil {
		return err
	}
	flag := "1"
	if !n.Enabled {
		flag = "0"
	}
	for k, v := range map[string]string{nightOnKey: flag, nightStartKey: n.Start, nightEndKey: n.End,
		nightDeliverKey: n.Deliver, nightIdleKey: strconv.Itoa(n.IdleMinutes), nightTZKey: n.Timezone} {
		if err := s.db.SetAppMeta(k, v); err != nil {
			return err
		}
	}
	s.rescheduleHeld()
	return nil
}

func parseHHMM(v string) (int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("time must be HH:MM, got %q", v)
	}
	return t.Hour()*60 + t.Minute(), nil
}

func (n NightSettings) validate() error {
	if _, err := time.LoadLocation(n.Timezone); err != nil || n.Timezone == "" {
		return fmt.Errorf("unknown timezone %q", n.Timezone)
	}
	for _, v := range []string{n.Start, n.End, n.Deliver} {
		if _, err := parseHHMM(v); err != nil {
			return err
		}
	}
	if n.IdleMinutes < 1 || n.IdleMinutes > 720 {
		return fmt.Errorf("idle delay must be between 1 and 720 minutes")
	}
	d, _ := parseHHMM(n.Deliver)
	if n.quietMinute(d) {
		return fmt.Errorf("delivery time %s must be outside the quiet range %s–%s", n.Deliver, n.Start, n.End)
	}
	return nil
}

func (n NightSettings) loc() *time.Location {
	if l, err := time.LoadLocation(n.Timezone); err == nil {
		return l
	}
	l, _ := time.LoadLocation(DefaultNightTZ)
	return l
}

// quietMinute: is the minute of the day inside [start, end) (wrapping midnight)?
func (n NightSettings) quietMinute(m int) bool {
	st, _ := parseHHMM(n.Start)
	en, _ := parseHHMM(n.End)
	switch {
	case st == en:
		return false
	case st < en:
		return m >= st && m < en
	default:
		return m >= st || m < en
	}
}

// InQuiet reports whether t falls in the quiet range, in the user's timezone
// (wall clock, so a daylight-saving change moves nothing).
func (n NightSettings) InQuiet(t time.Time) bool {
	l := t.In(n.loc())
	return n.quietMinute(l.Hour()*60 + l.Minute())
}

// NextDeliver is the first delivery time strictly after t.
func (n NightSettings) NextDeliver(t time.Time) time.Time {
	loc := n.loc()
	d, _ := parseHHMM(n.Deliver)
	l := t.In(loc)
	at := time.Date(l.Year(), l.Month(), l.Day(), d/60, d%60, 0, 0, loc)
	if !at.After(t) {
		at = time.Date(l.Year(), l.Month(), l.Day()+1, d/60, d%60, 0, 0, loc)
	}
	return at
}

// userAwake: the user wrote to the assistant (any channel) within the idle delay.
func (s *Service) userAwake(n NightSettings, now time.Time) bool {
	ts, err := s.db.LastAssistantUserMessage()
	if err != nil || ts == 0 {
		return false
	}
	return now.Sub(time.UnixMilli(ts)) < time.Duration(n.IdleMinutes)*time.Minute
}

// spontaneous: the message was not written in answer to the user.
func spontaneous(o origin) bool {
	switch o.channel {
	case "task", "place", "message", "session":
		return true
	}
	return false
}

// holdUntil says whether a spontaneous message written now must be held, and
// until when.
func (s *Service) holdUntil(o origin) (time.Time, bool) {
	if !spontaneous(o) {
		return time.Time{}, false
	}
	n, now := s.Night(), s.clock()
	if !n.Enabled || !n.InQuiet(now) || s.userAwake(n, now) {
		return time.Time{}, false
	}
	return n.NextDeliver(now), true
}

// hold records a message in the thread as held; nothing is sent.
func (s *Service) hold(o origin, text string, until time.Time) {
	tool, jid := s.waTarget(o)
	m, err := s.db.AddHeldAssistantMessage(o.channel, text, s.clock().UnixMilli(), until.UnixMilli(), tool, jid)
	if err != nil {
		log.Printf("assistant: hold message: %v", err)
		return
	}
	s.linkImages(m.ID, o.images) // held with it: sent with the digest
	log.Printf("assistant: night mode: message held until %s", until.In(s.Night().loc()).Format("2006-01-02 15:04 MST"))
}

// rescheduleHeld moves every held message to the next delivery time after now
// under the current settings (delivery time changed); a disabled mode makes
// them due at once (the next sweep delivers them).
func (s *Service) rescheduleHeld() {
	held, err := s.db.HeldAssistantMessages()
	if err != nil || len(held) == 0 {
		return
	}
	n := s.Night()
	until := n.NextDeliver(s.clock()).UnixMilli()
	if !n.Enabled {
		until = 1 // due now
	}
	for _, m := range held {
		_ = s.db.RescheduleAssistantMessage(m.ID, until)
	}
}

// HeldCount is the number of messages waiting for delivery.
func (s *Service) HeldCount() int {
	held, _ := s.db.HeldAssistantMessages()
	return len(held)
}

// sweepHeld delivers the held messages that are due (delivery time reached, or
// the mode switched off). It also runs at start, which catches up a delivery
// time that passed while the backend was stopped.
func (s *Service) sweepHeld() {
	held, err := s.db.HeldAssistantMessages()
	if err != nil || len(held) == 0 {
		return
	}
	now := s.clock().UnixMilli()
	due := false
	for _, m := range held {
		if m.HeldUntil <= now {
			due = true
		}
	}
	if due {
		s.flushHeld("morning")
	}
}

// flushHeld delivers every held message now, as one digest per WhatsApp target.
// kind is "morning" (delivery time) or "catchup" (the user wrote).
func (s *Service) flushHeld(kind string) {
	s.hmu.Lock()
	defer s.hmu.Unlock()
	held, err := s.db.HeldAssistantMessages()
	if err != nil || len(held) == 0 {
		return
	}
	type key struct{ tool, jid string }
	var order []key
	groups := map[key][]heldItem{}
	images := map[key][]store.AssistantAttachment{}
	for _, m := range held {
		k := key{m.HeldTool, m.HeldJID}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		text := m.Text
		if n := liveImages(m.Attachments); len(n) > 0 {
			text = strings.TrimSpace(text + fmt.Sprintf(" 📷 %d image%s", len(n), map[bool]string{true: "s", false: ""}[len(n) > 1]))
		}
		groups[k] = append(groups[k], heldItem{m.ID, m.TS, text})
		images[k] = append(images[k], liveImages(m.Attachments)...)
	}
	loc, now := s.Night().loc(), s.clock()
	for _, k := range order {
		items := groups[k]
		text := digestText(kind, items, loc, now)
		ids := make([]int64, len(items))
		for i, it := range items {
			ids[i] = it.id
		}
		// Mark first, then send: the mutex keeps a second flush out, and a failed
		// send falls back to the phone push like any reply.
		if _, err := s.db.ReleaseAssistantMessages(ids); err != nil {
			log.Printf("assistant: release held: %v", err)
			continue
		}
		log.Printf("assistant: night mode: %s digest of %d message(s)", kind, len(items))
		s.dispatch(k.tool, k.jid, text, digestPush(items, loc, now))
		s.sendWhatsAppImages(k.tool, k.jid, images[k])
	}
}

type heldItem struct {
	id   int64
	ts   int64
	text string
}

// digestText lays held messages out for a phone: a header, one bullet per
// message with its time, long ones cut at a word, the whole capped.
func digestText(kind string, items []heldItem, loc *time.Location, now time.Time) string {
	var b strings.Builder
	title := "🌅 Récapitulatif de la nuit"
	if kind == "catchup" {
		title = "🔁 Rattrapage"
	}
	plural := "s"
	if len(items) == 1 {
		plural = ""
	}
	fmt.Fprintf(&b, "%s — %d message%s retenu%s\n", title, len(items), plural, plural)
	shown := 0
	for _, it := range items {
		line := fmt.Sprintf("\n• %s — %s\n", heldTime(it.ts, loc, now), cutWords(it.text, digestItemRunes))
		if shown > 0 && b.Len()+len(line) > digestMaxRunes {
			break
		}
		b.WriteString(line)
		shown++
	}
	if rest := len(items) - shown; rest > 0 {
		fmt.Fprintf(&b, "\n… et %d autre%s : voir le fil de l'assistant.", rest, map[bool]string{true: "s", false: ""}[rest > 1])
	} else if shown < len(items) || anyCut(items) {
		b.WriteString("\n(Messages abrégés : texte complet dans le fil de l'assistant.)")
	}
	return strings.TrimSpace(b.String())
}

func anyCut(items []heldItem) bool {
	for _, it := range items {
		if len([]rune(strings.Join(strings.Fields(it.text), " "))) > digestItemRunes {
			return true
		}
	}
	return false
}

// heldTime is "HH:MM", with the date when the message is more than a day old.
func heldTime(ts int64, loc *time.Location, now time.Time) string {
	t := time.UnixMilli(ts).In(loc)
	if now.Sub(t) > 24*time.Hour {
		return t.Format("02.01 15:04")
	}
	return t.Format("15:04")
}

// cutWords flattens t to one line and cuts it at a word boundary.
func cutWords(t string, max int) string {
	t = strings.Join(strings.Fields(t), " ")
	r := []rune(t)
	if len(r) <= max {
		return t
	}
	cut := string(r[:max])
	if i := strings.LastIndex(cut, " "); i > max/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:.-") + "…"
}

// digestPush is the short text of the phone notification of a digest.
func digestPush(items []heldItem, loc *time.Location, now time.Time) string {
	if len(items) == 1 {
		return heldTime(items[0].ts, loc, now) + " — " + items[0].text
	}
	return fmt.Sprintf("%d messages retenus — %s", len(items), items[len(items)-1].text)
}
