package mcpremote

import (
	"sync"
	"time"
)

// limiter is a small in-memory sliding-window counter keyed by string.
type limiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newLimiter() *limiter { return &limiter{hits: map[string][]time.Time{}} }

// allow records a hit and reports whether key stays within n hits per window.
func (l *limiter) allow(key string, n int, window time.Duration, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := now.Add(-window)
	h := l.hits[key][:0:0]
	for _, t := range l.hits[key] {
		if t.After(cut) {
			h = append(h, t)
		}
	}
	if len(h) >= n {
		l.hits[key] = h
		return false
	}
	l.hits[key] = append(h, now)
	if len(l.hits) > 5000 { // bound memory under a flood of distinct keys
		for k, v := range l.hits {
			if len(v) == 0 || v[len(v)-1].Before(cut) {
				delete(l.hits, k)
			}
		}
	}
	return true
}

// count returns the hits of key within window without recording one.
func (l *limiter) count(key string, window time.Duration, now time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, t := range l.hits[key] {
		if t.After(now.Add(-window)) {
			n++
		}
	}
	return n
}

func (l *limiter) record(key string, now time.Time) {
	l.mu.Lock()
	l.hits[key] = append(l.hits[key], now)
	l.mu.Unlock()
}
