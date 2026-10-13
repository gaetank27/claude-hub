package tools

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/smtp"
	"net/textproto"
	"os"
	"path/filepath"
	"time"
)

func init() { register(email{}) }

// email sends mail over SMTP. Reception (IMAP) is intentionally out of scope for
// this first connector; the fields and Test cover outbound delivery.
type email struct{}

func (email) Type() string  { return "email" }
func (email) Label() string { return "Email (SMTP)" }

func (email) Fields() []Field {
	return []Field{
		{Key: "smtp_host", Label: "Serveur SMTP", Type: FieldText, Required: true,
			Placeholder: "smtp.gmail.com"},
		{Key: "smtp_port", Label: "Port", Type: FieldNumber, Required: true, Placeholder: "587"},
		{Key: "smtp_user", Label: "Utilisateur", Type: FieldText, Required: true},
		{Key: "smtp_password", Label: "Mot de passe", Type: FieldPassword, Secret: true, Required: true},
		{Key: "from", Label: "Expéditeur (From)", Type: FieldText, Required: true,
			Placeholder: "moi@exemple.com"},
		{Key: "skip_verify", Label: "Ne pas vérifier le certificat TLS (serveur self-signed)", Type: FieldBool},
	}
}

func (email) Test(ctx context.Context, cfg map[string]string) error {
	if err := missing(cfg, "smtp_host", "smtp_port", "smtp_user", "smtp_password"); err != nil {
		return err
	}
	c, err := smtpConnect(cfg)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Quit()
}

func (email) Tools() []ToolSpec {
	return []ToolSpec{{
		Name:        "email_send",
		Description: "Send an email via the configured SMTP account.",
		Args: []ArgSpec{
			{Name: "to", Type: "string", Description: "Recipient address", Required: true},
			{Name: "subject", Type: "string", Description: "Subject", Required: true},
			{Name: "body", Type: "string", Description: "Plain-text body", Required: true},
			{Name: "attachments", Type: "array", Items: "string",
				Description: "Optional list of local file paths on the host to attach"},
		},
	}}
}

func (email) Call(ctx context.Context, cfg map[string]string, action string, args map[string]any) (string, error) {
	if action != "email_send" {
		return "", fmt.Errorf("unknown action %q", action)
	}
	to := argString(args, "to", "")
	subject := argString(args, "subject", "")
	body := argString(args, "body", "")
	if to == "" || subject == "" {
		return "", fmt.Errorf("to and subject are required")
	}
	from := cfg["from"]
	if from == "" {
		from = cfg["smtp_user"]
	}
	attachments := argStrings(args, "attachments")
	msg, err := buildMessage(from, to, subject, body, attachments)
	if err != nil {
		return "", err
	}

	c, err := smtpConnect(cfg)
	if err != nil {
		return "", err
	}
	defer c.Close()
	if err := c.Mail(from); err != nil {
		return "", err
	}
	if err := c.Rcpt(to); err != nil {
		return "", err
	}
	wc, err := c.Data()
	if err != nil {
		return "", err
	}
	if _, err := wc.Write(msg); err != nil {
		return "", err
	}
	if err := wc.Close(); err != nil {
		return "", err
	}
	_ = c.Quit()
	if len(attachments) > 0 {
		return fmt.Sprintf("Email sent to %s with %d attachment(s)", to, len(attachments)), nil
	}
	return "Email sent to " + to, nil
}

// buildMessage assembles the RFC 5322 message. Without attachments it stays a
// plain text/plain body (unchanged from the original behaviour); with
// attachments it becomes a multipart/mixed message whose first part is the body
// and whose following parts are the base64-encoded files.
func buildMessage(from, to, subject, body string, attachments []string) ([]byte, error) {
	var buf bytes.Buffer
	header := func(k, v string) { fmt.Fprintf(&buf, "%s: %s\r\n", k, v) }
	header("From", from)
	header("To", to)
	header("Subject", subject)
	header("MIME-Version", "1.0")

	if len(attachments) == 0 {
		header("Content-Type", "text/plain; charset=UTF-8")
		buf.WriteString("\r\n")
		buf.WriteString(body)
		return buf.Bytes(), nil
	}

	mw := multipart.NewWriter(&buf)
	header("Content-Type", "multipart/mixed; boundary="+mw.Boundary())
	buf.WriteString("\r\n")

	tp, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type": {"text/plain; charset=UTF-8"},
	})
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(tp, body); err != nil {
		return nil, err
	}

	for _, path := range attachments {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read attachment %q: %w", path, err)
		}
		name := filepath.Base(path)
		ct := mime.TypeByExtension(filepath.Ext(name))
		if ct == "" {
			ct = "application/octet-stream"
		}
		part, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {ct},
			"Content-Transfer-Encoding": {"base64"},
			"Content-Disposition":       {fmt.Sprintf("attachment; filename=%q", name)},
		})
		if err != nil {
			return nil, err
		}
		if err := writeBase64Lines(part, data); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeBase64Lines encodes data to base64 wrapped at 76 columns with CRLF, as
// required by RFC 2045 for the base64 transfer encoding.
func writeBase64Lines(w io.Writer, data []byte) error {
	const lineLen = 76
	enc := base64.StdEncoding.EncodeToString(data)
	for i := 0; i < len(enc); i += lineLen {
		end := i + lineLen
		if end > len(enc) {
			end = len(enc)
		}
		if _, err := io.WriteString(w, enc[i:end]+"\r\n"); err != nil {
			return err
		}
	}
	return nil
}

// (context is part of the Connector interface signature; SMTP dialing uses its own
// timeouts rather than ctx, which is acceptable for these short calls.)

// smtpConnect dials the SMTP server, negotiates TLS (implicit on 465, STARTTLS
// otherwise) and authenticates. The caller closes/quits the returned client.
func smtpConnect(cfg map[string]string) (*smtp.Client, error) {
	host := cfg["smtp_host"]
	addr := net.JoinHostPort(host, cfg["smtp_port"])
	// InsecureSkipVerify covers self-signed certs and certs lacking a SAN for the
	// host/IP being dialed (common on LAN mail servers reached by IP).
	tlsCfg := &tls.Config{ServerName: host, InsecureSkipVerify: cfg["skip_verify"] == "true"}

	var (
		c   *smtp.Client
		err error
	)
	if cfg["smtp_port"] == "465" {
		conn, derr := tls.DialWithDialer(&net.Dialer{Timeout: 15 * time.Second}, "tcp", addr, tlsCfg)
		if derr != nil {
			return nil, derr
		}
		c, err = smtp.NewClient(conn, host)
	} else {
		conn, derr := net.DialTimeout("tcp", addr, 15*time.Second)
		if derr != nil {
			return nil, derr
		}
		if c, err = smtp.NewClient(conn, host); err == nil {
			if ok, _ := c.Extension("STARTTLS"); ok {
				err = c.StartTLS(tlsCfg)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	auth := smtp.PlainAuth("", cfg["smtp_user"], cfg["smtp_password"], host)
	if err := c.Auth(auth); err != nil {
		c.Close()
		return nil, fmt.Errorf("smtp auth: %w", err)
	}
	return c, nil
}
