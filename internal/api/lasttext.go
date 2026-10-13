package api

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// lastTextTail bounds how much of the journal end is scanned for the last
// assistant text message.
const lastTextTail = 4 << 20

type lastTextEntry struct {
	mtime time.Time
	size  int64
	ts    *time.Time
}

var lastTextCache sync.Map // journal path -> lastTextEntry

// journalLastText returns the timestamp of the latest assistant message that
// carries visible text (not thinking, tool calls or stream deltas): the only
// output that should flag a session as unread. Nil when there is none.
func journalLastText(path string) *time.Time {
	if path == "" {
		return nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if v, ok := lastTextCache.Load(path); ok {
		e := v.(lastTextEntry)
		if e.mtime.Equal(fi.ModTime()) && e.size == fi.Size() {
			return e.ts
		}
	}
	ts := scanLastText(path, fi.Size())
	lastTextCache.Store(path, lastTextEntry{fi.ModTime(), fi.Size(), ts})
	return ts
}

func scanLastText(path string, size int64) *time.Time {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if size > lastTextTail {
		if _, err := f.Seek(size-lastTextTail, io.SeekStart); err != nil {
			return nil
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	var best int64
	for _, line := range bytes.Split(data, []byte("\n")) {
		if !bytes.Contains(line, []byte(`"type":"assistant"`)) || !bytes.Contains(line, []byte(`"type":"text"`)) {
			continue
		}
		var ev struct {
			TS      int64  `json:"_ts"`
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &ev) != nil || ev.Type != "assistant" {
			continue // also skips a partial first line of the tail window
		}
		for _, c := range ev.Message.Content {
			if c.Type == "text" && strings.TrimSpace(c.Text) != "" && ev.TS > best {
				best = ev.TS
			}
		}
	}
	if best == 0 {
		return nil
	}
	t := time.UnixMilli(best).UTC()
	return &t
}
