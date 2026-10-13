package push

import (
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// AssistantSessionID is the sessionId of the assistant's pushes. It is not a
	// session of the Hub: the app opens its Assistant screen for it.
	AssistantSessionID = "assistant"
	assistantProject   = "Assistant"

	// assistantExcerpt is the longest notification text, in characters.
	assistantExcerpt = 180
	// assistantEvery is the shortest gap between two assistant pushes; messages
	// arriving inside it are merged into one push sent when it ends.
	assistantEvery = 20 * time.Second
)

// assistantGate spaces the assistant's pushes: the first goes out at once, the
// ones that follow within the window collapse into a single trailing push that
// carries the latest text.
type assistantGate struct {
	mu      sync.Mutex
	last    time.Time
	waiting string
	timer   *time.Timer
}

// NotifyAssistant pushes a short, markdown-free excerpt of an assistant message
// to the registered phones, as a "reply" payload for the Assistant screen.
func (n *Notifier) NotifyAssistant(text string) {
	text = Excerpt(text, assistantExcerpt)
	if text == "" {
		return
	}
	g := &n.gate
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.timer != nil { // a push is already waiting: the newer text replaces it
		g.waiting = text
		return
	}
	if wait := assistantEvery - time.Since(g.last); wait > 0 {
		g.waiting = text
		g.timer = time.AfterFunc(wait, func() {
			g.mu.Lock()
			t := g.waiting
			g.waiting, g.timer, g.last = "", nil, time.Now()
			g.mu.Unlock()
			n.sendAssistant(t)
		})
		return
	}
	g.last = time.Now()
	go n.sendAssistant(text)
}

func (n *Notifier) sendAssistant(text string) {
	n.send(Payload{SessionID: AssistantSessionID, Kind: "reply", Project: assistantProject, Text: text})
}

var (
	reFence  = regexp.MustCompile("(?s)```.*?(```|$)")
	reImage  = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	reLink   = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	reLead   = regexp.MustCompile(`(?m)^\s*(#{1,6}\s+|>\s?|[-*+•]\s+|\d+[.)]\s+)`)
	reMarks  = regexp.MustCompile("[*_~`]{1,3}")
	reTable  = regexp.MustCompile(`(?m)^\s*\|?[\s:|-]+\|[\s:|-]*$`)
	reSpaces = regexp.MustCompile(`\s+`)
)

// Excerpt reduces markdown text to a plain one-line extract of at most max
// characters (an ellipsis marks a cut).
func Excerpt(s string, max int) string {
	s = reFence.ReplaceAllString(s, " ")
	s = reImage.ReplaceAllString(s, "$1")
	s = reLink.ReplaceAllString(s, "$1")
	s = reTable.ReplaceAllString(s, " ")
	s = reLead.ReplaceAllString(s, "")
	s = reMarks.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "|", " ")
	s = strings.TrimSpace(reSpaces.ReplaceAllString(s, " "))
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)[:max]
	return strings.TrimSpace(string(r)) + "…"
}
