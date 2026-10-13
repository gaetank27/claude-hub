// Package usage reports the Claude subscription usage (the 5-hour session window
// and the weekly limits) for the hub's top-of-page meter. It reads the OAuth
// access token the claude CLI stores in its credentials file and queries
// Anthropic's usage endpoint — the same data the CLI's /usage view shows.
// Results are cached briefly so frequent UI polls don't hammer the upstream.
package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

const (
	usageURL   = "https://api.anthropic.com/api/oauth/usage"
	betaHeader = "oauth-2025-04-20"
	// cacheTTL is deliberately several minutes: the endpoint rate-limits per token
	// (some plans, e.g. Team, far below 1 req/min), and the guard + sampler +
	// per-project meter all share this cache, so a short TTL meant ~1 real call per
	// minute per account → HTTP 429 → fallback to stale data. The displayed
	// countdown is computed client-side from the ABSOLUTE resetsAt, so a few-minute-
	// old fetch still renders an accurate, smoothly-ticking countdown.
	cacheTTL    = 5 * time.Minute
	errBackoff  = 5 * time.Minute // back off after a rate-limit before retrying
	httpTimeout = 10 * time.Second
)

// ErrRateLimited is returned when the usage endpoint answers HTTP 429. Some plan
// types (notably Team/Enterprise) are rate-limited hard and essentially never
// report these windows; backing off avoids hammering the endpoint pointlessly.
var ErrRateLimited = errors.New("usage endpoint rate limited")

// Window is one usage limit: how much of it is consumed and when it resets.
type Window struct {
	Utilization float64    `json:"utilization"`        // percent, 0..100
	ResetsAt    *time.Time `json:"resetsAt,omitempty"` // nil when not tracked
}

// Usage is the subset of the upstream report the UI shows.
type Usage struct {
	Session    *Window `json:"session,omitempty"`    // 5-hour rolling window
	Week       *Window `json:"week,omitempty"`       // 7-day, all models
	WeekOpus   *Window `json:"weekOpus,omitempty"`   // 7-day, Opus (when separately capped)
	WeekSonnet *Window `json:"weekSonnet,omitempty"` // 7-day, Sonnet (when separately capped)
}

// credentials mirrors the fields we need from ~/.claude/.credentials.json.
type credentials struct {
	ClaudeAiOauth struct {
		AccessToken string `json:"accessToken"`
		ExpiresAt   int64  `json:"expiresAt"` // unix milliseconds
	} `json:"claudeAiOauth"`
}

// upstream mirrors the relevant fields of the usage endpoint response.
type upstream struct {
	FiveHour       *windowJSON `json:"five_hour"`
	SevenDay       *windowJSON `json:"seven_day"`
	SevenDayOpus   *windowJSON `json:"seven_day_opus"`
	SevenDaySonnet *windowJSON `json:"seven_day_sonnet"`
}

type windowJSON struct {
	Utilization float64    `json:"utilization"`
	ResetsAt    *time.Time `json:"resets_at"`
}

func (w *windowJSON) toWindow() *Window {
	if w == nil {
		return nil
	}
	return &Window{Utilization: w.Utilization, ResetsAt: w.ResetsAt}
}

// Client fetches usage with a short per-path TTL cache. Build one with New.
//
// The credentials path is resolved lazily on each fetch (the default meter
// follows the active subscription, which the user can switch at runtime). The
// cache is keyed by the resolved path, so several accounts (the default plus each
// project's) are cached independently and a switch never serves another account's
// usage. Caching matters: the upstream usage endpoint rate-limits (HTTP 429), so
// the per-project meter MUST share the same TTL as the default one rather than
// hitting the endpoint on every poll/navigation.
type cacheEntry struct {
	u   Usage
	err error // non-nil for a cached rate-limit (negative cache)
	at  time.Time
}

type Client struct {
	credPath func() string
	http     *http.Client

	mu    sync.Mutex
	cache map[string]cacheEntry // keyed by credentials path
}

func New(credPath func() string) *Client {
	return &Client{credPath: credPath, http: &http.Client{Timeout: httpTimeout}, cache: map[string]cacheEntry{}}
}

// Get returns the active account's usage, served from cache when fresh. A non-nil
// error means the data is temporarily unavailable (no active account, token
// missing/expired, upstream down/rate-limited); callers should degrade (hide the
// meter or fall back to a snapshot), not fail hard.
func (c *Client) Get(ctx context.Context) (Usage, error) {
	return c.getCached(ctx, c.credPath())
}

// GetCachedFor returns a specific account's usage with the same per-path TTL cache
// as Get. Used by the per-project meter so it is rate-limit-safe (it polls every
// minute and refetches on navigation, which would otherwise hammer the endpoint).
func (c *Client) GetCachedFor(ctx context.Context, credPath string) (Usage, error) {
	return c.getCached(ctx, credPath)
}

func (c *Client) getCached(ctx context.Context, path string) (Usage, error) {
	c.mu.Lock()
	if e, ok := c.cache[path]; ok {
		ttl := cacheTTL
		if e.err != nil {
			ttl = errBackoff // a cached rate-limit: stay backed off longer
		}
		if time.Since(e.at) < ttl {
			u, err := e.u, e.err
			c.mu.Unlock()
			return u, err
		}
	}
	c.mu.Unlock()

	u, err := c.fetch(ctx, path)
	// Cache successes (short TTL) and rate-limits (long backoff) only. Other
	// transient errors (token briefly expired, upstream blip) are NOT cached, so
	// the meter recovers as soon as upstream does instead of staying dark.
	if err == nil || errors.Is(err, ErrRateLimited) {
		c.mu.Lock()
		c.cache[path] = cacheEntry{u: u, err: err, at: time.Now()}
		c.mu.Unlock()
	}
	return u, err
}

// GetFor fetches usage for an explicit credentials path, bypassing the
// active-account resolution and the TTL cache. The session guard uses it to poll
// every connected account's 5-hour window, not just the active one, so it can
// pause the sessions of whichever subscription hits its limit.
func (c *Client) GetFor(ctx context.Context, credPath string) (Usage, error) {
	return c.fetch(ctx, credPath)
}

func (c *Client) fetch(ctx context.Context, credPath string) (Usage, error) {
	if credPath == "" {
		return Usage{}, fmt.Errorf("no active account")
	}
	raw, err := os.ReadFile(credPath)
	if err != nil {
		return Usage{}, fmt.Errorf("read credentials: %w", err)
	}
	var cr credentials
	if err := json.Unmarshal(raw, &cr); err != nil {
		return Usage{}, fmt.Errorf("parse credentials: %w", err)
	}
	tok := cr.ClaudeAiOauth.AccessToken
	if tok == "" {
		return Usage{}, fmt.Errorf("no oauth access token")
	}
	// The claude CLI refreshes this token in place. If it is expired we do NOT
	// refresh it ourselves: that would race the CLI and could corrupt its auth
	// file. Report unavailable until the next session refreshes it.
	if cr.ClaudeAiOauth.ExpiresAt > 0 && time.Now().UnixMilli() >= cr.ClaudeAiOauth.ExpiresAt {
		return Usage{}, fmt.Errorf("oauth token expired")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, usageURL, nil)
	if err != nil {
		return Usage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("anthropic-beta", betaHeader)
	resp, err := c.http.Do(req)
	if err != nil {
		return Usage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return Usage{}, fmt.Errorf("%w (HTTP 429)", ErrRateLimited)
	}
	if resp.StatusCode != http.StatusOK {
		return Usage{}, fmt.Errorf("usage endpoint: HTTP %d", resp.StatusCode)
	}
	var up upstream
	if err := json.NewDecoder(resp.Body).Decode(&up); err != nil {
		return Usage{}, fmt.Errorf("decode usage: %w", err)
	}
	return Usage{
		Session:    up.FiveHour.toWindow(),
		Week:       up.SevenDay.toWindow(),
		WeekOpus:   up.SevenDayOpus.toWindow(),
		WeekSonnet: up.SevenDaySonnet.toWindow(),
	}, nil
}
