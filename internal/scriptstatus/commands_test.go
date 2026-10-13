package scriptstatus

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// cmdEnv: a script "demo" with a key allowing pause/resume/set-interval.
func cmdEnv(t *testing.T) (*env, string) {
	e := newEnv(t)
	_, secret, err := CreateKey(e.svc.db, "demo", "", nil, 0, false, []string{"pause", "resume", "set-interval"})
	if err != nil {
		t.Fatal(err)
	}
	return e, secret
}

// beat sends a heartbeat (rate-limit windows reset) and decodes the reply.
func (e *env) beat(secret, body string) (int, struct {
	OK       bool          `json:"ok"`
	Commands []WireCommand `json:"commands"`
}) {
	e.svc.mu.Lock()
	delete(e.svc.rate, "127.0.0.1")
	delete(e.svc.srate, "demo")
	e.svc.mu.Unlock()
	code, raw := e.post("demo", secret, body, "")
	var out struct {
		OK       bool          `json:"ok"`
		Commands []WireCommand `json:"commands"`
	}
	if code == 200 {
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			e.t.Fatalf("reply %q: %v", raw, err)
		}
	}
	return code, out
}

func ackBody(id, result, msg string) string {
	return `{"state":"ok","summary":"x","acks":[{"id":"` + id + `","result":"` + result + `","message":"` + msg + `"}]}`
}

func stateOf(t *testing.T, e *env, id string) Command {
	t.Helper()
	cs, err := e.svc.ListCommands("demo", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("command %s not found", id)
	return Command{}
}

func TestCommandDeliveryAndAck(t *testing.T) {
	e, secret := cmdEnv(t)
	c, err := e.svc.SendCommand("demo", "set-interval", json.RawMessage(`{"seconds":120}`), "user", 0)
	if err != nil {
		t.Fatal(err)
	}
	if c.State != StatePending || c.ExpiresAt-c.CreatedAt != DefaultTTL.Milliseconds() {
		t.Fatalf("%+v", c)
	}
	// 1st heartbeat: delivered
	code, r := e.beat(secret, okBody)
	if code != 200 || !r.OK || len(r.Commands) != 1 || r.Commands[0].ID != c.ID || r.Commands[0].Name != "set-interval" {
		t.Fatalf("%d %+v", code, r)
	}
	if p, _ := r.Commands[0].Params.(map[string]any); p["seconds"] != float64(120) {
		t.Fatalf("params %+v", r.Commands[0].Params)
	}
	if got := stateOf(t, e, c.ID); got.State != StateDelivered || got.Deliveries != 1 {
		t.Fatalf("%+v", got)
	}
	// delivered at most once per heartbeat: the next one (before the ack timeout) is empty
	e.advance(time.Minute)
	if _, r = e.beat(secret, okBody); len(r.Commands) != 0 {
		t.Fatalf("redelivered too early: %+v", r.Commands)
	}
	// ack in the following heartbeat
	e.advance(time.Minute)
	if code, r = e.beat(secret, ackBody(c.ID, "ok", "intervalle 120 s")); code != 200 || len(r.Commands) != 0 {
		t.Fatalf("%d %+v", code, r)
	}
	if got := stateOf(t, e, c.ID); got.State != StateOK || got.Message != "intervalle 120 s" || got.DoneAt == 0 {
		t.Fatalf("%+v", got)
	}
	// a second ack (replay) cannot change a finished command
	e.beat(secret, ackBody(c.ID, "failed", "rejoué"))
	if got := stateOf(t, e, c.ID); got.State != StateOK {
		t.Fatalf("replayed ack applied: %+v", got)
	}
}

func TestCommandFailedAckAndRetryOnNoAck(t *testing.T) {
	e, secret := cmdEnv(t)
	c1, _ := e.svc.SendCommand("demo", "pause", nil, "assistant", 0)
	c2, _ := e.svc.SendCommand("demo", "resume", nil, "user", 0)
	_, r := e.beat(secret, okBody)
	if len(r.Commands) != 2 || r.Commands[0].ID != c1.ID {
		t.Fatalf("order/len: %+v", r.Commands)
	}
	if c := stateOf(t, e, c1.ID); c.CreatedBy != "assistant" {
		t.Fatalf("createdBy %q", c.CreatedBy)
	}
	// c1 failed ack, c2 never acked -> redelivered after ackTimeout
	e.advance(ackTimeout + time.Second)
	_, r = e.beat(secret, ackBody(c1.ID, "failed", "refusé"))
	if got := stateOf(t, e, c1.ID); got.State != StateFailed || got.Message != "refusé" {
		t.Fatalf("%+v", got)
	}
	if len(r.Commands) != 1 || r.Commands[0].ID != c2.ID {
		t.Fatalf("redelivery: %+v", r.Commands)
	}
	// delivered maxDeliveries times without ack -> expired
	for i := 0; i < maxDeliveries; i++ {
		e.advance(ackTimeout + time.Second)
		e.beat(secret, okBody)
	}
	got := stateOf(t, e, c2.ID)
	if got.State != StateExpired || got.Deliveries != maxDeliveries {
		t.Fatalf("%+v", got)
	}
	e.advance(ackTimeout + time.Second)
	if _, r = e.beat(secret, okBody); len(r.Commands) != 0 {
		t.Fatalf("delivered after expiry: %+v", r.Commands)
	}
}

func TestCommandExpiry(t *testing.T) {
	e, secret := cmdEnv(t)
	c, _ := e.svc.SendCommand("demo", "pause", nil, "user", 5*time.Minute)
	e.advance(6 * time.Minute)
	_, r := e.beat(secret, okBody)
	if len(r.Commands) != 0 {
		t.Fatalf("expired command delivered: %+v", r.Commands)
	}
	if got := stateOf(t, e, c.ID); got.State != StateExpired {
		t.Fatalf("%+v", got)
	}
	// a command delivered but never acked also expires with its TTL
	c2, _ := e.svc.SendCommand("demo", "pause", nil, "user", 15*time.Minute)
	e.beat(secret, okBody)
	e.advance(16 * time.Minute)
	if got := stateOf(t, e, c2.ID); got.State != StateExpired {
		t.Fatalf("%+v", got)
	}
	// ack after expiry is ignored
	e.beat(secret, ackBody(c2.ID, "ok", "trop tard"))
	if got := stateOf(t, e, c2.ID); got.State != StateExpired {
		t.Fatalf("%+v", got)
	}
}

func TestCommandCancel(t *testing.T) {
	e, secret := cmdEnv(t)
	c, _ := e.svc.SendCommand("demo", "pause", nil, "user", 0)
	if err := e.svc.CancelCommand(c.ID); err != nil {
		t.Fatal(err)
	}
	if _, r := e.beat(secret, okBody); len(r.Commands) != 0 {
		t.Fatalf("cancelled command delivered: %+v", r.Commands)
	}
	if got := stateOf(t, e, c.ID); got.State != StateCancelled {
		t.Fatalf("%+v", got)
	}
	// delivered -> cannot be cancelled (409); unknown -> 404
	c2, _ := e.svc.SendCommand("demo", "resume", nil, "user", 0)
	e.beat(secret, okBody)
	var ce *CmdError
	if err := e.svc.CancelCommand(c2.ID); !errors.As(err, &ce) || ce.Code != 409 {
		t.Fatalf("%v", err)
	}
	if err := e.svc.CancelCommand("nope"); !errors.As(err, &ce) || ce.Code != 404 {
		t.Fatalf("%v", err)
	}
}

func TestCommandAllowList(t *testing.T) {
	e, secret := cmdEnv(t)
	var ce *CmdError
	for _, name := range []string{"stop", "rm -rf", "PAUSE", "pause;ls", "", "1pause", strings.Repeat("a", 40)} {
		_, err := e.svc.SendCommand("demo", name, nil, "user", 0)
		if !errors.As(err, &ce) || ce.Code != 400 {
			t.Fatalf("%q accepted: %v", name, err)
		}
	}
	// script with no key / disabled key: nothing allowed
	if _, err := e.svc.SendCommand("other", "pause", nil, "user", 0); !errors.As(err, &ce) || ce.Code != 400 {
		t.Fatalf("%v", err)
	}
	// queued, then removed from the allow-list: never delivered
	c, _ := e.svc.SendCommand("demo", "pause", nil, "user", 0)
	keys, _ := ListKeys(e.svc.db)
	list := []string{"resume"}
	if err := UpdateKey(e.svc.db, keys[0].ID, nil, nil, nil, nil, &list); err != nil {
		t.Fatal(err)
	}
	if _, r := e.beat(secret, okBody); len(r.Commands) != 0 {
		t.Fatalf("command off the allow-list delivered: %+v", r.Commands)
	}
	if got := stateOf(t, e, c.ID); got.State != StateCancelled {
		t.Fatalf("%+v", got)
	}
	// invalid allow-list entries are refused
	bad := []string{"ok", "rm -rf /"}
	if err := UpdateKey(e.svc.db, keys[0].ID, nil, nil, nil, nil, &bad); err == nil {
		t.Fatal("bad allow-list accepted")
	}
}

func TestCommandNotDeliveredToDisabledKeyOrWrongIP(t *testing.T) {
	e := newEnv(t)
	k, secret, _ := CreateKey(e.svc.db, "demo", "", []string{"203.0.113.9"}, 0, false, []string{"pause"})
	if _, err := e.svc.SendCommand("demo", "pause", nil, "user", 0); err != nil {
		t.Fatal(err)
	}
	code, body := e.post("demo", secret, okBody, "198.51.100.1")
	if code != 401 || strings.Contains(body, "commands") {
		t.Fatalf("%d %s", code, body)
	}
	f := false
	if err := UpdateKey(e.svc.db, k.ID, nil, nil, nil, &f, nil); err != nil {
		t.Fatal(err)
	}
	code, body = e.post("demo", secret, okBody, "203.0.113.9")
	if code != 401 || strings.Contains(body, "pause") {
		t.Fatalf("%d %s", code, body)
	}
	// a disabled key no longer authorises new commands either
	if _, err := e.svc.SendCommand("demo", "pause", nil, "user", 0); err == nil {
		t.Fatal("command accepted for a script with only a disabled key")
	}
}

func TestCommandParamsValidation(t *testing.T) {
	e, _ := cmdEnv(t)
	long := `{"a":"` + strings.Repeat("x", maxParamString+1) + `"}`
	big := `{"a":[` + strings.TrimSuffix(strings.Repeat(`"`+strings.Repeat("y", 150)+`",`, 16), ",") + `],"b":[` + strings.TrimSuffix(strings.Repeat(`"`+strings.Repeat("y", 150)+`",`, 2), ",") + `]}`
	cases := map[string]string{
		"array":    `[1]`,
		"string":   `"x"`,
		"deep":     `{"a":{"b":{"c":{"d":1}}}}`,
		"long":     long,
		"big":      big,
		"badkey":   `{"a b":1}`,
		"badkey2":  `{"__proto__x":1,"1a":2}`,
		"control":  `{"a":"x\u0000y"}`,
		"newline":  `{"a":"x\ny"}`,
		"bigarray": `{"a":[` + strings.TrimSuffix(strings.Repeat("1,", maxParamArray+1), ",") + `]}`,
		"notjson":  `{`,
		"trailing": `{"a":1} x`,
	}
	for name, p := range cases {
		_, err := e.svc.SendCommand("demo", "pause", json.RawMessage(p), "user", 0)
		var ce *CmdError
		if !errors.As(err, &ce) || ce.Code != 400 {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	c, err := e.svc.SendCommand("demo", "set-interval", json.RawMessage(` { "seconds" : 60, "why": "test", "n": {"x": [1,2]} } `), "user", 0)
	if err != nil {
		t.Fatal(err)
	}
	if c.Params.(map[string]any)["seconds"] != float64(60) {
		t.Fatalf("%+v", c.Params)
	}
	// ttl bounds
	for _, ttl := range []time.Duration{time.Second, MaxTTL + time.Hour} {
		if _, err := e.svc.SendCommand("demo", "pause", nil, "user", ttl); err == nil {
			t.Errorf("ttl %v accepted", ttl)
		}
	}
}

func TestCommandQueueBounds(t *testing.T) {
	e, secret := cmdEnv(t)
	for i := 0; i < maxActive; i++ {
		if _, err := e.svc.SendCommand("demo", "pause", nil, "user", 0); err != nil {
			t.Fatal(err)
		}
	}
	var ce *CmdError
	if _, err := e.svc.SendCommand("demo", "pause", nil, "user", 0); !errors.As(err, &ce) || ce.Code != 409 {
		t.Fatalf("%v", err)
	}
	// at most maxPerDelivery per heartbeat, the rest on the next one
	_, r := e.beat(secret, okBody)
	if len(r.Commands) != maxPerDelivery {
		t.Fatalf("%d delivered", len(r.Commands))
	}
	_, r = e.beat(secret, okBody)
	if len(r.Commands) != maxPerDelivery {
		t.Fatalf("2nd batch %d", len(r.Commands))
	}
	// history stays short
	for i := 0; i < cmdHistoryKeep+10; i++ {
		c, err := e.svc.SendCommand("demo", "pause", nil, "user", 0)
		if err != nil {
			// queue still full of delivered ones: cancel nothing, just stop
			break
		}
		_ = e.svc.CancelCommand(c.ID)
	}
	cs, _ := e.svc.ListCommands("demo", 200)
	fin := 0
	for _, c := range cs {
		if c.State == StateCancelled {
			fin++
		}
	}
	if fin > cmdHistoryKeep {
		t.Fatalf("%d finished commands kept", fin)
	}
}

// A client that knows nothing about commands (no "acks", ignores "commands")
// keeps working exactly as before.
func TestCommandBackwardCompatibleClient(t *testing.T) {
	e := newEnv(t)
	_, secret := func() (Key, string) { k, s, _ := CreateKey(e.svc.db, "demo", "", nil, 0, false, nil); return k, s }()
	code, raw := e.post("demo", secret, okBody, "")
	if code != 200 {
		t.Fatalf("%d", code)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil || m["ok"] != true {
		t.Fatalf("%s", raw)
	}
	if cmds, ok := m["commands"].([]any); !ok || len(cmds) != 0 {
		t.Fatalf("commands field: %s", raw)
	}
	rep, ok, _ := e.svc.Get("demo")
	if !ok || rep.State != "ok" {
		t.Fatalf("%+v", rep)
	}
}

func TestAckValidation(t *testing.T) {
	e, secret := cmdEnv(t)
	for _, body := range []string{
		`{"state":"ok","summary":"","acks":"x"}`,
		`{"state":"ok","summary":"","acks":[{"id":"not-a-uuid","result":"ok"}]}`,
		`{"state":"ok","summary":"","acks":[{"id":"6f9619ff-8b86-d011-b42d-00c04fc964ff","result":"maybe"}]}`,
	} {
		if code, _ := e.post("demo", secret, body, ""); code != 400 {
			t.Errorf("%s -> %d", body, code)
		}
		e.svc.mu.Lock()
		delete(e.svc.rate, "127.0.0.1")
		delete(e.svc.srate, "demo")
		e.svc.mu.Unlock()
	}
	// an ack for another script's command (or an unknown id) is harmless
	other, secret2 := func() (Key, string) {
		k, s, _ := CreateKey(e.svc.db, "other", "", nil, 0, false, []string{"pause"})
		return k, s
	}()
	_ = other
	c, _ := e.svc.SendCommand("other", "pause", nil, "user", 0)
	e.post("other", secret2, okBody, "")
	if code, _ := e.post("demo", secret, ackBody(c.ID, "ok", "pirate"), ""); code != 200 {
		t.Fatalf("code %d", code)
	}
	cs, _ := e.svc.ListCommands("other", 10)
	if cs[0].State != StateDelivered {
		t.Fatalf("cross-script ack applied: %+v", cs[0])
	}
	// the ack message is sanitised and bounded
	e.advance(time.Minute)
	e.svc.mu.Lock()
	delete(e.svc.srate, "other")
	delete(e.svc.rate, "127.0.0.1")
	e.svc.mu.Unlock()
	msg := strings.Repeat("é", 300)
	e.post("other", secret2, `{"state":"ok","summary":"","acks":[{"id":"`+c.ID+`","result":"ok","message":"a\u0007b`+msg+`"}]}`, "")
	cs, _ = e.svc.ListCommands("other", 10)
	if cs[0].State != StateOK || strings.ContainsRune(cs[0].Message, 7) || len([]rune(cs[0].Message)) > maxAckMessage {
		t.Fatalf("%q", cs[0].Message)
	}
}

func TestDefaultCommandsForCloudStack(t *testing.T) {
	e := newEnv(t)
	k, _, err := CreateKey(e.svc.db, "cloudstack-migrations", "", nil, 0, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(k.AllowedCommands, ",") != "pause,report-now,resume,set-interval,stop" {
		t.Fatalf("%v", k.AllowedCommands)
	}
	k2, _, _ := CreateKey(e.svc.db, "demo", "", nil, 0, false, nil)
	if len(k2.AllowedCommands) != 0 {
		t.Fatalf("unknown script must default to no command: %v", k2.AllowedCommands)
	}
}

func TestCommandAdminHandlers(t *testing.T) {
	e, _ := cmdEnv(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/script-commands", e.svc.ListCommandsHandler)
	mux.HandleFunc("POST /api/script-commands", e.svc.SendCommandHandler)
	mux.HandleFunc("DELETE /api/script-commands/{id}", e.svc.CancelCommandHandler)
	do := func(method, path, body string) (int, string) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	if code, _ := do("POST", "/api/script-commands", `{"script":"demo","name":"stop"}`); code != 400 {
		t.Fatalf("off-list command: %d", code)
	}
	code, body := do("POST", "/api/script-commands", `{"script":"demo","name":"set-interval","params":{"seconds":90},"ttlMinutes":30}`)
	if code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	var c Command
	_ = json.Unmarshal([]byte(body), &c)
	if c.CreatedBy != "user" || c.ExpiresAt-c.CreatedAt != (30*time.Minute).Milliseconds() {
		t.Fatalf("%+v", c)
	}
	if code, body = do("GET", "/api/script-commands?script=demo", ""); code != 200 || !strings.Contains(body, c.ID) {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ = do("DELETE", "/api/script-commands/"+c.ID, ""); code != 204 {
		t.Fatalf("%d", code)
	}
	if code, _ = do("DELETE", "/api/script-commands/"+c.ID, ""); code != 409 {
		t.Fatalf("%d", code)
	}
}

func TestMigrationCommandsNotInDefaultAllowList(t *testing.T) {
	for script, cmds := range DefaultCommands {
		for _, c := range cmds {
			if c == "plan-migrations" || c == "start-migrations" || c == "cancel-migrations" {
				t.Fatalf("%s: %s must be opted in by the user, not part of the default allow-list", script, c)
			}
		}
	}
	// A key created with the defaults cannot queue them.
	e := newEnv(t)
	if _, _, err := CreateKey(e.svc.db, "cloudstack-migrations", "", nil, 0, false, nil); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"plan-migrations", "start-migrations", "cancel-migrations"} {
		p := `{}`
		if n == "start-migrations" {
			p = `{"planId":"p-0123456789ab"}`
		}
		if _, err := e.svc.SendCommand("cloudstack-migrations", n, json.RawMessage(p), "assistant", 0); err == nil || !strings.Contains(err.Error(), "non autorisée") {
			t.Fatalf("%s must be refused without opt-in, got %v", n, err)
		}
	}
}

func TestMigrationCommandParams(t *testing.T) {
	e := newEnv(t)
	if _, _, err := CreateKey(e.svc.db, "cloudstack-migrations", "", nil, 0, false,
		[]string{"plan-migrations", "start-migrations", "cancel-migrations", "pause"}); err != nil {
		t.Fatal(err)
	}
	many := func(n int) string {
		xs := make([]string, n)
		for i := range xs {
			xs[i] = `"vm` + strings.Repeat("a", i%3) + `"`
		}
		return strings.Join(xs, ",")
	}
	ok := []struct{ name, params string }{
		{"plan-migrations", `{"vms":["web01","db-02","a.b_c"],"destinationPool":"Datastore 1 (SSD)"}`},
		{"plan-migrations", `{"vms":["00000000-0000-4000-8000-000000000101"],"destinationPool":"pool-dst"}`},
		{"plan-migrations", `{"vms":[` + many(20) + `],"destinationPool":"p"}`},
		{"start-migrations", `{"planId":"p-0123456789ab"}`},
		{"start-migrations", `{"planId":"p-0123456789ab","onError":"stop"}`},
		{"cancel-migrations", `{}`},
		{"cancel-migrations", ``},
	}
	for _, c := range ok {
		if _, err := e.svc.SendCommand("cloudstack-migrations", c.name, json.RawMessage(c.params), "user", 0); err != nil {
			t.Errorf("%s %s should be accepted: %v", c.name, c.params, err)
		}
		// keep under the per-script active limit
		cs, _ := e.svc.ListCommands("cloudstack-migrations", 50)
		for _, x := range cs {
			_ = e.svc.CancelCommand(x.ID)
		}
	}
	bad := []struct{ name, params string }{
		{"plan-migrations", ``},
		{"plan-migrations", `{}`},
		{"plan-migrations", `{"vms":[],"destinationPool":"p"}`},
		{"plan-migrations", `{"vms":"web01","destinationPool":"p"}`},
		{"plan-migrations", `{"vms":["web01"]}`},
		{"plan-migrations", `{"vms":["web01"],"destinationPool":""}`},
		{"plan-migrations", `{"vms":["web01"],"destinationPool":"p","force":true}`},
		{"plan-migrations", `{"vms":["a b"],"destinationPool":"p"}`},
		{"plan-migrations", `{"vms":["a;rm -rf /"],"destinationPool":"p"}`},
		{"plan-migrations", `{"vms":["$(id)"],"destinationPool":"p"}`},
		{"plan-migrations", `{"vms":["../x"],"destinationPool":"p"}`},
		{"plan-migrations", `{"vms":[1],"destinationPool":"p"}`},
		{"plan-migrations", `{"vms":["` + strings.Repeat("a", 64) + `"],"destinationPool":"p"}`},
		{"plan-migrations", `{"vms":["web01"],"destinationPool":"p\"; drop"}`},
		{"plan-migrations", `{"vms":["web01"],"destinationPool":"trailing "}`},
		{"plan-migrations", `{"vms":["web01"],"destinationPool":["a"]}`},
		{"plan-migrations", `{"vms":[` + many(21) + `],"destinationPool":"p"}`},
		{"start-migrations", ``},
		{"start-migrations", `{}`},
		{"start-migrations", `{"planId":"abc"}`},
		{"start-migrations", `{"planId":"p-0123456789AB"}`},
		{"start-migrations", `{"planId":"p-0123456789ab","vms":["x"]}`},
		{"start-migrations", `{"planId":"p-0123456789ab","onError":"retry"}`},
		{"start-migrations", `{"planId":["p-0123456789ab"]}`},
		{"cancel-migrations", `{"force":true}`},
	}
	for _, c := range bad {
		if _, err := e.svc.SendCommand("cloudstack-migrations", c.name, json.RawMessage(c.params), "assistant", 0); err == nil {
			t.Errorf("%s %s must be rejected", c.name, c.params)
		}
	}
	// Other commands keep their generic validation only.
	if _, err := e.svc.SendCommand("cloudstack-migrations", "pause", json.RawMessage(`{"any":"thing"}`), "user", 0); err != nil {
		t.Errorf("pause: %v", err)
	}
}
