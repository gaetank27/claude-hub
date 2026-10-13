package scriptstatus

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			im.Set(x, y, color.RGBA{uint8(x), uint8(y), 90, 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, im, &jpeg.Options{Quality: 60}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// imgEnv adds the image routes (public upload + admin) to the test server.
func imgEnv(t *testing.T) (*env, string) {
	t.Helper()
	e := newEnv(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/script-status/{script}", e.svc.Receive)
	mux.HandleFunc("POST /webhook/script-status/{script}/image", e.svc.ReceiveImage)
	mux.HandleFunc("GET /api/script-images/{script}", e.svc.ImageListHandler)
	mux.HandleFunc("GET /api/script-images/{script}/{id}", e.svc.ImageGetHandler)
	mux.HandleFunc("DELETE /api/script-images/{script}", e.svc.ImageDeleteHandler)
	mux.HandleFunc("GET /api/script-dashboard", e.svc.DashboardHandler)
	e.srv.Config.Handler = mux
	k, secret, err := CreateKey(e.svc.db, "shot", "", nil, 0, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetKeyImages(e.svc.db, k.ID, true); err != nil {
		t.Fatal(err)
	}
	return e, secret
}

func (e *env) upload(script, key, ctype string, body []byte) (int, string) {
	req, _ := http.NewRequest("POST", e.srv.URL+"/webhook/script-status/"+script+"/image", bytes.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if key != "" {
		req.Header.Set(KeyHeader, key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (e *env) get(path string) (*http.Response, []byte) {
	resp, err := http.Get(e.srv.URL + path)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func filesIn(dir string) int {
	n := 0
	_ = filepath.Walk(dir, func(_ string, i os.FileInfo, err error) error {
		if err == nil && !i.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func TestImageUploadAndRead(t *testing.T) {
	e, key := imgEnv(t)
	src := append(testJPEG(t, 320, 200), []byte("<script>alert(1)</script>trailing payload")...)
	code, body := e.upload("shot", key, "image/jpeg", src)
	if code != 200 {
		t.Fatalf("upload: %d %s", code, body)
	}
	var out struct {
		ID            int64
		Width, Height int
	}
	_ = json.Unmarshal([]byte(body), &out)
	if out.ID == 0 || out.Width != 320 || out.Height != 200 {
		t.Fatalf("response: %s", body)
	}
	imgs, _ := e.svc.ListImages("shot")
	if len(imgs) != 1 || imgs[0].Mime != "image/jpeg" || imgs[0].Size == 0 {
		t.Fatalf("list: %+v", imgs)
	}
	resp, data := e.get(fmt.Sprintf("/api/script-images/shot/%d", out.ID))
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" ||
		resp.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(resp.Header.Get("Cache-Control"), "no-store") {
		t.Fatalf("get: %d %v", resp.StatusCode, resp.Header)
	}
	// Re-encoded: a real JPEG, without what was appended to the upload.
	if _, err := jpeg.Decode(bytes.NewReader(data)); err != nil || bytes.Contains(data, []byte("trailing payload")) {
		t.Fatalf("stored image not re-encoded (err=%v)", err)
	}
	// File on disk: private, under the data dir.
	st, err := os.Stat(e.svc.imagePath("shot", out.ID, "image/jpeg"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v %v", err, st)
	}
	// PNG accepted too; dashboard lists both, newest first.
	if code, body := e.upload("shot", key, "image/png", testPNG(t, 40, 30)); code != 200 {
		t.Fatalf("png: %d %s", code, body)
	}
	e.post("shot", key, `{"state":"ok","summary":"s"}`, "")
	_, dash := e.get("/api/script-dashboard")
	var d struct {
		Scripts []struct{ Images []ImageInfo }
	}
	_ = json.Unmarshal(dash, &d)
	if len(d.Scripts) != 1 || len(d.Scripts[0].Images) != 2 || d.Scripts[0].Images[0].Mime != "image/png" {
		t.Fatalf("dashboard: %s", dash)
	}
	// Another script's name never reaches this script's image.
	if resp, _ := e.get(fmt.Sprintf("/api/script-images/other/%d", out.ID)); resp.StatusCode != 404 {
		t.Fatalf("cross-script read: %d", resp.StatusCode)
	}
	for _, p := range []string{"/api/script-images/shot/0", "/api/script-images/shot/abc", "/api/script-images/shot/999", "/api/script-images/..%2F..%2Fetc/1"} {
		if resp, _ := e.get(p); resp.StatusCode != 404 {
			t.Fatalf("%s: %d", p, resp.StatusCode)
		}
	}
}

func TestImageRequiresPermissionAndKey(t *testing.T) {
	e, key := imgEnv(t)
	img := testJPEG(t, 32, 32)
	// A key without allow_images: authenticated, but refused.
	_, plain, _ := CreateKey(e.svc.db, "plain", "", nil, 0, false, nil)
	if code, _ := e.upload("plain", plain, "image/jpeg", img); code != 403 {
		t.Fatalf("no permission: %d", code)
	}
	// The permission is per key: a key of another script does not open this one.
	if code, _ := e.upload("shot", plain, "image/jpeg", img); code != 401 {
		t.Fatalf("wrong key: %d", code)
	}
	if code, _ := e.upload("shot", "", "image/jpeg", img); code != 401 {
		t.Fatalf("no key: %d", code)
	}
	if filesIn(e.svc.imgDir) != 0 {
		t.Fatal("a refused upload left a file")
	}
	// Withdrawn permission stops uploads at once.
	keys, _ := ListKeys(e.svc.db)
	for _, k := range keys {
		if k.Script == "shot" {
			if !k.AllowImages {
				t.Fatal("AllowImages not reported")
			}
			_ = SetKeyImages(e.svc.db, k.ID, false)
		}
	}
	if code, _ := e.upload("shot", key, "image/jpeg", img); code != 403 {
		t.Fatalf("withdrawn: %d", code)
	}
	// IP allow-list applies to images like to reports.
	k2, s2, _ := CreateKey(e.svc.db, "pinned", "", []string{"203.0.113.9"}, 0, false, nil)
	_ = SetKeyImages(e.svc.db, k2.ID, true)
	if code, _ := e.upload("pinned", s2, "image/jpeg", img); code != 401 {
		t.Fatalf("ip: %d", code)
	}
}

func TestImageValidation(t *testing.T) {
	e, key := imgEnv(t)
	e.svc.irate = nil // not the subject here
	e.svc.irate = map[string]*window{}
	jpg := testJPEG(t, 32, 32)
	gif := []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;")
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	cases := []struct {
		name, ctype string
		body        []byte
		want        int
	}{
		{"json content-type", "application/json", jpg, 415},
		{"no content-type", "", jpg, 415},
		{"svg", "image/svg+xml", svg, 415},
		{"gif declared", "image/gif", gif, 415},
		{"gif as jpeg", "image/jpeg", gif, 400},
		{"svg as png", "image/png", svg, 400},
		{"png declared jpeg", "image/jpeg", testPNG(t, 8, 8), 400},
		{"empty", "image/jpeg", nil, 400},
		{"truncated", "image/jpeg", jpg[:len(jpg)/2], 400},
		{"garbage after magic", "image/jpeg", append([]byte{0xFF, 0xD8, 0xFF}, bytes.Repeat([]byte("x"), 200)...), 400},
		{"too large", "image/jpeg", append(append([]byte{}, jpg...), make([]byte, maxImageBody)...), 413},
	}
	for i, c := range cases {
		e.advance(time.Minute + time.Second) // fresh rate windows
		code, body := e.upload("shot", key, c.ctype, c.body)
		if code != c.want {
			t.Errorf("%d %s: got %d %s, want %d", i, c.name, code, body, c.want)
		}
	}
	if n := filesIn(e.svc.imgDir); n != 0 {
		t.Fatalf("%d file(s) stored by invalid uploads", n)
	}
	// Decompression bomb: tiny file, huge declared size.
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewGray(image.Rect(0, 0, 5000, 5000)))
	if b.Len() < maxImageBody {
		e.advance(2 * time.Minute)
		if code, _ := e.upload("shot", key, "image/png", b.Bytes()); code != 400 {
			t.Fatalf("pixel budget: %d", code)
		}
	}
}

func TestImageRetention(t *testing.T) {
	e, key := imgEnv(t)
	img := testJPEG(t, 16, 16)
	for i := 0; i < imagesKeep+5; i++ {
		e.advance(time.Minute)
		if code, body := e.upload("shot", key, "image/jpeg", img); code != 200 {
			t.Fatalf("upload %d: %d %s", i, code, body)
		}
	}
	imgs, _ := e.svc.ListImages("shot")
	if len(imgs) != imagesKeep || filesIn(e.svc.imgDir) != imagesKeep {
		t.Fatalf("kept %d rows, %d files, want %d", len(imgs), filesIn(e.svc.imgDir), imagesKeep)
	}
	if imgs[0].ID <= imgs[len(imgs)-1].ID {
		t.Fatal("not newest first")
	}
	oldest := imgs[len(imgs)-1].ID
	// Past the age limit: unreadable at once, even before the purge runs…
	e.advance(imageMaxAge + time.Minute)
	if l, _ := e.svc.ListImages("shot"); len(l) != 0 {
		t.Fatalf("expired images still listed: %d", len(l))
	}
	if resp, _ := e.get(fmt.Sprintf("/api/script-images/shot/%d", oldest)); resp.StatusCode != 404 {
		t.Fatalf("expired image served: %d", resp.StatusCode)
	}
	// …and gone from the disk after it, with no upload needed.
	e.svc.PurgeImages()
	if n := filesIn(e.svc.imgDir); n != 0 {
		t.Fatalf("%d file(s) left after the purge", n)
	}
	var rows int
	_ = e.svc.db.QueryRow(`SELECT COUNT(*) FROM script_images`).Scan(&rows)
	if rows != 0 {
		t.Fatalf("%d row(s) left", rows)
	}
}

func TestImageDeleteAndOrphans(t *testing.T) {
	e, key := imgEnv(t)
	img := testJPEG(t, 16, 16)
	for i := 0; i < 3; i++ {
		e.advance(time.Minute)
		e.upload("shot", key, "image/jpeg", img)
	}
	// A stray file (no row) is swept with the next purge.
	stray := filepath.Join(e.svc.imgDir, "shot", "stray.jpg")
	_ = os.WriteFile(stray, img, 0o600)
	e.svc.PurgeImages()
	if _, err := os.Stat(stray); err == nil {
		t.Fatal("orphan file kept")
	}
	if filesIn(e.svc.imgDir) != 3 {
		t.Fatalf("purge removed live images: %d", filesIn(e.svc.imgDir))
	}
	req, _ := http.NewRequest("DELETE", e.srv.URL+"/api/script-images/shot", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("delete: %v %v", err, resp)
	}
	resp.Body.Close()
	if l, _ := e.svc.ListImages("shot"); len(l) != 0 || filesIn(e.svc.imgDir) != 0 {
		t.Fatalf("delete left %d rows, %d files", len(l), filesIn(e.svc.imgDir))
	}
}

func TestImageRateLimit(t *testing.T) {
	e, key := imgEnv(t)
	img := testJPEG(t, 16, 16)
	for i := 0; i < imageRate; i++ {
		if code, _ := e.upload("shot", key, "image/jpeg", img); code != 200 {
			t.Fatalf("upload %d: %d", i, code)
		}
	}
	if code, _ := e.upload("shot", key, "image/jpeg", img); code != 429 {
		t.Fatalf("over the rate: %d", code)
	}
	// Status reports are not blocked by the image rate.
	if code, _ := e.post("shot", key, `{"state":"ok","summary":"s"}`, ""); code != 200 {
		t.Fatalf("report blocked: %d", code)
	}
}

func TestImageKeyAPI(t *testing.T) {
	e, _ := imgEnv(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/script-keys", e.svc.CreateKeyHandler)
	mux.HandleFunc("PATCH /api/script-keys/{id}", e.svc.UpdateKeyHandler)
	e.srv.Config.Handler = mux
	do := func(method, path, body string) (int, string) {
		req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	// Off unless asked for; the "screenshot" script gets its harmless commands.
	code, body := do("POST", "/api/script-keys", `{"script":"screenshot"}`)
	var a struct{ Key Key }
	_ = json.Unmarshal([]byte(body), &a)
	if code != 201 || a.Key.AllowImages || strings.Join(a.Key.AllowedCommands, ",") != "capture-now,pause,resume,set-interval,stop" {
		t.Fatalf("default: %d %s", code, body)
	}
	code, body = do("POST", "/api/script-keys", `{"script":"screenshot","allowImages":true}`)
	var b struct{ Key Key }
	_ = json.Unmarshal([]byte(body), &b)
	if code != 201 || !b.Key.AllowImages {
		t.Fatalf("create with images: %d %s", code, body)
	}
	if code, body := do("PATCH", "/api/script-keys/"+a.Key.ID, `{"allowImages":true}`); code != 204 {
		t.Fatalf("patch: %d %s", code, body)
	}
	// A patch that does not mention it leaves it alone.
	if code, _ := do("PATCH", "/api/script-keys/"+a.Key.ID, `{"silenceMinutes":20}`); code != 204 {
		t.Fatal("patch 2")
	}
	keys, _ := ListKeys(e.svc.db)
	for _, k := range keys {
		if k.ID == a.Key.ID && (!k.AllowImages || k.SilenceMin != 20) {
			t.Fatalf("key after patches: %+v", k)
		}
	}
}
