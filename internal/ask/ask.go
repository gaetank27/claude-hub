// Package ask bridges a Claude session's blocking "ask the user" tool to the
// human in the browser. Because the hub runs claude headless
// (--output-format stream-json), the built-in AskUserQuestion tool is auto-denied
// by the CLI (no interactive answer channel). Instead the hub injects an
// mcp__ask__ask_user tool (cmd/ask-mcp) whose handler calls the loopback endpoint
// registered here. That call BLOCKS until the user answers in the UI, so the
// session genuinely waits for a human instead of continuing on an assumed
// non-answer.
//
// Flow: ask-mcp POSTs /ask {sessionId} → Broker.Ask blocks → user clicks in the
// UI → POST /api/sessions/{id}/answer → Broker.Answer unblocks → ask-mcp returns
// the answer as the tool_result, and claude resumes with the real choice.
package ask

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
)

// Broker tracks the at-most-one in-flight question per session and the channel
// that delivers its answer. Questions are keyed by session: a session asks one
// question at a time (a single AskUserQuestion tool call), so a new question for
// the same session supersedes any stale pending one.
type Broker struct {
	mu      sync.Mutex
	pending map[string]chan string
}

// New returns an empty Broker.
func New() *Broker {
	return &Broker{pending: make(map[string]chan string)}
}

// Ask registers a pending question for sessionID and blocks until the user
// answers (via Answer) or ctx is cancelled — the latter happens when the
// ask-mcp process dies (session stopped/restarted), so a dropped question never
// leaks a goroutine. Returns the answer text, or ctx.Err() on cancellation.
func (b *Broker) Ask(ctx context.Context, sessionID string) (string, error) {
	ch := make(chan string, 1)
	b.mu.Lock()
	b.pending[sessionID] = ch
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		// Only delete if still ours: a superseding Ask may have replaced it.
		if b.pending[sessionID] == ch {
			delete(b.pending, sessionID)
		}
		b.mu.Unlock()
	}()

	select {
	case ans := <-ch:
		return ans, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Answer delivers the user's answer to a session's pending question. It reports
// whether a pending question was actually waiting (false when the question was
// already answered, superseded, or the session is no longer asking).
func (b *Broker) Answer(sessionID, answer string) bool {
	b.mu.Lock()
	ch, ok := b.pending[sessionID]
	if ok {
		delete(b.pending, sessionID)
	}
	b.mu.Unlock()
	if !ok {
		return false
	}
	ch <- answer // buffered (cap 1); never blocks
	return true
}

// Pending reports whether a session currently has a question awaiting an answer.
func (b *Broker) Pending(sessionID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.pending[sessionID]
	return ok
}

// RegisterLoopback mounts the ask-mcp-facing endpoint onto mux, sharing the same
// localhost listener and token (X-Rag-Token) as the RAG/tools loopback so only
// hub-launched MCP processes can reach it. POST /ask {sessionId} blocks until the
// user answers, then returns {answer}.
func RegisterLoopback(mux *http.ServeMux, b *Broker, token string) {
	mux.HandleFunc("POST /ask", func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get("X-Rag-Token") != token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var req struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SessionID == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		// Blocks until the user answers or the request context is cancelled (the
		// ask-mcp connection drops when the session's runner stops).
		answer, err := b.Ask(r.Context(), req.SessionID)
		if err != nil {
			// Client gone: nothing to write. Use 499-ish; the MCP is already away.
			http.Error(w, "cancelled", 499)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"answer": answer})
	})
}
