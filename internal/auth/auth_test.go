package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSealOpenRoundTrip(t *testing.T) {
	a := &Authenticator{cfg: Config{CookieSecret: []byte("test-secret")}}

	sealed := a.seal(sessionClaims{Sub: "u1", Email: "x@y.z", Exp: 123})
	var got sessionClaims
	if !a.open(sealed, &got) {
		t.Fatal("open rejected a value it sealed")
	}
	if got.Sub != "u1" || got.Email != "x@y.z" || got.Exp != 123 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestOpenRejectsTamperAndForeignKey(t *testing.T) {
	a := &Authenticator{cfg: Config{CookieSecret: []byte("test-secret")}}
	sealed := a.seal(sessionClaims{Sub: "u1"})

	var sink sessionClaims
	if a.open(sealed+"x", &sink) {
		t.Fatal("open accepted a tampered tag")
	}
	// A cookie signed with a different key must not verify.
	other := &Authenticator{cfg: Config{CookieSecret: []byte("other-secret")}}
	if a.open(other.seal(sessionClaims{Sub: "u1"}), &sink) {
		t.Fatal("open accepted a value signed with a foreign key")
	}
}

func TestSession(t *testing.T) {
	a := &Authenticator{cfg: Config{CookieSecret: []byte("test-secret")}}
	withCookie := func(value string) *http.Request {
		r, _ := http.NewRequest("GET", "/auth/me", nil)
		if value != "" {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: value})
		}
		return r
	}

	// Valid, unexpired cookie => claims and ok.
	valid := a.seal(sessionClaims{Sub: "u1", Email: "x@y.z", Exp: time.Now().Add(time.Hour).Unix()})
	if sc, ok := a.session(withCookie(valid)); !ok || sc.Email != "x@y.z" {
		t.Fatalf("valid cookie rejected: ok=%v claims=%+v", ok, sc)
	}
	// Expired cookie => not ok.
	expired := a.seal(sessionClaims{Sub: "u1", Exp: time.Now().Add(-time.Hour).Unix()})
	if _, ok := a.session(withCookie(expired)); ok {
		t.Fatal("expired cookie accepted")
	}
	// Tampered cookie => not ok.
	if _, ok := a.session(withCookie(valid + "x")); ok {
		t.Fatal("tampered cookie accepted")
	}
	// Absent cookie => not ok.
	if _, ok := a.session(withCookie("")); ok {
		t.Fatal("missing cookie accepted")
	}
}

func TestAllowed(t *testing.T) {
	// No allowlist => any authenticated user passes.
	if !allowed(OIDCSettings{}, "anyone", "any@one.z") {
		t.Fatal("empty allowlist should allow any authenticated user")
	}

	s := OIDCSettings{
		AllowedSubs:   []string{"sub-1"},
		AllowedEmails: []string{"ok@example.com"},
	}
	cases := []struct {
		sub, email string
		want       bool
	}{
		{"sub-1", "", true},                   // sub match
		{"", "ok@example.com", true},          // email match
		{"", "OK@EXAMPLE.COM", true},          // email is case-insensitive
		{"nope", "nobody@example.com", false}, // no match
	}
	for _, c := range cases {
		if got := allowed(s, c.sub, c.email); got != c.want {
			t.Errorf("allowed(%q,%q)=%v want %v", c.sub, c.email, got, c.want)
		}
	}
}

func TestSetSessionLifetime(t *testing.T) {
	a := &Authenticator{cfg: Config{CookieSecret: []byte("test-secret")}}

	rec := httptest.NewRecorder()
	a.setSession(rec, httptest.NewRequest("POST", "/", nil), "u1", "x@y.z", "local", false)
	c := rec.Result().Cookies()[0]
	var sc sessionClaims
	if !a.open(c.Value, &sc) || sc.Forever {
		t.Fatalf("default session: bad claims %+v", sc)
	}
	if left := time.Until(time.Unix(sc.Exp, 0)); c.MaxAge != int(sessionTTL.Seconds()) || left > sessionTTL || left < sessionTTL-time.Minute {
		t.Fatalf("default session: MaxAge %d, expires in %v, want %v", c.MaxAge, left, sessionTTL)
	}

	rec = httptest.NewRecorder()
	a.setSession(rec, httptest.NewRequest("POST", "/", nil), "u1", "x@y.z", "local", true)
	c = rec.Result().Cookies()[0]
	if !a.open(c.Value, &sc) || !sc.Forever || c.MaxAge != int(rememberCookieTTL.Seconds()) {
		t.Fatalf("remembered session: MaxAge %d, claims %+v", c.MaxAge, sc)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(c)
	if _, ok := a.session(r); !ok {
		t.Fatal("remembered session rejected")
	}

	// Without the Forever mark, a missing/past expiry is still a dead session.
	r = httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: a.seal(sessionClaims{Sub: "u1"})})
	if _, ok := a.session(r); ok {
		t.Fatal("session without expiry accepted")
	}
}
