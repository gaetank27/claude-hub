package usage

import (
	"encoding/json"
	"testing"
)

// TestUpstreamMapping locks the decode + mapping of the usage endpoint response
// (the shape verified against a real account): named windows map to our fields,
// resets_at parses to a time, and null windows stay nil.
func TestUpstreamMapping(t *testing.T) {
	const body = `{
	  "five_hour": {"utilization": 23.0, "resets_at": "2026-05-29T21:10:00.5+00:00"},
	  "seven_day": {"utilization": 12.0, "resets_at": "2026-06-04T09:00:00+00:00"},
	  "seven_day_opus": null,
	  "seven_day_sonnet": {"utilization": 0.0, "resets_at": null}
	}`
	var up upstream
	if err := json.Unmarshal([]byte(body), &up); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	u := Usage{
		Session:    up.FiveHour.toWindow(),
		Week:       up.SevenDay.toWindow(),
		WeekOpus:   up.SevenDayOpus.toWindow(),
		WeekSonnet: up.SevenDaySonnet.toWindow(),
	}
	if u.Session == nil || u.Session.Utilization != 23 || u.Session.ResetsAt == nil {
		t.Fatalf("session window wrong: %+v", u.Session)
	}
	if u.Week == nil || u.Week.Utilization != 12 || u.Week.ResetsAt == nil {
		t.Fatalf("week window wrong: %+v", u.Week)
	}
	if u.WeekOpus != nil {
		t.Fatalf("opus must stay nil when upstream null, got %+v", u.WeekOpus)
	}
	if u.WeekSonnet == nil || u.WeekSonnet.ResetsAt != nil {
		t.Fatalf("sonnet window wrong (resets_at null): %+v", u.WeekSonnet)
	}
}
