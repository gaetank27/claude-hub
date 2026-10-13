package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultOllamaHost  = "http://127.0.0.1:11434"
	defaultOllamaModel = "nomic-embed-text"
	// ollamaMaxBatch bounds the inputs per /api/embed call: CPU inference of a
	// large model is memory- and time-bound, so we keep each request modest.
	ollamaMaxBatch = 32
)

// OllamaEmbedder generates embeddings with a local Ollama model — fully on-host,
// no third-party service, no data egress.
type OllamaEmbedder struct {
	host  string
	model string
	http  *http.Client
}

// newOllamaFromEnv builds an Ollama embedder from OLLAMA_HOST / OLLAMA_EMBED_MODEL,
// defaulting to a local server and nomic-embed-text (a light, CPU-friendly model).
func newOllamaFromEnv() *OllamaEmbedder {
	host := os.Getenv("OLLAMA_HOST")
	if host == "" {
		host = defaultOllamaHost
	}
	// OLLAMA_HOST is often "host:port" without a scheme; normalize to a URL.
	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		host = "http://" + host
	}
	model := os.Getenv("OLLAMA_EMBED_MODEL")
	if model == "" {
		model = defaultOllamaModel
	}
	return &OllamaEmbedder{
		host:  strings.TrimRight(host, "/"),
		model: model,
		// CPU inference of a large model can take a few seconds per batch.
		http: &http.Client{Timeout: 180 * time.Second},
	}
}

func (o *OllamaEmbedder) Name() string { return "ollama:" + o.model }

func (o *OllamaEmbedder) Embed(ctx context.Context, texts []string, kind InputKind) ([][]float32, error) {
	prepared := make([]string, len(texts))
	for i, t := range texts {
		prepared[i] = o.withPrefix(t, kind)
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(prepared); start += ollamaMaxBatch {
		end := start + ollamaMaxBatch
		if end > len(prepared) {
			end = len(prepared)
		}
		batch, err := o.embedBatch(ctx, prepared[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
	}
	return out, nil
}

// withPrefix applies the retrieval task prefixes nomic-embed-text needs to embed
// documents and queries asymmetrically. bge-m3 (and most others) are symmetric and
// need no prefix, so this is a no-op for them.
func (o *OllamaEmbedder) withPrefix(text string, kind InputKind) string {
	if !strings.Contains(o.model, "nomic") {
		return text
	}
	if kind == InputQuery {
		return "search_query: " + text
	}
	return "search_document: " + text
}

func (o *OllamaEmbedder) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	// truncate=true: a chunk longer than the model's context (e.g. all-minilm caps
	// at 256 tokens) is truncated to fit instead of failing the whole document.
	body, _ := json.Marshal(map[string]any{"model": o.model, "input": texts, "truncate": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.host+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama embed (%s): %w", o.host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var buf bytes.Buffer
		buf.ReadFrom(resp.Body)
		return nil, fmt.Errorf("ollama embed: %s: %s", resp.Status, strings.TrimSpace(buf.String()))
	}
	var parsed struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("ollama decode: %w", err)
	}
	if len(parsed.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama returned %d vectors for %d inputs", len(parsed.Embeddings), len(texts))
	}
	return parsed.Embeddings, nil
}
