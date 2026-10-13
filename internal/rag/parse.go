package rag

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/ledongthuc/pdf"
)

// ExtractText turns a raw source into plain text ready for chunking. Text and
// markdown are kept verbatim (markdown structure is meaningful to the embedder);
// PDFs are run through a pure-Go text extractor.
func ExtractText(sourceType string, raw []byte) (string, error) {
	switch sourceType {
	case SourceText, SourceMarkdown:
		return string(raw), nil
	case SourcePDF:
		return extractPDF(raw)
	default:
		return "", fmt.Errorf("unsupported source type %q", sourceType)
	}
}

// Re-export the store-side constants under the rag package so callers (parsers,
// indexer) don't have to import store just for the string literals.
const (
	SourceText     = "text"
	SourceMarkdown = "markdown"
	SourcePDF      = "pdf"
	SourceWeb      = "web"
)

func extractPDF(raw []byte) (string, error) {
	r, err := pdf.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", fmt.Errorf("read pdf: %w", err)
	}
	var b strings.Builder
	for i := 1; i <= r.NumPage(); i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		txt, err := p.GetPlainText(nil)
		if err != nil {
			continue // skip unreadable pages rather than failing the whole document
		}
		b.WriteString(txt)
		b.WriteString("\n")
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "", fmt.Errorf("pdf contained no extractable text (scanned image?)")
	}
	return out, nil
}

var (
	scriptStyleRe = regexp.MustCompile(`(?is)<(script|style|head)[^>]*>.*?</(script|style|head)>`)
	tagRe         = regexp.MustCompile(`(?s)<[^>]+>`)
	titleRe       = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	wsRe          = regexp.MustCompile(`[ \t]*\n\s*\n\s*`)
)

// FetchWeb downloads a web page and returns its extracted text and a best-effort
// title (the <title> tag, else the URL). It strips scripts/styles and tags — good
// enough for ingesting documentation pages without a heavyweight HTML/readability
// dependency.
func FetchWeb(ctx context.Context, url string) (text, title string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "claude-hub-rag/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("fetch %s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 8MB cap
	if err != nil {
		return "", "", err
	}
	raw := string(body)
	title = url
	if m := titleRe.FindStringSubmatch(raw); m != nil {
		if t := strings.TrimSpace(html.UnescapeString(m[1])); t != "" {
			title = t
		}
	}
	stripped := scriptStyleRe.ReplaceAllString(raw, " ")
	stripped = tagRe.ReplaceAllString(stripped, " ")
	stripped = html.UnescapeString(stripped)
	stripped = wsRe.ReplaceAllString(stripped, "\n\n")
	text = strings.TrimSpace(stripped)
	if text == "" {
		return "", "", fmt.Errorf("no extractable text at %s", url)
	}
	return text, title, nil
}
