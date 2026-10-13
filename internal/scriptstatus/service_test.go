package scriptstatus

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"system/internal/store"
)

type env struct {
	t   *testing.T
	svc *Service
	srv *httptest.Server
	now time.Time
	mu  sync.Mutex
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e := &env{t: t, now: time.Date(2026, 10, 11, 12, 0, 0, 0, time.Local)}
	e.svc = New(db, dir)
	e.svc.now = func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now }
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/script-status/{script}", e.svc.Receive)
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)
	return e
}

func (e *env) advance(d time.Duration) { e.mu.Lock(); e.now = e.now.Add(d); e.mu.Unlock() }

func (e *env) post(script, key, body, xff string) (int, string) {
	req, _ := http.NewRequest("POST", e.srv.URL+"/webhook/script-status/"+script, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set(KeyHeader, key)
	}
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, sb.String()
}

const okBody = `{"state":"ok","summary":"2 jobs","data":{"jobs":2}}`

func TestAcceptAndRead(t *testing.T) {
	e := newEnv(t)
	_, secret, err := CreateKey(e.svc.db, "demo", "t", nil, 0, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := e.post("demo", secret, okBody, ""); code != 200 {
		t.Fatalf("code %d", code)
	}
	rep, ok, _ := e.svc.Get("demo")
	if !ok || rep.State != "ok" || rep.Summary != "2 jobs" || rep.Effective != "ok" {
		t.Fatalf("%+v", rep)
	}
	// the secret is not stored in clear
	var n int
	e.svc.db.QueryRow(`SELECT count(*) FROM script_keys WHERE key_hash=?`, secret).Scan(&n)
	if n != 0 {
		t.Fatal("secret stored in clear")
	}
}

func TestHistoryBounded(t *testing.T) {
	e := newEnv(t)
	_, secret, _ := CreateKey(e.svc.db, "demo", "", nil, 0, false, nil)
	for i := 0; i < historyKeep+10; i++ {
		e.advance(time.Second)
		e.svc.mu.Lock()
		delete(e.svc.rate, "127.0.0.1")
		delete(e.svc.srate, "demo")
		e.svc.mu.Unlock()
		if code, _ := e.post("demo", secret, okBody, ""); code != 200 {
			t.Fatalf("code %d at %d", code, i)
		}
	}
	h, _ := e.svc.History("demo", 100)
	if len(h) != historyKeep {
		t.Fatalf("history %d", len(h))
	}
}

func TestAuthFailuresGenericAndLogged(t *testing.T) {
	e := newEnv(t)
	_, secret, _ := CreateKey(e.svc.db, "demo", "", nil, 0, false, nil)
	_, other, _ := CreateKey(e.svc.db, "other", "", nil, 0, false, nil)
	cases := []struct{ script, key string }{
		{"demo", ""}, {"demo", "hsk_wrong"}, {"unknown", secret}, {"demo", other}, {"BAD NAME", secret},
	}
	var bodies []string
	for i, c := range cases {
		code, body := e.post(strings.ReplaceAll(c.script, " ", "%20"), c.key, okBody, "198.51.100."+string(rune('1'+i)))
		if code != 401 {
			t.Fatalf("case %v: %d", c, code)
		}
		bodies = append(bodies, body)
	}
	for _, b := range bodies {
		if b != bodies[0] {
			t.Fatalf("error bodies differ: %q vs %q", b, bodies[0])
		}
	}
	data, _ := os.ReadFile(e.svc.LogPath())
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != len(cases) {
		t.Fatalf("log lines %d: %s", len(lines), data)
	}
	for _, l := range lines {
		if !strings.Contains(l, "script-status AUTH-FAIL ip=198.51.100.") {
			t.Fatalf("bad log line %q", l)
		}
	}
	if strings.Contains(string(data), "BAD") || strings.Contains(string(data), "wrong") {
		t.Fatal("attacker-controlled text in log")
	}
}

func TestLogInjectionImpossible(t *testing.T) {
	e := newEnv(t)
	// A script name carrying a newline + fake line must be logged as "-".
	req, _ := http.NewRequest("POST", e.srv.URL+"/webhook/script-status/x%0A2026-01-01%2000:00:00%20script-status%20AUTH-FAIL%20ip=9.9.9.9", strings.NewReader("{}"))
	req.Header.Set(KeyHeader, "k")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	data, _ := os.ReadFile(e.svc.LogPath())
	if strings.Contains(string(data), "9.9.9.9") || strings.Count(string(data), "\n") != 1 {
		t.Fatalf("log injection: %q", data)
	}
	// Garbage X-Forwarded-For is ignored, never logged.
	e.post("demo", "k", "{}", "1.1.1.1 AUTH-FAIL ip=8.8.8.8")
	data, _ = os.ReadFile(e.svc.LogPath())
	if strings.Contains(string(data), "8.8.8.8") {
		t.Fatalf("xff injection: %q", data)
	}
}

func TestIPAllowlist(t *testing.T) {
	e := newEnv(t)
	_, secret, err := CreateKey(e.svc.db, "demo", "", []string{"203.0.113.0/24", "2001:db8::1"}, 0, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := e.post("demo", secret, okBody, "203.0.113.77"); code != 200 {
		t.Fatalf("allowed ip: %d", code)
	}
	if code, _ := e.post("demo", secret, okBody, "198.51.100.5"); code != 401 {
		t.Fatalf("forbidden ip: %d", code)
	}
	data, _ := os.ReadFile(e.svc.LogPath())
	if !strings.Contains(string(data), "reason=ip-not-allowed") {
		t.Fatalf("log: %s", data)
	}
	if _, _, err := CreateKey(e.svc.db, "demo", "", []string{"not-an-ip"}, 0, false, nil); err == nil {
		t.Fatal("invalid ip accepted")
	}
}

func TestBanAfterFailures(t *testing.T) {
	e := newEnv(t)
	_, secret, _ := CreateKey(e.svc.db, "demo", "", nil, 0, false, nil)
	for i := 0; i < failMax; i++ {
		if code, _ := e.post("demo", "bad", okBody, "198.51.100.9"); code != 401 {
			t.Fatalf("fail %d: %d", i, code)
		}
	}
	// even the right key is refused from the banned IP …
	if code, _ := e.post("demo", secret, okBody, "198.51.100.9"); code != 429 {
		t.Fatalf("banned: %d", code)
	}
	// … while another IP is unaffected
	if code, _ := e.post("demo", secret, okBody, "198.51.100.10"); code != 200 {
		t.Fatalf("other ip: %d", code)
	}
	// ban expires
	e.advance(banFor + time.Second)
	if code, _ := e.post("demo", secret, okBody, "198.51.100.9"); code != 200 {
		t.Fatalf("after ban: %d", code)
	}
	data, _ := os.ReadFile(e.svc.LogPath())
	if strings.Count(string(data), "BLOCKED") != 1 {
		t.Fatalf("blocked lines: %s", data)
	}
}

func TestRateLimit(t *testing.T) {
	e := newEnv(t)
	_, secret, _ := CreateKey(e.svc.db, "demo", "", nil, 0, false, nil)
	got429 := false
	for i := 0; i < scriptRate+5; i++ {
		if code, _ := e.post("demo", secret, okBody, "198.51.100.20"); code == 429 {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatal("no rate limiting")
	}
}

func TestValidation(t *testing.T) {
	e := newEnv(t)
	_, secret, _ := CreateKey(e.svc.db, "demo", "", nil, 0, false, nil)
	bad := map[string]string{
		"not json":       `nope`,
		"unknown field":  `{"state":"ok","summary":"x","evil":1}`,
		"muet from user": `{"state":"muet","summary":"x"}`,
		"bad state":      `{"state":"OK","summary":"x"}`,
		"data array":     `{"state":"ok","summary":"x","data":[1]}`,
		"data string":    `{"state":"ok","summary":"x","data":"s"}`,
		"too deep":       `{"state":"ok","summary":"x","data":` + strings.Repeat(`{"a":`, 12) + `1` + strings.Repeat(`}`, 12) + `}`,
		"summary long":   `{"state":"ok","summary":"` + strings.Repeat("a", maxSummary+1) + `"}`,
		"trailing data":  `{"state":"ok","summary":"x"}{"a":1}`,
		"bad ts":         `{"state":"ok","summary":"x","ts":"yesterday"}`,
		"data too large": `{"state":"ok","summary":"x","data":{"a":"` + strings.Repeat("a", maxData) + `"}}`,
	}
	for name, body := range bad {
		if code, resp := e.post("demo", secret, body, ""); code != 400 {
			t.Errorf("%s: %d %s", name, code, resp)
		}
	}
	if code, _ := e.post("demo", secret, `{"state":"ok","summary":"`+strings.Repeat("a", maxBody)+`"}`, ""); code != 413 {
		t.Errorf("oversize body: %d", code)
	}
	// control chars stripped
	if code, _ := e.post("demo", secret, `{"state":"warn","summary":"a\u0000b\nc‮d","ts":"2026-10-11T10:00:00Z"}`, ""); code != 200 {
		t.Fatalf("code %d", code)
	}
	rep, _, _ := e.svc.Get("demo")
	if rep.Summary != "ab cd" {
		t.Errorf("summary %q", rep.Summary)
	}
	// wrong content type
	req, _ := http.NewRequest("POST", e.srv.URL+"/webhook/script-status/demo", strings.NewReader(okBody))
	req.Header.Set(KeyHeader, secret)
	req.Header.Set("Content-Type", "text/plain")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 415 {
		t.Errorf("content-type: %d", resp.StatusCode)
	}
}

func TestSilenceAndWake(t *testing.T) {
	e := newEnv(t)
	_, secret, _ := CreateKey(e.svc.db, "demo", "", nil, 10, true, nil)
	var mu sync.Mutex
	var wakes []string
	e.svc.SetWake(func(script, state string) error {
		mu.Lock()
		defer mu.Unlock()
		wakes = append(wakes, script+":"+state)
		return nil
	})
	e.post("demo", secret, okBody, "")
	time.Sleep(50 * time.Millisecond) // the post-receive CheckWake goroutine
	e.advance(9 * time.Minute)
	if rep, _, _ := e.svc.Get("demo"); rep.Effective != "ok" {
		t.Fatalf("9 min: %s", rep.Effective)
	}
	e.advance(2 * time.Minute)
	rep, _, _ := e.svc.Get("demo")
	if rep.Effective != "muet" || rep.State != "ok" {
		t.Fatalf("11 min: %+v", rep)
	}
	e.svc.CheckWake("demo")
	e.svc.CheckWake("demo") // no duplicate
	mu.Lock()
	if len(wakes) != 1 || wakes[0] != "demo:muet" {
		t.Fatalf("wakes %v", wakes)
	}
	mu.Unlock()
	// a finished script ("done") never goes silent
	e.post("demo", secret, `{"state":"done","summary":"fini"}`, "")
	e.advance(30 * time.Minute)
	if rep, _, _ := e.svc.Get("demo"); rep.Effective != "done" {
		t.Fatalf("done must not turn muet: %s", rep.Effective)
	}
	// recovery then error → woken again, once
	e.post("demo", secret, `{"state":"error","summary":"boom"}`, "")
	time.Sleep(50 * time.Millisecond)
	e.svc.CheckWake("demo")
	mu.Lock()
	defer mu.Unlock()
	if len(wakes) != 2 || wakes[1] != "demo:error" {
		t.Fatalf("wakes %v", wakes)
	}
}

func TestNoWakeWhenDisabled(t *testing.T) {
	e := newEnv(t)
	_, secret, _ := CreateKey(e.svc.db, "demo", "", nil, 0, false, nil)
	called := false
	e.svc.SetWake(func(string, string) error { called = true; return nil })
	e.post("demo", secret, `{"state":"error","summary":"x"}`, "")
	time.Sleep(50 * time.Millisecond)
	if called {
		t.Fatal("woken although wake=false")
	}
}

func TestKeyRevocation(t *testing.T) {
	e := newEnv(t)
	k, secret, _ := CreateKey(e.svc.db, "demo", "", nil, 0, false, nil)
	f := false
	if err := UpdateKey(e.svc.db, k.ID, nil, nil, nil, &f, nil); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.post("demo", secret, okBody, ""); code != 401 {
		t.Fatalf("disabled key accepted: %d", code)
	}
}

func TestClientIPOnlyTrustsLoopbackProxy(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = "203.0.113.5:4444"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	if ip := clientIP(r); ip.String() != "203.0.113.5" {
		t.Fatalf("spoofed XFF trusted: %s", ip)
	}
	r.RemoteAddr = "127.0.0.1:4444"
	r.Header.Set("X-Forwarded-For", "9.9.9.9, 1.2.3.4")
	if ip := clientIP(r); ip.String() != "1.2.3.4" {
		t.Fatalf("proxy XFF: %s", ip)
	}
}

func TestDashboardHandler(t *testing.T) {
	e := newEnv(t)
	_, secret, _ := CreateKey(e.svc.db, "demo", "", nil, 10, false, nil)
	e.post("demo", secret, `{"state":"running","summary":"<img src=x onerror=alert(1)>","data":{"jobs":[{"vm":"<b>vm1</b>"}]}}`, "")
	e.advance(time.Minute)
	e.post("demo", secret, okBody, "")
	e.advance(11 * time.Minute)

	rec := httptest.NewRecorder()
	e.svc.DashboardHandler(rec, httptest.NewRequest("GET", "/api/script-dashboard", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	var got struct {
		Scripts []struct {
			Status  Report
			History []HistoryItem
		}
		Now int64
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Scripts) != 1 || got.Now == 0 {
		t.Fatalf("%s", rec.Body.String())
	}
	s := got.Scripts[0]
	if s.Status.Effective != "muet" || s.Status.State != "ok" || len(s.History) != 2 || s.History[0].State != "ok" || s.History[1].State != "running" {
		t.Fatalf("%+v", s)
	}
	// stays JSON data (the client escapes on render); the key itself never leaks
	if strings.Contains(rec.Body.String(), secret) || strings.Contains(rec.Body.String(), "key_hash") {
		t.Fatal("leak")
	}
}

func TestRegenerateKey(t *testing.T) {
	e := newEnv(t)
	k, old, _ := CreateKey(e.svc.db, "demo", "", []string{"203.0.113.5"}, 30, true, nil)
	if code, _ := e.post("demo", old, okBody, "203.0.113.5"); code != 200 && code != 202 && code != 204 {
		t.Fatalf("initial post: %d", code)
	}
	k2, fresh, err := RegenerateKey(e.svc.db, k.ID)
	if err != nil || fresh == old || !strings.HasPrefix(fresh, "hsk_") {
		t.Fatalf("regenerate: %v %q", err, fresh)
	}
	if k2.SilenceMin != 30 || len(k2.AllowedIPs) != 1 || k2.LastUsed != 0 {
		t.Fatalf("settings not kept: %+v", k2)
	}
	if code, _ := e.post("demo", old, okBody, "203.0.113.5"); code != 401 {
		t.Fatalf("old secret still accepted: %d", code)
	}
	if code, _ := e.post("demo", fresh, okBody, "203.0.113.5"); code >= 300 {
		t.Fatalf("new secret rejected: %d", code)
	}
	if _, _, err := RegenerateKey(e.svc.db, "nope"); err == nil {
		t.Fatal("unknown id must fail")
	}
}
