package tools

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildMessagePlain(t *testing.T) {
	msg, err := buildMessage("me@x.com", "you@y.com", "Hi", "hello body", nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ReadMessage(bytes.NewReader(msg))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ct := m.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("want text/plain, got %q", ct)
	}
	body, _ := io.ReadAll(m.Body)
	if string(body) != "hello body" {
		t.Fatalf("body = %q", body)
	}
}

func TestBuildMessageWithAttachments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.txt")
	want := []byte("file contents \x00\x01 binary-ish")
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatal(err)
	}

	msg, err := buildMessage("me@x.com", "you@y.com", "Hi", "see attached", []string{path})
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ReadMessage(bytes.NewReader(msg))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mediaType, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	if mediaType != "multipart/mixed" {
		t.Fatalf("want multipart/mixed, got %q", mediaType)
	}

	mr := multipart.NewReader(m.Body, params["boundary"])
	// First part: the text body.
	bodyPart, err := mr.NextPart()
	if err != nil {
		t.Fatalf("body part: %v", err)
	}
	if b, _ := io.ReadAll(bodyPart); string(b) != "see attached" {
		t.Fatalf("body part = %q", b)
	}
	// Second part: the attachment, base64-decoded by the multipart reader.
	att, err := mr.NextPart()
	if err != nil {
		t.Fatalf("attachment part: %v", err)
	}
	if cd := att.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="report.txt"`) {
		t.Fatalf("disposition = %q", cd)
	}
	if cte := att.Header.Get("Content-Transfer-Encoding"); cte != "base64" {
		t.Fatalf("transfer-encoding = %q", cte)
	}
	// The multipart reader does not decode base64; do it ourselves.
	raw, _ := io.ReadAll(att)
	got, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(string(raw), "\r\n", ""))
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("attachment content = %q, want %q", got, want)
	}
}

func TestBuildMessageMissingAttachment(t *testing.T) {
	if _, err := buildMessage("me@x.com", "you@y.com", "Hi", "b", []string{"/no/such/file"}); err == nil {
		t.Fatal("expected error for missing attachment")
	}
}
