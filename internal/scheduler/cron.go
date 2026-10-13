// Package scheduler runs recurring tasks on cron schedules, launching a new
// Claude session per fire and injecting the task's prompt as the first message.
package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Next returns the next time after `from` that the 5-field cron expression
// fires (UTC). Fields: minute hour dom month dow (standard vixie-cron order).
// Supports: * */N N N-M N,M,… for each field.
func Next(expr string, from time.Time) (time.Time, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return time.Time{}, fmt.Errorf("cron: expected 5 fields, got %d", len(fields))
	}
	mins, err := parseField(fields[0], 0, 59)
	if err != nil {
		return time.Time{}, fmt.Errorf("cron minute: %w", err)
	}
	hrs, err := parseField(fields[1], 0, 23)
	if err != nil {
		return time.Time{}, fmt.Errorf("cron hour: %w", err)
	}
	doms, err := parseField(fields[2], 1, 31)
	if err != nil {
		return time.Time{}, fmt.Errorf("cron dom: %w", err)
	}
	mons, err := parseField(fields[3], 1, 12)
	if err != nil {
		return time.Time{}, fmt.Errorf("cron month: %w", err)
	}
	dows, err := parseField(fields[4], 0, 6)
	if err != nil {
		return time.Time{}, fmt.Errorf("cron dow: %w", err)
	}

	// Start searching from the next minute.
	t := from.UTC().Truncate(time.Minute).Add(time.Minute)
	// Bound the search to avoid an infinite loop on an impossible expression
	// (e.g. "30 feb" equivalent). 4 years covers every leap-year cycle.
	limit := t.Add(4 * 365 * 24 * time.Hour)

	for t.Before(limit) {
		if !matches(mons, int(t.Month())) {
			// Advance to the first day of the next matching month.
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if !matches(doms, t.Day()) || !matches(dows, int(t.Weekday())) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if !matches(hrs, t.Hour()) {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, time.UTC)
			continue
		}
		if !matches(mins, t.Minute()) {
			t = t.Add(time.Minute)
			continue
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("cron: no next time found within 4 years")
}

// Validate returns an error if expr is not a valid 5-field cron expression.
func Validate(expr string) error {
	_, err := Next(expr, time.Now())
	return err
}

// matches reports whether v is in the set (nil = wildcard "any").
func matches(set map[int]bool, v int) bool {
	if set == nil {
		return true
	}
	return set[v]
}

// parseField parses a single cron field string into a set of matching integers,
// or nil for "any" (bare "*").
func parseField(field string, min, max int) (map[int]bool, error) {
	if field == "*" {
		return nil, nil
	}
	out := make(map[int]bool)
	for _, part := range strings.Split(field, ",") {
		if strings.HasPrefix(part, "*/") {
			step, err := strconv.Atoi(part[2:])
			if err != nil || step <= 0 {
				return nil, fmt.Errorf("invalid step %q", part)
			}
			for v := min; v <= max; v += step {
				out[v] = true
			}
		} else if idx := strings.Index(part, "-"); idx > 0 {
			lo, err1 := strconv.Atoi(part[:idx])
			hi, err2 := strconv.Atoi(part[idx+1:])
			if err1 != nil || err2 != nil || lo > hi || lo < min || hi > max {
				return nil, fmt.Errorf("invalid range %q", part)
			}
			for v := lo; v <= hi; v++ {
				out[v] = true
			}
		} else {
			v, err := strconv.Atoi(part)
			if err != nil || v < min || v > max {
				return nil, fmt.Errorf("invalid value %q (expected %d-%d)", part, min, max)
			}
			out[v] = true
		}
	}
	return out, nil
}
