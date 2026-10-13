package tools

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	proton "github.com/ProtonMail/go-proton-api"
	pgpcrypto "github.com/ProtonMail/gopenpgp/v2/crypto"
)

// Write side of the Proton connector: create / update / delete a plain event
// (no attendees, no recurrence) through PUT /calendar/v1/{id}/events/sync, the
// bulk endpoint Proton's web client uses. go-proton-api has no calendar write
// calls, so the request is sent with the session's own access token.
//
// Event layout (as the web client builds it):
//   - shared, signed only:      UID, DTSTAMP, DTSTART, DTEND, SEQUENCE
//   - shared, encrypted+signed: CREATED, SUMMARY, DESCRIPTION, LOCATION
//   - all encrypted with one random session key, itself encrypted to the
//     calendar key (SharedKeyPacket); every signature is a detached armored
//     signature by the member's address key.
//
// The format is reverse-engineered from Proton's public WebClients sources and
// has only been exercised against a local fake (proton_write_test.go).

const (
	protonEventPermissions = 3 // DEFAULT_EVENT_PERMISSIONS of the web client
	protonProdID           = "-//Claude Hub//Proton Calendar//EN"
)

// protonCalCtx is everything needed to read and write one calendar's events.
type protonCalCtx struct {
	calKR    *pgpcrypto.KeyRing
	addrKR   *pgpcrypto.KeyRing // signs the parts
	memberID string
	email    string // Author of the parts
}

func protonCalContext(ctx context.Context, sess *protonSession, calID string) (*protonCalCtx, error) {
	members, err := sess.client.GetCalendarMembers(ctx, calID)
	if err != nil {
		return nil, err
	}
	pass, err := sess.client.GetCalendarPassphrase(ctx, calID)
	if err != nil {
		return nil, err
	}
	for _, m := range members {
		kr, ok := sess.addrKRs[strings.ToLower(m.Email)]
		if !ok {
			continue
		}
		cp, derr := pass.Decrypt(m.ID, kr)
		if derr != nil {
			continue
		}
		keys, err := sess.client.GetCalendarKeys(ctx, calID)
		if err != nil {
			return nil, err
		}
		calKR, err := keys.Unlock(cp)
		if err != nil {
			return nil, err
		}
		return &protonCalCtx{calKR: calKR, addrKR: kr, memberID: m.ID, email: m.Email}, nil
	}
	return nil, fmt.Errorf("could not decrypt the calendar passphrase (no matching address key)")
}

// ---- ICS building ----

func icsEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, ";", `\;`)
	s = strings.ReplaceAll(s, ",", `\,`)
	s = strings.ReplaceAll(s, "\r\n", `\n`)
	return strings.ReplaceAll(s, "\n", `\n`)
}

// icsFold wraps a content line at 75 octets (continuation lines start with a
// space), never splitting inside a UTF-8 sequence.
func icsFold(line string) string {
	var b strings.Builder
	n := 0
	for _, r := range line {
		l := len(string(r))
		if n+l > 75 {
			b.WriteString("\r\n ")
			n = 1
		}
		b.WriteRune(r)
		n += l
	}
	return b.String()
}

func icsWrap(lines ...string) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:" + protonProdID + "\r\nCALSCALE:GREGORIAN\r\nBEGIN:VEVENT\r\n")
	for _, l := range lines {
		if l != "" {
			b.WriteString(icsFold(l) + "\r\n")
		}
	}
	b.WriteString("END:VEVENT\r\nEND:VCALENDAR")
	return b.String()
}

// protonEventSpec is the user-facing description of a simple event.
type protonEventSpec struct {
	UID         string
	Title       string
	Location    string
	Description string
	Start, End  time.Time // Europe/Zurich wall times; for all-day: dates, End exclusive
	AllDay      bool
	Sequence    int
	Created     time.Time
}

func (e protonEventSpec) dtLine(name string, t time.Time) string {
	if e.AllDay {
		return name + ";VALUE=DATE:" + t.Format("20060102")
	}
	return name + ";TZID=" + protonUserTZ + ":" + t.In(protonLocation()).Format("20060102T150405")
}

// parts returns the signed-only and the encrypted VCALENDAR bodies.
func (e protonEventSpec) parts(now time.Time) (signed, secret string) {
	signed = icsWrap(
		"UID:"+e.UID,
		"DTSTAMP:"+now.UTC().Format("20060102T150405Z"),
		e.dtLine("DTSTART", e.Start),
		e.dtLine("DTEND", e.End),
		fmt.Sprintf("SEQUENCE:%d", e.Sequence),
	)
	secret = icsWrap(
		"CREATED:"+e.Created.UTC().Format("20060102T150405Z"),
		"SUMMARY:"+icsEscape(e.Title),
		optLine("DESCRIPTION", e.Description),
		optLine("LOCATION", e.Location),
	)
	return signed, secret
}

func optLine(name, v string) string {
	if strings.TrimSpace(v) == "" {
		return ""
	}
	return name + ":" + icsEscape(v)
}

// ---- wire payloads ----

type protonWirePart struct {
	Type      int
	Data      string
	Signature string
}

type protonWireEvent struct {
	Permissions        int
	SharedKeyPacket    string           `json:",omitempty"`
	SharedEventContent []protonWirePart `json:",omitempty"`
	Notifications      any              // null = the calendar's default notifications
	Color              any
}

type protonSyncItem struct {
	ID             string           `json:",omitempty"`
	Overwrite      *int             `json:",omitempty"`
	Event          *protonWireEvent `json:",omitempty"`
	DeletionReason *int             `json:",omitempty"`
}

type protonSyncBody struct {
	MemberID string
	Events   []protonSyncItem
}

// protonEncryptShared builds the SharedEventContent parts. A nil sk means a new
// session key (create); update reuses the event's existing one.
func protonEncryptShared(cc *protonCalCtx, sk *pgpcrypto.SessionKey, signed, secret string) (keyPacket string, parts []protonWirePart, err error) {
	if sk == nil {
		if sk, err = pgpcrypto.GenerateSessionKey(); err != nil {
			return "", nil, err
		}
		kp, err := cc.calKR.EncryptSessionKey(sk)
		if err != nil {
			return "", nil, err
		}
		keyPacket = base64.StdEncoding.EncodeToString(kp)
	}
	sigSigned, err := cc.addrKR.SignDetached(pgpcrypto.NewPlainMessageFromString(signed))
	if err != nil {
		return "", nil, err
	}
	armSigned, err := sigSigned.GetArmored()
	if err != nil {
		return "", nil, err
	}
	sigSecret, err := cc.addrKR.SignDetached(pgpcrypto.NewPlainMessageFromString(secret))
	if err != nil {
		return "", nil, err
	}
	armSecret, err := sigSecret.GetArmored()
	if err != nil {
		return "", nil, err
	}
	packet, err := sk.Encrypt(pgpcrypto.NewPlainMessageFromString(secret))
	if err != nil {
		return "", nil, err
	}
	parts = []protonWirePart{
		{Type: int(proton.CalendarEventTypeSigned), Data: signed, Signature: armSigned},
		{Type: int(proton.CalendarEventTypeEncrypted | proton.CalendarEventTypeSigned),
			Data: base64.StdEncoding.EncodeToString(packet), Signature: armSecret},
	}
	return keyPacket, parts, nil
}

// ---- raw request ----

type protonSyncResponse struct {
	Code      int
	Responses []struct {
		Index    int
		Response struct {
			Code  int
			Error string
			Event proton.CalendarEvent
		}
	}
}

// protonRawPut sends an authenticated PUT with the session's tokens. On a 401
// it lets the library refresh the session (any cheap call does) and retries once.
func protonRawPut(ctx context.Context, sess *protonSession, appVersion, path string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	do := func() (int, []byte, error) {
		uid, acc := sess.getAuth()
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, protonHostURL+path, bytes.NewReader(payload))
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/vnd.protonmail.v1+json")
		req.Header.Set("x-pm-appversion", appVersion)
		req.Header.Set("x-pm-uid", uid)
		req.Header.Set("Authorization", "Bearer "+acc)
		client := &http.Client{Timeout: 30 * time.Second}
		if protonTransport != nil {
			client.Transport = protonTransport
		}
		res, err := client.Do(req)
		if err != nil {
			return 0, nil, &proton.NetError{Cause: err, Message: "network error while communicating with API"}
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
		return res.StatusCode, b, nil
	}
	status, b, err := do()
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized {
		if _, err := sess.client.GetUser(ctx); err != nil {
			return err
		}
		if status, b, err = do(); err != nil {
			return err
		}
	}
	if status < 200 || status >= 300 {
		return protonWireError(status, b)
	}
	return json.Unmarshal(b, out)
}

func protonWireError(status int, b []byte) error {
	apiErr := &proton.APIError{Status: status}
	_ = json.Unmarshal(b, apiErr)
	apiErr.Status = status
	return fmt.Errorf("%d PUT calendar sync: %w", status, apiErr)
}

// protonSyncOne sends one item and returns the resulting event.
func protonSyncOne(ctx context.Context, sess *protonSession, appVersion, calID string, cc *protonCalCtx, item protonSyncItem) (proton.CalendarEvent, error) {
	var res protonSyncResponse
	body := protonSyncBody{MemberID: cc.memberID, Events: []protonSyncItem{item}}
	if err := protonRawPut(ctx, sess, appVersion, "/calendar/v1/"+calID+"/events/sync", body, &res); err != nil {
		return proton.CalendarEvent{}, err
	}
	if len(res.Responses) != 1 {
		return proton.CalendarEvent{}, fmt.Errorf("réponse Proton inattendue (%d réponses pour 1 demande)", len(res.Responses))
	}
	r := res.Responses[0].Response
	if r.Code != 1000 {
		return proton.CalendarEvent{}, fmt.Errorf("Proton a refusé la modification (code %d) : %s", r.Code, r.Error)
	}
	return r.Event, nil
}

// ---- argument parsing ----

// protonParseWhen reads "YYYY-MM-DD" (all day) or "YYYY-MM-DDTHH:MM" in Europe/Zurich.
func protonParseWhen(s string) (t time.Time, allDay bool, err error) {
	s = strings.TrimSpace(s)
	loc := protonLocation()
	if t, err = time.ParseInLocation("2006-01-02", s, loc); err == nil {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), true, nil
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02T15:04:05"} {
		if t, err = time.ParseInLocation(layout, s, loc); err == nil {
			return t, false, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("date/heure invalide %q (attendu AAAA-MM-JJ ou AAAA-MM-JJTHH:MM, fuseau %s)", s, protonUserTZ)
}

func protonNewUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b) + "@proton.me", nil
}

// protonBuildSpec validates the user arguments of a create (base == nil) or an
// update (base = the current event; empty arguments keep its values).
func protonBuildSpec(args map[string]any, base *protonEventSpec) (protonEventSpec, error) {
	var spec protonEventSpec
	if base != nil {
		spec = *base
	}
	if v, ok := args["title"].(string); ok && strings.TrimSpace(v) != "" {
		spec.Title = strings.TrimSpace(v)
	}
	if v, ok := args["location"].(string); ok {
		spec.Location = v
	}
	if v, ok := args["description"].(string); ok {
		spec.Description = v
	}
	startArg, endArg := argString(args, "start", ""), argString(args, "end", "")
	if startArg != "" {
		t, allDay, err := protonParseWhen(startArg)
		if err != nil {
			return spec, err
		}
		dur := time.Hour
		if base != nil {
			dur = base.End.Sub(base.Start)
		}
		spec.Start, spec.AllDay = t, allDay
		if endArg == "" {
			spec.End = t.Add(dur)
			if allDay && (base == nil || dur <= 0) {
				spec.End = t.AddDate(0, 0, 1)
			}
		}
	}
	if endArg != "" {
		t, allDay, err := protonParseWhen(endArg)
		if err != nil {
			return spec, err
		}
		if allDay != spec.AllDay {
			return spec, fmt.Errorf("start et end doivent être tous deux des dates ou tous deux des dates-heures")
		}
		if allDay {
			t = t.AddDate(0, 0, 1) // end is the last day, inclusive; ICS wants it exclusive
		}
		spec.End = t
	}
	if strings.TrimSpace(spec.Title) == "" {
		return spec, fmt.Errorf("title est requis")
	}
	if spec.Start.IsZero() {
		return spec, fmt.Errorf("start est requis")
	}
	if spec.End.IsZero() {
		spec.End = spec.Start.Add(time.Hour)
		if spec.AllDay {
			spec.End = spec.Start.AddDate(0, 0, 1)
		}
	}
	if !spec.End.After(spec.Start) {
		return spec, fmt.Errorf("la fin doit être après le début")
	}
	if len(spec.Title) > 500 || len(spec.Description) > 10000 || len(spec.Location) > 500 {
		return spec, fmt.Errorf("titre, lieu ou description trop long")
	}
	return spec, nil
}

// ---- actions ----

func protonCreateEvent(ctx context.Context, sess *protonSession, appVersion, calID string, args map[string]any) (string, error) {
	spec, err := protonBuildSpec(args, nil)
	if err != nil {
		return "", err
	}
	if spec.UID, err = protonNewUID(); err != nil {
		return "", err
	}
	now := time.Now()
	spec.Created = now
	cc, err := protonCalContext(ctx, sess, calID)
	if err != nil {
		return "", err
	}
	signed, secret := spec.parts(now)
	kp, parts, err := protonEncryptShared(cc, nil, signed, secret)
	if err != nil {
		return "", err
	}
	zero := 0
	ev, err := protonSyncOne(ctx, sess, appVersion, calID, cc, protonSyncItem{
		Overwrite: &zero,
		Event: &protonWireEvent{
			Permissions: protonEventPermissions, SharedKeyPacket: kp, SharedEventContent: parts,
		},
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Événement créé : %q (%s) [id=%s, calendar_id=%s]. Vérifie-le avec proton_list_events.", spec.Title, protonDescribeWhen(spec), ev.ID, calID), nil
}

func protonDescribeWhen(s protonEventSpec) string {
	if s.AllDay {
		return s.Start.Format("2006-01-02") + " (toute la journée)"
	}
	loc := protonLocation()
	return s.Start.In(loc).Format("2006-01-02 15:04") + "–" + s.End.In(loc).Format("15:04")
}

// protonLoadEditable fetches an event and returns it with its decrypted spec,
// refusing anything the simple writer cannot reproduce faithfully: recurring
// events, exceptions, events with attendees or encrypted calendar parts.
func protonLoadEditable(ctx context.Context, sess *protonSession, calID, eventID string, cc *protonCalCtx) (proton.CalendarEvent, protonEventSpec, error) {
	e, err := sess.client.GetCalendarEvent(ctx, calID, eventID)
	if err != nil {
		return e, protonEventSpec{}, err
	}
	ics := protonEventICS(e, cc.calKR)
	if _, ok := ics.first("RRULE"); ok {
		return e, protonEventSpec{}, fmt.Errorf("événement récurrent : non pris en charge en écriture (à modifier dans Proton Calendar)")
	}
	if _, ok := ics.first("RECURRENCE-ID"); ok {
		return e, protonEventSpec{}, fmt.Errorf("occurrence modifiée d'une série : non pris en charge en écriture")
	}
	if len(e.Attendees) > 0 || len(e.AttendeesEvents) > 0 || ics["ATTENDEE"] != nil || ics["ORGANIZER"] != nil {
		return e, protonEventSpec{}, fmt.Errorf("événement avec invités : non pris en charge en écriture (cela enverrait des messages à des tiers)")
	}
	if e.CalendarKeyPacket != "" || len(e.CalendarEvents) > 0 && protonHasEncrypted(e.CalendarEvents) {
		return e, protonEventSpec{}, fmt.Errorf("événement avec partie calendrier chiffrée : non pris en charge en écriture")
	}
	if e.SharedKeyPacket == "" {
		return e, protonEventSpec{}, fmt.Errorf("événement sans clé de session partagée : non modifiable")
	}
	sp := protonEventSpec{
		UID: ics.text("UID"), Title: ics.text("SUMMARY"), Location: ics.text("LOCATION"),
		Description: ics.text("DESCRIPTION"), AllDay: bool(e.FullDay), Created: time.Now(),
	}
	if sp.UID == "" {
		return e, sp, fmt.Errorf("événement illisible (UID absent) : non modifiable")
	}
	ds, okS := ics.first("DTSTART")
	de, okE := ics.first("DTEND")
	if !okS || !okE {
		return e, sp, fmt.Errorf("événement illisible (dates absentes) : non modifiable")
	}
	var allS, allE bool
	var errS, errE error
	sp.Start, allS, errS = icsTime(ds)
	sp.End, allE, errE = icsTime(de)
	if errS != nil || errE != nil || allS != allE {
		return e, sp, fmt.Errorf("événement illisible (dates) : non modifiable")
	}
	sp.AllDay = allS
	if p, ok := ics.first("SEQUENCE"); ok {
		fmt.Sscanf(p.Value, "%d", &sp.Sequence)
	}
	if p, ok := ics.first("CREATED"); ok {
		if t, _, err := icsTime(p); err == nil {
			sp.Created = t
		}
	}
	return e, sp, nil
}

func protonHasEncrypted(parts []proton.CalendarEventPart) bool {
	for _, p := range parts {
		if p.Type&proton.CalendarEventTypeEncrypted != 0 {
			return true
		}
	}
	return false
}

func protonUpdateEvent(ctx context.Context, sess *protonSession, appVersion, calID, eventID string, args map[string]any) (string, error) {
	cc, err := protonCalContext(ctx, sess, calID)
	if err != nil {
		return "", err
	}
	e, cur, err := protonLoadEditable(ctx, sess, calID, eventID, cc)
	if err != nil {
		return "", err
	}
	spec, err := protonBuildSpec(args, &cur)
	if err != nil {
		return "", err
	}
	spec.Sequence = cur.Sequence + 1
	kpRaw, err := base64.StdEncoding.DecodeString(e.SharedKeyPacket)
	if err != nil {
		return "", fmt.Errorf("clé de session illisible: %w", err)
	}
	sk, err := cc.calKR.DecryptSessionKey(kpRaw)
	if err != nil {
		return "", fmt.Errorf("clé de session indéchiffrable: %w", err)
	}
	signed, secret := spec.parts(time.Now())
	_, parts, err := protonEncryptShared(cc, sk, signed, secret)
	if err != nil {
		return "", err
	}
	if _, err := protonSyncOne(ctx, sess, appVersion, calID, cc, protonSyncItem{
		ID: eventID,
		Event: &protonWireEvent{
			Permissions: protonEventPermissions, SharedEventContent: parts,
		},
	}); err != nil {
		return "", err
	}
	return fmt.Sprintf("Événement modifié : %q (%s) [id=%s]. Vérifie-le avec proton_list_events.", spec.Title, protonDescribeWhen(spec), eventID), nil
}

func protonDeleteEvent(ctx context.Context, sess *protonSession, appVersion, calID, eventID string) (string, error) {
	cc, err := protonCalContext(ctx, sess, calID)
	if err != nil {
		return "", err
	}
	_, cur, err := protonLoadEditable(ctx, sess, calID, eventID, cc)
	if err != nil {
		return "", err
	}
	zero := 0
	if _, err := protonSyncOne(ctx, sess, appVersion, calID, cc, protonSyncItem{ID: eventID, DeletionReason: &zero}); err != nil {
		return "", err
	}
	return fmt.Sprintf("Événement supprimé : %q (%s) [id=%s].", cur.Title, protonDescribeWhen(cur), eventID), nil
}

// protonWriteCall runs one write action. Arguments are validated before the
// session (and so the network) is touched; a failure feeds the same pause policy
// as reads, and a write is never retried.
func protonWriteCall(ctx context.Context, cfg map[string]string, action string, args map[string]any) (string, error) {
	eventID := strings.TrimSpace(argString(args, "event_id", ""))
	switch action {
	case "proton_create_event":
		if _, err := protonBuildSpec(args, nil); err != nil {
			return "", err
		}
	case "proton_update_event":
		if eventID == "" {
			return "", fmt.Errorf("event_id est requis")
		}
		if _, err := protonBuildSpec(args, &protonEventSpec{Title: "x", Start: time.Unix(0, 0), End: time.Unix(3600, 0)}); err != nil {
			return "", err
		}
	default:
		if eventID == "" {
			return "", fmt.Errorf("event_id est requis")
		}
	}
	appVersion, err := protonAppVersionOf(cfg)
	if err != nil {
		return "", err
	}
	sess, err := protonGetSession(ctx, cfg, cfg["_tool_id"])
	if err != nil {
		return "", err
	}
	calID, err := protonPickCalendar(ctx, sess, argString(args, "calendar_id", ""))
	if err != nil {
		return "", protonRecordFailure(cfg["_tool_id"], cfg, err, time.Now())
	}
	var out string
	switch action {
	case "proton_create_event":
		out, err = protonCreateEvent(ctx, sess, appVersion, calID, args)
	case "proton_update_event":
		out, err = protonUpdateEvent(ctx, sess, appVersion, calID, eventID, args)
	default:
		out, err = protonDeleteEvent(ctx, sess, appVersion, calID, eventID)
	}
	if err != nil {
		return "", protonRecordFailure(cfg["_tool_id"], cfg, err, time.Now())
	}
	return out, nil
}

// protonPickCalendar returns calID, or the only calendar when none is given.
func protonPickCalendar(ctx context.Context, sess *protonSession, calID string) (string, error) {
	if calID != "" {
		return calID, nil
	}
	cals, err := sess.client.GetCalendars(ctx)
	if err != nil {
		return "", err
	}
	if len(cals) != 1 {
		return "", fmt.Errorf("plusieurs agendas : précise calendar_id (voir proton_list_calendars)")
	}
	return cals[0].ID, nil
}
