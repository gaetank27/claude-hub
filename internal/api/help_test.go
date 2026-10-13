package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"system/internal/auth"
)

type helpResp struct {
	Lang     string `json:"lang"`
	Chapters []struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Tagline  string `json:"tagline"`
		Sections []struct {
			Heading string `json:"heading"`
			Blocks  []struct {
				Kind  string   `json:"kind"`
				Text  string   `json:"text"`
				Items []string `json:"items"`
				Src   string   `json:"src"`
			} `json:"blocks"`
		} `json:"sections"`
	} `json:"chapters"`
}

func fetchHelp(t *testing.T, h http.Handler, url string) (*httptest.ResponseRecorder, helpResp) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
	var out helpResp
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return rec, out
}

func TestHelpRoute(t *testing.T) {
	h := http.HandlerFunc((&Server{}).getHelp)

	rec, fr := fetchHelp(t, h, "/api/help?lang=fr")
	if rec.Code != 200 || fr.Lang != "fr" || len(fr.Chapters) < 8 {
		t.Fatalf("fr: %d %q %d", rec.Code, fr.Lang, len(fr.Chapters))
	}
	_, en := fetchHelp(t, h, "/api/help?lang=en")
	if en.Lang != "en" || len(en.Chapters) != len(fr.Chapters) {
		t.Fatalf("en: %q %d", en.Lang, len(en.Chapters))
	}
	for i := range fr.Chapters {
		if fr.Chapters[i].ID != en.Chapters[i].ID {
			t.Fatalf("chapter id mismatch at %d", i)
		}
	}
	// Default and unknown languages fall back to English (same rule as the web UI).
	for _, u := range []string{"/api/help", "/api/help?lang=xx", "/api/help?lang="} {
		if _, d := fetchHelp(t, h, u); d.Lang != "en" || d.Chapters[0].Title != en.Chapters[0].Title {
			t.Fatalf("%s: lang %q", u, d.Lang)
		}
	}
	if _, d := fetchHelp(t, h, "/api/help?lang=fr-CH"); d.Lang != "fr" {
		t.Fatalf("fr-CH: %q", d.Lang)
	}

	// Recent features must be present, and screenshots must be /help/ paths.
	all := func(r helpResp) string { b, _ := json.Marshal(r); return string(b) }
	for _, want := range []string{"Clés d'API", "script_command_send", "proxmox_backup_report", "regroup", "catégorie"} {
		if !strings.Contains(all(fr), want) {
			t.Errorf("fr help lacks %q", want)
		}
	}
	for _, want := range []string{"API keys", "script_command_send", "proxmox_backup_report"} {
		if !strings.Contains(all(en), want) {
			t.Errorf("en help lacks %q", want)
		}
	}
	shots := 0
	for _, c := range fr.Chapters {
		for _, s := range c.Sections {
			for _, b := range s.Blocks {
				if b.Kind == "shot" {
					shots++
					if !strings.HasPrefix(b.Src, "/help/screenshots/fr/") {
						t.Errorf("bad shot src %q", b.Src)
					}
				}
			}
		}
	}
	if shots == 0 {
		t.Error("no screenshots referenced")
	}

	// ETag revalidation.
	etag := rec.Header().Get("ETag")
	if etag == "" || rec.Header().Get("Cache-Control") == "" {
		t.Fatal("missing cache headers")
	}
	req := httptest.NewRequest("GET", "/api/help?lang=fr", nil)
	req.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusNotModified || rec2.Body.Len() != 0 {
		t.Fatalf("expected 304, got %d", rec2.Code)
	}
}

// The route sits under /api/, so the global auth middleware must refuse
// unauthenticated callers.
func TestHelpRouteRequiresAuth(t *testing.T) {
	s := &Server{auth: auth.New(auth.Config{})}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/help?lang=fr", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}
