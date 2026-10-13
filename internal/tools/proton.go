package tools

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	proton "github.com/ProtonMail/go-proton-api"
	pgpcrypto "github.com/ProtonMail/gopenpgp/v2/crypto"
)

// Proton connector: Proton Calendar only, through the official go-proton-api
// (reads) and a raw sync call (simple-event writes, see proton_write.go). Mail goes through Proton Bridge and the IMAP connector. The
// account is connected once from the Hub UI (see proton_session.go); calls only
// ever resume that session, they never log in with a password.

func init() { register(protonConnector{}) }

type protonConnector struct{}

func (protonConnector) Type() string  { return "proton" }
func (protonConnector) Label() string { return "Proton Agenda" }

func (protonConnector) Fields() []Field {
	return []Field{
		{Key: "username", Label: "Nom d'utilisateur Proton", Type: FieldText, Required: true,
			Placeholder: "user@proton.me"},
		{Key: pkAppVersion, Label: "Identité d'application (avancé)", Type: FieldText,
			Placeholder: protonDefaultAppVersion},
	}
}

// Test checks the stored session without any password login.
func (protonConnector) Test(ctx context.Context, cfg map[string]string) error {
	_, err := protonGetSession(ctx, cfg, cfg["_tool_id"])
	return err
}

func (protonConnector) Tools() []ToolSpec {
	return []ToolSpec{
		{
			Name:        "proton_list_calendars",
			Description: "List the Proton Calendar calendars of the user (read-only).",
			Args:        []ArgSpec{},
		},
		{
			Name: "proton_list_events",
			Description: "List Proton Calendar events in a date range (read-only): title, time in Europe/Zurich, location, description. " +
				"Recurring events are expanded into their occurrences; all-day events are marked.",
			Args: []ArgSpec{
				{Name: "start", Type: "string", Required: true,
					Description: "First day, ISO 8601 date, e.g. 2026-10-12"},
				{Name: "end", Type: "string",
					Description: "Last day included, e.g. 2026-10-18 (default: same day as start; max 92 days)"},
				{Name: "calendar_id", Type: "string",
					Description: "Calendar ID from proton_list_calendars (default: all calendars)"},
			},
		},
		{
			Name: "proton_create_event",
			Description: "WRITE: create a simple event (no guests, no recurrence) in a Proton calendar. " +
				"Only call after the user has explicitly confirmed title, date and time.",
			Args: protonWriteArgs(true),
		},
		{
			Name: "proton_update_event",
			Description: "WRITE: modify title/time/location/description of a simple Proton event (not recurring, no guests). " +
				"Omitted fields are kept. Only call after the user has explicitly confirmed the change.",
			Args: append([]ArgSpec{{Name: "event_id", Type: "string", Required: true,
				Description: "Event id shown by proton_list_events ([id=...])"}}, protonWriteArgs(false)...),
		},
		{
			Name: "proton_delete_event",
			Description: "WRITE: delete a simple Proton event (not recurring, no guests). " +
				"Only call after the user has explicitly confirmed which event.",
			Args: []ArgSpec{
				{Name: "event_id", Type: "string", Required: true, Description: "Event id shown by proton_list_events ([id=...])"},
				{Name: "calendar_id", Type: "string", Description: "Calendar ID (optional if the account has a single calendar)"},
			},
		},
	}
}

func protonWriteArgs(create bool) []ArgSpec {
	return []ArgSpec{
		{Name: "title", Type: "string", Required: create, Description: "Event title"},
		{Name: "start", Type: "string", Required: create,
			Description: "Europe/Zurich, YYYY-MM-DDTHH:MM, or YYYY-MM-DD for an all-day event"},
		{Name: "end", Type: "string",
			Description: "Same format as start (default: start + 1 h; for all-day: last day, inclusive)"},
		{Name: "location", Type: "string", Description: "Location"},
		{Name: "description", Type: "string", Description: "Notes"},
		{Name: "calendar_id", Type: "string", Description: "Calendar ID (optional if the account has a single calendar)"},
	}
}

func (protonConnector) Call(ctx context.Context, cfg map[string]string, action string, args map[string]any) (string, error) {
	switch action {
	case "proton_list_calendars", "proton_list_events":
	case "proton_create_event", "proton_update_event", "proton_delete_event":
		return protonWriteCall(ctx, cfg, action, args)
	default:
		return "", fmt.Errorf("unknown action %q", action)
	}
	// Validate arguments before touching the session or the network.
	var from, to time.Time
	if action == "proton_list_events" {
		var err error
		if from, to, err = protonRange(argString(args, "start", ""), argString(args, "end", "")); err != nil {
			return "", err
		}
	}
	sess, err := protonGetSession(ctx, cfg, cfg["_tool_id"])
	if err != nil {
		return "", err
	}
	var out string
	switch action {
	case "proton_list_calendars":
		out, err = protonListCalendars(ctx, sess)
	default:
		out, err = protonListEvents(ctx, sess, argString(args, "calendar_id", ""), from, to)
	}
	if err != nil {
		return "", protonRecordFailure(cfg["_tool_id"], cfg, err, time.Now())
	}
	return out, nil
}

// protonRange turns the inclusive start/end dates (Europe/Zurich) into [from, to).
func protonRange(start, end string) (time.Time, time.Time, error) {
	loc := protonLocation()
	if start == "" {
		return time.Time{}, time.Time{}, fmt.Errorf("start is required")
	}
	from, err := time.ParseInLocation("2006-01-02", start, loc)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid start date %q (want YYYY-MM-DD)", start)
	}
	last := from
	if end != "" {
		if last, err = time.ParseInLocation("2006-01-02", end, loc); err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid end date %q (want YYYY-MM-DD)", end)
		}
	}
	if last.Before(from) {
		return time.Time{}, time.Time{}, fmt.Errorf("end is before start")
	}
	to := last.AddDate(0, 0, 1)
	if to.Sub(from) > 93*24*time.Hour {
		return time.Time{}, time.Time{}, fmt.Errorf("range too long (max 92 days)")
	}
	return from, to, nil
}

func protonListCalendars(ctx context.Context, sess *protonSession) (string, error) {
	cals, err := sess.client.GetCalendars(ctx)
	if err != nil {
		return "", err
	}
	if len(cals) == 0 {
		return "No calendars found.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Calendars (%d):\n", len(cals))
	for _, c := range cals {
		fmt.Fprintf(&b, "  id=%s  %s\n", c.ID, c.Name)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

const protonMaxEvents = 200

func protonListEvents(ctx context.Context, sess *protonSession, calID string, from, to time.Time) (string, error) {
	var calIDs []string
	if calID != "" {
		calIDs = []string{calID}
	} else {
		cals, err := sess.client.GetCalendars(ctx)
		if err != nil {
			return "", err
		}
		for _, c := range cals {
			calIDs = append(calIDs, c.ID)
		}
	}

	var occs []protonOccurrence
	for _, id := range calIDs {
		list, err := protonCalendarOccurrences(ctx, sess, id, from, to)
		if err != nil {
			return "", err
		}
		occs = append(occs, list...)
	}
	if len(occs) == 0 {
		return "No events in this period.", nil
	}
	truncated := len(occs) > protonMaxEvents
	out := protonFormatOccurrences(occs)
	if truncated {
		out += fmt.Sprintf("\n(%d occurrences found; narrow the date range for the full list)", len(occs))
	}
	return out, nil
}

func protonCalendarOccurrences(ctx context.Context, sess *protonSession, calID string, from, to time.Time) ([]protonOccurrence, error) {
	calKR, err := protonCalKR(ctx, sess, calID)
	if err != nil {
		return nil, err
	}
	filter := url.Values{}
	filter.Set("Start", strconv.FormatInt(from.Unix(), 10))
	filter.Set("End", strconv.FormatInt(to.Unix(), 10))
	filter.Set("Timezone", protonUserTZ)
	events, err := sess.client.GetAllCalendarEvents(ctx, calID, filter)
	if err != nil {
		return nil, err
	}

	type decoded struct {
		ev  proton.CalendarEvent
		ics icsEvent
	}
	all := make([]decoded, 0, len(events))
	exceptions := map[string]map[int64]bool{} // UID -> original starts replaced by an override
	for _, e := range events {
		ics := protonEventICS(e, calKR)
		all = append(all, decoded{e, ics})
		if p, ok := ics.first("RECURRENCE-ID"); ok {
			if t, _, err := icsTime(p); err == nil {
				if exceptions[e.UID] == nil {
					exceptions[e.UID] = map[int64]bool{}
				}
				exceptions[e.UID][t.Unix()] = true
			}
		}
	}
	var out []protonOccurrence
	for _, d := range all {
		if _, isOverride := d.ics.first("RECURRENCE-ID"); isOverride {
			delete(d.ics, "RRULE")
		}
		out = append(out, protonOccurrences(d.ev, d.ics, from, to, exceptions[d.ev.UID])...)
	}
	return out, nil
}

// protonCalKR unlocks the calendar key ring for the given calendar.
func protonCalKR(ctx context.Context, sess *protonSession, calID string) (*pgpcrypto.KeyRing, error) {
	members, err := sess.client.GetCalendarMembers(ctx, calID)
	if err != nil {
		return nil, err
	}
	pass, err := sess.client.GetCalendarPassphrase(ctx, calID)
	if err != nil {
		return nil, err
	}
	var calPassphrase []byte
	for _, member := range members {
		kr, ok := sess.addrKRs[strings.ToLower(member.Email)]
		if !ok {
			continue
		}
		if kp, derr := pass.Decrypt(member.ID, kr); derr == nil {
			calPassphrase = kp
			break
		}
	}
	if calPassphrase == nil {
		return nil, fmt.Errorf("could not decrypt the calendar passphrase (no matching address key)")
	}
	calKeys, err := sess.client.GetCalendarKeys(ctx, calID)
	if err != nil {
		return nil, err
	}
	return calKeys.Unlock(calPassphrase)
}
