package api

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// helpJSON is generated from web/src/lib/help.ts by web/scripts/gen-help.mjs
// (run by `npm run build`): {"fr":[chapters],"en":[chapters]}. Chapters are kept
// as raw JSON so they reach clients exactly as help.ts defines them.
//
//go:embed helpdata/help.json
var helpJSON []byte

type helpPayload struct {
	body []byte
	etag string
}

var (
	helpOnce  sync.Once
	helpByLng map[string]helpPayload
)

func loadHelp() {
	var raw map[string]json.RawMessage
	helpByLng = map[string]helpPayload{}
	if json.Unmarshal(helpJSON, &raw) != nil {
		return
	}
	for lang, chapters := range raw {
		body, err := json.Marshal(struct {
			Lang     string          `json:"lang"`
			Chapters json.RawMessage `json:"chapters"`
		}{lang, chapters})
		if err != nil {
			continue
		}
		sum := sha256.Sum256(body)
		helpByLng[lang] = helpPayload{body: body, etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
	}
}

// getHelp serves the Hub's manual as data (GET /api/help?lang=fr|en) for clients
// that cannot read the web bundle, such as the Android app. Same rule as the web
// UI's getHelpChapters: French when lang starts with "fr", English otherwise
// (missing or unknown language included). Shot `src` values are paths under
// /help/screenshots/<lang>/, served by the SPA handler. Behind the usual auth.
func (s *Server) getHelp(w http.ResponseWriter, r *http.Request) {
	helpOnce.Do(loadHelp)
	lang := "en"
	if strings.HasPrefix(strings.ToLower(r.URL.Query().Get("lang")), "fr") {
		lang = "fr"
	}
	p, ok := helpByLng[lang]
	if !ok {
		http.Error(w, "help unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("ETag", p.etag)
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("Vary", "Cookie, Authorization")
	if inm := r.Header.Get("If-None-Match"); inm != "" && strings.Contains(inm, p.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(p.body)
}
