package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"system/internal/streamjson"
)

// maxInputFrameBytes bounds a single browser→runner WebSocket message. It
// matches the runner's per-line scanner buffer (internal/runner/socket.go) so a
// frame that survives the upgrade also fits downstream; the composer's image
// size caps keep real frames well under it.
const maxInputFrameBytes = 8 * 1024 * 1024

// wsInput is the browser→runner frame: a user prompt with optional pasted
// images and inline documents (e.g. PDFs). Each carries raw base64 (no data:
// URI prefix) and a media type. Larger files (zip/tgz/…) are not inlined here;
// they are uploaded out-of-band to the project working dir via the attachments
// endpoint and referenced by path in Text.
type wsInput struct {
	Text   string `json:"text"`
	Images []struct {
		MediaType string `json:"mediaType"`
		Data      string `json:"data"`
	} `json:"images"`
	Documents []struct {
		MediaType string `json:"mediaType"`
		Data      string `json:"data"`
	} `json:"documents"`
	// Voice marks a turn dictated in voice mode whose reply will be read aloud.
	Voice bool `json:"voice"`
}

// voiceHint tells the model its answer will be spoken. stripVoiceHint in the
// web client hides it from the transcript.
const voiceHint = "[Mode vocal : ta réponse sera lue à voix haute. Réponds en phrases courtes et naturelles, sans markdown, sans liste, sans code ni URL.]\n"

// buildInput turns a browser frame into a stream-json user line. A frame is the
// JSON envelope above; a frame that does not parse as that envelope is treated
// as a legacy raw prompt string, so an old browser tab still works across a
// rolling restart.
func buildInput(data []byte) ([]byte, error) {
	var in wsInput
	if err := json.Unmarshal(data, &in); err == nil && (in.Text != "" || len(in.Images) > 0 || len(in.Documents) > 0) {
		if in.Voice {
			in.Text = voiceHint + in.Text
		}
		if len(in.Images) == 0 && len(in.Documents) == 0 {
			return streamjson.UserInput(in.Text)
		}
		imgs := make([]streamjson.ImageBlock, len(in.Images))
		for i, img := range in.Images {
			imgs[i] = streamjson.ImageBlock{MediaType: img.MediaType, Data: img.Data}
		}
		docs := make([]streamjson.DocumentBlock, len(in.Documents))
		for i, doc := range in.Documents {
			docs[i] = streamjson.DocumentBlock{MediaType: doc.MediaType, Data: doc.Data}
		}
		return streamjson.UserInputBlocks(in.Text, imgs, docs)
	}
	return streamjson.UserInput(string(data))
}

// inputText extracts the prompt text from a browser frame (envelope or legacy raw string).
func inputText(data []byte) string {
	var in wsInput
	if err := json.Unmarshal(data, &in); err == nil && (in.Text != "" || len(in.Images) > 0 || len(in.Documents) > 0) {
		return in.Text
	}
	return string(data)
}

// streamSession bridges a browser WebSocket to the session's runner socket:
//   - runner → browser: every journaled/live stream-json line is forwarded as a
//     text frame (the connection opens with the full journal replay, so a
//     browser that reconnects after a backend restart sees the whole history);
//   - browser → runner: each incoming frame is a JSON {text, images} envelope
//     (see buildInput), turned into a stream-json user message and written to
//     claude's stdin.
func (s *Server) streamSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	conn, err := s.mgr.Attach(sessionID)
	if err != nil {
		writeNotFoundOr500(w, err)
		return
	}
	defer conn.Close()

	// The hub is reachable on a public domain, so a cross-site page must not be
	// able to open this socket against an authenticated user's cookie (CSWSH).
	// When the public origin is known, restrict the upgrade to it; OriginPatterns
	// matches the browser's Origin host, which is robust to the reverse proxy
	// rewriting the Host header. With no known origin (dev) the check is skipped
	// so a local browser still connects.
	opts := &websocket.AcceptOptions{}
	if len(s.allowedOrigins) > 0 {
		opts.OriginPatterns = s.allowedOrigins
	} else {
		opts.InsecureSkipVerify = true
	}
	ws, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	defer ws.Close(websocket.StatusInternalError, "closing")

	// A pasted-image frame is several MB of base64; the library's default 32KB
	// read limit would close the socket with StatusMessageTooBig and the input
	// would never reach the runner. Lift it to the runner's 8MB per-line cap
	// (socket.go), which the composer's size caps already stay under.
	ws.SetReadLimit(maxInputFrameBytes)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// runner → browser
	go func() {
		defer cancel()
		sc := bufio.NewScanner(conn)
		sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			if ev := streamjson.Parse(line); ev.IsTurnBoundary() {
				s.db.TouchSession(sessionID, time.Now().UTC())
			}
			if err := ws.Write(ctx, websocket.MessageText, line); err != nil {
				return
			}
		}
	}()

	// browser → runner
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		input, err := buildInput(data)
		if err != nil {
			continue
		}
		s.mgr.MaybeAutoName(sessionID, inputText(data))
		if _, err := conn.Write(append(input, '\n')); err != nil {
			return
		}
	}
}
