// Package rag implements the global Retrieval-Augmented Generation knowledge
// base: parsing ingested sources, chunking them, embedding the chunks, and
// ranking them by cosine similarity for retrieval. Embedding generation is hidden
// behind the Embedder interface so the provider is pluggable; the default is a
// fully-local Ollama model (no data leaves the host).
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

// InputKind distinguishes documents (indexed once) from queries (issued at search
// time). Some providers — Voyage included — embed the two asymmetrically for
// better retrieval, so the caller must say which side it is embedding.
type InputKind int

const (
	InputDocument InputKind = iota
	InputQuery
)

// Embedder turns text into vectors. Implementations must preserve input order in
// the returned slice and may batch internally.
type Embedder interface {
	Embed(ctx context.Context, texts []string, kind InputKind) ([][]float32, error)
	// Name identifies the provider/model, for logging and the UI status banner.
	Name() string
}

// NewEmbedderFromEnv resolves the configured embedder. Selection:
//   - SYSTEM_EMBEDDER=ollama (default for this deployment): a local Ollama model
//     (OLLAMA_HOST, OLLAMA_EMBED_MODEL), no data leaves the host.
//   - SYSTEM_EMBEDDER=voyage or, when unset, a present VOYAGE_API_KEY: the hosted
//     Voyage API (kept as an optional fallback, unused in the local-only setup).
//
// ok is false when nothing is configured, so the indexer can surface a clear
// "embeddings provider not configured" state instead of failing every document.
func NewEmbedderFromEnv() (e Embedder, ok bool) {
	switch strings.ToLower(os.Getenv("SYSTEM_EMBEDDER")) {
	case "ollama":
		return newOllamaFromEnv(), true
	case "voyage":
		return voyageFromEnv()
	default:
		// Auto: prefer a configured local Ollama, else Voyage if keyed.
		if os.Getenv("OLLAMA_HOST") != "" || os.Getenv("OLLAMA_EMBED_MODEL") != "" {
			return newOllamaFromEnv(), true
		}
		return voyageFromEnv()
	}
}

func voyageFromEnv() (Embedder, bool) {
	key := os.Getenv("VOYAGE_API_KEY")
	if key == "" {
		return nil, false
	}
	model := os.Getenv("VOYAGE_MODEL")
	if model == "" {
		model = "voyage-3-lite"
	}
	return NewVoyageEmbedder(key, model), true
}

// voyageMaxBatch is Voyage's per-request input cap.
const voyageMaxBatch = 96

// VoyageEmbedder calls the Voyage AI embeddings API.
type VoyageEmbedder struct {
	apiKey string
	model  string
	http   *http.Client
}

func NewVoyageEmbedder(apiKey, model string) *VoyageEmbedder {
	return &VoyageEmbedder{
		apiKey: apiKey,
		model:  model,
		http:   &http.Client{Timeout: 60 * time.Second},
	}
}

func (v *VoyageEmbedder) Name() string { return "voyage:" + v.model }

func (v *VoyageEmbedder) Embed(ctx context.Context, texts []string, kind InputKind) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += voyageMaxBatch {
		end := start + voyageMaxBatch
		if end > len(texts) {
			end = len(texts)
		}
		batch, err := v.embedBatch(ctx, texts[start:end], kind)
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
	}
	return out, nil
}

func (v *VoyageEmbedder) embedBatch(ctx context.Context, texts []string, kind InputKind) ([][]float32, error) {
	inputType := "document"
	if kind == InputQuery {
		inputType = "query"
	}
	body, _ := json.Marshal(map[string]any{
		"input":      texts,
		"model":      v.model,
		"input_type": inputType,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.voyageai.com/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+v.apiKey)

	resp, err := v.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var buf bytes.Buffer
		buf.ReadFrom(resp.Body)
		return nil, fmt.Errorf("voyage embeddings: %s: %s", resp.Status, buf.String())
	}
	var parsed struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("voyage decode: %w", err)
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("voyage returned %d vectors for %d inputs", len(parsed.Data), len(texts))
	}
	// Reorder defensively by the API-reported index.
	vecs := make([][]float32, len(texts))
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(vecs) {
			return nil, fmt.Errorf("voyage out-of-range index %d", d.Index)
		}
		vecs[d.Index] = d.Embedding
	}
	return vecs, nil
}
