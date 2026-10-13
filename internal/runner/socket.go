package runner

import (
	"bufio"
	"net"
	"os"
	"strconv"
	"sync"
	"time"
)

// subscriberBuffer bounds how far a client may lag before it is dropped.
// A dropped client simply reconnects and replays the journal, so no data is
// ever truly lost — this only protects the hub from a stalled reader.
const subscriberBuffer = 1024

// Hub fans out live stream-json lines to connected socket clients and serves
// the journal replay on subscribe. Publish and Subscribe are mutually exclusive
// so every line reaches each subscriber exactly once: a line is either part of a
// subscriber's replay snapshot or delivered live to it, never both nor neither.
type Hub struct {
	mu      sync.Mutex
	journal *Journal
	jpath   string
	subs    map[chan []byte]struct{}
}

func newHub(journal *Journal, journalPath string) *Hub {
	return &Hub{
		journal: journal,
		jpath:   journalPath,
		subs:    make(map[chan []byte]struct{}),
	}
}

// Publish appends a line to the journal (fsync'd when sync is true) and
// broadcasts it to all live subscribers.
func (h *Hub) Publish(line []byte, sync bool) error {
	line = stampTime(line)
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.journal.Append(line, sync); err != nil {
		return err
	}
	for ch := range h.subs {
		select {
		case ch <- line:
		default:
			// Subscriber too slow: drop it. It will reconnect and replay.
			close(ch)
			delete(h.subs, ch)
		}
	}
	return nil
}

// subscribe registers a live channel and returns the journal replay snapshot
// captured atomically with registration.
func (h *Hub) subscribe() (replay []byte, ch chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	replay, _ = ReadJournal(h.jpath)
	ch = make(chan []byte, subscriberBuffer)
	h.subs[ch] = struct{}{}
	return replay, ch
}

func (h *Hub) unsubscribe(ch chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[ch]; ok {
		close(ch)
		delete(h.subs, ch)
	}
}

// SocketServer accepts client connections on a Unix socket. Each connection
// receives the journal replay then the live stream; lines sent by the client
// are forwarded to onInput (which writes them to the claude stdin).
type SocketServer struct {
	hub     *Hub
	ln      net.Listener
	onInput func([]byte)
}

func listenSocket(path string, hub *Hub, onInput func([]byte)) (*SocketServer, error) {
	// Remove a stale socket left by a previous crashed runner.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return &SocketServer{hub: hub, ln: ln, onInput: onInput}, nil
}

func (s *SocketServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return // listener closed
		}
		go s.handle(conn)
	}
}

func (s *SocketServer) handle(conn net.Conn) {
	defer conn.Close()
	replay, ch := s.hub.subscribe()
	defer s.hub.unsubscribe(ch)

	// Read user input from the client and feed it to claude.
	go func() {
		sc := bufio.NewScanner(conn)
		sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for sc.Scan() {
			line := make([]byte, len(sc.Bytes()))
			copy(line, sc.Bytes())
			if s.onInput != nil {
				s.onInput(line)
			}
		}
	}()

	// Send replay snapshot, then stream live lines.
	if len(replay) > 0 {
		if _, err := conn.Write(replay); err != nil {
			return
		}
	}
	for line := range ch {
		if _, err := conn.Write(append(line, '\n')); err != nil {
			return
		}
	}
}

func (s *SocketServer) close() {
	s.ln.Close()
}

// stampTime injects a "_ts" (unix ms) field into a JSON-object line so the UI
// can show when each message happened; claude's stream-json carries no
// timestamps. Non-object lines are returned untouched.
func stampTime(line []byte) []byte {
	if len(line) < 2 || line[0] != '{' {
		return line
	}
	pre := append([]byte(`{"_ts":`), strconv.FormatInt(time.Now().UnixMilli(), 10)...)
	if line[1] != '}' {
		pre = append(pre, ',')
	}
	return append(pre, line[1:]...)
}
