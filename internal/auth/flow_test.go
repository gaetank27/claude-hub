package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"system/internal/secretbox"
	"system/internal/store"
)

// newTestAuth wires an Authenticator over a throwaway SQLite store + secretbox,
// the same way main.go does, so the setup→login→2FA flow can be exercised end to
// end through the real HTTP handlers.
func newTestAuth(t *testing.T) *Authenticator {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	box, err := secretbox.Open(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatalf("secretbox.Open: %v", err)
	}
	return New(Config{CookieSecret: []byte("test-cookie-secret"), DB: db, Secrets: box, TOTPIssuer: "Test"})
}

func postJSON(t *testing.T, h http.HandlerFunc, path string, body any, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	buf, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", path, bytes.NewReader(buf))
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func TestSetupAndLoginFlow(t *testing.T) {
	a := newTestAuth(t)

	// 1. Fresh install => setup required.
	{
		r := httptest.NewRequest("GET", "/setup/status", nil)
		w := httptest.NewRecorder()
		a.SetupStatusHandler(w, r)
		var got struct {
			NeedsSetup  bool `json:"needsSetup"`
			OIDCEnabled bool `json:"oidcEnabled"`
		}
		json.Unmarshal(w.Body.Bytes(), &got)
		if !got.NeedsSetup || got.OIDCEnabled {
			t.Fatalf("fresh status = %+v, want needsSetup=true oidcEnabled=false", got)
		}
	}

	// 2. Create the admin and capture the TOTP secret.
	var secret string
	{
		w := postJSON(t, a.SetupAdminHandler, "/setup/admin",
			map[string]string{"username": "admin", "email": "a@b.c", "password": "hunter2hunter2"}, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("setup/admin = %d: %s", w.Code, w.Body.String())
		}
		var got struct{ Secret, QrPng, OtpauthUrl string }
		json.Unmarshal(w.Body.Bytes(), &got)
		if got.Secret == "" || got.QrPng == "" {
			t.Fatalf("setup/admin missing secret/qr: %s", w.Body.String())
		}
		secret = got.Secret
	}

	code := func() string {
		c, err := totp.GenerateCode(secret, time.Now())
		if err != nil {
			t.Fatalf("GenerateCode: %v", err)
		}
		return c
	}

	// 3. Confirm TOTP enrolment with a live code.
	if w := postJSON(t, a.SetupTOTPConfirmHandler, "/setup/totp/confirm",
		map[string]string{"username": "admin", "code": code()}, nil); w.Code != http.StatusOK {
		t.Fatalf("setup/totp/confirm = %d: %s", w.Code, w.Body.String())
	}

	// 4. Finish setup => session cookie issued, status flips.
	{
		w := postJSON(t, a.SetupFinishHandler, "/setup/finish", map[string]string{"username": "admin"}, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("setup/finish = %d: %s", w.Code, w.Body.String())
		}
		if a.SetupComplete() != true {
			t.Fatal("SetupComplete still false after finish")
		}
	}

	// 5. Re-running a setup mutation is now refused.
	if w := postJSON(t, a.SetupAdminHandler, "/setup/admin",
		map[string]string{"username": "evil", "password": "longenough"}, nil); w.Code != http.StatusConflict {
		t.Fatalf("setup/admin after completion = %d, want 409", w.Code)
	}

	// 6. Local login: wrong password rejected.
	if w := postJSON(t, a.LocalLoginHandler, "/auth/local-login",
		map[string]string{"username": "admin", "password": "wrong"}, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("login wrong password = %d, want 401", w.Code)
	}

	// 7. Local login: correct password => TOTP required + pending cookie.
	var pending []*http.Cookie
	{
		w := postJSON(t, a.LocalLoginHandler, "/auth/local-login",
			map[string]string{"username": "admin", "password": "hunter2hunter2"}, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("login = %d: %s", w.Code, w.Body.String())
		}
		var got struct {
			TOTPRequired bool `json:"totpRequired"`
		}
		json.Unmarshal(w.Body.Bytes(), &got)
		if !got.TOTPRequired {
			t.Fatal("login did not require TOTP")
		}
		pending = w.Result().Cookies()
	}

	// 8. TOTP step with a live code => full session cookie.
	var session []*http.Cookie
	{
		w := postJSON(t, a.TOTPVerifyHandler, "/auth/totp", map[string]string{"code": code()}, pending)
		if w.Code != http.StatusOK {
			t.Fatalf("totp verify = %d: %s", w.Code, w.Body.String())
		}
		session = w.Result().Cookies()
	}

	// 9. The session cookie authenticates whoami.
	{
		r := httptest.NewRequest("GET", "/auth/me", nil)
		for _, c := range session {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		a.WhoamiHandler(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("whoami = %d: %s", w.Code, w.Body.String())
		}
	}
}

func TestDeviceRevocation(t *testing.T) {
	a := newTestAuth(t)
	gate := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	hit := func(c *http.Cookie) int {
		r := httptest.NewRequest("GET", "/api/projects", nil)
		r.AddCookie(c)
		w := httptest.NewRecorder()
		gate.ServeHTTP(w, r)
		return w.Code
	}
	signIn := func(ua string) *http.Cookie {
		r := httptest.NewRequest("POST", "/auth/totp", nil)
		r.Header.Set("User-Agent", ua)
		w := httptest.NewRecorder()
		a.setSession(w, r, "u1", "x@y.z", "local", true)
		return w.Result().Cookies()[0]
	}
	laptop, phone := signIn("laptop"), signIn("phone")
	if hit(laptop) != http.StatusOK || hit(phone) != http.StatusOK {
		t.Fatal("fresh sign-ins rejected")
	}

	// The list flags the caller's own device.
	r := httptest.NewRequest("GET", "/api/devices", nil)
	r.AddCookie(laptop)
	w := httptest.NewRecorder()
	a.DevicesHandler(w, r)
	var devices []deviceDTO
	json.Unmarshal(w.Body.Bytes(), &devices)
	if len(devices) != 2 {
		t.Fatalf("want 2 devices, got %d", len(devices))
	}
	var phoneID string
	for _, d := range devices {
		if d.Current != (d.UserAgent == "laptop") || d.ExpiresAt != nil {
			t.Fatalf("unexpected device %+v", d)
		}
		if d.UserAgent == "phone" {
			phoneID = d.ID
		}
	}

	// Revoking the phone kills its cookie at once and leaves the laptop alone.
	r = httptest.NewRequest("DELETE", "/api/devices/"+phoneID, nil)
	r.SetPathValue("id", phoneID)
	w = httptest.NewRecorder()
	a.RevokeDeviceHandler(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d", w.Code)
	}
	if hit(phone) != http.StatusUnauthorized || hit(laptop) != http.StatusOK {
		t.Fatal("revocation did not apply to exactly the phone")
	}

	// Signing out ends the session server-side, not just in the browser.
	r = httptest.NewRequest("POST", "/auth/logout", nil)
	r.AddCookie(laptop)
	a.LogoutHandler(httptest.NewRecorder(), r)
	if hit(laptop) != http.StatusUnauthorized {
		t.Fatal("cookie still valid after logout")
	}

	// A never-expiring cookie with no server-side row cannot be revoked, so it
	// is refused; short-lived tooling cookies still work.
	forever := &http.Cookie{Name: sessionCookie, Value: a.seal(sessionClaims{Sub: "u1", Forever: true})}
	minted := &http.Cookie{Name: sessionCookie, Value: MintSession([]byte("test-cookie-secret"), "docgen", "d@x", time.Hour)}
	if hit(forever) != http.StatusUnauthorized || hit(minted) != http.StatusOK {
		t.Fatal("row-less cookie policy broken")
	}
}
