package tools

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

const (
	testMailPlain = "From: Alice <alice@example.org>\r\nTo: me@example.org\r\nSubject: Facture octobre\r\nDate: Mon, 05 Oct 2026 10:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nBonjour, voici la facture.\r\n"
	testMailMulti = "From: Bob <bob@example.org>\r\nTo: me@example.org\r\nSubject: Rapport\r\nDate: Tue, 06 Oct 2026 10:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=OUT\r\n\r\n" +
		"--OUT\r\nContent-Type: multipart/alternative; boundary=ALT\r\n\r\n" +
		"--ALT\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nVersion texte unique.\r\n" +
		"--ALT\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>Version <b>HTML</b> dupliquée.</p>\r\n--ALT--\r\n" +
		"--OUT\r\nContent-Type: application/pdf; name=rapport.pdf\r\nContent-Disposition: attachment; filename=rapport.pdf\r\nContent-Transfer-Encoding: 7bit\r\n\r\n%PDF-fake\r\n--OUT--\r\n"
	testMailHTML = "From: News <news@example.org>\r\nSubject: Lettre\r\nDate: Wed, 07 Oct 2026 10:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<html><head><style>p{}</style></head><body><p>Seulement du HTML &amp; du texte</p></body></html>\r\n"
)

func startTestIMAP(t *testing.T) map[string]string {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("me", "secret")
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{testMailPlain, testMailMulti, testMailHTML} {
		if _, err := user.Append("INBOX", strings.NewReader(m), &imap.AppendOptions{Time: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	mem.AddUser(user)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}},
		InsecureAuth: true,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	return map[string]string{"host": host, "port": port, "username": "me", "password": "secret", "tls": "false"}
}

func imapCall(t *testing.T, cfg map[string]string, action string, args map[string]any) string {
	t.Helper()
	out, err := imapConnector{}.Call(context.Background(), cfg, action, args)
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	return out
}

func TestIMAPReadTools(t *testing.T) {
	cfg := startTestIMAP(t)

	if err := (imapConnector{}).Test(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if out := imapCall(t, cfg, "imap_list_mailboxes", nil); !strings.Contains(out, "INBOX (3 messages, 3 unread)") {
		t.Errorf("mailboxes:\n%s", out)
	}
	list := imapCall(t, cfg, "imap_list_messages", map[string]any{"mailbox": "INBOX", "limit": float64(2)})
	if !strings.Contains(list, "showing 2 of 3") || strings.Index(list, "Lettre") > strings.Index(list, "Rapport") || strings.Contains(list, "Facture") {
		t.Errorf("list must be newest first and limited:\n%s", list)
	}

	// Search
	if out := imapCall(t, cfg, "imap_search_messages", map[string]any{"from": "alice"}); !strings.Contains(out, "Facture octobre") || strings.Contains(out, "Rapport") {
		t.Errorf("search from:\n%s", out)
	}
	if out := imapCall(t, cfg, "imap_search_messages", map[string]any{"text": "facture"}); !strings.Contains(out, "Facture octobre") {
		t.Errorf("search text:\n%s", out)
	}
	if out := imapCall(t, cfg, "imap_search_messages", map[string]any{"subject": "introuvable"}); !strings.Contains(out, "No matching") {
		t.Errorf("empty search:\n%s", out)
	}
	if _, err := (imapConnector{}).Call(context.Background(), cfg, "imap_search_messages", map[string]any{"since": "hier"}); err == nil {
		t.Error("bad date accepted")
	}

	// Read: one rendition only + attachment listed.
	msg := imapCall(t, cfg, "imap_get_message", map[string]any{"mailbox": "INBOX", "uid": float64(2)})
	if !strings.Contains(msg, "Version texte unique.") || strings.Contains(msg, "dupliquée") {
		t.Errorf("multipart/alternative must show a single rendition:\n%s", msg)
	}
	if !strings.Contains(msg, "Attachments (1)") || !strings.Contains(msg, "rapport.pdf") || strings.Contains(msg, "%PDF") {
		t.Errorf("attachment list wrong:\n%s", msg)
	}
	html := imapCall(t, cfg, "imap_get_message", map[string]any{"mailbox": "INBOX", "uid": float64(3)})
	if !strings.Contains(html, "Seulement du HTML & du texte") || strings.Contains(html, "<p>") || strings.Contains(html, "p{}") {
		t.Errorf("html-only message not converted:\n%s", html)
	}
	// Reading does not mark as read.
	if out := imapCall(t, cfg, "imap_list_mailboxes", nil); !strings.Contains(out, "3 unread") {
		t.Errorf("reading flipped the unread state:\n%s", out)
	}
	if out := imapCall(t, cfg, "imap_list_messages", map[string]any{"unread_only": true}); !strings.Contains(out, "of 3") {
		t.Errorf("unread_only:\n%s", out)
	}
}
