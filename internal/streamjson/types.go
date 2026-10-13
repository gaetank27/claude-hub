// Package streamjson models the newline-delimited JSON protocol exchanged with
// the `claude` CLI when run with --output-format stream-json / --input-format
// stream-json. Slice 1 only needs to forward raw lines and recognize a few
// structural events (init, result) — it deliberately does not fully decode the
// nested Anthropic message payloads.
package streamjson

import (
	"encoding/json"
)

// Event types emitted by the CLI on stdout.
const (
	TypeSystem    = "system" // session lifecycle, incl. subtype "init"
	TypeAssistant = "assistant"
	TypeUser      = "user"
	TypeResult    = "result"       // end of a turn — carries usage/cost
	TypeStream    = "stream_event" // partial chunks (--include-partial-messages)
)

// Envelope is a lightly-parsed view of a single stream-json line. The original
// bytes are preserved verbatim in Raw so the runner can journal and fan them out
// without lossy re-serialization.
type Envelope struct {
	Raw []byte // the exact line as received (without trailing newline)

	Type      string `json:"type"`
	Subtype   string `json:"subtype,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`

	// Present on result events; best-effort, may be zero.
	Usage        *Usage   `json:"usage,omitempty"`
	TotalCostUSD *float64 `json:"total_cost_usd,omitempty"`
	// DurationMS is the wall-clock time of a turn (result events); per-turn, so it
	// sums directly across a session. It is Claude's working time, distinct from
	// the session's start→end span.
	DurationMS *int `json:"duration_ms,omitempty"`
}

// Usage carries token accounting from a result event. Used by later slices for
// tokenomics; parsed here so the journal stays the single source of truth.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// Parse decodes the structural header of a stream-json line, retaining the raw
// bytes. A line that fails to decode is still returned with Raw set so it can be
// journaled and forwarded unchanged.
func Parse(line []byte) Envelope {
	cp := make([]byte, len(line))
	copy(cp, line)
	e := Envelope{Raw: cp}
	_ = json.Unmarshal(line, &e) // tolerate malformed/partial lines
	return e
}

// IsTurnBoundary reports whether this event ends a turn, i.e. a point at which
// the journal must be fsync'd so a crash cannot lose a completed exchange.
func (e Envelope) IsTurnBoundary() bool {
	return e.Type == TypeResult
}

// UserInput builds an input line for the CLI's --input-format stream-json mode,
// representing a text-only user prompt with a string content payload.
func UserInput(text string) ([]byte, error) {
	msg := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": text,
		},
	}
	return json.Marshal(msg)
}

// ImageBlock is a base64-encoded image for UserInputBlocks. Data is the raw
// base64 payload (no data: URI prefix); MediaType is e.g. "image/png".
type ImageBlock struct {
	MediaType string
	Data      string
}

// DocumentBlock is a base64-encoded document (e.g. a PDF) for UserInputBlocks.
// Data is the raw base64 payload (no data: URI prefix); MediaType is e.g.
// "application/pdf". Rendered as an Anthropic "document" content block so the
// model reads it natively (PDF vision/OCR), the same way images use vision.
type DocumentBlock struct {
	MediaType string
	Data      string
}

// UserInputBlocks builds a user message whose content is an array of blocks: an
// optional leading text block, then image blocks, then document blocks. This is
// the Anthropic Messages content-block shape, which the CLI accepts on
// --input-format stream-json (verified against claude 2.1.x). Use it when a
// prompt carries images or documents; for text-only prompts UserInput's string
// form is sufficient.
func UserInputBlocks(text string, images []ImageBlock, docs []DocumentBlock) ([]byte, error) {
	content := make([]any, 0, 1+len(images)+len(docs))
	if text != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	for _, img := range images {
		content = append(content, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": img.MediaType,
				"data":       img.Data,
			},
		})
	}
	for _, doc := range docs {
		content = append(content, map[string]any{
			"type": "document",
			"source": map[string]any{
				"type":       "base64",
				"media_type": doc.MediaType,
				"data":       doc.Data,
			},
		})
	}
	msg := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": content,
		},
	}
	return json.Marshal(msg)
}
