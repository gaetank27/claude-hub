// Package whatsapp manages persistent WhatsApp Web connections for the Hub.
// Each configured WhatsApp tool gets its own whatsmeow client backed by a
// per-tool SQLite database under <DataDir>/whatsapp/<toolID>.db.
//
// Two modes:
//   - personal: exposes read/send actions to Claude sessions via MCP tools.
//   - bot: additionally listens for incoming messages and routes them to Hub
//     sessions, creating a new session per chat in the configured project.
package whatsapp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	waProto "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waEvents "go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	stdlog "log"
	"google.golang.org/protobuf/proto"

	"system/internal/store"
	"system/internal/streamjson"
)

// ipv4HTTPClient returns an *http.Client whose dialer is restricted to IPv4.
// WhatsApp resolves to IPv6 addresses on some DNS servers; this server has no
// IPv6 route, so we force tcp4 for all WA connections.
// We clone http.DefaultTransport to preserve all TLS/ALPN/HTTP2 defaults and
// only override the DialContext — same approach as whatsmeow's SetProxy.
func ipv4HTTPClient() *http.Client {
	d := &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = func(ctx context.Context, _, addr string) (net.Conn, error) {
		return d.DialContext(ctx, "tcp4", addr)
	}
	return &http.Client{Transport: t}
}

// applyIPv4 forces all HTTP/WS connections for a whatsmeow client to use IPv4.
func applyIPv4(c *whatsmeow.Client) {
	h := ipv4HTTPClient()
	c.SetWebsocketHTTPClient(h)
	c.SetPreLoginHTTPClient(h)
	c.SetMediaHTTPClient(h)
}

// waLogger wraps Go's standard logger to implement waLog.Logger, logging only
// WARN+ events so the journal stays readable without debug noise.
type waLogger struct{ prefix string }

func newWALogger(toolID string) waLog.Logger { return &waLogger{prefix: "whatsapp/" + toolID} }

func (l *waLogger) Errorf(msg string, args ...any) {
	stdlog.Printf("[WA ERROR] "+l.prefix+": "+msg, args...)
}
func (l *waLogger) Warnf(msg string, args ...any) {
	stdlog.Printf("[WA WARN]  "+l.prefix+": "+msg, args...)
}
func (l *waLogger) Infof(msg string, args ...any) {
	stdlog.Printf("[WA INFO]  "+l.prefix+": "+msg, args...)
}
func (l *waLogger) Debugf(_ string, _ ...any) {} // too verbose
func (l *waLogger) Sub(mod string) waLog.Logger {
	return &waLogger{prefix: l.prefix + "/" + mod}
}

// Status of a WhatsApp connection.
type Status string

const (
	StatusDisconnected Status = "disconnected"
	StatusPairing      Status = "pairing" // QR code shown, awaiting scan
	StatusConnected    Status = "connected"
	StatusError        Status = "error"
)

// QRInfo is the payload returned by the QR endpoint.
type QRInfo struct {
	Status Status `json:"status"`
	QR     string `json:"qr,omitempty"` // set only while pairing
	Error  string `json:"error,omitempty"`
}

// Chat is a minimal chat entry returned by GetChats.
type Chat struct {
	JID          string    `json:"jid"`
	Name         string    `json:"name"`
	LastMessage  string    `json:"lastMessage,omitempty"`
	LastActivity time.Time `json:"lastActivity"` // zero = no known message
}

// Message is a stored message returned by GetMessages.
type Message struct {
	ID         string    `json:"id"`
	Chat       string    `json:"chat,omitempty"`
	From       string    `json:"from"`
	SenderName string    `json:"senderName,omitempty"`
	Text       string    `json:"text"`
	Kind       string    `json:"kind,omitempty"` // text | image | video | voice | audio | document | sticker | location | contact
	Timestamp  time.Time `json:"timestamp"`
	FromMe     bool      `json:"fromMe"`
}

// IncomingMessage is what a MessageObserver is told about each message received
// from someone else, before any bot whitelist.
type IncomingMessage struct {
	ToolID     string
	Chat       string   // chat JID as WhatsApp delivered it
	Aliases    []string // every JID form known for that chat (@lid, @s.whatsapp.net)
	Sender     string   // sender JID, phone-number form when known
	SenderName string
	Text       string
	Kind       string
	Time       time.Time
	Group      bool // group, broadcast, status or newsletter
}

// MessageObserver is told about every message received from another person.
type MessageObserver interface {
	ObserveWhatsApp(IncomingMessage)
}

// HubManager is the subset of session.Manager that the WhatsApp manager needs.
type HubManager interface {
	LaunchSession(ctx context.Context, projectID, permissionMode, model, comment, source string) (store.Session, error)
	SendUserMessage(sessionID, text string) error
	SendUserBlocks(sessionID, text string, images []streamjson.ImageBlock) error
	GetSessionState(sessionID string) (string, error)
	StopSession(ctx context.Context, sessionID string) error
}

// AssistantRouter receives the bot-mode messages instead of a per-chat session
// when the personal assistant is wired in (see internal/assistant).
type AssistantRouter interface {
	HandleWhatsApp(toolID, chatJID, sender, text string, images []streamjson.ImageBlock)
	// AcceptMedia vets a photo/document before it is downloaded (nil = take it).
	AcceptMedia(name, mime string, size int64) error
	// RefuseMedia tells the user on WhatsApp why a file was not taken.
	RefuseMedia(toolID, chatJID string, reason error)
	// HandleWhatsAppMedia receives a downloaded photo/document with its caption.
	HandleWhatsAppMedia(toolID, chatJID, sender, caption string, m Media)
}

// Media is a photo or document the user sent to the assistant.
type Media struct {
	Name string // original file name (generated for photos)
	Mime string
	Data []byte
}

// entry holds one live WhatsApp connection.
type entry struct {
	mu     sync.RWMutex
	client *whatsmeow.Client
	status Status
	qr     string // latest QR code string (empty unless pairing)
	errMsg string

	// bot-mode routing
	botMode        bool
	botProjectID   string
	allowedNumbers []string  // empty = all numbers allowed
	sessions       map[string]string // chatJID → Hub sessionID
	sessionMu    sync.Mutex

	// persistent message store (for GetMessages / GetChats)
	hist *historyDB

	histReq   map[string]time.Time // chat → last on-demand history request
	histReqMu sync.Mutex

	cancel context.CancelFunc
}

func (e *entry) setStatus(s Status, qr, errMsg string) {
	e.mu.Lock()
	e.status = s
	e.qr = qr
	e.errMsg = errMsg
	e.mu.Unlock()
}

func (e *entry) getQRInfo() QRInfo {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return QRInfo{Status: e.status, QR: e.qr, Error: e.errMsg}
}

// Manager maintains all WhatsApp connections for the Hub.
type Manager struct {
	mu      sync.Mutex
	entries map[string]*entry // toolID → entry
	dataDir string            // base dir; per-tool DBs go under <dataDir>/whatsapp/
	hub     HubManager
	// assistant, when set, takes every bot-mode message.
	assistant AssistantRouter
	observer  MessageObserver
}

// SetObserver registers the listener told about every received message.
func (m *Manager) SetObserver(o MessageObserver) {
	m.mu.Lock()
	m.observer = o
	m.mu.Unlock()
}

// SetAssistant routes bot-mode messages to the personal assistant.
func (m *Manager) SetAssistant(a AssistantRouter) {
	m.mu.Lock()
	m.assistant = a
	m.mu.Unlock()
}

// transcribeScript is the faster-whisper transcription helper, written to
// <dataDir>/transcribe.py on startup so updates are always deployed.
const transcribeScript = `#!/usr/bin/env python3
"""Transcribe audio via faster-whisper. Usage: transcribe.py <audio_path>"""
import sys, os
from faster_whisper import WhisperModel

model_dir = os.path.join(os.path.dirname(os.path.abspath(__file__)), "whisper_model")
model = WhisperModel("small", device="cpu", compute_type="int8", download_root=model_dir)
audio_path = sys.argv[1]
segments, _ = model.transcribe(audio_path, beam_size=5)
print(" ".join(s.text.strip() for s in segments))
`

// New creates a Manager. hub may be nil if bot mode is not used.
func New(dataDir string, hub HubManager) *Manager {
	m := &Manager{
		entries: make(map[string]*entry),
		dataDir: dataDir,
		hub:     hub,
	}
	scriptPath := filepath.Join(dataDir, "transcribe.py")
	if err := os.WriteFile(scriptPath, []byte(transcribeScript), 0o755); err != nil {
		stdlog.Printf("[WA WARN] failed to write transcribe script: %v", err)
	}
	return m
}

// transcribeAudio saves audioData to a temp file and runs the whisper script.
func (m *Manager) transcribeAudio(audioData []byte) (string, error) {
	tmp, err := os.CreateTemp("", "wa-audio-*.ogg")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(audioData); err != nil {
		tmp.Close()
		return "", err
	}
	tmp.Close()

	scriptPath := filepath.Join(m.dataDir, "transcribe.py")
	out, err := exec.Command("python3", scriptPath, tmp.Name()).Output()
	if err != nil {
		return "", fmt.Errorf("whisper: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// dbPath returns the SQLite path for a tool.
func (m *Manager) dbPath(toolID string) string {
	return filepath.Join(m.dataDir, "whatsapp", toolID+".db")
}

// sessionsPath returns the path of the persistent chatJID→sessionID mapping file.
func (m *Manager) sessionsPath(toolID string) string {
	return filepath.Join(m.dataDir, "whatsapp", toolID+".sessions.json")
}

// loadPersistedSessions reads the chatJID→sessionID mapping from disk.
func (m *Manager) loadPersistedSessions(toolID string) map[string]string {
	data, err := os.ReadFile(m.sessionsPath(toolID))
	if err != nil {
		return make(map[string]string)
	}
	out := make(map[string]string)
	if err := json.Unmarshal(data, &out); err != nil || out == nil {
		return make(map[string]string)
	}
	return out
}

// writeFileAtomic writes data to a temp file and renames it over path, so a
// crash or a concurrent reader never sees a half-written file.
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// persistSessions saves the chatJID→sessionID mapping to disk (best-effort).
func (m *Manager) persistSessions(toolID string, sessions map[string]string) {
	data, err := json.Marshal(sessions)
	if err != nil {
		return
	}
	if err := writeFileAtomic(m.sessionsPath(toolID), data); err != nil {
		stdlog.Printf("[WA] %s: persist sessions: %v", toolID, err)
	}
}

// messagesPath is the legacy JSON buffer, imported once into the history store.
func (m *Manager) messagesPath(toolID string) string {
	return filepath.Join(m.dataDir, "whatsapp", toolID+".messages.json")
}

// historyPath returns the path of the persistent message store.
func (m *Manager) historyPath(toolID string) string {
	return filepath.Join(m.dataDir, "whatsapp", toolID+".history.db")
}

// Ensure starts (or restarts) the WA client for toolID with the given config.
// If the device was previously paired it connects immediately; otherwise it
// stays in disconnected state until StartQR is called.
func (m *Manager) Ensure(toolID string, botMode bool, botProjectID string, allowedNumbers []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if e, ok := m.entries[toolID]; ok {
		// Already running — just update bot config.
		e.mu.Lock()
		e.botMode = botMode
		e.botProjectID = botProjectID
		e.allowedNumbers = allowedNumbers
		e.mu.Unlock()
		return nil
	}

	e := &entry{
		botMode:        botMode,
		botProjectID:   botProjectID,
		allowedNumbers: allowedNumbers,
		sessions:       m.loadPersistedSessions(toolID),
		histReq:        map[string]time.Time{},
		status:         StatusDisconnected,
	}
	m.entries[toolID] = e

	if err := os.MkdirAll(filepath.Dir(m.dbPath(toolID)), 0o700); err != nil {
		return fmt.Errorf("mkdir whatsapp data: %w", err)
	}
	hist, err := openHistory(m.historyPath(toolID))
	if err != nil {
		return fmt.Errorf("open whatsapp history: %w", err)
	}
	e.hist = hist

	logger := newWALogger(toolID)
	container, err := sqlstore.New(context.Background(), "sqlite",
		"file:"+m.dbPath(toolID)+"?_pragma=foreign_keys(1)", logger)
	if err != nil {
		return fmt.Errorf("open whatsapp db: %w", err)
	}

	device, err := container.GetFirstDevice(context.Background())
	if err != nil {
		return fmt.Errorf("get device: %w", err)
	}

	client := whatsmeow.NewClient(device, logger)
	applyIPv4(client)
	e.client = client
	if n := hist.importLegacy(m.messagesPath(toolID), func(j string) string { return canonicalJID(client, j) }); n > 0 {
		stdlog.Printf("[WA INFO]  whatsapp/%s: imported %d legacy buffered messages", toolID, n)
	}

	client.AddEventHandler(func(evt any) {
		m.handleEvent(toolID, e, evt)
	})

	// Auto-connect if already paired.
	if device.ID != nil {
		ctx, cancel := context.WithCancel(context.Background())
		e.cancel = cancel
		e.setStatus(StatusPairing, "", "")
		go func() {
			if err := client.Connect(); err != nil {
				e.setStatus(StatusError, "", err.Error())
			}
			<-ctx.Done()
		}()
	}

	return nil
}

// StartQR begins a QR pairing flow for an unpaired client. It stores new QR
// codes in the entry so the polling endpoint can serve them. Blocks until
// success, timeout or ctx cancellation.
func (m *Manager) StartQR(ctx context.Context, toolID string) error {
	m.mu.Lock()
	e, ok := m.entries[toolID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("tool %s not initialised — call Ensure first", toolID)
	}

	e.mu.RLock()
	already := e.status == StatusConnected
	e.mu.RUnlock()
	if already {
		return nil // already connected
	}

	if e.cancel != nil {
		e.cancel() // stop any prior attempt
	}

	// Re-open a fresh client so GetQRChannel works on a clean state.
	logger := newWALogger(toolID)
	container, err := sqlstore.New(ctx, "sqlite",
		"file:"+m.dbPath(toolID)+"?_pragma=foreign_keys(1)", logger)
	if err != nil {
		return fmt.Errorf("open whatsapp db: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return fmt.Errorf("get device: %w", err)
	}

	client := whatsmeow.NewClient(device, logger)
	applyIPv4(client)
	e.mu.Lock()
	e.client = client
	e.mu.Unlock()

	client.AddEventHandler(func(evt any) {
		m.handleEvent(toolID, e, evt)
	})

	// connCtx must be created BEFORE GetQRChannel: whatsmeow watches it and
	// disconnects the client when it is cancelled. Passing the HTTP-request ctx
	// here caused an immediate cancellation (StartQR returns before the handler
	// goroutine's defer cancel() fires), breaking the pairing mid-scan.
	connCtx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel

	qrChan, err := client.GetQRChannel(connCtx)
	if err != nil {
		cancel()
		return fmt.Errorf("get QR channel: %w", err)
	}

	go func() {
		// Do NOT defer cancel() here — that would cancel connCtx and kill
		// the QR channel as soon as Connect() returns, which prevents
		// whatsmeow's built-in auto-reconnect from working. Connect() returns
		// when WA closes the socket; auto-reconnect then runs in the background
		// using cli.BackgroundEventCtx (our connCtx is unrelated to it).
		// We only cancel on a hard error that won't auto-reconnect.
		stdlog.Printf("[WA INFO]  whatsapp/%s: connecting…", toolID)
		if err := client.Connect(); err != nil {
			stdlog.Printf("[WA ERROR] whatsapp/%s: Connect() error: %v", toolID, err)
			e.setStatus(StatusError, "", err.Error())
			cancel()
		} else {
			stdlog.Printf("[WA INFO]  whatsapp/%s: Connect() returned (auto-reconnect may follow)", toolID)
		}
	}()

	go func() {
		// connCtx lifetime is tied to the QR pairing flow, not to a single
		// Connect() call. Cancel when the flow ends (success/timeout/error).
		defer cancel()
		for evt := range qrChan {
			stdlog.Printf("[WA INFO]  whatsapp/%s: QR event=%q error=%v", toolID, evt.Event, evt.Error)
			switch evt.Event {
			case "code":
				e.setStatus(StatusPairing, evt.Code, "")
			case "success":
				e.setStatus(StatusConnected, "", "")
				return
			case "timeout":
				e.setStatus(StatusDisconnected, "", "QR code expired — try again")
				return
			case "error":
				msg := "pairing error"
				if evt.Error != nil {
					msg = evt.Error.Error()
				}
				e.setStatus(StatusError, "", msg)
				return
			default:
				e.setStatus(StatusError, "", "unexpected QR event: "+evt.Event)
				return
			}
		}
		stdlog.Printf("[WA INFO]  whatsapp/%s: QR channel closed", toolID)
	}()

	return nil
}

// QRInfo returns the current pairing status for a tool.
func (m *Manager) QRInfo(toolID string) QRInfo {
	m.mu.Lock()
	e, ok := m.entries[toolID]
	m.mu.Unlock()
	if !ok {
		return QRInfo{Status: StatusDisconnected}
	}
	return e.getQRInfo()
}

// IsConnected reports whether the tool's WA client is currently online.
func (m *Manager) IsConnected(toolID string) bool {
	m.mu.Lock()
	e, ok := m.entries[toolID]
	m.mu.Unlock()
	if !ok {
		return false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.status == StatusConnected
}

// SendMessage sends a text message to jidStr via the tool's WA account.
func (m *Manager) SendMessage(ctx context.Context, toolID, jidStr, text string) error {
	m.mu.Lock()
	e, ok := m.entries[toolID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("no whatsapp client for tool %s", toolID)
	}
	e.mu.RLock()
	client := e.client
	connected := e.status == StatusConnected
	e.mu.RUnlock()
	if !connected || client == nil {
		return fmt.Errorf("whatsapp not connected")
	}

	jid, err := types.ParseJID(jidStr)
	if err != nil {
		return fmt.Errorf("invalid JID %q: %w", jidStr, err)
	}
	msg := &waProto.Message{Conversation: proto.String(text)}
	resp, err := client.SendMessage(ctx, jid, msg)
	if err == nil {
		// Store the outgoing message so get_messages returns the full conversation.
		e.record(canonicalJID(client, jid.String()), Message{
			ID: resp.ID, From: "me", Text: text, Kind: "text", Timestamp: time.Now(), FromMe: true,
		})
	}
	return err
}

// SendImage uploads an image (already vetted and re-encoded by the caller) and
// sends it to jidStr with an optional caption.
func (m *Manager) SendImage(ctx context.Context, toolID, jidStr string, data []byte, mime, caption string) error {
	m.mu.Lock()
	e, ok := m.entries[toolID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("no whatsapp client for tool %s", toolID)
	}
	e.mu.RLock()
	client := e.client
	connected := e.status == StatusConnected
	e.mu.RUnlock()
	if !connected || client == nil {
		return fmt.Errorf("whatsapp not connected")
	}
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		return fmt.Errorf("invalid JID %q: %w", jidStr, err)
	}
	up, err := client.Upload(ctx, data, whatsmeow.MediaImage)
	if err != nil {
		return fmt.Errorf("upload image: %w", err)
	}
	img := &waProto.ImageMessage{
		URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
		Mimetype: proto.String(mime), FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
		FileLength: proto.Uint64(up.FileLength),
	}
	if caption != "" {
		img.Caption = proto.String(caption)
	}
	resp, err := client.SendMessage(ctx, jid, &waProto.Message{ImageMessage: img})
	if err == nil {
		e.record(canonicalJID(client, jid.String()), Message{
			ID: resp.ID, From: "me", Text: caption, Kind: "image", Timestamp: time.Now(), FromMe: true,
		})
	}
	return err
}

// record stores a message (best-effort).
func (e *entry) record(chat string, msg Message) {
	if e.hist == nil {
		return
	}
	if err := e.hist.add(chat, msg); err != nil {
		stdlog.Printf("[WA WARN] store message: %v", err)
	}
}

// canonicalJID maps a chat JID to the key it is stored under: the phone-number
// form (@s.whatsapp.net) when a @lid is known to belong to one, else unchanged.
func canonicalJID(c *whatsmeow.Client, raw string) string {
	jid, err := types.ParseJID(raw)
	if err != nil {
		return raw
	}
	jid = jid.ToNonAD()
	if jid.Server == types.HiddenUserServer && c != nil && c.Store != nil && c.Store.LIDs != nil {
		if pn, err := c.Store.LIDs.GetPNForLID(context.Background(), jid); err == nil && !pn.IsEmpty() {
			return pn.ToNonAD().String()
		}
	}
	return jid.String()
}

// chatAliases lists every key a chat may be stored under (canonical first).
func chatAliases(c *whatsmeow.Client, raw string) []string {
	canon := canonicalJID(c, raw)
	out := []string{canon}
	add := func(s string) {
		for _, o := range out {
			if o == s {
				return
			}
		}
		out = append(out, s)
	}
	add(raw)
	if jid, err := types.ParseJID(canon); err == nil && jid.Server == types.DefaultUserServer && c != nil && c.Store != nil && c.Store.LIDs != nil {
		if lid, err := c.Store.LIDs.GetLIDForPN(context.Background(), jid); err == nil && !lid.IsEmpty() {
			add(lid.ToNonAD().String())
		}
	}
	return out
}

// maxChats caps GetChats.
const maxChats = 500

// GetChats lists chats, most recently active first. Contacts without any known
// message follow, by name. query (optional) filters by name or number, ignoring
// case and accents. The @lid and phone-number forms of a contact are merged.
func (m *Manager) GetChats(toolID, query string, limit int) ([]Chat, error) {
	m.mu.Lock()
	e, ok := m.entries[toolID]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no whatsapp client for tool %s", toolID)
	}
	e.mu.RLock()
	client := e.client
	e.mu.RUnlock()
	if client == nil || e.hist == nil {
		return nil, fmt.Errorf("whatsapp not connected")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > maxChats {
		limit = maxChats
	}

	byJID := map[string]*Chat{}
	get := func(jid string) *Chat {
		c := byJID[jid]
		if c == nil {
			c = &Chat{JID: jid}
			byJID[jid] = c
		}
		return c
	}
	if contacts, err := client.Store.Contacts.GetAllContacts(context.Background()); err == nil {
		for jid, info := range contacts {
			name := info.FullName
			if name == "" {
				name = info.PushName
			}
			if name == "" {
				name = info.BusinessName
			}
			c := get(canonicalJID(client, jid.String()))
			if c.Name == "" || (name != "" && info.FullName != "") {
				c.Name = name
			}
		}
	}
	acts, err := e.hist.lastActivity()
	if err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	for _, a := range acts {
		c := get(canonicalJID(client, a.chat))
		if c.LastActivity.IsZero() || a.ts.After(c.LastActivity) {
			c.LastActivity = a.ts
			c.LastMessage = Message{Text: a.text, Kind: a.kind}.Render()
		}
	}
	// Names for groups and unnamed chats.
	var groups map[string]string
	for jid, c := range byJID {
		if c.Name != "" && !strings.HasSuffix(c.Name, "@lid") {
			continue
		}
		if strings.HasSuffix(jid, "@"+types.GroupServer) {
			if groups == nil {
				groups = map[string]string{}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				if gs, err := client.GetJoinedGroups(ctx); err == nil {
					for _, g := range gs {
						groups[g.JID.String()] = g.Name
					}
				}
				cancel()
			}
			c.Name = groups[jid]
		}
	}

	q := fold(strings.TrimSpace(query))
	qDigits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, q)
	out := make([]Chat, 0, len(byJID))
	for jid, c := range byJID {
		if c.Name == "" && c.LastActivity.IsZero() {
			continue // bare LID/number with nothing to show
		}
		if c.Name == "" {
			c.Name = jid
		}
		if q != "" && !strings.Contains(fold(c.Name), q) && !(len(qDigits) >= 3 && strings.Contains(jid, qDigits)) {
			continue
		}
		out = append(out, *c)
	}
	sortChats(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// onDemandCooldown limits history requests sent to the phone per chat.
const onDemandCooldown = 10 * time.Minute

// GetMessages returns the stored messages of a chat (oldest first), whichever
// JID form (@lid or phone number) they arrived under. requested reports that
// older history was asked of the phone and may show up on a later call.
func (m *Manager) GetMessages(toolID, chatJID string, limit int) (msgs []Message, requested bool, err error) {
	m.mu.Lock()
	e, ok := m.entries[toolID]
	m.mu.Unlock()
	if !ok {
		return nil, false, fmt.Errorf("no whatsapp client for tool %s", toolID)
	}
	if e.hist == nil {
		return nil, false, fmt.Errorf("history store not ready")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	e.mu.RLock()
	client := e.client
	connected := e.status == StatusConnected
	e.mu.RUnlock()
	aliases := chatAliases(client, chatJID)
	msgs, err = e.hist.messages(aliases, limit)
	if err != nil {
		return nil, false, err
	}
	if len(msgs) < limit && connected && client != nil {
		requested = m.requestHistory(e, client, aliases, limit-len(msgs))
	}
	return msgs, requested, nil
}

// requestHistory asks the phone for messages older than the oldest we hold
// (WhatsApp's on-demand history sync). It needs an anchor message, so it does
// nothing for a chat with no stored message. Replies arrive as HistorySync
// events and are stored; the phone must be online.
func (m *Manager) requestHistory(e *entry, client *whatsmeow.Client, aliases []string, count int) bool {
	anchor, ok := e.hist.oldest(aliases)
	if !ok {
		return false
	}
	e.histReqMu.Lock()
	if time.Since(e.histReq[aliases[0]]) < onDemandCooldown {
		e.histReqMu.Unlock()
		return false
	}
	e.histReq[aliases[0]] = time.Now()
	e.histReqMu.Unlock()
	chat, err := types.ParseJID(anchor.Chat)
	if err != nil {
		return false
	}
	if count < 50 {
		count = 50
	}
	info := &types.MessageInfo{
		MessageSource: types.MessageSource{Chat: chat, IsFromMe: anchor.FromMe},
		ID:            anchor.ID,
		Timestamp:     anchor.Timestamp,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := client.SendPeerMessage(ctx, client.BuildHistorySyncRequest(info, count)); err != nil {
		stdlog.Printf("[WA WARN] history request: %v", err)
		return false
	}
	return true
}

// storeHistory records a history blob pushed by the phone.
func (m *Manager) storeHistory(e *entry, client *whatsmeow.Client, evt *waEvents.HistorySync) {
	if evt.Data == nil {
		return
	}
	n := 0
	for _, conv := range evt.Data.GetConversations() {
		chat, err := types.ParseJID(conv.GetID())
		if err != nil || chat.Server == types.BroadcastServer {
			continue
		}
		key := canonicalJID(client, chat.String())
		for _, hm := range conv.GetMessages() {
			parsed, err := client.ParseWebMessage(chat, hm.GetMessage())
			if err != nil || parsed.Message == nil {
				continue
			}
			text, kind := describe(parsed.Message)
			if kind == "" {
				continue
			}
			from := "me"
			if !parsed.Info.IsFromMe {
				from = canonicalJID(client, parsed.Info.Sender.String())
			}
			e.record(key, Message{
				ID: parsed.Info.ID, From: from, SenderName: parsed.Info.PushName, Text: text, Kind: kind,
				Timestamp: parsed.Info.Timestamp, FromMe: parsed.Info.IsFromMe,
			})
			n++
		}
	}
	stdlog.Printf("[WA INFO]  whatsapp: history sync stored %d messages", n)
}

// Stop disconnects a WA client.
func (m *Manager) Stop(toolID string) {
	m.mu.Lock()
	e, ok := m.entries[toolID]
	if ok {
		delete(m.entries, toolID)
	}
	m.mu.Unlock()
	if ok {
		if e.cancel != nil {
			e.cancel()
		}
		e.mu.RLock()
		c := e.client
		e.mu.RUnlock()
		if c != nil {
			c.Disconnect()
		}
	}
}

// handleEvent processes whatsmeow events for one tool entry.
func (m *Manager) handleEvent(toolID string, e *entry, evt any) {
	switch v := evt.(type) {
	case *waEvents.Connected:
		e.setStatus(StatusConnected, "", "")
	case *waEvents.Disconnected:
		e.mu.RLock()
		cur := e.status
		e.mu.RUnlock()
		if cur != StatusPairing {
			e.setStatus(StatusDisconnected, "", "")
		}
	case *waEvents.Message:
		m.handleIncoming(toolID, e, v)
	case *waEvents.HistorySync:
		e.mu.RLock()
		client := e.client
		e.mu.RUnlock()
		if client != nil {
			go m.storeHistory(e, client, v)
		}
	}
}

// numberAllowed reports whether senderNum (the digits of a WhatsApp JID, i.e.
// country code + number) matches a whitelist entry. International entries
// ("+41790000000", "0041 79 000 00 00", "41790000000") must match exactly.
// A local-format entry ("0790000000") carries no country code, so it matches
// that national number behind any 1–3 digit country code — prefer the
// international format, which is unambiguous.
func numberAllowed(entry, senderNum string) bool {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, entry)
	international := strings.HasPrefix(strings.TrimSpace(entry), "+")
	if strings.HasPrefix(digits, "00") {
		digits, international = digits[2:], true
	}
	if digits == "" || senderNum == "" {
		return false
	}
	if international || !strings.HasPrefix(digits, "0") {
		return digits == senderNum
	}
	national := strings.TrimLeft(digits, "0")
	cc := len(senderNum) - len(national)
	// A bare national number is too short to be trusted as a suffix.
	return len(national) >= 7 && cc >= 1 && cc <= 3 && strings.HasSuffix(senderNum, national)
}

// handleIncoming processes a received WhatsApp message.
func (m *Manager) handleIncoming(toolID string, e *entry, evt *waEvents.Message) {
	e.mu.RLock()
	client := e.client
	e.mu.RUnlock()
	chatJID := evt.Info.Chat.String()
	chatKey := canonicalJID(client, chatJID)
	// A document sent with a caption arrives wrapped in another message.
	if w := evt.Message.GetDocumentWithCaptionMessage().GetMessage(); w != nil && evt.Message.GetDocumentMessage() == nil {
		evt.Message = w
	}

	if evt.Info.IsFromMe {
		// Sent by the user from another device (or by us): history only.
		if text, kind := describe(evt.Message); kind != "" {
			e.record(chatKey, Message{
				ID: evt.Info.ID, From: "me", Text: text, Kind: kind,
				Timestamp: evt.Info.Timestamp, FromMe: true,
			})
		}
		return
	}

	m.mu.Lock()
	assistantRouter := m.assistant
	m.mu.Unlock()
	// With the assistant wired in, photos/documents are downloaded later, and only
	// once the message is known to be the user's own chat with it.
	text, images := m.extractContent(e, evt, assistantRouter == nil)
	capt, kind := describe(evt.Message)
	caption := text // what the user wrote with a photo/document (media routing)
	if kind == "image" || kind == "document" {
		text = capt // history/observers also see the document's file name
		caption = capt
		if kind == "document" {
			caption = evt.Message.GetDocumentMessage().GetCaption()
		}
	}
	if text == "" && len(images) == 0 && kind == "" {
		return
	}
	if kind == "" {
		kind = "text"
	}

	senderJID := evt.Info.Sender
	// When the message is addressed by LID, Sender.User is an internal ID;
	// the actual phone number is in SenderAlt (parsed from sender_pn attribute).
	if senderJID.Server == types.HiddenUserServer && !evt.Info.SenderAlt.IsEmpty() {
		senderJID = evt.Info.SenderAlt
	}
	senderKey := canonicalJID(client, senderJID.String())
	e.record(chatKey, Message{
		ID: evt.Info.ID, From: senderKey, SenderName: evt.Info.PushName, Text: text, Kind: kind,
		Timestamp: evt.Info.Timestamp,
	})

	m.mu.Lock()
	observer := m.observer
	m.mu.Unlock()
	if observer != nil {
		observer.ObserveWhatsApp(IncomingMessage{
			ToolID: toolID, Chat: chatJID, Aliases: chatAliases(client, chatJID),
			Sender: senderKey, SenderName: evt.Info.PushName, Text: text, Kind: kind,
			Time:  evt.Info.Timestamp,
			Group: evt.Info.IsGroup || evt.Info.Chat.Server != types.DefaultUserServer && evt.Info.Chat.Server != types.HiddenUserServer,
		})
	}

	media := assistantRouter != nil && (kind == "image" || kind == "document" || kind == "video")
	if text == "" && len(images) == 0 && !media {
		return // media with no caption: stored and observed, nothing to route
	}

	// Whitelist check.
	e.mu.RLock()
	allowed := e.allowedNumbers
	e.mu.RUnlock()
	if len(allowed) > 0 {
		senderNum := senderJID.User
		ok := false
		for _, n := range allowed {
			if numberAllowed(n, senderNum) {
				ok = true
				break
			}
		}
		if !ok {
			stdlog.Printf("[WA BOT] %s: sender %s (LID=%s) blocked by whitelist %v", toolID, senderNum, evt.Info.Sender.User, allowed)
			return
		}
	}

	// Bot mode: route to a Hub session.
	e.mu.RLock()
	botMode := e.botMode
	projectID := e.botProjectID
	e.mu.RUnlock()

	m.mu.Lock()
	assistant := m.assistant
	m.mu.Unlock()
	if botMode && assistant != nil {
		sender := evt.Info.PushName
		if sender == "" {
			sender = evt.Info.Sender.User
		}
		if media {
			m.routeMedia(e, assistant, toolID, chatJID, sender, caption, evt)
			return
		}
		assistant.HandleWhatsApp(toolID, chatJID, sender, text, images)
		return
	}

	if !botMode || m.hub == nil || projectID == "" {
		stdlog.Printf("[WA BOT] %s: not routing — botMode=%v hub=%v projectID=%q", toolID, botMode, m.hub != nil, projectID)
		return
	}

	// Find or create a session for this chat.
	e.sessionMu.Lock()
	prevSessID := e.sessions[chatJID]
	e.sessionMu.Unlock()

	if prevSessID != "" {
		// Check if the previous session is still running.
		state, err := m.hub.GetSessionState(prevSessID)
		if err == nil && state == "running" {
			stdlog.Printf("[WA BOT] %s: injecting into session %.8s", toolID, prevSessID)
			// Prepend a reminder so the bot never forgets to reply via WhatsApp.
			reminder := fmt.Sprintf("[Rappel : réponds UNIQUEMENT via mcp__tools__whatsapp_send_message avec chat_jid=%q — ne réponds jamais dans ce terminal]\n\n%s", chatJID, text)
			m.sendToSession(prevSessID, reminder, images)
			return
		}
		// Session is gone or stopped — will create a new one below.
		stdlog.Printf("[WA BOT] %s: previous session %.8s is %v, creating new", toolID, prevSessID, state)
	}

	// Create a new session for this chat, stopping any previous zombie first.
	if prevSessID != "" {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = m.hub.StopSession(stopCtx, prevSessID)
		stopCancel()
	}

	senderName := evt.Info.PushName
	if senderName == "" {
		senderName = evt.Info.Sender.User
	}
	comment := "WhatsApp · " + senderName

	stdlog.Printf("[WA BOT] %s: launching session for chat %s sender %s", toolID, chatJID, evt.Info.Sender.User)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sess, err := m.hub.LaunchSession(ctx, projectID, "bypassPermissions", "", comment, "whatsapp")
	if err != nil {
		stdlog.Printf("[WA BOT] %s: LaunchSession failed: %v", toolID, err)
		return
	}
	stdlog.Printf("[WA BOT] %s: session created %.8s", toolID, sess.ID)

	e.sessionMu.Lock()
	e.sessions[chatJID] = sess.ID
	m.persistSessions(toolID, e.sessions)
	e.sessionMu.Unlock()

	// Initial context + first message.
	initial := fmt.Sprintf(
		"Tu es Claude, assistant IA de l'utilisateur, disponible via WhatsApp.\n"+
			"Expéditeur : %s (%s)\n"+
			"Chat JID : %s\n\n"+
			"RÈGLE ABSOLUE : tu dois TOUJOURS répondre via mcp__tools__whatsapp_send_message avec chat_jid=%q.\n"+
			"Ne réponds JAMAIS dans ce terminal — l'utilisateur ne le voit pas.\n\n"+
			"Si le message ci-dessous manque de contexte, utilise d'abord mcp__tools__whatsapp_get_messages\n"+
			"(tool_id=%q, chat_jid=%q, limit=50) pour lire l'historique (messages reçus et envoyés), puis réponds sur WhatsApp.\n\n"+
			"Message reçu :\n%s",
		senderName, evt.Info.Sender.String(), chatJID, chatJID, toolID, chatJID, text,
	)
	m.sendToSession(sess.ID, initial, images)
}

// sendToSession injects a message with optional images into a bot session.
func (m *Manager) sendToSession(sessID, text string, images []streamjson.ImageBlock) {
	if len(images) > 0 {
		_ = m.hub.SendUserBlocks(sessID, text, images)
	} else {
		_ = m.hub.SendUserMessage(sessID, text)
	}
}

// extractContent returns the text and images from an incoming WhatsApp message.
// Audio messages are transcribed synchronously via faster-whisper.
func (m *Manager) extractContent(e *entry, evt *waEvents.Message, inlineImages bool) (string, []streamjson.ImageBlock) {
	// Plain text
	if t := evt.Message.GetConversation(); t != "" {
		return t, nil
	}
	if t := evt.Message.GetExtendedTextMessage().GetText(); t != "" {
		return t, nil
	}

	// Image with optional caption
	if img := evt.Message.GetImageMessage(); img != nil && !inlineImages {
		return img.GetCaption(), nil
	}
	if img := evt.Message.GetImageMessage(); img != nil {
		e.mu.RLock()
		client := e.client
		e.mu.RUnlock()
		if client == nil {
			return img.GetCaption(), nil
		}
		data, err := client.Download(context.Background(), img)
		if err != nil {
			stdlog.Printf("[WA WARN] image download: %v", err)
			return img.GetCaption(), nil
		}
		mime := img.GetMimetype()
		if mime == "" {
			mime = "image/jpeg"
		}
		if idx := strings.Index(mime, ";"); idx >= 0 {
			mime = strings.TrimSpace(mime[:idx])
		}
		return img.GetCaption(), []streamjson.ImageBlock{{
			MediaType: mime,
			Data:      base64.StdEncoding.EncodeToString(data),
		}}
	}

	// Voice / audio → transcribe
	if audio := evt.Message.GetAudioMessage(); audio != nil {
		e.mu.RLock()
		client := e.client
		e.mu.RUnlock()
		if client == nil {
			return "[Message vocal]", nil
		}
		data, err := client.Download(context.Background(), audio)
		if err != nil {
			stdlog.Printf("[WA WARN] audio download: %v", err)
			return "[Message vocal — téléchargement échoué]", nil
		}
		text, err := m.transcribeAudio(data)
		if err != nil {
			stdlog.Printf("[WA WARN] transcription: %v", err)
			return "[Message vocal — transcription échouée]", nil
		}
		if text == "" {
			return "[Message vocal — vide]", nil
		}
		return "[Vocal] " + text, nil
	}

	return "", nil
}

// SanitizeJID normalises a phone-number string to a WhatsApp JID by appending
// "@s.whatsapp.net" if there is no "@" present.
func SanitizeJID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	if !strings.Contains(raw, "@") {
		raw = raw + "@s.whatsapp.net"
	}
	return raw
}

// mediaTimeout bounds one photo/document download.
const mediaTimeout = 2 * time.Minute

// routeMedia downloads the photo/document of a message from the user's own
// private chat with the assistant and hands it over. Groups and other chats never
// reach the download; unsupported or oversized files are refused up front.
func (m *Manager) routeMedia(e *entry, assistant AssistantRouter, toolID, chatJID, sender, caption string, evt *waEvents.Message) {
	if evt.Info.IsGroup || evt.Info.Chat.Server != types.DefaultUserServer && evt.Info.Chat.Server != types.HiddenUserServer {
		return
	}
	var (
		dl         whatsmeow.DownloadableMessage
		name, mime string
		size       int64
	)
	switch {
	case evt.Message.GetImageMessage() != nil:
		img := evt.Message.GetImageMessage()
		dl, mime, size = img, mimeBase(img.GetMimetype(), "image/jpeg"), int64(img.GetFileLength())
		ext := map[string]string{"image/png": "png", "image/webp": "webp", "image/gif": "gif"}[mime]
		if ext == "" {
			ext = "jpg"
		}
		name = "photo-" + evt.Info.Timestamp.Format("20060102-150405") + "." + ext
	case evt.Message.GetDocumentMessage() != nil:
		d := evt.Message.GetDocumentMessage()
		dl, name, mime, size = d, d.GetFileName(), mimeBase(d.GetMimetype(), ""), int64(d.GetFileLength())
	case evt.Message.GetVideoMessage() != nil:
		v := evt.Message.GetVideoMessage()
		dl, name, mime, size = v, "video.mp4", "video/mp4", int64(v.GetFileLength())
	default:
		return
	}
	if err := assistant.AcceptMedia(name, mime, size); err != nil {
		stdlog.Printf("[WA BOT] %s: media refused: %v", toolID, err)
		assistant.RefuseMedia(toolID, chatJID, err)
		return
	}
	e.mu.RLock()
	client := e.client
	e.mu.RUnlock()
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), mediaTimeout)
	defer cancel()
	data, err := client.Download(ctx, dl)
	if err != nil {
		stdlog.Printf("[WA WARN] media download: %v", err)
		assistant.RefuseMedia(toolID, chatJID, fmt.Errorf("Je n'ai pas pu télécharger ce fichier depuis WhatsApp. Renvoie-le."))
		return
	}
	assistant.HandleWhatsAppMedia(toolID, chatJID, sender, caption, Media{Name: name, Mime: mime, Data: data})
}

func mimeBase(mime, def string) string {
	if i := strings.Index(mime, ";"); i >= 0 {
		mime = mime[:i]
	}
	if mime = strings.ToLower(strings.TrimSpace(mime)); mime != "" {
		return mime
	}
	return def
}
