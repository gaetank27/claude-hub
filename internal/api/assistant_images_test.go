package api

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"system/internal/assistant"
	"system/internal/auth"
	"system/internal/imgsafe"
	"system/internal/secretbox"
	"system/internal/store"
	"system/internal/streamjson"
)

// fakeHub only creates the assistant project; no session is ever launched.
type fakeHub struct{ db *store.DB }

func (h fakeHub) CreateProject(ctx context.Context, name, dir, category string) (store.Project, error) {
	p := store.Project{ID: "p1", Name: name, Slug: "assistant", Dir: dir, CreatedAt: time.Now()}
	return p, h.db.CreateProject(p)
}
func (fakeHub) LaunchSession(ctx context.Context, projectID, permissionMode, model, comment, source string) (store.Session, error) {
	return store.Session{}, os.ErrInvalid
}
func (fakeHub) SendUserMessage(sessionID, text string) error { return nil }
func (fakeHub) SendUserBlocks(sessionID, text string, images []streamjson.ImageBlock) error {
	return nil
}
func (fakeHub) StopSession(ctx context.Context, sessionID string) error { return nil }

// The thread API exposes outgoing images as attachments[{id,kind,mime,width,
// height,caption,url}] and the route serves the exact bytes, nosniff, private
// cache, behind the Hub's authentication.
func TestAssistantOutgoingImageAPI(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	box, _ := secretbox.Open(filepath.Join(dir, "secret.key"))
	projects := filepath.Join(dir, "projects")
	projDir := filepath.Join(projects, "assistant")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	asst := assistant.New(db, fakeHub{db}, nil, box)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	// Pre-create the project the service adopts.
	if err := db.CreateProject(store.Project{ID: "p1", Name: "Assistant", Slug: "assistant", Dir: projDir, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	_ = db.SetAppMeta("assistant_project_id", "p1")
	go asst.Run(ctx)
	for i := 0; i < 100 && asst.ProjectID() == ""; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if asst.ProjectID() == "" {
		t.Fatal("assistant not ready")
	}
	asst.SetImageGuard(imgsafe.NewGuard(imgsafe.DefaultPolicy(projects, "", filepath.Join(dir, "data"))))

	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 20, 10)))
	src := filepath.Join(projects, "gen", "carte.png")
	_ = os.MkdirAll(filepath.Dir(src), 0o755)
	_ = os.WriteFile(src, b.Bytes(), 0o644)

	asst.TestQueueTurn("web")
	if _, err := asst.SendImage(src, "Carte"); err != nil {
		t.Fatal(err)
	}
	asst.TestAnswer("Voici")

	s := (&Server{db: db}).WithAssistant(asst)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/assistant/messages", s.assistantMessages)
	mux.HandleFunc("GET /api/assistant/attachments/{id}", s.assistantAttachment)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/assistant/messages", nil))
	var out struct {
		Messages []struct {
			Text        string `json:"text"`
			Attachments []struct {
				ID, Kind, Mime, Caption, URL, Direction string
				Width, Height                           int
				Expired                                 bool
			} `json:"attachments"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Messages) != 1 || len(out.Messages[0].Attachments) != 1 {
		t.Fatalf("%v %s", err, rec.Body.String())
	}
	a := out.Messages[0].Attachments[0]
	if a.Kind != "image" || a.Mime != "image/png" || a.Width != 20 || a.Height != 10 || a.Caption != "Carte" ||
		a.Direction != "out" || a.URL != "/api/assistant/attachments/"+a.ID {
		t.Fatalf("attachment json: %+v", a)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", a.URL, nil))
	h := rec.Header()
	if rec.Code != 200 || h.Get("Content-Type") != "image/png" || h.Get("X-Content-Type-Options") != "nosniff" ||
		h.Get("Cache-Control") != "private, max-age=3600" || Format(rec.Body.Bytes()) != "png" {
		t.Fatalf("serve: %d %v", rec.Code, h)
	}
	if rec = httptest.NewRecorder(); true {
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/assistant/attachments/not-an-id", nil))
		if rec.Code != 404 {
			t.Fatalf("unknown id: %d", rec.Code)
		}
	}

	// Behind the Hub's authentication middleware: no session, no image.
	guarded := auth.New(auth.Config{DB: db, Secrets: box, CookieSecret: []byte("0123456789abcdef0123456789abcdef")}).Middleware(mux)
	rec = httptest.NewRecorder()
	guarded.ServeHTTP(rec, httptest.NewRequest("GET", a.URL, nil))
	if rec.Code != http.StatusUnauthorized || bytes.Contains(rec.Body.Bytes(), []byte("PNG")) {
		t.Fatalf("unauthenticated: %d", rec.Code)
	}
}

func Format(b []byte) string { return imgsafe.Format(b) }
