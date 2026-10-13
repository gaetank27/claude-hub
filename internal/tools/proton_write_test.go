package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	proton "github.com/ProtonMail/go-proton-api"
	pgpcrypto "github.com/ProtonMail/gopenpgp/v2/crypto"
)

// fakeProtonCalendar is a minimal Proton Calendar API with real crypto: an
// address key, a calendar key locked by a passphrase encrypted to the address
// key, and an event store fed by PUT /events/sync. It also checks, like the real
// server would, that every part is signed by the address key.
type fakeProtonCalendar struct {
	t        *testing.T
	mu       sync.Mutex
	addrKR   *pgpcrypto.KeyRing
	calKey   string // armored locked calendar key
	pass     string // armored encrypted passphrase
	passSig  string
	events   map[string]proton.CalendarEvent
	next     int
	puts     int
	authSeen string
	failWith *proton.APIError // next sync answers this HTTP error
	extra    []proton.CalendarEvent
}

const fakeCalEmail = "me@proton.test"

func newFakeProtonCalendar(t *testing.T) (*fakeProtonCalendar, *protonSession) {
	t.Helper()
	f := &fakeProtonCalendar{t: t, events: map[string]proton.CalendarEvent{}}
	ak, err := pgpcrypto.GenerateKey("me", fakeCalEmail, "x25519", 0)
	must(t, err)
	f.addrKR, err = pgpcrypto.NewKeyRing(ak)
	must(t, err)
	calKey, err := pgpcrypto.GenerateKey("cal", "cal@x", "x25519", 0)
	must(t, err)
	passphrase := []byte("cal-passphrase")
	locked, err := calKey.Lock(passphrase)
	must(t, err)
	f.calKey, err = locked.Armor()
	must(t, err)
	enc, err := f.addrKR.Encrypt(pgpcrypto.NewPlainMessage(passphrase), nil)
	must(t, err)
	f.pass, err = enc.GetArmored()
	must(t, err)
	sig, err := f.addrKR.SignDetached(pgpcrypto.NewPlainMessage(passphrase))
	must(t, err)
	f.passSig, err = sig.GetArmored()
	must(t, err)

	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	old := protonHostURL
	protonHostURL = srv.URL
	t.Cleanup(func() { protonHostURL = old })

	m := proton.New(proton.WithHostURL(srv.URL), proton.WithAppVersion(protonDefaultAppVersion))
	t.Cleanup(m.Close)
	client := m.NewClient("uid-1", "acc-1", "ref-1")
	sess := &protonSession{manager: m, client: client, addrKRs: map[string]*pgpcrypto.KeyRing{fakeCalEmail: f.addrKR}}
	sess.setAuth("uid-1", "acc-1")
	return f, sess
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fakeProtonCalendar) reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeProtonCalendar) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/calendar/v1")
	switch {
	case p == "":
		f.reply(w, map[string]any{"Code": 1000, "Calendars": []proton.Calendar{{ID: "cal1", Name: "Perso"}}})
	case p == "/cal1/members":
		f.reply(w, map[string]any{"Code": 1000, "Members": []proton.CalendarMember{{ID: "mem1", Email: fakeCalEmail}}})
	case p == "/cal1/passphrase":
		f.reply(w, map[string]any{"Code": 1000, "Passphrase": proton.CalendarPassphrase{
			ID: "p1", MemberPassphrases: []proton.MemberPassphrase{{MemberID: "mem1", Passphrase: f.pass, Signature: f.passSig}}}})
	case p == "/cal1/keys":
		f.reply(w, map[string]any{"Code": 1000, "Keys": []proton.CalendarKey{{ID: "k1", CalendarID: "cal1", PrivateKey: f.calKey}}})
	case p == "/cal1/events/count":
		f.reply(w, map[string]any{"Code": 1000, "Total": len(f.events) + len(f.extra)})
	case p == "/cal1/events" && r.Method == http.MethodGet:
		var list []proton.CalendarEvent
		for _, e := range f.events {
			list = append(list, e)
		}
		list = append(list, f.extra...)
		f.reply(w, map[string]any{"Code": 1000, "Events": list})
	case strings.HasPrefix(p, "/cal1/events/") && p != "/cal1/events/sync" && r.Method == http.MethodGet:
		id := strings.TrimPrefix(p, "/cal1/events/")
		for _, e := range append(f.extra, f.eventList()...) {
			if e.ID == id {
				f.reply(w, map[string]any{"Code": 1000, "Event": e})
				return
			}
		}
		w.WriteHeader(422)
		f.reply(w, map[string]any{"Code": 2501, "Error": "event does not exist"})
	case p == "/cal1/events/sync" && r.Method == http.MethodPut:
		f.sync(w, r)
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(404)
	}
}

func (f *fakeProtonCalendar) eventList() []proton.CalendarEvent {
	var l []proton.CalendarEvent
	for _, e := range f.events {
		l = append(l, e)
	}
	return l
}

func (f *fakeProtonCalendar) sync(w http.ResponseWriter, r *http.Request) {
	f.puts++
	f.authSeen = r.Header.Get("Authorization") + "|" + r.Header.Get("x-pm-uid") + "|" + r.Header.Get("x-pm-appversion")
	if f.failWith != nil {
		e := f.failWith
		f.failWith = nil
		w.WriteHeader(e.Status)
		f.reply(w, map[string]any{"Code": e.Code, "Error": e.Message})
		return
	}
	var body struct {
		MemberID string
		Events   []struct {
			ID             string
			Overwrite      *int
			DeletionReason *int
			Event          *struct {
				Permissions        int
				SharedKeyPacket    string
				SharedEventContent []struct {
					Type            int
					Data, Signature string
				}
				Notifications any
			}
		}
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.t.Errorf("bad sync body: %v", err)
	}
	if body.MemberID != "mem1" {
		f.t.Errorf("MemberID = %q", body.MemberID)
	}
	var responses []map[string]any
	for i, it := range body.Events {
		resp := map[string]any{"Code": 1000}
		switch {
		case it.Event != nil && it.ID == "":
			f.next++
			ev := proton.CalendarEvent{ID: fmt.Sprintf("ev%d", f.next), CalendarID: "cal1", SharedKeyPacket: it.Event.SharedKeyPacket, Author: fakeCalEmail}
			ev.SharedEvents = f.parts(it.Event.SharedEventContent)
			f.events[ev.ID] = ev
			resp["Event"] = ev
		case it.Event != nil:
			ev, ok := f.events[it.ID]
			if !ok {
				ev, ok = f.byExtra(it.ID)
			}
			if !ok {
				resp = map[string]any{"Code": 2501, "Error": "unknown event"}
				break
			}
			if it.Event.SharedKeyPacket != "" {
				f.t.Errorf("update must reuse the session key, got a new SharedKeyPacket")
			}
			ev.SharedEvents = f.parts(it.Event.SharedEventContent)
			f.events[ev.ID] = ev
			resp["Event"] = ev
		case it.DeletionReason != nil:
			if _, ok := f.events[it.ID]; !ok {
				resp = map[string]any{"Code": 2501, "Error": "unknown event"}
				break
			}
			delete(f.events, it.ID)
		}
		responses = append(responses, map[string]any{"Index": i, "Response": resp})
	}
	f.reply(w, map[string]any{"Code": 1001, "Responses": responses})
}

func (f *fakeProtonCalendar) byExtra(id string) (proton.CalendarEvent, bool) {
	for _, e := range f.extra {
		if e.ID == id {
			return e, true
		}
	}
	return proton.CalendarEvent{}, false
}

// parts verifies the detached signatures (over the plaintext, by the address
// key) the way Proton does, then stores the parts as the API returns them.
func (f *fakeProtonCalendar) parts(in []struct {
	Type            int
	Data, Signature string
}) []proton.CalendarEventPart {
	var out []proton.CalendarEventPart
	for _, p := range in {
		if p.Type&int(proton.CalendarEventTypeSigned) == 0 || p.Signature == "" {
			f.t.Errorf("unsigned part type=%d", p.Type)
		}
		out = append(out, proton.CalendarEventPart{Type: proton.CalendarEventType(p.Type), Data: p.Data, Signature: p.Signature, Author: fakeCalEmail})
	}
	return out
}

// verifyAll decrypts every stored part and checks its signature.
func (f *fakeProtonCalendar) verifyAll(sess *protonSession, calID string) {
	cc, err := protonCalContext(context.Background(), sess, calID)
	must(f.t, err)
	for _, e := range f.events {
		for _, p := range e.SharedEvents {
			plain := p.Data
			if p.Type&proton.CalendarEventTypeEncrypted != 0 {
				plain, err = protonDecryptPart(p.Data, e.SharedKeyPacket, cc.calKR)
				must(f.t, err)
			}
			sig, err := pgpcrypto.NewPGPSignatureFromArmored(p.Signature)
			must(f.t, err)
			if err := f.addrKR.VerifyDetached(pgpcrypto.NewPlainMessageFromString(plain), sig, pgpcrypto.GetUnixTime()); err != nil {
				f.t.Errorf("bad signature on part type=%d: %v", p.Type, err)
			}
		}
	}
}

func listAll(t *testing.T, sess *protonSession) string {
	t.Helper()
	loc := protonLocation()
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, loc)
	// The fake returns every stored event whatever the window.
	out, err := protonListEvents(context.Background(), sess, "cal1", from, from.AddDate(0, 3, 0))
	must(t, err)
	return out
}

func TestProtonWriteCreateUpdateDeleteAgainstFake(t *testing.T) {
	f, sess := newFakeProtonCalendar(t)
	ctx := context.Background()

	out, err := protonCreateEvent(ctx, sess, protonDefaultAppVersion, "cal1", map[string]any{
		"title": "Dentiste, Dr; Müller", "start": "2026-10-20T14:30", "location": "Rue 1", "description": "ligne1\nligne2",
	})
	must(t, err)
	if !strings.Contains(out, "id=ev1") {
		t.Fatalf("create output: %s", out)
	}
	if f.authSeen != "Bearer acc-1|uid-1|"+protonDefaultAppVersion {
		t.Fatalf("auth headers: %q", f.authSeen)
	}
	listed := listAll(t, sess)
	for _, want := range []string{"2026-10-20 14:30–15:30", "Dentiste, Dr; Müller", "@ Rue 1", "ligne1\n      ligne2"} {
		if !strings.Contains(listed, want) {
			t.Fatalf("read-back misses %q:\n%s", want, listed)
		}
	}
	f.verifyAll(sess, "cal1")
	kp1 := f.events["ev1"].SharedKeyPacket

	// Update: only the time and the title change, the rest is kept, SEQUENCE bumps.
	if _, err := protonUpdateEvent(ctx, sess, protonDefaultAppVersion, "cal1", "ev1", map[string]any{
		"title": "Dentiste (reporté)", "start": "2026-10-21T09:00",
	}); err != nil {
		t.Fatal(err)
	}
	listed = listAll(t, sess)
	for _, want := range []string{"2026-10-21 09:00–10:00", "Dentiste (reporté)", "@ Rue 1"} {
		if !strings.Contains(listed, want) {
			t.Fatalf("after update misses %q:\n%s", want, listed)
		}
	}
	if strings.Contains(listed, "2026-10-20") {
		t.Fatalf("old date still listed:\n%s", listed)
	}
	if f.events["ev1"].SharedKeyPacket != kp1 {
		t.Fatal("session key packet changed on update")
	}
	cc, _ := protonCalContext(ctx, sess, "cal1")
	ics := protonEventICS(f.events["ev1"], cc.calKR)
	if p, _ := ics.first("SEQUENCE"); p.Value != "1" {
		t.Fatalf("SEQUENCE = %q, want 1", p.Value)
	}
	f.verifyAll(sess, "cal1")

	// Delete.
	if out, err = protonDeleteEvent(ctx, sess, protonDefaultAppVersion, "cal1", "ev1"); err != nil || !strings.Contains(out, "Dentiste (reporté)") {
		t.Fatalf("delete: %v %s", err, out)
	}
	if len(f.events) != 0 {
		t.Fatal("event still stored")
	}
}

func TestProtonWriteAllDayEvent(t *testing.T) {
	f, sess := newFakeProtonCalendar(t)
	_, err := protonCreateEvent(context.Background(), sess, protonDefaultAppVersion, "cal1", map[string]any{
		"title": "Vacances", "start": "2026-10-26", "end": "2026-10-28",
	})
	must(t, err)
	out := listAll(t, sess)
	if !strings.Contains(out, "2026-10-26 → 2026-10-28 (all day)") {
		t.Fatalf("all-day range wrong:\n%s", out)
	}
	f.verifyAll(sess, "cal1")
}

func TestProtonWriteRefusesRiskyEvents(t *testing.T) {
	f, sess := newFakeProtonCalendar(t)
	cc, err := protonCalContext(context.Background(), sess, "cal1")
	must(t, err)
	mk := func(id, ics string, attendees int) {
		kp, parts, err := protonEncryptShared(cc, nil, icsWrap("UID:"+id, "DTSTART:20261020T100000Z", "DTEND:20261020T110000Z"+ics), icsWrap("SUMMARY:x"))
		must(t, err)
		ev := proton.CalendarEvent{ID: id, SharedKeyPacket: kp}
		for _, p := range parts {
			ev.SharedEvents = append(ev.SharedEvents, proton.CalendarEventPart{Type: proton.CalendarEventType(p.Type), Data: p.Data, Signature: p.Signature})
		}
		ev.Attendees = make([]proton.CalendarAttendee, attendees)
		f.extra = append(f.extra, ev)
	}
	mk("rec", "\r\nRRULE:FREQ=WEEKLY", 0)
	mk("inv", "", 1)
	for _, id := range []string{"rec", "inv"} {
		if _, err := protonDeleteEvent(context.Background(), sess, protonDefaultAppVersion, "cal1", id); err == nil {
			t.Errorf("%s: delete should be refused", id)
		}
		if _, err := protonUpdateEvent(context.Background(), sess, protonDefaultAppVersion, "cal1", id, map[string]any{"title": "y"}); err == nil {
			t.Errorf("%s: update should be refused", id)
		}
	}
	if f.puts != 0 {
		t.Fatalf("a refused event must not reach the API, got %d PUT", f.puts)
	}
}

func TestProtonWriteValidation(t *testing.T) {
	bad := []map[string]any{
		{"start": "2026-10-20T10:00"},                                    // no title
		{"title": "x"},                                                   // no start
		{"title": "x", "start": "demain"},                                // bad date
		{"title": "x", "start": "2026-10-20T10:00", "end": "2026-10-20"}, // mixed
		{"title": "x", "start": "2026-10-20T10:00", "end": "2026-10-20T09:00"},
	}
	for i, a := range bad {
		if _, err := protonBuildSpec(a, nil); err == nil {
			t.Errorf("case %d accepted: %v", i, a)
		}
	}
	// Argument errors are caught before any session or network use.
	fakeConfig(t, nil)
	if _, err := protonWriteCall(context.Background(), map[string]string{}, "proton_create_event", map[string]any{"start": "2026-10-20"}); err == nil || strings.Contains(err.Error(), "connecté") {
		t.Fatalf("want a validation error, got %v", err)
	}
	if _, err := protonWriteCall(context.Background(), map[string]string{}, "proton_delete_event", map[string]any{}); err == nil || !strings.Contains(err.Error(), "event_id") {
		t.Fatalf("want event_id error, got %v", err)
	}
}

func TestProtonWriteAntiAbuseStartsPauseAndIsNotRetried(t *testing.T) {
	f, sess := newFakeProtonCalendar(t)
	f.failWith = &proton.APIError{Status: 422, Code: 9001, Message: "Human verification required"}
	_, err := protonCreateEvent(context.Background(), sess, protonDefaultAppVersion, "cal1", map[string]any{"title": "x", "start": "2026-10-20T10:00"})
	if err == nil || protonClassify(err) != protonErrHuman {
		t.Fatalf("want a human-verification error, got %v", err)
	}
	if f.puts != 1 || len(f.events) != 0 {
		t.Fatalf("puts=%d events=%d", f.puts, len(f.events))
	}
}

func TestProtonPickCalendar(t *testing.T) {
	_, sess := newFakeProtonCalendar(t)
	if id, err := protonPickCalendar(context.Background(), sess, ""); err != nil || id != "cal1" {
		t.Fatalf("single calendar: %q %v", id, err)
	}
	if id, _ := protonPickCalendar(context.Background(), sess, "other"); id != "other" {
		t.Fatal("explicit calendar_id ignored")
	}
}
