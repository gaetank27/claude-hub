package session

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"regexp"
	"sync"
	"time"

	"system/internal/streamjson"
)

// prURLRe matches a GitHub pull-request URL claude prints after opening a PR for
// approved autonomous work. Compiled once.
var prURLRe = regexp.MustCompile(`https://github\.com/[^/\s"]+/[^/\s"]+/pull/\d+`)

// Hand-back tuning for autonomous sessions. Conservative defaults; later wired to
// env/config. maxTurns bounds a runaway autonomous loop; inactivity flags a
// session that finished a turn and is idle, waiting for a human.
const (
	defaultMaxTurns   = 40
	defaultInactivity = 3 * time.Minute
)

// Attention is the hand-back state of an autonomous session: when an event
// requires the user to step back in, Needed is set with a human-readable Reason.
// It is derived state, recomputed from the journal stream, never persisted.
type Attention struct {
	Needed bool   `json:"needed"`
	Reason string `json:"reason,omitempty"`
	Turns  int    `json:"turns"`
}

// supervisor watches one autonomous session's stream and maintains its Attention
// state. It implements the slice-2 hand-back triggers: explicit question, plan
// awaiting validation, unrecoverable error / rate limit, max-turns, inactivity.
type supervisor struct {
	maxTurns   int
	inactivity time.Duration
	cancel     context.CancelFunc

	// onPRURL is invoked once with the pull-request URL claude reports for the
	// session's approved work, so the backend can persist it. May be nil.
	onPRURL func(string)

	mu           sync.Mutex
	turns        int
	lastActivity time.Time
	attention    Attention
	prSeen       bool // a PR URL has already been reported for this supervisor
}

func (s *supervisor) snapshot() Attention {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.attention
	a.Turns = s.turns
	return a
}

func (s *supervisor) set(reason string) { s.attention = Attention{Needed: true, Reason: reason} }
func (s *supervisor) clear()            { s.attention = Attention{} }

// observe folds one stream-json event into the attention state. Triggers are
// evaluated by priority; ordinary assistant activity clears transient attention.
func (s *supervisor) observe(ev streamjson.Envelope) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastActivity = time.Now()

	// A PR URL can appear in any assistant event; detect it independently of the
	// attention switch below. Fire the callback at most once per supervisor (the
	// journal is fully replayed on every reconnect, which would otherwise re-fire
	// it). Run off the lock so a DB write never stalls the stream loop.
	if !s.prSeen && s.onPRURL != nil {
		if m := prURLRe.Find(ev.Raw); m != nil {
			s.prSeen = true
			url := string(m)
			go s.onPRURL(url)
		}
	}

	switch {
	case bytes.Contains(ev.Raw, []byte(`"name":"AskUserQuestion"`)):
		s.set("question en attente de réponse")
	case bytes.Contains(ev.Raw, []byte(`"name":"ExitPlanMode"`)):
		s.set("plan à valider")
	case ev.Type == "rate_limit_event":
		s.set("limite de quota atteinte")
	case ev.Type == streamjson.TypeResult:
		s.turns++
		if ev.IsError {
			s.set("dernier tour en erreur")
		} else if s.turns >= s.maxTurns {
			s.set(fmt.Sprintf("max-turns atteint (%d)", s.turns))
		}
		// A successful result ends a turn: leave attention as-is. If the session
		// then stays idle, the inactivity ticker flags it; if claude keeps going,
		// the next assistant event clears.
	case ev.Type == streamjson.TypeAssistant:
		s.clear()
	}
}

func (s *supervisor) checkIdle() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.attention.Needed && !s.lastActivity.IsZero() &&
		time.Since(s.lastActivity) > s.inactivity {
		s.set("session inactive, en attente d'instructions")
	}
}

// run consumes the session stream until ctx is cancelled, reconnecting when the
// runner restarts (e.g. the resume after plan approval). Each (re)connection
// replays the full journal, so turns are recounted from scratch every time.
func (s *supervisor) run(ctx context.Context, dial func() (net.Conn, error)) {
	idle := time.NewTicker(5 * time.Second)
	defer idle.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-idle.C:
				s.checkIdle()
			}
		}
	}()

	for ctx.Err() == nil {
		conn, err := dial()
		if err != nil {
			sleepCtx(ctx, time.Second)
			continue
		}
		s.mu.Lock()
		s.turns = 0
		s.mu.Unlock()

		sc := bufio.NewScanner(conn)
		sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for sc.Scan() {
			s.observe(streamjson.Parse(sc.Bytes()))
		}
		conn.Close()
		sleepCtx(ctx, 500*time.Millisecond) // runner gone/restarting; retry
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
