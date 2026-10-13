package assistant

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"system/internal/imgsafe"
	"system/internal/store"
)

// Images the assistant sends to the user. The model gives the path of an image
// file; the Hub vets it (imgsafe: allow-listed dirs, magic bytes, size, re-encode,
// rate limit), keeps a re-encoded copy in its own outgoing store (a sibling of the
// received files, same retention) and attaches it to the answer of the turn in
// progress. The answer then goes where its text goes: the thread (web page and
// app) and, for WhatsApp turns and automatic runs, the user's WhatsApp chat. The
// same rules as text apply: a spontaneous answer is held at night and delivered
// with the digest, and a [silence] answer sends nothing.

const (
	outgoingDir = "pieces-jointes-sortantes" // under the assistant project dir
	// maxImagesPerReply bounds the images one answer can carry.
	maxImagesPerReply = 4
)

// SetImageGuard wires the outgoing-image policy and rate limit (nil = refuse).
func (s *Service) SetImageGuard(g *imgsafe.Guard) {
	s.mu.Lock()
	s.img = g
	s.mu.Unlock()
}

func (s *Service) imageGuard() *imgsafe.Guard {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.img
}

// SendImage is the assistant_send_image tool: it vets the file at path and
// attaches it to the answer of the turn in progress. caption is optional.
func (s *Service) SendImage(path, caption string) (string, error) {
	s.mu.Lock()
	if len(s.queue) == 0 {
		s.mu.Unlock()
		return "", errors.New("aucune réponse en cours : une image s'attache à ta réponse à un message ou à une consigne")
	}
	if len(s.queue[0].images) >= maxImagesPerReply {
		s.mu.Unlock()
		return "", fmt.Errorf("au plus %d images par réponse", maxImagesPerReply)
	}
	o := s.queue[0]
	s.mu.Unlock()

	dest := "le fil de l'assistant (page web et app)"
	if tool, jid := s.waTarget(o); tool != "" && jid != "" {
		dest += " et WhatsApp"
	}
	g := s.imageGuard()
	img, err := g.Vet(path)
	if err != nil {
		log.Printf("assistant: image refused: path=%q: %v", path, err)
		return "", err
	}
	att, err := s.storeOutgoing(img, imgsafe.CleanCaption(caption))
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	if len(s.queue) == 0 {
		s.mu.Unlock()
		s.DiscardAttachments([]store.AssistantAttachment{att})
		return "", errors.New("la réponse s'est terminée avant l'attache de l'image")
	}
	s.queue[0].images = append(s.queue[0].images, att)
	s.mu.Unlock()
	log.Printf("assistant: image queued: src=%q bytes=%d→%d %dx%d dest=%q id=%s", img.SrcPath, img.SrcSize, len(img.Data), img.Width, img.Height, dest, att.ID)
	msg := "Image attachée à ta réponse finale : elle partira avec elle vers " + dest +
		" (retenue la nuit pour une réponse automatique, rien si ta réponse est [silence]). Écris ta réponse finale normalement."
	if img.Note != "" {
		msg += " (" + img.Note + ")"
	}
	return msg, nil
}

// storeOutgoing writes the vetted image under the outgoing store and records it
// (not yet linked to a message). Its name is random: the id is the only handle.
func (s *Service) storeOutgoing(img *imgsafe.Image, caption string) (store.AssistantAttachment, error) {
	var zero store.AssistantAttachment
	root, err := s.projectDir()
	if err != nil {
		return zero, err
	}
	id := uuid.NewString()
	name := "image-" + uuid.NewString()[:8] + "." + img.Ext
	rel := filepath.Join(outgoingDir, id, name)
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return zero, err
	}
	if err := os.WriteFile(full, img.Data, 0o600); err != nil {
		return zero, err
	}
	a := store.AssistantAttachment{ID: id, Name: name, Mime: img.Mime, Size: int64(len(img.Data)), Path: rel, TS: time.Now().UnixMilli(),
		Direction: "out", Width: img.Width, Height: img.Height, Caption: caption, Kind: "image"}
	if err := s.db.CreateAssistantAttachment(a); err != nil {
		_ = os.RemoveAll(filepath.Dir(full))
		return zero, err
	}
	a.URL = store.AssistantAttachmentURL(id)
	return a, nil
}

// linkImages attaches the images of a reply to its recorded message.
func (s *Service) linkImages(msgID int64, imgs []store.AssistantAttachment) []store.AssistantAttachment {
	if len(imgs) == 0 {
		return nil
	}
	ids := make([]string, len(imgs))
	for i, a := range imgs {
		ids[i] = a.ID
	}
	out, err := s.db.LinkAssistantAttachments(msgID, ids)
	if err != nil {
		log.Printf("assistant: link images: %v", err)
		s.DiscardAttachments(imgs)
		return nil
	}
	return out
}

func liveImages(atts []store.AssistantAttachment) []store.AssistantAttachment {
	var out []store.AssistantAttachment
	for _, a := range atts {
		if a.Direction == "out" && !a.Expired {
			out = append(out, a)
		}
	}
	return out
}

// sendWhatsAppImages sends the images to a WhatsApp target ("" = none) and
// returns how many went out. Every send is logged.
func (s *Service) sendWhatsAppImages(toolID, jid string, atts []store.AssistantAttachment) int {
	if s.wa == nil || toolID == "" || jid == "" {
		return 0
	}
	n := 0
	for _, a := range liveImages(atts) {
		full, err := s.AttachmentFile(a)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			log.Printf("assistant: whatsapp image %s: %v", a.ID, err)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		err = s.wa.SendImage(ctx, toolID, jid, data, a.Mime, a.Caption)
		cancel()
		if err != nil {
			log.Printf("assistant: whatsapp image %s: %v", a.ID, err)
			continue
		}
		log.Printf("assistant: image sent: id=%s bytes=%d dest=whatsapp:%s", a.ID, a.Size, jid)
		n++
	}
	return n
}

func imagePush(atts []store.AssistantAttachment) string {
	if len(atts) > 0 && atts[0].Caption != "" {
		return "📷 " + atts[0].Caption
	}
	return "📷 Image"
}

// TestQueueTurn and TestAnswer let other packages' tests play a turn without a
// live session: queue an origin, then answer it.
func (s *Service) TestQueueTurn(channel string) {
	s.mu.Lock()
	s.queue = append(s.queue, origin{channel: channel})
	s.mu.Unlock()
}

func (s *Service) TestAnswer(text string) {
	s.mu.Lock()
	o := s.queue[0]
	s.queue = s.queue[1:]
	s.mu.Unlock()
	s.reply(o, text)
}
