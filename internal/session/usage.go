package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"

	"system/internal/store"
	"system/internal/streamjson"
)

// AccountTotals is the cumulative usage of every session stamped with a Claude
// account, plus how many of them are currently live.
type AccountTotals struct {
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
	Active       int
}

// AccountUsageTotals sums the journal-derived usage of all sessions belonging to
// a Claude account and counts its live sessions. Cheap (SessionUsage is cached by
// journal size+mtime); used by the metrics sampler to feed the per-account RRD.
func (m *Manager) AccountUsageTotals(accountID string) AccountTotals {
	var t AccountTotals
	sessions, err := m.db.ListSessions()
	if err != nil {
		return t
	}
	for _, s := range sessions {
		if s.ClaudeAccountID != accountID {
			continue
		}
		u := m.SessionUsage(s.ID)
		t.InputTokens += int64(u.InputTokens)
		t.OutputTokens += int64(u.OutputTokens)
		t.CostUSD += u.CostUSD
		if s.State == store.StateRunning || s.State == store.StateStarting {
			t.Active++
		}
	}
	return t
}

// SessionUsage is the cost/token accounting derived from a session's journal.
type SessionUsage struct {
	CostUSD      float64 `json:"costUSD"`
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	// ContextTokens is the current context-window occupancy: the prompt size of
	// the last API call (input + cache writes + cache reads of the latest
	// assistant message), not a sum across the session.
	ContextTokens int `json:"contextTokens"`
	// ComputeMS is the cumulative wall-clock time Claude spent working (sum of the
	// per-turn result durations), distinct from the session's start→end span.
	ComputeMS int `json:"computeMs"`
	// Skills maps each skill invoked in the session (via the Skill tool) to how
	// many times it was used. Empty when the session used no skills.
	Skills map[string]int `json:"skills,omitempty"`
	// Rags maps each RAG knowledge base queried in the session to how many times
	// it was searched. Keys are the collection ref passed to knowledge_search, or
	// the sentinels ragAllCollections (a global search with no explicit
	// collection) and ragProjectCode (the project's private code RAG). Empty when
	// the session queried no RAG.
	Rags map[string]int `json:"rags,omitempty"`
}

// Sentinel RAG refs used as map keys in SessionUsage.Rags when a search does not
// name a specific collection. The frontend maps them to friendly labels.
const (
	ragAllCollections = "*all*"     // knowledge_search without an explicit collection (every global base)
	ragProjectCode    = "*project*" // project_code_search against the project's private RAG
)

// usageEntry caches a journal's computed usage, keyed by the file's size+mtime
// so a frozen (finished) journal is parsed at most once.
type usageEntry struct {
	size  int64
	mtime int64
	val   SessionUsage
}

// SessionUsage returns the cost/token/context totals for a session, derived from
// its journal. Results are cached by (size, mtime): a finished session's journal
// never changes, so it is parsed once; a running session is re-parsed only when
// its journal grows. A missing/unreadable journal yields a zero value (a
// just-created session has no usage yet), never an error.
func (m *Manager) SessionUsage(sessionID string) SessionUsage {
	s, err := m.db.GetSession(sessionID)
	if err != nil {
		return SessionUsage{}
	}
	fi, err := os.Stat(s.JournalPath)
	if err != nil {
		return SessionUsage{}
	}
	size, mtime := fi.Size(), fi.ModTime().UnixNano()

	m.usageMu.Lock()
	if e, ok := m.usageCache[sessionID]; ok && e.size == size && e.mtime == mtime {
		m.usageMu.Unlock()
		return e.val
	}
	m.usageMu.Unlock()

	val := computeUsage(s.JournalPath)

	m.usageMu.Lock()
	m.usageCache[sessionID] = usageEntry{size: size, mtime: mtime, val: val}
	m.usageMu.Unlock()
	return val
}

// assistantMessage decodes the parts of an assistant message we account for:
// the per-call token usage (message.usage, mirroring the frontend's contextOf —
// a single API call's prompt size) and the tool_use content blocks, used to
// detect Skill invocations.
type assistantMessage struct {
	Message struct {
		Usage   *streamjson.Usage `json:"usage"`
		Content []struct {
			Type  string `json:"type"`
			ID    string `json:"id"`
			Name  string `json:"name"`
			Input struct {
				Skill      string `json:"skill"`
				Collection string `json:"collection"`
			} `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

// computeUsage parses a journal and aggregates usage, mirroring the frontend's
// aggregateUsage + contextOf (web/src/components/SessionStream.tsx):
//   - input/output tokens are per-turn and sum directly across result events;
//   - total_cost_usd is cumulative within a CLI run and resets on --resume, so
//     it is summed as a per-run delta (full value taken when it drops below the
//     previous, marking a new run) rather than summed raw;
//   - context is the latest assistant message's usage (input + cache), NOT the
//     result usage, which aggregates the whole turn's calls.
func computeUsage(journalPath string) SessionUsage {
	f, err := os.Open(journalPath)
	if err != nil {
		return SessionUsage{}
	}
	defer f.Close()

	var u SessionUsage
	var prevCost float64
	// seenTools dedups Skill invocations by tool_use id, so a tool block that
	// recurs across journal lines is counted once.
	seenTools := map[string]bool{}
	sc := bufio.NewScanner(f)
	// Journals can carry large lines (base64 image blocks); match the runner's
	// 16MB scanner ceiling so a long line is never silently truncated.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		ev := streamjson.Parse(line)
		switch ev.Type {
		case streamjson.TypeResult:
			if ev.TotalCostUSD != nil {
				c := *ev.TotalCostUSD
				if c >= prevCost {
					u.CostUSD += c - prevCost
				} else {
					u.CostUSD += c
				}
				prevCost = c
			}
			if ev.Usage != nil {
				u.InputTokens += ev.Usage.InputTokens
				u.OutputTokens += ev.Usage.OutputTokens
			}
			if ev.DurationMS != nil {
				u.ComputeMS += *ev.DurationMS
			}
		case streamjson.TypeAssistant:
			// Decode only assistant lines that carry usage (for context size), a
			// Skill tool_use (skills breakdown) or a knowledge RAG tool_use (rags
			// breakdown); others are skipped.
			if !bytes.Contains(line, []byte(`"usage"`)) &&
				!bytes.Contains(line, []byte(`"name":"Skill"`)) &&
				!bytes.Contains(line, []byte("mcp__knowledge__")) {
				continue
			}
			var a assistantMessage
			if json.Unmarshal(line, &a) != nil {
				continue
			}
			if a.Message.Usage != nil {
				mu := a.Message.Usage
				u.ContextTokens = mu.InputTokens + mu.CacheCreationInputTokens + mu.CacheReadInputTokens
			}
			for _, c := range a.Message.Content {
				// Dedup by tool_use id (unique per call), so a block recurring
				// across journal lines is counted once.
				if c.Type != "tool_use" || c.ID == "" || seenTools[c.ID] {
					continue
				}
				switch {
				case c.Name == "Skill" && c.Input.Skill != "":
					seenTools[c.ID] = true
					bumpCount(&u.Skills, c.Input.Skill)
				case c.Name == "mcp__knowledge__knowledge_search":
					seenTools[c.ID] = true
					ref := c.Input.Collection
					if ref == "" {
						ref = ragAllCollections
					}
					bumpCount(&u.Rags, ref)
				case c.Name == "mcp__knowledge__project_code_search":
					seenTools[c.ID] = true
					bumpCount(&u.Rags, ragProjectCode)
				}
			}
		}
	}
	return u
}

// bumpCount increments m[key], lazily allocating the map on first use.
func bumpCount(m *map[string]int, key string) {
	if *m == nil {
		*m = map[string]int{}
	}
	(*m)[key]++
}
