package rag

import "strings"

// Chunking parameters. Sizes are in characters (~4 chars/token), tuned so a chunk
// stays well under typical embedding context while carrying enough surrounding
// text to be self-contained. Overlap preserves continuity across boundaries.
const (
	chunkMaxChars = 2000
	chunkOverlap  = 200
)

// Chunk is a slice of source text ready to embed.
type Chunk struct {
	Idx        int
	Text       string
	TokenCount int // rough estimate (chars/4), for display only
}

// Chunkify splits text into overlapping chunks, snapping cut points to the
// nearest paragraph or whitespace boundary so chunks don't split mid-word. Empty
// or whitespace-only input yields no chunks.
func Chunkify(text string) []Chunk {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	runes := []rune(text)
	var chunks []Chunk
	idx := 0
	for start := 0; start < len(runes); {
		end := start + chunkMaxChars
		if end >= len(runes) {
			end = len(runes)
		} else {
			end = snapBoundary(runes, start, end)
		}
		piece := strings.TrimSpace(string(runes[start:end]))
		if piece != "" {
			chunks = append(chunks, Chunk{Idx: idx, Text: piece, TokenCount: len(piece) / 4})
			idx++
		}
		if end >= len(runes) {
			break
		}
		// Step forward, keeping an overlap window for continuity.
		next := end - chunkOverlap
		if next <= start {
			next = end // pathological: no boundary found, avoid an infinite loop
		}
		start = next
	}
	return chunks
}

// snapBoundary pulls the cut point back to the last paragraph break, then to the
// last whitespace, within the chunk window — but never past a small floor so a
// boundaryless blob still makes progress.
func snapBoundary(runes []rune, start, end int) int {
	floor := start + chunkMaxChars/2
	window := string(runes[start:end])
	if i := strings.LastIndex(window, "\n\n"); i >= 0 && start+i > floor {
		return start + i
	}
	if i := strings.LastIndexAny(window, " \n\t"); i >= 0 && start+i > floor {
		return start + i
	}
	return end
}
