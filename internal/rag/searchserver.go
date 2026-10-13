package rag

import (
	"encoding/json"
	"net/http"
)

// SearchResult is one retrieved chunk as returned to the rag-mcp tool.
type SearchResult struct {
	Text       string  `json:"text"`
	Title      string  `json:"title"`
	Source     string  `json:"source"`
	Collection string  `json:"collection"`
	Score      float32 `json:"score"`
}

// RegisterLoopback mounts the loopback-only retrieval endpoints consumed by the
// rag-mcp server onto mux. The listener is intentionally separate from the
// authenticated public API: it binds to localhost and is gated by a shared token
// (X-Rag-Token) so only hub-launched MCP processes can query it.
// POST /search {query,collection,k} -> {results}; GET /collections.
func RegisterLoopback(mux *http.ServeMux, ix *Indexer, token string) {
	gate := func(w http.ResponseWriter, r *http.Request) bool {
		if token != "" && r.Header.Get("X-Rag-Token") != token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return false
		}
		return true
	}
	mux.HandleFunc("POST /search", func(w http.ResponseWriter, r *http.Request) {
		if !gate(w, r) {
			return
		}
		var req struct {
			Query      string `json:"query"`
			Collection string `json:"collection"`
			K          int    `json:"k"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.K <= 0 || req.K > 20 {
			req.K = 5
		}
		scored, err := ix.Search(r.Context(), req.Query, req.Collection, req.K)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		results := make([]SearchResult, 0, len(scored))
		for _, s := range scored {
			results = append(results, SearchResult{
				Text:       s.Text,
				Title:      s.DocTitle,
				Source:     s.DocSourceRef,
				Collection: s.CollectionName,
				Score:      s.Score,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"results": results})
	})
	// Lets the rag-mcp discovery tool enumerate the available knowledge bases.
	mux.HandleFunc("GET /collections", func(w http.ResponseWriter, r *http.Request) {
		if !gate(w, r) {
			return
		}
		cols, err := ix.Collections()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := make([]map[string]string, 0, len(cols))
		for _, c := range cols {
			out = append(out, map[string]string{
				"name": c.Name, "slug": c.Slug, "description": c.Description,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"collections": out})
	})
}
