package tools

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	proton "github.com/ProtonMail/go-proton-api"
	"github.com/ProtonMail/go-proton-api/server"
)

// hvFront sits in front of the library's mock API and answers POST /auth/v4
// with Code=9001 (+ HV details) until the request carries the CAPTCHA token.
type hvFront struct {
	mu        sync.Mutex
	auths     int // POST /auth/v4 received
	infos     int
	withToken int
	lastType  string
	details   string // JSON for "Details"; "" = answer without details
	alwaysHV  bool   // refuse even with the token
}

func newHVFront(t *testing.T, details string) (*hvFront, *server.Server) {
	t.Helper()
	s := server.New(server.WithTLS(false))
	t.Cleanup(s.Close)
	if _, _, err := s.CreateUser("alice", []byte("pw")); err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(s.GetHostURL())
	proxy := httputil.NewSingleHostReverseProxy(target)
	f := &hvFront{details: details}
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/auth/v4/info") {
			f.mu.Lock()
			f.infos++
			f.mu.Unlock()
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/auth/v4") {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			f.mu.Lock()
			f.auths++
			tok := r.Header.Get("x-pm-human-verification-token")
			if tok != "" {
				f.withToken++
				f.lastType = r.Header.Get("x-pm-human-verification-token-type")
			}
			refuse := tok == "" || f.alwaysHV
			f.mu.Unlock()
			if refuse {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnprocessableEntity)
				d := ""
				if f.details != "" {
					d = `,"Details":` + f.details
				}
				_, _ = w.Write([]byte(`{"Code":9001,"Error":"For security reasons, please complete CAPTCHA"` + d + `}`))
				return
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)
	protonHostURL = front.URL
	t.Cleanup(func() { protonHostURL = "https://mail.proton.me/api" })
	return f, s
}

const hvDetails = `{"HumanVerificationMethods":["captcha","email"],"HumanVerificationToken":"tok-SECRET+/=1"}`

func resetHV(t *testing.T) {
	t.Helper()
	protonHVDrop("tool")
	protonDropPending("tool", false)
	t.Cleanup(func() { protonHVDrop("tool"); protonDropPending("tool", false) })
}

func TestCaptchaFlowSendsTokenOnceAndConnects(t *testing.T) {
	resetHV(t)
	f, _ := newHVFront(t, hvDetails)
	live := fakeConfig(t, map[string]string{"username": "alice"})
	ctx := context.Background()

	res, err := ProtonConnect(ctx, "tool", snapshot(live), ProtonConnectInput{Username: "alice", Password: "pw"})
	if err != nil || res.Status != "needs_captcha" {
		t.Fatalf("first step: %+v %v", res, err)
	}
	if want := "https://verify.proton.me/?methods=captcha,email&token=tok-SECRET%2B%2F%3D1"; res.URL != want {
		t.Fatalf("link = %q, want %q", res.URL, want)
	}
	if !verifyLinkOK(res.URL) || res.ExpiresAt == nil {
		t.Fatal("link host / expiry")
	}
	if _, paused := protonPausedUntil(live, time.Now()); paused || live[pkPauseCount] != "" {
		t.Fatal("a normal CAPTCHA wait must not start the pause")
	}
	for k, v := range live {
		if strings.Contains(v, "pw") || strings.Contains(v, "SECRET") {
			t.Fatalf("secret leaked into config %s", k)
		}
	}
	// Submitting again without confirmation re-shows the link, no network.
	before := f.auths
	again, err := ProtonConnect(ctx, "tool", snapshot(live), ProtonConnectInput{})
	if err != nil || again.Status != "needs_captcha" || again.URL != res.URL || f.auths != before {
		t.Fatalf("re-show: %+v %v auths %d->%d", again, err, before, f.auths)
	}

	// Confirmation: exactly one more auth, carrying the token, type captcha.
	res, err = ProtonConnect(ctx, "tool", snapshot(live), ProtonConnectInput{CaptchaDone: true})
	if err != nil || res.Status != "connected" {
		t.Fatalf("confirm: %+v %v", res, err)
	}
	if f.auths != before+1 || f.withToken != 1 || f.lastType != "captcha" {
		t.Fatalf("auths=%d withToken=%d type=%q", f.auths, f.withToken, f.lastType)
	}
	if live[pkUID] == "" || live[pkRefresh] == "" || live[pkKeyPass] == "" {
		t.Fatal("session not persisted")
	}
	if protonHVPeek("tool") != nil {
		t.Fatal("HV state kept after success")
	}
}

func TestCaptchaSecondRefusalPauses(t *testing.T) {
	resetHV(t)
	f, _ := newHVFront(t, hvDetails)
	f.alwaysHV = true
	live := fakeConfig(t, map[string]string{"username": "alice"})
	ctx := context.Background()
	if res, err := ProtonConnect(ctx, "tool", snapshot(live), ProtonConnectInput{Username: "alice", Password: "pw"}); err != nil || res.Status != "needs_captcha" {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := ProtonConnect(ctx, "tool", snapshot(live), ProtonConnectInput{CaptchaDone: true}); err == nil {
		t.Fatal("second 9001 accepted")
	}
	if _, paused := protonPausedUntil(live, time.Now()); !paused {
		t.Fatal("a real refusal after the CAPTCHA must pause")
	}
	if f.auths != 2 {
		t.Fatalf("auth requests = %d, want 2 (no automatic retry)", f.auths)
	}
	if protonHVPeek("tool") != nil {
		t.Fatal("HV state kept after refusal")
	}
}

func TestCaptchaCancelWipesAndSendsNothing(t *testing.T) {
	resetHV(t)
	f, _ := newHVFront(t, hvDetails)
	live := fakeConfig(t, map[string]string{"username": "alice"})
	ctx := context.Background()
	if _, err := ProtonConnect(ctx, "tool", snapshot(live), ProtonConnectInput{Username: "alice", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	h := protonHVPeek("tool")
	pw := h.password
	if !ProtonConnectCancel("tool") {
		t.Fatal("cancel reported nothing pending")
	}
	if !bytes.Equal(pw, make([]byte, len(pw))) || h.token != "" {
		t.Fatal("secrets not wiped on cancel")
	}
	auths := f.auths
	if _, err := ProtonConnect(ctx, "tool", snapshot(live), ProtonConnectInput{CaptchaDone: true}); err == nil {
		t.Fatal("confirmation accepted after cancel")
	}
	if f.auths != auths {
		t.Fatal("request sent after cancel")
	}
	if _, paused := protonPausedUntil(live, time.Now()); paused {
		t.Fatal("cancel must not pause")
	}
}

func TestCaptchaExpires(t *testing.T) {
	resetHV(t)
	newHVFront(t, hvDetails)
	old := protonHVTTL
	protonHVTTL = 50 * time.Millisecond
	defer func() { protonHVTTL = old }()
	live := fakeConfig(t, map[string]string{"username": "alice"})
	if _, err := ProtonConnect(context.Background(), "tool", snapshot(live), ProtonConnectInput{Username: "alice", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	h := protonHVPeek("tool")
	time.Sleep(300 * time.Millisecond)
	if protonHVPeek("tool") != nil || h.token != "" {
		t.Fatal("wait not expired / secrets kept")
	}
}

func TestCaptchaRespectsExistingPause(t *testing.T) {
	resetHV(t)
	f, _ := newHVFront(t, hvDetails)
	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	live := fakeConfig(t, map[string]string{"username": "alice", pkPauseUntil: until, pkPauseCount: "1", pkLastError: "9001"})
	if _, err := ProtonConnect(context.Background(), "tool", snapshot(live), ProtonConnectInput{Username: "alice", Password: "pw"}); err == nil || !strings.Contains(err.Error(), "pause") {
		t.Fatalf("connect during pause: %v", err)
	}
	if f.auths != 0 || f.infos != 0 {
		t.Fatalf("network call during the pause (%d auth, %d info)", f.auths, f.infos)
	}
}

func TestCaptchaWithoutUsableTokenExplainsAndDoesNotPause(t *testing.T) {
	for name, details := range map[string]string{
		"none":      "",
		"no token":  `{"HumanVerificationMethods":["captcha"]}`,
		"no method": `{"HumanVerificationMethods":["sms"],"HumanVerificationToken":"t"}`,
		"weird":     `{"HumanVerificationMethods":["cap tcha"],"HumanVerificationToken":"t"}`,
	} {
		t.Run(name, func(t *testing.T) {
			resetHV(t)
			newHVFront(t, details)
			live := fakeConfig(t, map[string]string{"username": "alice"})
			_, err := ProtonConnect(context.Background(), "tool", snapshot(live), ProtonConnectInput{Username: "alice", Password: "pw"})
			if err == nil || !strings.Contains(err.Error(), "lien exploitable") {
				t.Fatalf("err = %v", err)
			}
			if _, paused := protonPausedUntil(live, time.Now()); paused {
				t.Fatal("must not pause")
			}
			if protonHVPeek("tool") != nil {
				t.Fatal("state kept")
			}
		})
	}
}

func TestVerifyLinkHostExact(t *testing.T) {
	if _, err := protonVerifyLink([]string{"captcha"}, ""); err == nil {
		t.Fatal("empty token accepted")
	}
	l, err := protonVerifyLink([]string{"Captcha"}, "a@evil.com/x")
	if err != nil || !verifyLinkOK(l) || strings.Contains(l, "@") {
		t.Fatalf("%q %v", l, err)
	}
}

func TestScrubMasksToken(t *testing.T) {
	got := protonScrubAPIError(&protonAPIErr9001)
	if strings.Contains(got, "SECRET") || !strings.Contains(got, "***") {
		t.Fatal(got)
	}
}

var protonAPIErr9001 = func() proton.APIError {
	return proton.APIError{Status: 422, Code: 9001, Message: "m", Details: []byte(hvDetails)}
}()
