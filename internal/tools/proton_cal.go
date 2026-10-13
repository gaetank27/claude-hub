package tools

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // Europe/Zurich must resolve even on a minimal host

	proton "github.com/ProtonMail/go-proton-api"
	pgpcrypto "github.com/ProtonMail/gopenpgp/v2/crypto"
	"github.com/teambition/rrule-go"
)

// protonUserTZ is the timezone events are shown in.
const protonUserTZ = "Europe/Zurich"

func protonLocation() *time.Location {
	loc, err := time.LoadLocation(protonUserTZ)
	if err != nil {
		return time.UTC
	}
	return loc
}

// ---- ICS parsing ----

type icsProp struct {
	Params map[string]string
	Value  string
}

// icsEvent is the merged set of VEVENT properties of one Proton calendar event
// (clear + decrypted shared/calendar parts).
type icsEvent map[string][]icsProp

func (e icsEvent) first(name string) (icsProp, bool) {
	p := e[name]
	if len(p) == 0 {
		return icsProp{}, false
	}
	return p[0], true
}

func (e icsEvent) text(name string) string {
	p, ok := e.first(name)
	if !ok {
		return ""
	}
	return icsUnescape(p.Value)
}

func icsUnescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n', 'N':
				b.WriteByte('\n')
			default:
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// parseICSInto adds the properties of every VEVENT in data (alarms and other
// nested components are skipped) to ev; properties already present are kept.
func parseICSInto(ev icsEvent, data string) {
	data = strings.ReplaceAll(data, "\r\n", "\n")
	data = strings.ReplaceAll(data, "\n ", "")
	data = strings.ReplaceAll(data, "\n\t", "")
	depth := 0
	inEvent := false
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "BEGIN:"):
			if line == "BEGIN:VEVENT" && depth == 0 {
				inEvent = true
			} else if inEvent {
				depth++
			}
			continue
		case strings.HasPrefix(line, "END:"):
			if line == "END:VEVENT" && depth == 0 {
				inEvent = false
			} else if inEvent && depth > 0 {
				depth--
			}
			continue
		}
		if !inEvent || depth > 0 {
			continue
		}
		colon := icsValueColon(line)
		if colon < 0 {
			continue
		}
		head, value := line[:colon], line[colon+1:]
		parts := strings.Split(head, ";")
		name := strings.ToUpper(parts[0])
		if _, dup := ev[name]; dup && name != "EXDATE" {
			continue
		}
		params := map[string]string{}
		for _, p := range parts[1:] {
			if k, v, ok := strings.Cut(p, "="); ok {
				params[strings.ToUpper(k)] = strings.Trim(v, `"`)
			}
		}
		ev[name] = append(ev[name], icsProp{Params: params, Value: value})
	}
}

// icsValueColon finds the colon separating the name+params from the value,
// ignoring colons inside quoted parameter values.
func icsValueColon(line string) int {
	quoted := false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '"':
			quoted = !quoted
		case ':':
			if !quoted {
				return i
			}
		}
	}
	return -1
}

// icsTime parses a DATE or DATE-TIME property. allDay reports a VALUE=DATE.
func icsTime(p icsProp) (t time.Time, allDay bool, err error) {
	v := strings.TrimSpace(p.Value)
	if p.Params["VALUE"] == "DATE" || len(v) == 8 {
		t, err = time.ParseInLocation("20060102", v, time.UTC)
		return t, true, err
	}
	if strings.HasSuffix(v, "Z") {
		t, err = time.ParseInLocation("20060102T150405Z", v, time.UTC)
		return t, false, err
	}
	loc := time.UTC
	if tz := p.Params["TZID"]; tz != "" {
		if l, lerr := time.LoadLocation(tz); lerr == nil {
			loc = l
		}
	}
	t, err = time.ParseInLocation("20060102T150405", v, loc)
	return t, false, err
}

// ---- decryption of the event parts ----

// protonDecodeParts decrypts one group of event parts (shared or calendar) into
// ev. Signatures are not verified: the data is read-only context for the
// assistant and attendee-authored parts are signed with keys we do not hold.
func protonDecodeParts(ev icsEvent, parts []proton.CalendarEventPart, keyPacket string, calKR *pgpcrypto.KeyRing) {
	// Encrypted parts first so the clear (signed-only) copies of the same
	// property never shadow the full version.
	ordered := append([]proton.CalendarEventPart(nil), parts...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Type&proton.CalendarEventTypeEncrypted > ordered[j].Type&proton.CalendarEventTypeEncrypted
	})
	for _, part := range ordered {
		data := part.Data
		if part.Type&proton.CalendarEventTypeEncrypted != 0 {
			dec, err := protonDecryptPart(part.Data, keyPacket, calKR)
			if err != nil {
				continue
			}
			data = dec
		}
		parseICSInto(ev, data)
	}
}

func protonDecryptPart(data, keyPacket string, calKR *pgpcrypto.KeyRing) (string, error) {
	var msg *pgpcrypto.PGPMessage
	if keyPacket != "" {
		kp, err := base64.StdEncoding.DecodeString(keyPacket)
		if err != nil {
			return "", err
		}
		raw, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return "", err
		}
		msg = pgpcrypto.NewPGPSplitMessage(kp, raw).GetPGPMessage()
	} else {
		var err error
		if msg, err = pgpcrypto.NewPGPMessageFromArmored(data); err != nil {
			return "", err
		}
	}
	dec, err := calKR.Decrypt(msg, nil, pgpcrypto.GetUnixTime())
	if err != nil {
		return "", err
	}
	return dec.GetString(), nil
}

// protonEventICS merges every readable part of a Proton event into one VEVENT.
func protonEventICS(e proton.CalendarEvent, calKR *pgpcrypto.KeyRing) icsEvent {
	ev := icsEvent{}
	protonDecodeParts(ev, e.SharedEvents, e.SharedKeyPacket, calKR)
	protonDecodeParts(ev, e.CalendarEvents, e.CalendarKeyPacket, calKR)
	return ev
}

// ---- occurrences ----

// protonOccurrence is one displayable instance of an event.
type protonOccurrence struct {
	ID          string
	Title       string
	Location    string
	Description string
	Start, End  time.Time
	AllDay      bool
	Recurring   bool
}

// protonOccurrences expands one event into the occurrences overlapping
// [from, to). exceptions holds the original start times (unix) replaced by a
// RECURRENCE-ID override of the same UID.
func protonOccurrences(e proton.CalendarEvent, ev icsEvent, from, to time.Time, exceptions map[int64]bool) []protonOccurrence {
	base := protonOccurrence{
		ID:          e.ID,
		Title:       ev.text("SUMMARY"),
		Location:    ev.text("LOCATION"),
		Description: ev.text("DESCRIPTION"),
		AllDay:      bool(e.FullDay),
	}
	if base.Title == "" {
		base.Title = "(sans titre)"
	}

	// Start/end: the ICS wins (it carries the TZID needed for DST-correct
	// recurrence); the API's unix times are the fallback.
	start := time.Unix(e.StartTime, 0)
	end := time.Unix(e.EndTime, 0)
	startTZ := start.Location()
	if p, ok := ev.first("DTSTART"); ok {
		if t, allDay, err := icsTime(p); err == nil {
			start, base.AllDay, startTZ = t, allDay, t.Location()
		}
	}
	if p, ok := ev.first("DTEND"); ok {
		if t, _, err := icsTime(p); err == nil {
			end = t
		}
	} else if p, ok := ev.first("DURATION"); ok {
		if dur, ok := icsDuration(p.Value); ok {
			end = start.Add(dur)
		}
	}
	if !end.After(start) {
		end = start
	}
	length := end.Sub(start)

	rp, recurring := ev.first("RRULE")
	if !recurring {
		if !protonOverlaps(start, end, from, to, base.AllDay) {
			return nil
		}
		base.Start, base.End = start, end
		return []protonOccurrence{base}
	}

	opt, err := rrule.StrToROptionInLocation(rp.Value, startTZ)
	if err != nil {
		// Unreadable rule: show the first instance rather than nothing.
		if protonOverlaps(start, end, from, to, base.AllDay) {
			base.Start, base.End, base.Recurring = start, end, true
			return []protonOccurrence{base}
		}
		return nil
	}
	opt.Dtstart = start
	rule, err := rrule.NewRRule(*opt)
	if err != nil {
		return nil
	}
	set := rrule.Set{}
	set.RRule(rule)
	set.DTStart(start)
	for _, ex := range ev["EXDATE"] {
		for _, v := range strings.Split(ex.Value, ",") {
			if t, _, err := icsTime(icsProp{Params: ex.Params, Value: v}); err == nil {
				set.ExDate(t)
			}
		}
	}

	// Search window widened by the event length so a running occurrence that
	// started before `from` is still found.
	var out []protonOccurrence
	for _, t := range set.Between(from.Add(-length-time.Second), to, true) {
		if exceptions[t.Unix()] {
			continue
		}
		o := base
		o.Start, o.End, o.Recurring = t, t.Add(length), true
		if protonOverlaps(o.Start, o.End, from, to, o.AllDay) {
			out = append(out, o)
		}
	}
	return out
}

func protonOverlaps(start, end, from, to time.Time, allDay bool) bool {
	if allDay {
		// All-day events are floating dates: compare them as local dates.
		loc := protonLocation()
		fromD := time.Date(from.In(loc).Year(), from.In(loc).Month(), from.In(loc).Day(), 0, 0, 0, 0, time.UTC)
		toD := time.Date(to.In(loc).Year(), to.In(loc).Month(), to.In(loc).Day(), 0, 0, 0, 0, time.UTC)
		if end.Equal(start) {
			end = start.AddDate(0, 0, 1)
		}
		return start.Before(toD) && end.After(fromD)
	}
	if end.Equal(start) {
		return !start.Before(from) && start.Before(to)
	}
	return start.Before(to) && end.After(from)
}

// icsDuration parses the subset of RFC 5545 durations calendars emit (PnW, PnDTnHnMnS).
func icsDuration(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimLeft(s, "+-")
	if !strings.HasPrefix(s, "P") {
		return 0, false
	}
	s = s[1:]
	var d time.Duration
	num := ""
	inTime := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			num += string(r)
		case r == 'T':
			inTime = true
		default:
			n, _ := strconv.Atoi(num)
			num = ""
			switch {
			case r == 'W':
				d += time.Duration(n) * 7 * 24 * time.Hour
			case r == 'D':
				d += time.Duration(n) * 24 * time.Hour
			case r == 'H' && inTime:
				d += time.Duration(n) * time.Hour
			case r == 'M' && inTime:
				d += time.Duration(n) * time.Minute
			case r == 'S' && inTime:
				d += time.Duration(n) * time.Second
			default:
				return 0, false
			}
		}
	}
	if neg {
		d = -d
	}
	return d, true
}

// protonFormatOccurrences renders occurrences in the user's timezone, soonest first.
func protonFormatOccurrences(occs []protonOccurrence) string {
	loc := protonLocation()
	sort.SliceStable(occs, func(i, j int) bool { return occs[i].Start.Before(occs[j].Start) })
	var b strings.Builder
	fmt.Fprintf(&b, "Events (%d), times in %s:\n", len(occs), protonUserTZ)
	for _, o := range occs {
		var when string
		if o.AllDay {
			last := o.End.AddDate(0, 0, -1)
			if !last.After(o.Start) {
				when = o.Start.Format("2006-01-02") + " (all day)"
			} else {
				when = o.Start.Format("2006-01-02") + " → " + last.Format("2006-01-02") + " (all day)"
			}
		} else {
			s, e := o.Start.In(loc), o.End.In(loc)
			if s.Format("2006-01-02") == e.Format("2006-01-02") {
				when = s.Format("2006-01-02 15:04") + "–" + e.Format("15:04")
			} else {
				when = s.Format("2006-01-02 15:04") + " → " + e.Format("2006-01-02 15:04")
			}
		}
		fmt.Fprintf(&b, "  %s  %s", when, o.Title)
		if o.Location != "" {
			fmt.Fprintf(&b, "  @ %s", o.Location)
		}
		if o.Recurring {
			b.WriteString("  [recurring]")
		}
		fmt.Fprintf(&b, "  [id=%s]\n", o.ID)
		if d := strings.TrimSpace(o.Description); d != "" {
			fmt.Fprintf(&b, "      %s\n", strings.ReplaceAll(truncate(d, 500), "\n", "\n      "))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
