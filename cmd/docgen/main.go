// Command docgen drives a headless Chromium (via chromedp, pure Go, no cgo) to
// document Claude Hub: it captures screenshots of the live UI and compiles the
// in-app Help section into a single PDF manual. It is an on-demand tool, never
// invoked by the always-on backend, so the running service stays lightweight.
//
// Typical flow (see Makefile target `docs`):
//
//	docgen -mode screenshots   # capture the key pages into web/public/help/screenshots
//	(cd web && npm run build)  # re-embed the fresh PNGs into the binary's assets
//	docgen -mode pdf           # print /aide?print to dist/claude-hub-manual.pdf
//
// The screenshots must exist before `npm run build` so they are embedded and
// served; the PDF is produced after the build so the printed Help page shows
// the fresh images.
//
// Auth: docgen targets the loopback instance (127.0.0.1:8787), where OIDC is
// off, so no login flow is needed.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"system/internal/auth"
)

// shot maps a UI route to the screenshot filename referenced from
// web/src/lib/help.ts (SHOTS + "/<file>").
type shot struct {
	route string
	file  string
}

var shots = []shot{
	{"/projets", "dashboard.png"},
	{"/assistant", "assistant.png"},
	{"/sandbox", "sandbox.png"},
	{"/monitoring", "monitoring.png"},
	{"/outils", "outils.png"},
	{"/skills", "skills.png"},
	{"/rag", "rag.png"},
	{"/configuration", "configuration.png"},
	{"/archivage", "archivage.png"},
	{"/tasks", "taches.png"},
	{"/scripts", "scripts.png"},
}

func main() {
	base := flag.String("base", "http://127.0.0.1:8787", "base URL of the running Claude Hub instance")
	shotsDir := flag.String("shots-dir", "web/public/help/screenshots", "directory to write screenshots into (a per-language subdirectory is appended)")
	pdfPath := flag.String("pdf", "dist/claude-hub-manual.pdf", "output path for the compiled PDF manual (the language code is inserted before the extension)")
	mode := flag.String("mode", "all", "what to generate: screenshots | pdf | all")
	lang := flag.String("lang", "all", "UI language(s) to render: en | fr | all")
	chromium := flag.String("chromium", envOr("SYSTEM_CHROMIUM_BIN", "/usr/bin/chromium"), "path to the Chromium/Chrome binary")
	settle := flag.Duration("settle", 2*time.Second, "extra wait after load for async data to render")
	// When the target instance has OIDC enabled, forge a session cookie from the
	// same HMAC secret instead of running the interactive login flow. Empty
	// secret => assume auth is off (loopback dev).
	secret := flag.String("session-secret", os.Getenv("SYSTEM_SESSION_SECRET"), "HMAC secret to forge a session cookie (SYSTEM_SESSION_SECRET); empty if auth is off")
	// The default deployment auto-generates a 48-byte *binary* key at
	// <DataDir>/session.key (SYSTEM_SESSION_SECRET unset). Such a key can contain
	// NUL and non-UTF8 bytes, so it cannot be passed through a string flag/env.
	// This flag reads the raw key bytes from that file instead.
	secretFile := flag.String("session-secret-file", os.Getenv("SYSTEM_SESSION_SECRET_FILE"), "file to read the raw HMAC key from (e.g. <DataDir>/session.key); overrides -session-secret")
	email := flag.String("email", "docgen@claude-hub.local", "identity stamped into the forged session cookie")
	flag.Parse()

	// Resolve the cookie-signing key: prefer the raw key file (binary-safe),
	// falling back to the string secret. Empty => assume auth is off.
	secretBytes := []byte(*secret)
	if *secretFile != "" {
		b, err := os.ReadFile(*secretFile)
		if err != nil {
			log.Fatalf("read session-secret-file %q: %v", *secretFile, err)
		}
		secretBytes = b
	}

	if *mode != "screenshots" && *mode != "pdf" && *mode != "all" {
		log.Fatalf("invalid -mode %q (want screenshots|pdf|all)", *mode)
	}
	langs, err := resolveLangs(*lang)
	if err != nil {
		log.Fatal(err)
	}

	// Allocator pinned to the system Chromium. NoSandbox is required when running
	// as a non-root user inside a container without user namespaces.
	allocOpts := append([]chromedp.ExecAllocatorOption{},
		chromedp.DefaultExecAllocatorOptions[:]...,
	)
	allocOpts = append(allocOpts,
		chromedp.ExecPath(*chromium),
		chromedp.NoSandbox,
		chromedp.Flag("hide-scrollbars", true),
		chromedp.WindowSize(1280, 900),
	)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(allocCtx, chromedp.WithLogf(log.Printf))
	defer cancel()
	ctx, cancelTimeout := context.WithTimeout(ctx, 3*time.Minute)
	defer cancelTimeout()

	if len(secretBytes) > 0 {
		if err := installSession(ctx, *base, secretBytes, *email); err != nil {
			log.Fatalf("auth: %v", err)
		}
		log.Printf("auth: installed forged session cookie for %s", *email)
	}

	if *mode == "screenshots" || *mode == "all" {
		for _, l := range langs {
			dir := filepath.Join(*shotsDir, l)
			if err := captureScreenshots(ctx, *base, dir, l, *settle); err != nil {
				log.Fatalf("screenshots (%s): %v", l, err)
			}
		}
	}
	if *mode == "pdf" || *mode == "all" {
		for _, l := range langs {
			if err := compilePDF(ctx, *base, langPath(*pdfPath, l), l, *settle); err != nil {
				log.Fatalf("pdf (%s): %v", l, err)
			}
		}
	}
}

// resolveLangs expands the -lang flag into the concrete list of language codes
// to render. "all" yields both supported languages.
func resolveLangs(lang string) ([]string, error) {
	switch lang {
	case "all":
		return []string{"en", "fr"}, nil
	case "en", "fr":
		return []string{lang}, nil
	default:
		return nil, fmt.Errorf("invalid -lang %q (want en|fr|all)", lang)
	}
}

// langPath inserts a language code before the file extension, e.g.
// dist/claude-hub-manual.pdf + "fr" -> dist/claude-hub-manual-fr.pdf.
func langPath(p, lang string) string {
	ext := filepath.Ext(p)
	return p[:len(p)-len(ext)] + "-" + lang + ext
}

// setLanguage forces the SPA's active language by writing the same localStorage
// key the i18next detector reads (claudehub.lang), then leaves the browser on
// the origin so a subsequent Navigate reloads with that language applied.
func setLanguage(ctx context.Context, base, lang string) error {
	return chromedp.Run(ctx,
		chromedp.Navigate(base),
		chromedp.Evaluate(fmt.Sprintf(`localStorage.setItem("claudehub.lang", %q)`, lang), nil),
	)
}

// installSession forges a valid session cookie from the backend's HMAC secret
// and sets it in the browser, so docgen can reach the authenticated UI without
// the interactive OIDC flow. The cookie is set non-secure so it rides plain HTTP
// to the loopback/LAN bind (the middleware validates the HMAC, not the Secure
// attribute).
func installSession(ctx context.Context, base string, secret []byte, email string) error {
	u, err := url.Parse(base)
	if err != nil {
		return fmt.Errorf("parse base %q: %w", base, err)
	}
	value := auth.MintSession(secret, "docgen", email, time.Hour)
	return chromedp.Run(ctx,
		network.Enable(),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return network.SetCookie(auth.SessionCookieName, value).
				WithDomain(u.Hostname()).
				WithPath("/").
				WithHTTPOnly(true).
				Do(ctx)
		}),
	)
}

func captureScreenshots(ctx context.Context, base, dir, lang string, settle time.Duration) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Pin the UI language before the first navigation; localStorage persists
	// across the per-shot reloads on the same origin.
	if err := setLanguage(ctx, base, lang); err != nil {
		return fmt.Errorf("set language %q: %w", lang, err)
	}
	for _, s := range shots {
		url := base + s.route
		var buf []byte
		log.Printf("capturing %s -> %s", url, s.file)
		err := chromedp.Run(ctx,
			emulation.SetDeviceMetricsOverride(1280, 900, 1.5, false),
			chromedp.Navigate(url),
			chromedp.WaitVisible("body", chromedp.ByQuery),
			chromedp.Sleep(settle),
			chromedp.FullScreenshot(&buf, 90),
		)
		if err != nil {
			return fmt.Errorf("capture %s: %w", url, err)
		}
		if err := os.WriteFile(filepath.Join(dir, s.file), buf, 0o644); err != nil {
			return err
		}
	}
	log.Printf("wrote %d %s screenshots to %s", len(shots), lang, dir)
	return nil
}

func compilePDF(ctx context.Context, base, out, lang string, settle time.Duration) error {
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	url := base + "/aide?print"
	log.Printf("compiling %s PDF from %s", lang, url)

	var pdf []byte
	err := chromedp.Run(ctx,
		// Force the light theme for a clean, printable white manual regardless
		// of the persisted UI theme, and pin the requested language. The SPA
		// reads claudehub.theme / claudehub.lang from localStorage on load, so
		// set them on the origin then navigate.
		chromedp.Navigate(base),
		chromedp.Evaluate(`localStorage.setItem("claudehub.theme","light")`, nil),
		chromedp.Evaluate(fmt.Sprintf(`localStorage.setItem("claudehub.lang", %q)`, lang), nil),
		chromedp.Navigate(url),
		chromedp.WaitVisible(".print-manual", chromedp.ByQuery),
		chromedp.Sleep(settle),
		// Ensure every screenshot is decoded before printing.
		waitImagesLoaded(),
		chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			pdf, _, err = page.PrintToPDF().
				WithPrintBackground(true).
				WithPaperWidth(8.27).   // A4 width in inches
				WithPaperHeight(11.69). // A4 height in inches
				WithMarginTop(0.5).
				WithMarginBottom(0.5).
				WithMarginLeft(0.5).
				WithMarginRight(0.5).
				Do(ctx)
			return err
		}),
	)
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, pdf, 0o644); err != nil {
		return err
	}
	log.Printf("wrote PDF (%d bytes) to %s", len(pdf), out)
	return nil
}

// waitImagesLoaded polls until all <img> on the page report complete, so
// PrintToPDF doesn't capture half-decoded screenshots.
func waitImagesLoaded() chromedp.Action {
	return chromedp.Poll(
		`Array.from(document.images).every(img => img.complete)`,
		nil,
		chromedp.WithPollingTimeout(15*time.Second),
	)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
