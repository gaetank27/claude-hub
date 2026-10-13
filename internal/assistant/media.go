package assistant

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"system/internal/store"
	"system/internal/whatsapp"
)

// mediaWindow groups files (and the captions/texts around them) sent in a row
// from the user's WhatsApp into one assistant turn: it restarts at each item.
var mediaWindow = 8 * time.Second

// mediaBurst is a group of WhatsApp items awaiting its single turn.
type mediaBurst struct {
	toolID, jid string
	texts       []string
	ids         []string
	timer       *time.Timer
}

func burstKey(toolID, jid string) string { return toolID + "|" + jid }

// AcceptMedia is the whatsapp.AssistantRouter pre-download check: nil when a
// file of that name/type/size can be taken, else a user-worded refusal.
func (s *Service) AcceptMedia(name, mime string, size int64) error {
	return CheckMeta(name, mime, size)
}

// RefuseMedia tells the user on WhatsApp why a file was not taken.
func (s *Service) RefuseMedia(toolID, chatJID string, reason error) {
	s.reply(origin{channel: "whatsapp", toolID: toolID, jid: chatJID}, "⚠️ "+reason.Error())
}

// HandleWhatsAppMedia stores a file the user sent to the assistant and queues it
// (with its caption) for the burst's turn.
func (s *Service) HandleWhatsAppMedia(toolID, chatJID, sender, caption string, m whatsapp.Media) {
	att, err := s.SaveAttachment(m.Name, m.Mime, bytes.NewReader(m.Data))
	if err != nil {
		if _, ok := err.(*AttachmentError); !ok {
			log.Printf("assistant: save whatsapp media from %s: %v", sender, err)
			err = fmt.Errorf("Je n'ai pas pu enregistrer ce fichier (%v).", err)
		}
		s.RefuseMedia(toolID, chatJID, err)
		return
	}
	key := burstKey(toolID, chatJID)
	s.mu.Lock()
	if s.media == nil {
		s.media = map[string]*mediaBurst{}
	}
	b := s.media[key]
	if b == nil {
		b = &mediaBurst{toolID: toolID, jid: chatJID}
		s.media[key] = b
		b.timer = time.AfterFunc(mediaWindow, func() { s.flushMedia(key) })
	} else {
		b.timer.Reset(mediaWindow)
	}
	if len(b.ids) >= MaxAttachments {
		s.mu.Unlock()
		s.RefuseMedia(toolID, chatJID, fmt.Errorf("Trop de pièces jointes d'un coup : %d au maximum par message, « %s » est ignoré.", MaxAttachments, att.Name))
		s.removeFiles([]store.AssistantAttachment{att})
		return
	}
	b.ids = append(b.ids, att.ID)
	if c := strings.TrimSpace(caption); c != "" {
		b.texts = append(b.texts, c)
	}
	s.mu.Unlock()
}

// joinMediaBurst adds a plain text to the user's open media burst, if any (so
// "photo" + "voici le contrat" is one turn). It reports whether it did.
func (s *Service) joinMediaBurst(toolID, chatJID, text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.media[burstKey(toolID, chatJID)]
	if b == nil {
		return false
	}
	if t := strings.TrimSpace(text); t != "" {
		b.texts = append(b.texts, t)
	}
	b.timer.Reset(mediaWindow)
	return true
}

func (s *Service) flushMedia(key string) {
	s.mu.Lock()
	b := s.media[key]
	delete(s.media, key)
	s.mu.Unlock()
	if b == nil {
		return
	}
	text := strings.Join(b.texts, "\n")
	if err := s.SendWithAttachments(context.Background(), "whatsapp", b.toolID, b.jid, text, b.ids, false); err != nil {
		log.Printf("assistant: whatsapp media turn: %v", err)
	}
}

// purgeLoop applies the retention hourly.
func (s *Service) purgeLoop(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n := s.PurgeAttachments(); n > 0 {
			log.Printf("assistant: purged %d attachment(s)", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
