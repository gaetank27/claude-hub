package tools

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	proton "github.com/ProtonMail/go-proton-api"
	"github.com/ProtonMail/go-proton-api/server"
	pgpcrypto "github.com/ProtonMail/gopenpgp/v2/crypto"
)

// fakeConfig wires an in-memory config updater and returns the live map.
func fakeConfig(t *testing.T, initial map[string]string) map[string]string {
	t.Helper()
	var mu sync.Mutex
	cfg := map[string]string{}
	for k, v := range initial {
		cfg[k] = v
	}
	SetConfigUpdater(func(_ string, mutate func(map[string]string)) error {
		mu.Lock()
		defer mu.Unlock()
		mutate(cfg)
		return nil
	})
	t.Cleanup(func() { SetConfigUpdater(nil) })
	return cfg
}

func snapshot(cfg map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range cfg {
		out[k] = v
	}
	return out
}

func TestProtonPauseDelayGrows(t *testing.T) {
	prev := time.Duration(0)
	for n := 1; n <= 7; n++ {
		d := protonPauseDelay(n)
		if d < prev {
			t.Fatalf("delay shrank at n=%d", n)
		}
		prev = d
	}
	if protonPauseDelay(1) != 15*time.Minute || protonPauseDelay(99) != 24*time.Hour {
		t.Fatalf("unexpected bounds: %v %v", protonPauseDelay(1), protonPauseDelay(99))
	}
}

func TestProtonClassify(t *testing.T) {
	wrap := func(status int, code proton.Code) error {
		return fmt.Errorf("%d POST x: %w", status, &proton.APIError{Status: status, Code: code})
	}
	cases := []struct {
		err  error
		want protonErrKind
	}{
		{wrap(422, 2028), protonErrAbuse},
		{wrap(429, 0), protonErrAbuse},
		{wrap(422, proton.HumanVerificationRequired), protonErrHuman},
		{wrap(400, proton.AuthRefreshTokenInvalid), protonErrSessionDead},
		{wrap(401, 0), protonErrSessionDead},
		{wrap(400, proton.AppVersionBadCode), protonErrAppVersion},
		{wrap(400, 5002), protonErrAppVersion},
		{wrap(400, proton.AppVersionMissingCode), protonErrAppVersion},
		{wrap(422, proton.PasswordWrong), protonErrBadCreds},
		{wrap(503, 0), protonErrTransient},
		{&proton.NetError{Cause: errors.New("boom"), Message: "x"}, protonErrTransient},
		{errors.New("other"), protonErrOther},
	}
	for i, c := range cases {
		if got := protonClassify(c.err); got != c.want {
			t.Errorf("case %d: got %v want %v", i, got, c.want)
		}
	}
}

func TestProtonAbuseStartsGrowingPauseWithoutRetry(t *testing.T) {
	live := fakeConfig(t, map[string]string{"username": "u", pkUID: "uid", pkRefresh: "r", pkKeyPass: "a2V5"})
	err2028 := fmt.Errorf("422 POST x: %w", &proton.APIError{Status: 422, Code: 2028, Message: "unusual activity"})
	now := time.Now()

	msg1 := protonRecordFailure("tool", snapshot(live), err2028, now)
	if msg1 == nil || !strings.Contains(msg1.Error(), "pause") {
		t.Fatalf("expected a pause message, got %v", msg1)
	}
	until1, paused := protonPausedUntil(live, now)
	if !paused || until1.Sub(now) < 14*time.Minute {
		t.Fatalf("expected ~15min pause, got %v %v", until1, paused)
	}
	// A call during the pause must not reach the network: no manager is built,
	// so a bogus host would fail loudly if it did.
	protonHostURL = "http://127.0.0.1:1"
	defer func() { protonHostURL = "https://mail.proton.me/api" }()
	if _, err := protonGetSession(context.Background(), snapshot(live), "tool"); err == nil || !strings.Contains(err.Error(), "pause") {
		t.Fatalf("expected pause error, got %v", err)
	}
	// Second failure doubles down.
	_ = protonRecordFailure("tool", snapshot(live), err2028, now)
	until2, _ := protonPausedUntil(live, now)
	if !until2.After(until1) {
		t.Fatalf("pause did not grow: %v then %v", until1, until2)
	}
}

func TestProtonDeadSessionClearsTokens(t *testing.T) {
	live := fakeConfig(t, map[string]string{"username": "u", pkUID: "uid", pkRefresh: "r", pkKeyPass: "a2V5"})
	dead := fmt.Errorf("400 POST x: %w", &proton.APIError{Status: 400, Code: proton.AuthRefreshTokenInvalid})
	err := protonRecordFailure("tool", snapshot(live), dead, time.Now())
	if err == nil || !strings.Contains(err.Error(), "Connecter le compte") {
		t.Fatalf("expected reconnect instructions, got %v", err)
	}
	if live[pkUID] != "" || live[pkRefresh] != "" {
		t.Fatal("dead session tokens should be dropped")
	}
	if ProtonStatusOf(live).Connected {
		t.Fatal("must read as disconnected")
	}
	if _, perr := protonPausedUntil(live, time.Now()); perr {
		t.Fatal("a dead session needs a reconnect, not a pause")
	}
}

func TestProtonNotConnected(t *testing.T) {
	_, err := protonGetSession(context.Background(), map[string]string{"username": "u"}, "tool")
	if err == nil || !strings.Contains(err.Error(), "n'est pas connecté") {
		t.Fatalf("got %v", err)
	}
}

func TestProtonRangeValidation(t *testing.T) {
	from, to, err := protonRange("2026-10-12", "2026-10-18")
	if err != nil || to.Sub(from) != 7*24*time.Hour {
		t.Fatalf("range: %v %v %v", from, to, err)
	}
	for _, c := range [][2]string{{"", ""}, {"nope", ""}, {"2026-10-12", "2026-10-01"}, {"2026-01-01", "2026-12-31"}} {
		if _, _, err := protonRange(c[0], c[1]); err == nil {
			t.Errorf("expected error for %v", c)
		}
	}
	// Invalid arguments are rejected before any session lookup.
	_, err = protonConnector{}.Call(context.Background(), map[string]string{}, "proton_list_events", map[string]any{"start": "x"})
	if err == nil || !strings.Contains(err.Error(), "invalid start") {
		t.Fatalf("got %v", err)
	}
}

// ---- calendar rendering ----

func ev(startUnix, endUnix int64, fullDay bool, ics string) (proton.CalendarEvent, icsEvent) {
	e := proton.CalendarEvent{ID: "evt1", UID: "uid1", StartTime: startUnix, EndTime: endUnix, FullDay: proton.Bool(fullDay)}
	i := icsEvent{}
	parseICSInto(i, ics)
	return e, i
}

func TestParseICSAndRendering(t *testing.T) {
	ics := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:uid1\r\nSUMMARY:Point\\, équipe\r\nLOCATION:Salle A\r\nDESCRIPTION:ligne 1\\nligne 2 qui est\r\n  repliée\r\nDTSTART;TZID=Europe/Zurich:20261012T090000\r\nDTEND;TZID=Europe/Zurich:20261012T100000\r\nBEGIN:VALARM\r\nTRIGGER:-PT10M\r\nSUMMARY:alarm\r\nEND:VALARM\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	e, i := ev(0, 0, false, ics)
	from, to, _ := protonRange("2026-10-12", "2026-10-12")
	occ := protonOccurrences(e, i, from, to, nil)
	if len(occ) != 1 {
		t.Fatalf("occurrences: %d", len(occ))
	}
	out := protonFormatOccurrences(occ)
	for _, want := range []string{"2026-10-12 09:00–10:00", "Point, équipe", "@ Salle A", "ligne 1\n      ligne 2 qui est repliée"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "alarm") {
		t.Error("VALARM content leaked into the event")
	}
}

func TestUTCEventShownInZurich(t *testing.T) {
	e, i := ev(0, 0, false, "BEGIN:VEVENT\nSUMMARY:x\nDTSTART:20261012T070000Z\nDTEND:20261012T080000Z\nEND:VEVENT")
	from, to, _ := protonRange("2026-10-12", "")
	out := protonFormatOccurrences(protonOccurrences(e, i, from, to, nil))
	if !strings.Contains(out, "09:00–10:00") { // CEST = UTC+2
		t.Fatalf("expected Zurich local time:\n%s", out)
	}
}

func TestAllDayEvents(t *testing.T) {
	e, i := ev(0, 0, true, "BEGIN:VEVENT\nSUMMARY:Vacances\nDTSTART;VALUE=DATE:20261012\nDTEND;VALUE=DATE:20261015\nEND:VEVENT")
	from, to, _ := protonRange("2026-10-14", "2026-10-14")
	out := protonFormatOccurrences(protonOccurrences(e, i, from, to, nil))
	if !strings.Contains(out, "2026-10-12 → 2026-10-14 (all day)") {
		t.Fatalf("all-day range wrong (DTEND is exclusive):\n%s", out)
	}
	// The day after the (exclusive) end is outside.
	from, to, _ = protonRange("2026-10-15", "2026-10-15")
	if len(protonOccurrences(e, i, from, to, nil)) != 0 {
		t.Fatal("event must not overlap its exclusive end date")
	}
}

func TestRecurringExpansionAcrossDST(t *testing.T) {
	// Weekly Monday 09:00 Zurich, DST ends 2026-10-25.
	e, i := ev(0, 0, false, "BEGIN:VEVENT\nUID:uid1\nSUMMARY:Hebdo\nDTSTART;TZID=Europe/Zurich:20261019T090000\nDTEND;TZID=Europe/Zurich:20261019T093000\nRRULE:FREQ=WEEKLY;BYDAY=MO\nEXDATE;TZID=Europe/Zurich:20261102T090000\nEND:VEVENT")
	from, to, _ := protonRange("2026-10-19", "2026-11-09")
	occ := protonOccurrences(e, i, from, to, nil)
	if len(occ) != 3 { // 19 Oct, 26 Oct, 9 Nov (2 Nov excluded)
		t.Fatalf("want 3 occurrences, got %d:\n%s", len(occ), protonFormatOccurrences(occ))
	}
	out := protonFormatOccurrences(occ)
	for _, d := range []string{"2026-10-19 09:00", "2026-10-26 09:00", "2026-11-09 09:00"} {
		if !strings.Contains(out, d) {
			t.Errorf("wall-clock time drifted, missing %q:\n%s", d, out)
		}
	}
	// An override of the 26th (RECURRENCE-ID) removes that instance from the series.
	ex := map[int64]bool{time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC).Unix(): true} // 09:00 CET
	if got := len(protonOccurrences(e, i, from, to, ex)); got != 2 {
		t.Fatalf("exception not honoured: %d", got)
	}
}

func TestRecurringOutsideRange(t *testing.T) {
	e, i := ev(0, 0, false, "BEGIN:VEVENT\nSUMMARY:Annuel\nDTSTART;TZID=Europe/Zurich:20200105T090000\nDTEND;TZID=Europe/Zurich:20200105T100000\nRRULE:FREQ=YEARLY\nEND:VEVENT")
	from, to, _ := protonRange("2026-10-12", "2026-10-18")
	if n := len(protonOccurrences(e, i, from, to, nil)); n != 0 {
		t.Fatalf("got %d", n)
	}
}

func TestICSDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{"PT1H30M": 90 * time.Minute, "P1D": 24 * time.Hour, "P1W": 7 * 24 * time.Hour, "PT45S": 45 * time.Second} {
		if got, ok := icsDuration(in); !ok || got != want {
			t.Errorf("%s: %v %v", in, got, ok)
		}
	}
	if _, ok := icsDuration("garbage"); ok {
		t.Error("garbage accepted")
	}
}

// Encrypted parts (title/location/description) are readable and merged with
// the clear, signed-only part.
func TestDecryptEventParts(t *testing.T) {
	key, err := pgpcrypto.GenerateKey("cal", "cal@x", "x25519", 0)
	if err != nil {
		t.Fatal(err)
	}
	kr, err := pgpcrypto.NewKeyRing(key)
	if err != nil {
		t.Fatal(err)
	}
	sk, err := pgpcrypto.GenerateSessionKey()
	if err != nil {
		t.Fatal(err)
	}
	kp, err := kr.EncryptSessionKey(sk)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := sk.Encrypt(pgpcrypto.NewPlainMessageFromString("BEGIN:VEVENT\r\nSUMMARY:Dentiste\r\nLOCATION:Rue 1\r\nEND:VEVENT"))
	if err != nil {
		t.Fatal(err)
	}
	e := proton.CalendarEvent{
		ID: "e", SharedKeyPacket: base64.StdEncoding.EncodeToString(kp),
		SharedEvents: []proton.CalendarEventPart{
			{Type: proton.CalendarEventTypeSigned, Data: "BEGIN:VEVENT\r\nUID:u\r\nDTSTART:20261012T070000Z\r\nDTEND:20261012T080000Z\r\nEND:VEVENT"},
			{Type: proton.CalendarEventTypeEncrypted | proton.CalendarEventTypeSigned, Data: base64.StdEncoding.EncodeToString(secret)},
		},
	}
	ics := protonEventICS(e, kr)
	if ics.text("SUMMARY") != "Dentiste" || ics.text("LOCATION") != "Rue 1" {
		t.Fatalf("encrypted part not merged: %v", ics)
	}
	if _, ok := ics.first("DTSTART"); !ok {
		t.Fatal("clear part lost")
	}
	// A wrong key just yields the clear data, no panic and no leak.
	other, _ := pgpcrypto.GenerateKey("o", "o@x", "x25519", 0)
	okr, _ := pgpcrypto.NewKeyRing(other)
	if protonEventICS(e, okr).text("SUMMARY") != "" {
		t.Fatal("decrypted with the wrong key?")
	}
}

// ---- login / persistence / refresh against the library's mock Proton API ----

func TestConnectPersistsSessionAndResumesByRefresh(t *testing.T) {
	s := server.New(server.WithTLS(false))
	defer s.Close()
	if _, _, err := s.CreateUser("alice", []byte("pw")); err != nil {
		t.Fatal(err)
	}
	protonHostURL = s.GetHostURL()
	defer func() { protonHostURL = "https://mail.proton.me/api" }()

	// Legacy fields of the former connector must be wiped by a successful connect.
	live := fakeConfig(t, map[string]string{"username": "alice", "password": "old", "mailbox_password": "old"})
	ctx := context.Background()

	res, err := ProtonConnect(ctx, "tool", snapshot(live), ProtonConnectInput{Username: "alice", Password: "pw"})
	if err != nil || res.Status != "connected" {
		t.Fatalf("connect: %+v %v", res, err)
	}
	for _, k := range []string{pkUID, pkRefresh, pkKeyPass} {
		if live[k] == "" {
			t.Fatalf("%s not persisted", k)
		}
	}
	if live["password"] != "" || live["mailbox_password"] != "" {
		t.Fatal("a password was kept in the stored config")
	}
	for k, v := range live {
		if strings.Contains(v, "pw") && k != pkKeyPass && k != pkRefresh && k != pkUID {
			t.Fatalf("password leaked into %s", k)
		}
	}

	// Simulate a backend restart: no in-memory session, only the stored state.
	protonSessionsMu.Lock()
	protonSessions = map[string]*protonSession{}
	protonSessionsMu.Unlock()
	var logins int
	s.AddCallWatcher(func(c server.Call) {
		if c.URL.Path == "/auth/v4" {
			logins++
		}
	})
	before := live[pkRefresh]
	sess, err := protonGetSession(ctx, snapshot(live), "tool")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if logins != 0 {
		t.Fatalf("resume must refresh, not log in with a password (%d logins)", logins)
	}
	if _, err := sess.client.GetUser(ctx); err != nil {
		t.Fatal(err)
	}
	if live[pkRefresh] == "" {
		t.Fatal("refresh token lost")
	}
	_ = before // the mock may or may not rotate the token; either way one is stored

	// A wrong password is refused without starting a pause.
	protonSessionsMu.Lock()
	protonSessions = map[string]*protonSession{}
	protonSessionsMu.Unlock()
	if _, err := ProtonConnect(ctx, "tool2", map[string]string{}, ProtonConnectInput{Username: "alice", Password: "bad"}); err == nil {
		t.Fatal("wrong password accepted")
	}
}

func TestProtonAppVersionResolution(t *testing.T) {
	t.Setenv(protonAppVersionEnv, "")
	if v, err := protonAppVersionOf(map[string]string{}); err != nil || v != protonDefaultAppVersion {
		t.Fatalf("default: %q %v", v, err)
	}
	if !protonAppVersionRE.MatchString(protonDefaultAppVersion) {
		t.Fatal("default identity must match the format check")
	}
	t.Setenv(protonAppVersionEnv, "external-drive-envtest@2.0.1")
	if v, _ := protonAppVersionOf(map[string]string{}); v != "external-drive-envtest@2.0.1" {
		t.Fatalf("env: %q", v)
	}
	if v, _ := protonAppVersionOf(map[string]string{pkAppVersion: " external-drive-tool@1.2.3-beta.4+x "}); v != "external-drive-tool@1.2.3-beta.4+x" {
		t.Fatalf("tool setting wins over env: %q", v)
	}
	for _, bad := range []string{"claude-hub", "external-drive-x@1.0", "External-Drive-x@1.0.0", "external-drive-x@1.0.0 extra"} {
		if _, err := protonAppVersionOf(map[string]string{pkAppVersion: bad}); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestProtonAppVersionRefusalIsClearAndDoesNotPause(t *testing.T) {
	live := fakeConfig(t, map[string]string{"username": "u", pkUID: "uid", pkRefresh: "r", pkKeyPass: "a2V5"})
	for _, code := range []proton.Code{5002, proton.AppVersionBadCode} {
		err := fmt.Errorf("400 POST x: %w", &proton.APIError{Status: 400, Code: code, Message: "Invalid app version"})
		got := protonRecordFailure("tool", snapshot(live), err, time.Now())
		if got == nil || !strings.Contains(got.Error(), "identité d'application") || !strings.Contains(got.Error(), "external-drive-") {
			t.Fatalf("code %d: unclear message: %v", code, got)
		}
		if _, paused := protonPausedUntil(live, time.Now()); paused {
			t.Fatalf("code %d must not start the anti-abuse pause", code)
		}
		if live[pkPauseCount] != "" {
			t.Fatalf("code %d must not count as an anti-abuse failure", code)
		}
	}
	// Same through the connect dialog.
	err := fmt.Errorf("400 POST x: %w", &proton.APIError{Status: 400, Code: 5002, Message: "Invalid app version"})
	if cerr := protonConnectError("tool", snapshot(live), err, time.Now()); !strings.Contains(cerr.Error(), "identité d'application") {
		t.Fatalf("connect error: %v", cerr)
	}
	if _, paused := protonPausedUntil(live, time.Now()); paused {
		t.Fatal("connect refusal for 5002 must not pause")
	}
}
