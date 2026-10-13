package api

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"system/internal/mcpremote"
	"system/internal/scriptstatus"
	"time"

	"github.com/google/uuid"

	"system/internal/assistant"
	"system/internal/store"
)

// WithScripts wires the script-status service into the server.
func (s *Server) WithScripts(sc *scriptstatus.Service) *Server {
	s.scripts = sc
	return s
}

// WithMCPRemote wires the remote MCP connector (nil = not served).
func (s *Server) WithMCPRemote(m *mcpremote.Service) *Server {
	s.mcpRemote = m
	return s
}

// WithAssistant wires the personal assistant into the server.
func (s *Server) WithAssistant(a *assistant.Service) *Server {
	s.assistant = a
	return s
}

func (s *Server) assistantOr503(w http.ResponseWriter) *assistant.Service {
	if s.assistant == nil || s.assistant.ProjectID() == "" {
		http.Error(w, "assistant not ready", http.StatusServiceUnavailable)
		return nil
	}
	return s.assistant
}

type assistantState struct {
	ProjectID string `json:"projectId"`
	Busy      bool   `json:"busy"`
	Person    string `json:"person"`
	// Push: the assistant notifies the phones; PushWhatsApp: even for answers
	// already sent on WhatsApp.
	Push         bool `json:"push"`
	PushWhatsApp bool `json:"pushWhatsapp"`
	// Model is the chosen alias ("" = Hub default); ModelEffective the model of
	// the live session ("" = none); ModelPending: the choice is not applied yet.
	Model          string `json:"model"`
	ModelEffective string `json:"modelEffective"`
	ModelPending   bool   `json:"modelPending"`
	// Night mode: spontaneous messages are held between nightStart and nightEnd
	// (local time of nightTimezone) unless the user wrote within
	// nightIdleMinutes; delivered as one digest at nightDeliver or as a catch-up
	// when the user writes. nightHeld = messages waiting right now.
	NightMode        bool   `json:"nightMode"`
	NightStart       string `json:"nightStart"`
	NightEnd         string `json:"nightEnd"`
	NightDeliver     string `json:"nightDeliver"`
	NightIdleMinutes int    `json:"nightIdleMinutes"`
	NightTimezone    string `json:"nightTimezone"`
	NightHeld        int    `json:"nightHeld"`
	// Grouped wake-ups: with several delegated requests running, session ends are
	// handed to the assistant at most once per batchMinutes; batchHeld = ends
	// retained right now.
	BatchWakeups bool `json:"batchWakeups"`
	BatchMinutes int  `json:"batchMinutes"`
	BatchHeld    int  `json:"batchHeld"`
}

func assistantStateOf(a *assistant.Service) assistantState {
	eff, pending := a.ModelState()
	n := a.Night()
	return assistantState{ProjectID: a.ProjectID(), Busy: a.Busy(), Person: a.Person(), Push: a.PushEnabled(),
		PushWhatsApp: a.PushWhatsApp(), Model: a.Model(), ModelEffective: eff, ModelPending: pending,
		NightMode: n.Enabled, NightStart: n.Start, NightEnd: n.End, NightDeliver: n.Deliver,
		NightIdleMinutes: n.IdleMinutes, NightTimezone: n.Timezone, NightHeld: a.HeldCount(),
		BatchWakeups: a.Batch().Enabled, BatchMinutes: a.Batch().Minutes, BatchHeld: a.BatchHeld()}
}

func (s *Server) assistantStatus(w http.ResponseWriter, r *http.Request) {
	a := s.assistantOr503(w)
	if a == nil {
		return
	}
	writeJSON(w, http.StatusOK, assistantStateOf(a))
}

func (s *Server) assistantMessages(w http.ResponseWriter, r *http.Request) {
	a := s.assistantOr503(w)
	if a == nil {
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	msgs, err := s.db.ListAssistantMessages(after, 200)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs, "busy": a.Busy()})
}

func (s *Server) assistantSend(w http.ResponseWriter, r *http.Request) {
	a := s.assistantOr503(w)
	if a == nil {
		return
	}
	var req struct {
		Text string `json:"text"`
		// Voice marks a dictated turn whose answer will be read aloud.
		Voice bool `json:"voice"`
		// Attachments are ids returned by POST /api/assistant/attachments; the
		// text may then be empty.
		Attachments []string `json:"attachments"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil ||
		strings.TrimSpace(req.Text) == "" && len(req.Attachments) == 0 {
		http.Error(w, "text or attachments required", http.StatusBadRequest)
		return
	}
	if len(req.Attachments) > 0 {
		if _, err := a.CheckAttachments(req.Attachments); err != nil {
			writeAttachmentErr(w, err)
			return
		}
		go a.SendWithAttachments(context.Background(), "web", "", "", req.Text, req.Attachments, req.Voice)
		writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
		return
	}
	// Starting the session can take a while: the answer (or the failure) arrives
	// in the thread, which the page polls.
	if req.Voice {
		go a.SendSpoken(context.Background(), "web", req.Text)
	} else {
		go a.Send(context.Background(), "web", "", "", "", req.Text, nil)
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}

func (s *Server) assistantReset(w http.ResponseWriter, r *http.Request) {
	a := s.assistantOr503(w)
	if a == nil {
		return
	}
	a.Reset(r.Context())
	if r.URL.Query().Get("history") == "1" {
		_ = a.ClearHistory()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) assistantSettings(w http.ResponseWriter, r *http.Request) {
	a := s.assistantOr503(w)
	if a == nil {
		return
	}
	var req struct {
		Person       *string `json:"person"`
		Push         *bool   `json:"push"`
		PushWhatsApp *bool   `json:"pushWhatsapp"`
		Model        *string `json:"model"`

		NightMode        *bool   `json:"nightMode"`
		NightStart       *string `json:"nightStart"`
		NightEnd         *string `json:"nightEnd"`
		NightDeliver     *string `json:"nightDeliver"`
		NightIdleMinutes *int    `json:"nightIdleMinutes"`
		NightTimezone    *string `json:"nightTimezone"`

		BatchWakeups *bool `json:"batchWakeups"`
		BatchMinutes *int  `json:"batchMinutes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	// Each field is optional: only the ones sent change.
	if req.Model != nil {
		if err := a.SetModel(*req.Model); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if req.NightMode != nil || req.NightStart != nil || req.NightEnd != nil || req.NightDeliver != nil ||
		req.NightIdleMinutes != nil || req.NightTimezone != nil {
		err := a.SetNight(assistant.NightPatch{Enabled: req.NightMode, Start: req.NightStart, End: req.NightEnd,
			Deliver: req.NightDeliver, IdleMinutes: req.NightIdleMinutes, Timezone: req.NightTimezone})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if req.BatchWakeups != nil || req.BatchMinutes != nil {
		if err := a.SetBatch(req.BatchWakeups, req.BatchMinutes); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if req.Person != nil {
		if err := a.SetPerson(*req.Person); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if req.Push != nil || req.PushWhatsApp != nil {
		on, wa := a.PushEnabled(), a.PushWhatsApp()
		if req.Push != nil {
			on = *req.Push
		}
		if req.PushWhatsApp != nil {
			wa = *req.PushWhatsApp
		}
		if err := a.SetPush(on, wa); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, assistantStateOf(a))
}

func (s *Server) assistantEntities(w http.ResponseWriter, r *http.Request) {
	a := s.assistantOr503(w)
	if a == nil {
		return
	}
	domain := r.URL.Query().Get("domain")
	if domain != "person" && domain != "zone" {
		http.Error(w, "domain must be person or zone", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	list, err := a.Entities(ctx, domain)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) assistantListTriggers(w http.ResponseWriter, r *http.Request) {
	list, err := s.db.ListAssistantTriggers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type triggerInput struct {
	Name    string `json:"name"`
	Place   string `json:"place"`
	Event   string `json:"event"`
	Prompt  string `json:"prompt"`
	Enabled *bool  `json:"enabled"`
}

func (in triggerInput) valid() bool {
	return strings.TrimSpace(in.Name) != "" && strings.TrimSpace(in.Place) != "" &&
		strings.TrimSpace(in.Prompt) != "" && (in.Event == "enter" || in.Event == "leave")
}

func (s *Server) assistantCreateTrigger(w http.ResponseWriter, r *http.Request) {
	var in triggerInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || !in.valid() {
		http.Error(w, "name, place, prompt and event (enter|leave) are required", http.StatusBadRequest)
		return
	}
	t := store.AssistantTrigger{
		ID: uuid.NewString(), Name: strings.TrimSpace(in.Name), Place: strings.TrimSpace(in.Place),
		Event: in.Event, Prompt: strings.TrimSpace(in.Prompt), Enabled: in.Enabled == nil || *in.Enabled,
		CreatedAt: time.Now().UnixMilli(),
	}
	if err := s.db.CreateAssistantTrigger(t); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) assistantUpdateTrigger(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	all, err := s.db.ListAssistantTriggers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	var cur *store.AssistantTrigger
	for i := range all {
		if all[i].ID == id {
			cur = &all[i]
		}
	}
	if cur == nil {
		http.NotFound(w, r)
		return
	}
	var in triggerInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if in.Name != "" {
		cur.Name = strings.TrimSpace(in.Name)
	}
	if in.Place != "" {
		cur.Place = strings.TrimSpace(in.Place)
	}
	if in.Event == "enter" || in.Event == "leave" {
		cur.Event = in.Event
	}
	if in.Prompt != "" {
		cur.Prompt = strings.TrimSpace(in.Prompt)
	}
	if in.Enabled != nil {
		cur.Enabled = *in.Enabled
	}
	if err := s.db.UpdateAssistantTrigger(*cur); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, cur)
}

func (s *Server) assistantDeleteTrigger(w http.ResponseWriter, r *http.Request) {
	if err := s.db.DeleteAssistantTrigger(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) assistantFireTrigger(w http.ResponseWriter, r *http.Request) {
	a := s.assistantOr503(w)
	if a == nil {
		return
	}
	id := r.PathValue("id")
	go a.FireTrigger(context.Background(), id)
	writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}

// maxAssistantUpload bounds one upload request (all files combined).
const maxAssistantUpload = assistant.MaxAttachments*assistant.MaxAttachmentSize + 1<<20

// writeAttachmentErr maps an attachment refusal to its HTTP status; the JSON
// body is {"error": <message for the user>, "code": too_large|type|…}.
func writeAttachmentErr(w http.ResponseWriter, err error) {
	var ae *assistant.AttachmentError
	if !errors.As(err, &ae) {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	status := map[string]int{"too_large": http.StatusRequestEntityTooLarge, "type": http.StatusUnsupportedMediaType}[ae.Code]
	if status == 0 {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, map[string]string{"error": ae.Message, "code": ae.Code})
}

// assistantUpload stores files for the next message (multipart field "files").
func (s *Server) assistantUpload(w http.ResponseWriter, r *http.Request) {
	a := s.assistantOr503(w)
	if a == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAssistantUpload)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeAttachmentErr(w, &assistant.AttachmentError{Code: "too_large", Message: "Envoi trop volumineux."})
		return
	}
	defer r.MultipartForm.RemoveAll()
	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		writeAttachmentErr(w, &assistant.AttachmentError{Code: "invalid", Message: "Aucun fichier."})
		return
	}
	if len(files) > assistant.MaxAttachments {
		writeAttachmentErr(w, &assistant.AttachmentError{Code: "too_many", Message: "Trop de fichiers : " + strconv.Itoa(assistant.MaxAttachments) + " au maximum par message."})
		return
	}
	saved := make([]store.AssistantAttachment, 0, len(files))
	for _, fh := range files {
		// Size and type are vetted before reading the body.
		if err := assistant.CheckMeta(fh.Filename, fh.Header.Get("Content-Type"), fh.Size); err != nil {
			a.DiscardAttachments(saved)
			writeAttachmentErr(w, err)
			return
		}
		f, err := fh.Open()
		if err != nil {
			a.DiscardAttachments(saved)
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		att, err := a.SaveAttachment(fh.Filename, fh.Header.Get("Content-Type"), f)
		f.Close()
		if err != nil {
			a.DiscardAttachments(saved)
			writeAttachmentErr(w, err)
			return
		}
		saved = append(saved, att)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"attachments": saved})
}

// assistantAttachment streams a stored file (session-authenticated like every
// other /api route). Images and PDFs display inline, everything else downloads.
func (s *Server) assistantAttachment(w http.ResponseWriter, r *http.Request) {
	a := s.assistantOr503(w)
	if a == nil {
		return
	}
	att, err := s.db.GetAssistantAttachment(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if att.Expired {
		http.Error(w, "attachment expired (purged by the retention policy)", http.StatusGone)
		return
	}
	full, err := a.AttachmentFile(att)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(full)
	if err != nil {
		http.Error(w, "attachment missing", http.StatusGone)
		return
	}
	defer f.Close()
	inline := (att.Kind == "image" || att.Mime == "application/pdf") && r.URL.Query().Get("download") != "1"
	disp := "attachment"
	if inline {
		disp = "inline"
	}
	h := w.Header()
	h.Set("Content-Type", att.Mime)
	h.Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": att.Name}))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'")
	h.Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, att.Name, time.UnixMilli(att.TS), f)
}
