package tools

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"time"

	gomail "github.com/emersion/go-message/mail"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func init() { register(imapConnector{}) }

type imapConnector struct{}

func (imapConnector) Type() string  { return "imap" }
func (imapConnector) Label() string { return "IMAP (e-mail)" }

func (imapConnector) Fields() []Field {
	return []Field{
		{Key: "host", Label: "Serveur IMAP", Type: FieldText, Required: true,
			Placeholder: "127.0.0.1"},
		{Key: "port", Label: "Port", Type: FieldText, Required: true,
			Placeholder: "1143"},
		{Key: "username", Label: "Utilisateur", Type: FieldText, Required: true,
			Placeholder: "user@proton.me"},
		{Key: "password", Label: "Mot de passe", Type: FieldPassword, Secret: true, Required: true},
		{Key: "tls", Label: "TLS", Type: FieldText, Required: false,
			Placeholder: "false"},
	}
}

func (imapConnector) Test(ctx context.Context, cfg map[string]string) error {
	if err := missing(cfg, "host", "port", "username", "password"); err != nil {
		return err
	}
	c, err := imapDial(cfg)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Login(cfg["username"], cfg["password"]).Wait()
}

func (imapConnector) Tools() []ToolSpec {
	return []ToolSpec{
		{
			Name:        "imap_list_mailboxes",
			Description: "List the mailboxes (folders) of the IMAP account with their message and unread counts. On Proton Bridge: INBOX, Sent, Drafts, Archive, Spam, Trash, All Mail, Starred, Folders/<name>, Labels/<name>.",
			Args:        []ArgSpec{},
		},
		{
			Name:        "imap_list_messages",
			Description: "List recent messages of a mailbox, newest first: subject, sender, date, read state, UID. Read-only: listing never marks anything as read.",
			Args: []ArgSpec{
				{Name: "mailbox", Type: "string", Description: "Mailbox name from imap_list_mailboxes, e.g. INBOX (default)."},
				{Name: "limit", Type: "integer", Description: "Max messages to return (default 20, max 100)."},
				{Name: "unread_only", Type: "boolean", Description: "Only unread messages."},
			},
		},
		{
			Name:        "imap_search_messages",
			Description: "Search a mailbox (server-side) and list the most recent matches, newest first. All given criteria must match.",
			Args: []ArgSpec{
				{Name: "mailbox", Type: "string", Description: "Mailbox name, e.g. INBOX (default). Use \"All Mail\" on Proton Bridge to search everywhere."},
				{Name: "text", Type: "string", Description: "Words to find in the headers or body."},
				{Name: "from", Type: "string", Description: "Sender name or address contains this."},
				{Name: "subject", Type: "string", Description: "Subject contains this."},
				{Name: "since", Type: "string", Description: "Received on or after this date (YYYY-MM-DD)."},
				{Name: "before", Type: "string", Description: "Received before this date (YYYY-MM-DD)."},
				{Name: "unread_only", Type: "boolean", Description: "Only unread messages."},
				{Name: "limit", Type: "integer", Description: "Max results (default 20, max 100)."},
			},
		},
		{
			Name:        "imap_get_message",
			Description: "Read one message by UID: headers, plain-text body (HTML converted to text) and the list of attachments (name, type, size; contents are not returned). Does not mark the message as read.",
			Args: []ArgSpec{
				{Name: "mailbox", Type: "string", Description: "Mailbox containing the message, e.g. INBOX.", Required: true},
				{Name: "uid", Type: "integer", Description: "UID of the message (from imap_list_messages / imap_search_messages).", Required: true},
			},
		},
	}
}

func (imapConnector) Call(ctx context.Context, cfg map[string]string, action string, args map[string]any) (string, error) {
	c, err := imapDial(cfg)
	if err != nil {
		return "", fmt.Errorf("imap connect: %w", err)
	}
	defer c.Close()

	if err := c.Login(cfg["username"], cfg["password"]).Wait(); err != nil {
		return "", fmt.Errorf("imap login: %w", err)
	}

	switch action {
	case "imap_list_mailboxes":
		return imapListMailboxes(c)

	case "imap_list_messages":
		mailbox := argString(args, "mailbox", "INBOX")
		limit := 20
		if v, ok := argInt(args, "limit"); ok && v > 0 {
			if v > 100 {
				v = 100
			}
			limit = v
		}
		return imapListMessages(c, mailbox, limit, argBool(args, "unread_only"))

	case "imap_search_messages":
		limit := 20
		if v, ok := argInt(args, "limit"); ok && v > 0 {
			limit = min(v, 100)
		}
		return imapSearchMessages(c, argString(args, "mailbox", "INBOX"), limit, args)

	case "imap_get_message":
		mailbox := argString(args, "mailbox", "INBOX")
		uid, ok := argInt(args, "uid")
		if !ok {
			return "", fmt.Errorf("uid is required")
		}
		return imapGetMessage(c, mailbox, imap.UID(uid))

	default:
		return "", fmt.Errorf("unknown action %q", action)
	}
}

// ---- helpers ----

const imapDialTimeout = 10 * time.Second

func imapDial(cfg map[string]string) (*imapclient.Client, error) {
	addr := cfg["host"] + ":" + cfg["port"]
	useTLS := cfg["tls"] == "true" || cfg["tls"] == "1"
	if useTLS {
		// Proton Bridge serves a self-signed cert on loopback; any other server
		// must present a valid certificate.
		return imapclient.DialTLS(addr, &imapclient.Options{
			Dialer:    &net.Dialer{Timeout: imapDialTimeout},
			TLSConfig: &tls.Config{ServerName: cfg["host"], InsecureSkipVerify: isLoopbackHost(cfg["host"])},
		})
	}
	if !isLoopbackHost(cfg["host"]) {
		return nil, fmt.Errorf("refusing plaintext IMAP to %s: set tls=true (plaintext is only allowed on loopback)", cfg["host"])
	}
	opts := &imapclient.Options{Dialer: &net.Dialer{Timeout: imapDialTimeout}}
	c, err := imapclient.DialInsecure(addr, opts)
	if err != nil {
		return nil, err
	}
	// Proton Bridge offers STARTTLS (self-signed cert) on its loopback port: use
	// it whenever advertised rather than sending the login in clear.
	if c.Caps().Has(imap.CapStartTLS) {
		_ = c.Close()
		return imapclient.DialStartTLS(addr, &imapclient.Options{
			Dialer:    opts.Dialer,
			TLSConfig: &tls.Config{ServerName: cfg["host"], InsecureSkipVerify: true}, // loopback only (checked above)
		})
	}
	return c, nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func imapListMailboxes(c *imapclient.Client) (string, error) {
	mailboxes, err := c.List("", "*", nil).Collect()
	if err != nil {
		return "", err
	}
	if len(mailboxes) == 0 {
		return "No mailboxes found.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Mailboxes (%d):\n", len(mailboxes))
	for _, m := range mailboxes {
		noselect := false
		for _, a := range m.Attrs {
			if a == imap.MailboxAttrNoSelect {
				noselect = true
			}
		}
		if noselect {
			fmt.Fprintf(&b, "  %s (container)\n", m.Mailbox)
			continue
		}
		st, err := c.Status(m.Mailbox, &imap.StatusOptions{NumMessages: true, NumUnseen: true}).Wait()
		if err != nil || st.NumMessages == nil {
			fmt.Fprintf(&b, "  %s\n", m.Mailbox)
			continue
		}
		unseen := uint32(0)
		if st.NumUnseen != nil {
			unseen = *st.NumUnseen
		}
		fmt.Fprintf(&b, "  %s (%d messages, %d unread)\n", m.Mailbox, *st.NumMessages, unseen)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

var imapListFetch = &imap.FetchOptions{Flags: true, Envelope: true, UID: true}

func imapSeen(flags []imap.Flag) bool {
	for _, f := range flags {
		if f == imap.FlagSeen {
			return true
		}
	}
	return false
}

// imapFormatList renders envelopes newest first.
func imapFormatList(title string, msgs []*imapclient.FetchMessageBuffer, total int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (showing %d of %d):\n", title, len(msgs), total)
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		env := m.Envelope
		if env == nil {
			continue
		}
		from := ""
		if len(env.From) > 0 {
			a := env.From[0]
			if a.Name != "" {
				from = fmt.Sprintf("%s <%s@%s>", a.Name, a.Mailbox, a.Host)
			} else {
				from = fmt.Sprintf("%s@%s", a.Mailbox, a.Host)
			}
		}
		date := ""
		if !env.Date.IsZero() {
			date = env.Date.In(time.Local).Format("2006-01-02 15:04")
		}
		state := "unread"
		if imapSeen(m.Flags) {
			state = "read"
		}
		fmt.Fprintf(&b, "  uid=%-6d %s [%s] %s | %s\n", m.UID, date, state, from, truncate(env.Subject, 80))
	}
	return strings.TrimRight(b.String(), "\n")
}

func imapListMessages(c *imapclient.Client, mailbox string, limit int, unreadOnly bool) (string, error) {
	if unreadOnly {
		return imapSearchMessages(c, mailbox, limit, map[string]any{"unread_only": true})
	}
	status, err := c.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return "", fmt.Errorf("select %q: %w", mailbox, err)
	}
	total := int(status.NumMessages)
	if total == 0 {
		return fmt.Sprintf("Mailbox %q is empty.", mailbox), nil
	}
	start := uint32(1)
	if total > limit {
		start = uint32(total - limit + 1)
	}
	var seqSet imap.SeqSet
	seqSet.AddRange(start, uint32(total))
	msgs, err := c.Fetch(seqSet, imapListFetch).Collect()
	if err != nil {
		return "", fmt.Errorf("fetch: %w", err)
	}
	return imapFormatList(fmt.Sprintf("Messages in %q", mailbox), msgs, total), nil
}

func imapDateArg(args map[string]any, key string) (time.Time, error) {
	v := argString(args, key, "")
	if v == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse("2006-01-02", v)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s date %q (want YYYY-MM-DD)", key, v)
	}
	return t, nil
}

func imapSearchMessages(c *imapclient.Client, mailbox string, limit int, args map[string]any) (string, error) {
	crit := &imap.SearchCriteria{}
	if v := argString(args, "text", ""); v != "" {
		crit.Text = append(crit.Text, v)
	}
	if v := argString(args, "from", ""); v != "" {
		crit.Header = append(crit.Header, imap.SearchCriteriaHeaderField{Key: "From", Value: v})
	}
	if v := argString(args, "subject", ""); v != "" {
		crit.Header = append(crit.Header, imap.SearchCriteriaHeaderField{Key: "Subject", Value: v})
	}
	var err error
	if crit.Since, err = imapDateArg(args, "since"); err != nil {
		return "", err
	}
	if crit.Before, err = imapDateArg(args, "before"); err != nil {
		return "", err
	}
	if argBool(args, "unread_only") {
		crit.NotFlag = append(crit.NotFlag, imap.FlagSeen)
	}
	if _, err := c.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return "", fmt.Errorf("select %q: %w", mailbox, err)
	}
	data, err := c.UIDSearch(crit, nil).Wait()
	if err != nil {
		return "", fmt.Errorf("search: %w", err)
	}
	uids := data.AllUIDs()
	if len(uids) == 0 {
		return fmt.Sprintf("No matching messages in %q.", mailbox), nil
	}
	total := len(uids)
	if total > limit {
		uids = uids[total-limit:] // UIDs ascend with arrival: keep the newest
	}
	msgs, err := c.Fetch(imap.UIDSetNum(uids...), imapListFetch).Collect()
	if err != nil {
		return "", fmt.Errorf("fetch: %w", err)
	}
	return imapFormatList(fmt.Sprintf("Matches in %q", mailbox), msgs, total), nil
}

func imapGetMessage(c *imapclient.Client, mailbox string, uid imap.UID) (string, error) {
	if _, err := c.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return "", fmt.Errorf("select %q: %w", mailbox, err)
	}

	// Peek: reading through the assistant must not flip the unread state.
	bodySection := &imap.FetchItemBodySection{Peek: true}
	msgs, err := c.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{bodySection},
	}).Collect()
	if err != nil {
		return "", fmt.Errorf("fetch uid=%d: %w", uid, err)
	}
	if len(msgs) == 0 {
		return "", fmt.Errorf("message uid=%d not found in %q", uid, mailbox)
	}
	raw := msgs[0].FindBodySection(bodySection)
	if raw == nil {
		return fmt.Sprintf("uid=%d: no body data returned.", uid), nil
	}
	return imapRenderMessage(raw)
}

const imapMaxBody = 12000

// imapRenderMessage turns a raw RFC 822 message into headers + plain text +
// attachment list. For multipart/alternative only one rendition is kept
// (text/plain preferred, else the HTML converted to text).
func imapRenderMessage(raw []byte) (string, error) {
	mr, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		return string(raw[:min(len(raw), 4096)]), nil
	}
	h := mr.Header
	var b strings.Builder
	if subj, err := h.Text("Subject"); err == nil {
		fmt.Fprintf(&b, "Subject: %s\n", subj)
	}
	if date, err := h.Date(); err == nil {
		fmt.Fprintf(&b, "Date: %s\n", date.In(time.Local).Format(time.RFC1123Z))
	}
	if from, err := h.AddressList("From"); err == nil {
		fmt.Fprintf(&b, "From: %s\n", formatAddrs(from))
	}
	if to, err := h.AddressList("To"); err == nil {
		fmt.Fprintf(&b, "To: %s\n", formatAddrs(to))
	}
	if cc, err := h.AddressList("Cc"); err == nil && len(cc) > 0 {
		fmt.Fprintf(&b, "Cc: %s\n", formatAddrs(cc))
	}
	b.WriteString("\n")

	var plain, html []string
	type attachment struct {
		name, ctype string
		size        int
	}
	var atts []attachment
	for {
		part, err := mr.NextPart()
		if err != nil {
			break // io.EOF or a malformed tail: render what we have
		}
		switch hd := part.Header.(type) {
		case *gomail.InlineHeader:
			body, _ := io.ReadAll(io.LimitReader(part.Body, 1<<20))
			ct, _, _ := hd.ContentType()
			if ct == "text/html" {
				html = append(html, string(body))
			} else if strings.HasPrefix(ct, "text/") || ct == "" {
				plain = append(plain, string(body))
			} else {
				atts = append(atts, attachment{"(inline)", ct, len(body)})
			}
		case *gomail.AttachmentHeader:
			name, _ := hd.Filename()
			ct, _, _ := hd.ContentType()
			n, _ := io.Copy(io.Discard, part.Body)
			atts = append(atts, attachment{name, ct, int(n)})
		}
	}
	text := strings.TrimSpace(strings.Join(plain, "\n"))
	if text == "" && len(html) > 0 {
		text = htmlToText(strings.Join(html, "\n"))
	}
	if r := []rune(text); len(r) > imapMaxBody {
		text = string(r[:imapMaxBody]) + "\n[truncated]"
	}
	b.WriteString(text)
	if len(atts) > 0 {
		fmt.Fprintf(&b, "\n\nAttachments (%d):\n", len(atts))
		for _, a := range atts {
			fmt.Fprintf(&b, "  - %s (%s, %d bytes)\n", a.name, a.ctype, a.size)
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func formatAddrs(addrs []*gomail.Address) string {
	parts := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if a.Name != "" {
			parts = append(parts, fmt.Sprintf("%s <%s>", a.Name, a.Address))
		} else {
			parts = append(parts, a.Address)
		}
	}
	return strings.Join(parts, ", ")
}

var (
	htmlBreakRe  = regexp.MustCompile(`(?i)<br\s*/?>|</p>|</div>|</li>|</tr>|</h[1-6]>`)
	htmlBlockRe  = regexp.MustCompile(`(?is)<(script|style|head)[^>]*>.*?</(script|style|head)>`)
	htmlTagRe    = regexp.MustCompile(`<[^>]*>`)
	htmlEntityRe = regexp.MustCompile(`&[a-zA-Z#0-9]+;`)
	blankLinesRe = regexp.MustCompile(`\n{3,}`)
	htmlEntities = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`,
		"&#39;", "'", "&nbsp;", " ", "&apos;", "'")
)

// htmlToText keeps the readable text of an HTML mail body.
func htmlToText(s string) string {
	s = htmlBlockRe.ReplaceAllString(s, "")
	s = htmlBreakRe.ReplaceAllString(s, "\n")
	s = htmlTagRe.ReplaceAllString(s, "")
	s = htmlEntities.Replace(s)
	s = htmlEntityRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(blankLinesRe.ReplaceAllString(s, "\n\n"))
}
