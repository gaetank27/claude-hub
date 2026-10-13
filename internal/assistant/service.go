// Package assistant is the Hub's personal assistant: one long-lived Claude
// session in a dedicated project, fed by every channel (the web page, WhatsApp,
// scheduled tasks, place triggers) and answering on the channel it was asked on.
//
// The conversation thread lives in the store (assistant_messages) so it survives
// the session: sessions are rotated and archived in the background, the thread is
// what the user sees.
package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"system/internal/scriptstatus"
	"time"

	"github.com/google/uuid"

	"system/internal/imgsafe"
	"system/internal/secretbox"
	"system/internal/store"
	"system/internal/streamjson"
)

const (
	// Source tags: the assistant's own session and the work it delegates.
	SourceAssistant = "assistant"
	SourceTask      = "assistant-task"

	projectMetaKey = "assistant_project_id"
	sessionMetaKey = "assistant_session_id"
	personMetaKey  = "assistant_person"
	waToolMetaKey  = "assistant_wa_tool"
	waJIDMetaKey   = "assistant_wa_jid"
	// pushMetaKey / pushWAMetaKey: "0" turns the phone notifications off / "1"
	// also sends them for answers already delivered on WhatsApp.
	pushMetaKey = "assistant_push"
	// modelMetaKey is the chosen model ("" = never set => DefaultModel, "hub" =
	// the Hub's global default); launchModelMetaKey is the setting the live
	// session was launched with, to spot a change to apply.
	modelMetaKey       = "assistant_model"
	launchModelMetaKey = "assistant_session_model"
	pushWAMetaKey      = "assistant_push_whatsapp"

	// contextMessages are replayed to a fresh session so it knows what was said.
	sweepEvery      = 15 // seconds between two looks at the held messages
	contextMessages = 12
	silenceMarker   = "[silence]"
)

// Hub is the part of session.Manager the assistant drives.
type Hub interface {
	CreateProject(ctx context.Context, name, dir, category string) (store.Project, error)
	LaunchSession(ctx context.Context, projectID, permissionMode, model, comment, source string) (store.Session, error)
	SendUserMessage(sessionID, text string) error
	SendUserBlocks(sessionID, text string, images []streamjson.ImageBlock) error
	StopSession(ctx context.Context, sessionID string) error
}

// WhatsApp sends a text or an image on a paired WhatsApp tool.
type WhatsApp interface {
	SendMessage(ctx context.Context, toolID, jid, text string) error
	SendImage(ctx context.Context, toolID, jid string, data []byte, mime, caption string) error
}

// origin remembers where a question came from, to route the answer back.
type origin struct {
	channel string
	toolID  string // whatsapp
	jid     string // whatsapp
	// wake lists the delegated sessions a "session" wake-up reports, so a wake-up
	// the assistant never received can be recognised and dropped (watch.go).
	wake []string
	// images are the outgoing images the assistant attached to this turn's answer
	// (outimages.go); they are linked to the reply when it is recorded.
	images []store.AssistantAttachment
}

// Service is the assistant. Create with New, start with Run.
type Service struct {
	db  *store.DB
	hub Hub
	wa  WhatsApp
	box *secretbox.Box
	// phone pushes a short notification to the user's phones (nil = none).
	phone func(text string)
	now   func() time.Time // test clock (nil = time.Now)
	hmu   sync.Mutex       // serialises the delivery of held messages (night.go)

	mu        sync.Mutex // guards everything below
	projectID string
	sessionID string
	offset    int64                  // read position in the current session's journal
	queue     []origin               // questions awaiting their answer, oldest first
	haState   string                 // last known state of the tracked person entity
	bursts    map[string]*pending    // message-trigger bursts being debounced
	media     map[string]*mediaBurst // WhatsApp files being grouped into one turn
	retention time.Duration          // how long received files are kept
	scripts   *scriptstatus.Service  // script status reports (scripts.go)
	img       *imgsafe.Guard         // outgoing-image checks and rate limit (outimages.go)

	// Delegated-session watch (watch.go).
	wmu       sync.Mutex
	asks      AskState
	finished  map[string]time.Time // session -> when its end was first seen
	asking    map[string]time.Time // session -> when its pending question was first seen
	asked     map[string]bool      // sessions whose pending question was already reported
	watchHold time.Time            // no delivery attempt before this (after a failed one)
}

func New(db *store.DB, hub Hub, wa WhatsApp, box *secretbox.Box) *Service {
	return &Service{db: db, hub: hub, wa: wa, box: box}
}

// ProjectID is the assistant project's id ("" until Run has created it).
func (s *Service) ProjectID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.projectID
}

// Run prepares the project, then watches the session journal and the tracked
// place until ctx is cancelled.
func (s *Service) Run(ctx context.Context) {
	if err := s.ensureProject(ctx); err != nil {
		log.Printf("assistant: project: %v", err)
		return
	}
	s.adoptSession()
	s.backfillWatch()
	go s.placeLoop(ctx)
	go s.purgeLoop(ctx)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for n := 0; ; n++ {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.pump()
			if n%sweepEvery == 0 {
				s.sweepHeld()
			}
			if n%watchEvery == 0 {
				s.watchPass(ctx)
			}
		}
	}
}

// ensureProject finds or creates the assistant project, writes its rules and the
// default tool policy, and makes sure the hub delegation tool exists.
func (s *Service) ensureProject(ctx context.Context) error {
	hubID := s.ensureHubTool()

	id, _ := s.db.GetAppMeta(projectMetaKey)
	var proj store.Project
	if id != "" {
		if p, err := s.db.GetProject(id); err == nil {
			proj = p
		}
	}
	if proj.ID == "" {
		p, err := s.hub.CreateProject(ctx, "Assistant", "", "assistant")
		if err != nil {
			return err
		}
		proj = p
		if err := s.db.SetAppMeta(projectMetaKey, p.ID); err != nil {
			return err
		}
		s.defaultAccess(p.ID)
	} else if hubID != "" {
		// Existing project restricted to a tool list: let it use the hub tool.
		if a, err := s.db.GetProjectAccess(proj.ID); err == nil && a.ToolsMode == "list" && !a.ToolAllowed(hubID) {
			a.AllowedTools = append(a.AllowedTools, hubID)
			_ = s.db.SetProjectAccess(proj.ID, a)
		}
	}

	if err := os.WriteFile(filepath.Join(proj.Dir, "CLAUDE.md"), []byte(claudeMD), 0o644); err != nil {
		return fmt.Errorf("write CLAUDE.md: %w", err)
	}
	mem := filepath.Join(proj.Dir, "memoire.md")
	if _, err := os.Stat(mem); os.IsNotExist(err) {
		_ = os.WriteFile(mem, []byte("# Mémoire de l'assistant\n\n"), 0o644)
	}

	s.mu.Lock()
	s.projectID = proj.ID
	s.mu.Unlock()
	return nil
}

// assistantToolTypes are the connectors a fresh assistant project may use. The
// owner widens or narrows the list from the project's Access panel.
var assistantToolTypes = map[string]bool{
	"homeassistant": true, "whatsapp": true, "imap": true, "proton": true,
	"email": true, "telegram": true, "hub": true, "outlook": true,
}

func (s *Service) defaultAccess(projectID string) {
	all, err := s.db.ListTools()
	if err != nil {
		return
	}
	ids := []string{}
	for _, t := range all {
		if assistantToolTypes[t.Type] {
			ids = append(ids, t.ID)
		}
	}
	if err := s.db.SetProjectAccess(projectID, store.ProjectAccess{ToolsMode: "list", AllowedTools: ids}); err != nil {
		log.Printf("assistant: default access: %v", err)
	}
}

// ensureHubTool creates the delegation connector instance on first start and
// returns its id.
func (s *Service) ensureHubTool() string {
	all, err := s.db.ListTools()
	if err != nil {
		return ""
	}
	for _, t := range all {
		if t.Type == "hub" {
			return t.ID
		}
	}
	enc, err := s.box.Encrypt([]byte("{}"))
	if err != nil {
		return ""
	}
	t := store.Tool{
		ID: uuid.NewString(), Type: "hub", Name: "Claude Hub (délégation)",
		Status: store.ToolConnected, Enabled: true, ConfigEnc: enc, CreatedAt: time.Now().UTC(),
	}
	if err := s.db.CreateTool(t); err != nil {
		log.Printf("assistant: create hub tool: %v", err)
		return ""
	}
	return t.ID
}

// adoptSession reattaches to the session recorded before a restart, starting at
// the end of its journal (earlier turns were already routed).
func (s *Service) adoptSession() {
	id, _ := s.db.GetAppMeta(sessionMetaKey)
	if id == "" {
		return
	}
	sess, err := s.db.GetSession(id)
	if err != nil || sess.State != store.StateRunning {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionID = id
	if fi, err := os.Stat(sess.JournalPath); err == nil {
		s.offset = fi.Size()
	}
}

// Send hands a message to the assistant. channel is web | whatsapp | task |
// place; for whatsapp, toolID/jid say where the answer goes. label describes the
// trigger for task/place messages. Images are optional (whatsapp).
func (s *Service) Send(ctx context.Context, channel, toolID, jid, label, text string, images []streamjson.ImageBlock) error {
	return s.send(ctx, channel, toolID, jid, label, text, images, false, nil)
}

// SendWithAttachments is Send for a turn carrying uploaded files (see
// SaveAttachment); text may be empty. spoken is as in SendSpoken.
func (s *Service) SendWithAttachments(ctx context.Context, channel, toolID, jid, text string, ids []string, spoken bool) error {
	atts, err := s.CheckAttachments(ids)
	if err != nil {
		return err
	}
	return s.send(ctx, channel, toolID, jid, "", text, nil, spoken, atts)
}

// SendSpoken is Send for a voice turn: the answer will be read aloud, so the
// model is told to keep it short and free of markdown. The hint is only part of
// the prompt, never of the stored thread.
func (s *Service) SendSpoken(ctx context.Context, channel, text string) error {
	return s.send(ctx, channel, "", "", "", text, nil, true, nil)
}

// VoiceHint tells the model its answer will be spoken.
const VoiceHint = "[Mode vocal : ta réponse sera lue à voix haute. Réponds en phrases courtes et naturelles, sans markdown, sans liste, sans code ni URL.]\n"

func (s *Service) send(ctx context.Context, channel, toolID, jid, label, text string, images []streamjson.ImageBlock, spoken bool, atts []store.AssistantAttachment) error {
	if strings.TrimSpace(text) == "" && len(images) == 0 && len(atts) == 0 {
		return fmt.Errorf("empty message")
	}
	role, shown := "user", text
	if shown == "" {
		shown = "[image]"
	}
	if channel == "task" || channel == "place" || channel == "message" || channel == "session" {
		role, shown = "event", label
	}
	history, _ := s.db.ListAssistantMessages(0, contextMessages)
	msg, err := s.db.AddAssistantMessage(channel, role, shown)
	if err != nil {
		return err
	}
	// The user wrote: whatever was held back for the night is handed over now,
	// before the answer to this message (night.go).
	caughtUp := 0
	if role == "user" {
		caughtUp = s.HeldCount()
		s.flushHeld("catchup")
	}
	if len(atts) > 0 {
		ids := make([]string, len(atts))
		for i, a := range atts {
			ids[i] = a.ID
		}
		if atts, err = s.db.LinkAssistantAttachments(msg.ID, ids); err != nil {
			return err
		}
	}
	if channel == "whatsapp" && toolID != "" && jid != "" {
		_ = s.db.SetAppMeta(waToolMetaKey, toolID)
		_ = s.db.SetAppMeta(waJIDMetaKey, jid)
	}

	sessID, fresh, err := s.ensureSession(ctx)
	if err != nil {
		s.reply(origin{channel: channel, toolID: toolID, jid: jid}, "⚠️ Je n'ai pas pu démarrer ma session : "+err.Error())
		return err
	}

	var b strings.Builder
	if fresh && len(history) > 0 {
		b.WriteString("Nouvelle session. Derniers échanges, pour le contexte :\n")
		for _, m := range history {
			who := "Utilisateur"
			if m.Role == "assistant" {
				who = "Toi"
			} else if m.Role == "event" {
				who = "Déclencheur"
			}
			line := oneLine(m.Text, 300)
			for _, a := range m.Attachments {
				if !a.Expired {
					full, _ := s.AttachmentFile(a)
					line += fmt.Sprintf(" [pièce jointe : %s]", full)
				}
			}
			fmt.Fprintf(&b, "- %s : %s\n", who, line)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "[%s · canal : %s]\n", time.Now().Format("2006-01-02 15:04 MST"), channel)
	if caughtUp > 0 {
		fmt.Fprintf(&b, "[Le Hub vient de remettre à l'utilisateur, en rattrapage, %d message(s) que tu avais écrits pendant la nuit : ne les répète pas, réponds seulement à son message.]\n", caughtUp)
	}
	if spoken {
		b.WriteString(VoiceHint)
	}
	if strings.TrimSpace(text) == "" && len(atts) > 0 {
		text = "(pas de texte, seulement des pièces jointes)"
	}
	b.WriteString(text)
	b.WriteString(s.attachmentsBlock(atts))

	ids, _ := ctx.Value(wakeIDsKey{}).([]string)
	s.mu.Lock()
	s.queue = append(s.queue, origin{channel: channel, toolID: toolID, jid: jid, wake: ids})
	s.mu.Unlock()

	if len(images) > 0 {
		err = s.hub.SendUserBlocks(sessID, b.String(), images)
	} else {
		err = s.hub.SendUserMessage(sessID, b.String())
	}
	if err != nil {
		s.mu.Lock()
		if n := len(s.queue); n > 0 {
			s.queue = s.queue[:n-1]
		}
		s.mu.Unlock()
		return err
	}
	return nil
}

// ensureSession returns the live assistant session, launching one when there is
// none. fresh reports a launch (the caller replays recent context).
func (s *Service) ensureSession(ctx context.Context) (id string, fresh bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.projectID == "" {
		return "", false, fmt.Errorf("assistant not ready")
	}
	if s.sessionID != "" {
		if sess, err := s.db.GetSession(s.sessionID); err == nil &&
			(sess.State == store.StateRunning || sess.State == store.StateStarting) {
			// A model change applies at the first turn once nothing is awaited:
			// never while an answer is pending (that would cut the turn).
			launched, _ := s.db.GetAppMeta(launchModelMetaKey)
			if len(s.queue) > 0 || launched == s.modelSetting() {
				return s.sessionID, false, nil
			}
			_ = s.hub.StopSession(ctx, s.sessionID)
			s.sessionID, s.offset = "", 0
		}
	}
	lctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	sess, err := s.hub.LaunchSession(lctx, s.projectID, "bypassPermissions", s.Model(), "Assistant · "+time.Now().Format("02.01 15:04"), SourceAssistant)
	if err != nil {
		return "", false, err
	}
	s.sessionID, s.offset, s.queue = sess.ID, 0, nil
	_ = s.db.SetAppMeta(sessionMetaKey, sess.ID)
	_ = s.db.SetAppMeta(launchModelMetaKey, s.modelSetting())
	return sess.ID, true, nil
}

// Busy reports whether an answer is still awaited.
func (s *Service) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue) > 0
}

// pump reads the new journal lines and routes each finished turn's answer.
func (s *Service) pump() {
	s.mu.Lock()
	id, off := s.sessionID, s.offset
	s.mu.Unlock()
	if id == "" {
		return
	}
	sess, err := s.db.GetSession(id)
	if err != nil {
		return
	}
	lines, newOff := readNew(sess.JournalPath, off)
	s.mu.Lock()
	if s.sessionID == id {
		s.offset = newOff
	}
	s.mu.Unlock()
	for _, line := range lines {
		if ids := wakeAcks(line); len(ids) > 0 {
			s.watchAck(ids)
		}
		var ev struct {
			Type    string `json:"type"`
			Result  string `json:"result"`
			IsError bool   `json:"is_error"`
		}
		if !strings.Contains(string(line), `"type":"result"`) || json.Unmarshal(line, &ev) != nil || ev.Type != "result" {
			continue
		}
		text := ev.Result
		if ev.IsError && strings.TrimSpace(text) == "" {
			text = "⚠️ Une erreur est survenue pendant le traitement."
		}
		s.mu.Lock()
		o := origin{channel: "web"}
		if len(s.queue) > 0 {
			o, s.queue = s.queue[0], s.queue[1:]
		}
		s.mu.Unlock()
		s.reply(o, text)
	}
	// A session that ended with questions pending would leave the UI waiting.
	if sess.State != store.StateRunning && sess.State != store.StateStarting {
		s.mu.Lock()
		pending := s.queue
		s.queue = nil
		s.mu.Unlock()
		for _, o := range pending {
			s.reply(o, "⚠️ Ma session s'est arrêtée avant de répondre. Renvoie ton message.")
		}
	}
}

// reply records an answer and delivers it to its channel. At night, a message
// the assistant writes on its own is held back instead (night.go).
func (s *Service) reply(o origin, text string) {
	text = strings.TrimSpace(text)
	if (text == "" && len(o.images) == 0) || strings.HasPrefix(strings.ToLower(text), silenceMarker) {
		s.DiscardAttachments(o.images) // nothing is sent: neither are the images
		return
	}
	if until, held := s.holdUntil(o); held {
		s.hold(o, text, until)
		return
	}
	if spontaneous(o) {
		// Delivered now, but earlier held messages come first (order, no loss).
		s.flushHeld("morning")
	}
	msg, err := s.db.AddAssistantMessage(o.channel, "assistant", text)
	if err != nil {
		log.Printf("assistant: record reply: %v", err)
	}
	atts := s.linkImages(msg.ID, o.images)
	tool, jid := s.waTarget(o)
	if text != "" {
		s.dispatch(tool, jid, text, text)
	}
	if len(atts) == 0 {
		return
	}
	sent := s.sendWhatsAppImages(tool, jid, atts)
	if text == "" && (sent == 0 || s.PushWhatsApp()) {
		s.notifyPhone(imagePush(atts))
	}
}

// dispatch sends text to a WhatsApp target (tool/jid, "" = none) and/or the
// phones, following the notification settings; push is the phone text.
func (s *Service) dispatch(tool, jid, text, push string) {
	if s.sendWhatsApp(tool, jid, text) {
		if s.PushWhatsApp() {
			s.notifyPhone(push)
		}
		return
	}
	s.notifyPhone(push)
}

// notifyPhone pushes text to the phones unless the notifications are off.
func (s *Service) notifyPhone(text string) {
	if s.phone != nil && s.PushEnabled() {
		s.phone(text)
	}
}

// SetPhoneNotifier wires the push of an assistant message to the phones.
func (s *Service) SetPhoneNotifier(f func(text string)) { s.phone = f }

// PushEnabled reports whether the assistant notifies the phones (default on).
func (s *Service) PushEnabled() bool {
	v, _ := s.db.GetAppMeta(pushMetaKey)
	return v != "0"
}

// PushWhatsApp reports whether answers already sent on WhatsApp are pushed too
// (default off, to avoid a double notification).
func (s *Service) PushWhatsApp() bool {
	v, _ := s.db.GetAppMeta(pushWAMetaKey)
	return v == "1"
}

// SetPush stores the notification settings.
func (s *Service) SetPush(enabled, alsoWhatsApp bool) error {
	flag := func(b bool) string {
		if b {
			return "1"
		}
		return "0"
	}
	if err := s.db.SetAppMeta(pushMetaKey, flag(enabled)); err != nil {
		return err
	}
	return s.db.SetAppMeta(pushWAMetaKey, flag(alsoWhatsApp))
}

// waTarget is the WhatsApp contact an answer goes to ("", "" = none).
func (s *Service) waTarget(o origin) (toolID, jid string) {
	toolID, jid = o.toolID, o.jid
	if o.channel == "session" && o.toolID == routeWeb {
		return "", "" // delegated from the web page: the thread is the answer
	}
	if o.channel == "task" || o.channel == "place" || o.channel == "message" || o.channel == "session" {
		// Automatic runs have no correspondent: tell the last WhatsApp contact
		// (a delegated session's wake-up prefers the channel it was asked on).
		if toolID == "" || jid == "" {
			toolID, _ = s.db.GetAppMeta(waToolMetaKey)
			jid, _ = s.db.GetAppMeta(waJIDMetaKey)
		}
	} else if o.channel != "whatsapp" {
		return "", ""
	}
	return toolID, jid
}

// sendWhatsApp sends text and reports whether it was sent.
func (s *Service) sendWhatsApp(toolID, jid, text string) bool {
	if s.wa == nil || toolID == "" || jid == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.wa.SendMessage(ctx, toolID, jid, text); err != nil {
		log.Printf("assistant: whatsapp reply: %v", err)
		return false // not delivered: the phone notification is the fallback
	}
	return true
}

// Reset closes the current session so the next message starts a clean one. The
// thread is kept.
func (s *Service) Reset(ctx context.Context) {
	s.mu.Lock()
	id := s.sessionID
	s.sessionID, s.offset, s.queue = "", 0, nil
	s.mu.Unlock()
	_ = s.db.SetAppMeta(sessionMetaKey, "")
	if id != "" {
		_ = s.hub.StopSession(ctx, id)
	}
}

func oneLine(t string, max int) string {
	t = strings.Join(strings.Fields(t), " ")
	if r := []rune(t); len(r) > max {
		return string(r[:max]) + "…"
	}
	return t
}

// HandleWhatsApp is the whatsapp.AssistantRouter entry point.
func (s *Service) HandleWhatsApp(toolID, chatJID, sender, text string, images []streamjson.ImageBlock) {
	if len(images) == 0 && s.joinMediaBurst(toolID, chatJID, text) {
		return
	}
	if err := s.Send(context.Background(), "whatsapp", toolID, chatJID, "", text, images); err != nil {
		log.Printf("assistant: whatsapp message from %s: %v", sender, err)
	}
}

// DefaultModel is what the assistant runs on until the user chooses (cost).
const DefaultModel = "sonnet"

// Models are the accepted values of SetModel ("" = the Hub's global default).
var Models = []string{"", "opus", "sonnet", "haiku"}

// modelSetting is the stored choice: "" when never set, "hub" for the Hub default.
func (s *Service) modelSetting() string {
	v, _ := s.db.GetAppMeta(modelMetaKey)
	return v
}

// Model is the chosen model alias; "" means the Hub's global default.
func (s *Service) Model() string {
	switch v := s.modelSetting(); v {
	case "":
		return DefaultModel
	case "hub":
		return ""
	default:
		return v
	}
}

// SetModel stores the model for the assistant's next session.
func (s *Service) SetModel(m string) error {
	ok := false
	for _, a := range Models {
		ok = ok || a == m
	}
	if !ok {
		return fmt.Errorf("unknown model %q", m)
	}
	if m == "" {
		m = "hub"
	}
	return s.db.SetAppMeta(modelMetaKey, m)
}

// ModelState reports the model the live session really runs on ("" = no live
// session) and whether the chosen setting differs from the one it launched with.
func (s *Service) ModelState() (effective string, pending bool) {
	s.mu.Lock()
	id := s.sessionID
	s.mu.Unlock()
	if id == "" {
		return "", false
	}
	sess, err := s.db.GetSession(id)
	if err != nil || (sess.State != store.StateRunning && sess.State != store.StateStarting) {
		return "", false
	}
	launched, _ := s.db.GetAppMeta(launchModelMetaKey)
	return sess.Model, launched != s.modelSetting()
}
