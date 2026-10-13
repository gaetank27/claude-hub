package imgsafe

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pngBytes(w, h int) []byte {
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		im.Set(x, x%h, color.RGBA{200, 10, 10, 255})
	}
	var b bytes.Buffer
	_ = png.Encode(&b, im)
	return b.Bytes()
}

func write(t *testing.T, path string, data []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func testPolicy(t *testing.T) (*Policy, string, string) {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	home := filepath.Join(root, "home")
	p := DefaultPolicy(filepath.Join(root, "projects"), home, filepath.Join(root, "data"))
	_ = os.MkdirAll(filepath.Join(root, "projects"), 0o755)
	p = DefaultPolicy(filepath.Join(root, "projects"), home, filepath.Join(root, "data"))
	return p, filepath.Join(root, "projects"), outside
}

func code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return "?"
}

func TestPrepareOK(t *testing.T) {
	p, proj, _ := testPolicy(t)
	f := write(t, filepath.Join(proj, "vac", "carte.png"), pngBytes(40, 30))
	img, err := p.Prepare(f)
	if err != nil || img.Mime != "image/png" || img.Width != 40 || img.Height != 30 || Format(img.Data) != "png" {
		t.Fatalf("%+v %v", img, err)
	}
}

func TestPrepareResizesAndStripsJPEG(t *testing.T) {
	p, proj, _ := testPolicy(t)
	var b bytes.Buffer
	_ = jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4000, 1000)), nil)
	data := append(b.Bytes(), []byte("TRAILING-PAYLOAD")...)
	img, err := p.Prepare(write(t, filepath.Join(proj, "a", "big.jpg"), data))
	if err != nil || img.Width != 2048 || img.Height != 512 || img.Mime != "image/jpeg" {
		t.Fatalf("%+v %v", img, err)
	}
	if bytes.Contains(img.Data, []byte("TRAILING-PAYLOAD")) {
		t.Fatal("appended payload survived the re-encode")
	}
}

func TestPrepareGIFAndWebPMagic(t *testing.T) {
	p, proj, _ := testPolicy(t)
	var b bytes.Buffer
	pal := []color.Color{color.White, color.Black}
	g := &gif.GIF{Image: []*image.Paletted{image.NewPaletted(image.Rect(0, 0, 8, 8), pal), image.NewPaletted(image.Rect(0, 0, 8, 8), pal)}, Delay: []int{1, 1}}
	_ = gif.EncodeAll(&b, g)
	img, err := p.Prepare(write(t, filepath.Join(proj, "a", "x.gif"), b.Bytes()))
	if err != nil || img.Mime != "image/png" || img.Note == "" {
		t.Fatalf("gif: %+v %v", img, err)
	}
	// A WebP header with garbage inside is refused as unreadable, not passed through.
	bad := append([]byte("RIFF\x10\x00\x00\x00WEBPVP8 "), make([]byte, 20)...)
	if _, err := p.Prepare(write(t, filepath.Join(proj, "a", "x.webp"), bad)); code(err) != "decode" {
		t.Fatalf("webp: %v", err)
	}
}

func TestRefusals(t *testing.T) {
	p, proj, outside := testPolicy(t)
	good := pngBytes(10, 10)
	root := filepath.Dir(proj)
	cases := map[string]struct {
		path string
		want string
	}{
		"svg":        {write(t, filepath.Join(proj, "a", "x.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)), "type"},
		"svg as png": {write(t, filepath.Join(proj, "a", "y.png"), []byte(`<svg onload="x()"/>`)), "type"},
		"text":       {write(t, filepath.Join(proj, "a", "z.png"), []byte("hello world, not an image")), "type"},
		"html":       {write(t, filepath.Join(proj, "a", "h.jpg"), []byte("<html><script>")), "type"},
		"empty":      {write(t, filepath.Join(proj, "a", "e.png"), nil), "empty"},
		"truncated":  {write(t, filepath.Join(proj, "a", "t.png"), good[:len(good)/2]), "decode"},
		"outside":    {write(t, filepath.Join(outside, "a.png"), good), "denied"},
		"etc":        {"/etc/passwd", "denied"},
		"proc":       {"/proc/self/environ", "denied"},
		"hidden dir": {write(t, filepath.Join(proj, ".ssh", "k.png"), good), "denied"},
		"hidden":     {write(t, filepath.Join(proj, "a", ".h.png"), good), "denied"},
		"env":        {write(t, filepath.Join(proj, "a", "secrets", "i.png"), good), "denied"},
		"token name": {write(t, filepath.Join(proj, "a", "api_token.png"), good), "denied"},
		"relative":   {"a/b.png", "path"},
		"missing":    {filepath.Join(proj, "nope.png"), "path"},
		"dir":        {proj, "path"},
		"nul":        {"/x\x00.png", "path"},
		"data dir":   {write(t, filepath.Join(root, "data", "i.png"), good), "denied"},
		"home ssh":   {write(t, filepath.Join(root, "home", ".ssh", "i.png"), good), "denied"},
	}
	for name, c := range cases {
		_, err := p.Prepare(c.path)
		if err == nil || code(err) != c.want {
			t.Errorf("%s: want %s, got %v", name, c.want, err)
		}
	}
}

func TestTraversalAndSymlinks(t *testing.T) {
	p, proj, outside := testPolicy(t)
	good := pngBytes(10, 10)
	secret := write(t, filepath.Join(outside, "photo.png"), good)
	write(t, filepath.Join(proj, "a", "ok.png"), good)
	// ".." that leaves the root once cleaned.
	if _, err := p.Prepare(filepath.Join(proj, "a", "..", "..", "..", filepath.Base(outside), "photo.png")); code(err) != "denied" {
		t.Errorf("traversal: %v", err)
	}
	// Symlink to a file outside the roots.
	if err := os.Symlink(secret, filepath.Join(proj, "a", "link.png")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Prepare(filepath.Join(proj, "a", "link.png")); code(err) != "denied" {
		t.Errorf("file symlink: %v", err)
	}
	// Symlinked directory pointing out.
	if err := os.Symlink(outside, filepath.Join(proj, "a", "dir")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Prepare(filepath.Join(proj, "a", "dir", "photo.png")); code(err) != "denied" {
		t.Errorf("dir symlink: %v", err)
	}
	// Symlink named innocently to a hidden file inside the root: the resolved name counts.
	write(t, filepath.Join(proj, "a", ".git", "x.png"), good)
	_ = os.Symlink(filepath.Join(proj, "a", ".git", "x.png"), filepath.Join(proj, "a", "fine.png"))
	if _, err := p.Prepare(filepath.Join(proj, "a", "fine.png")); code(err) != "denied" {
		t.Errorf("symlink to hidden: %v", err)
	}
	if _, err := p.Prepare(filepath.Join(proj, "a", "ok.png")); err != nil {
		t.Errorf("ok: %v", err)
	}
}

func TestTooLargeAndBomb(t *testing.T) {
	p, proj, _ := testPolicy(t)
	p.MaxBytes = 1000
	big := write(t, filepath.Join(proj, "a", "b.png"), pngBytes(300, 300))
	if _, err := p.Prepare(big); code(err) != "too_large" {
		t.Errorf("size: %v", err)
	}
	p.MaxBytes = DefaultMaxBytes
	p.MaxPixels = 100
	if _, err := p.Prepare(big); code(err) != "too_large" {
		t.Errorf("pixels: %v", err)
	}
}

func TestLimiter(t *testing.T) {
	now := time.Now()
	l := NewLimiter(3)
	l.Now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if err := l.Allow(); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Allow(); code(err) != "rate" {
		t.Fatalf("4th: %v", err)
	}
	now = now.Add(61 * time.Minute)
	if err := l.Allow(); err != nil {
		t.Fatalf("after window: %v", err)
	}
}

func TestGuardVetConsumesOnlyValid(t *testing.T) {
	p, proj, _ := testPolicy(t)
	g := NewGuard(p)
	g.Limiter = NewLimiter(1)
	if _, err := g.Vet("/etc/passwd"); err == nil {
		t.Fatal("want refusal")
	}
	f := write(t, filepath.Join(proj, "a", "ok.png"), pngBytes(5, 5))
	if _, err := g.Vet(f); err != nil {
		t.Fatalf("refused path must not use the quota: %v", err)
	}
	if _, err := g.Vet(f); code(err) != "rate" {
		t.Fatalf("second: %v", err)
	}
	var nilGuard *Guard
	if _, err := nilGuard.Vet(f); err == nil {
		t.Fatal("nil guard must refuse")
	}
}

func TestCleanCaption(t *testing.T) {
	if got := CleanCaption("  a\nb\x00c\td "); got != "a bc d" {
		t.Errorf("got %q", got)
	}
	if got := CleanCaption(strings.Repeat("é", 300)); len([]rune(got)) != 201 {
		t.Errorf("len %d", len([]rune(got)))
	}
}
