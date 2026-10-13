package dnsprov

import (
	"context"
	"errors"
	"strings"
	"testing"

	"system/internal/tools"
)

type memMeta map[string]string

func (m memMeta) GetAppMeta(k string) (string, error) {
	if v, ok := m[k]; ok {
		return v, nil
	}
	return "", errors.New("not found")
}
func (m memMeta) SetAppMeta(k, v string) error { m[k] = v; return nil }

type call struct {
	typ, action string
	args        map[string]any
}

// fake swaps the network seams for recorders: no provider is ever contacted.
func fake(t *testing.T) *[]call {
	var calls []call
	oc, or, ol := callConnector, cfRead, ikList
	t.Cleanup(func() { callConnector, cfRead, ikList = oc, or, ol })
	callConnector = func(_ context.Context, typ string, _ map[string]string, action string, args map[string]any) (string, error) {
		calls = append(calls, call{typ, action, args})
		return "ok", nil
	}
	cfRead = func(_ context.Context, _ map[string]string, kind string, _ map[string]any) (any, error) {
		if kind == "zones" {
			return []tools.CloudflareZone{{ID: "z1", Name: "example.com"}}, nil
		}
		return []tools.CloudflareRecord{{ID: "r1", Type: "A", Name: "hub.example.com", Content: "1.2.3.4"}, {ID: "r2", Type: "A", Name: "x.example.com", Content: "9.9.9.9"}}, nil
	}
	ikList = func(context.Context, map[string]string, string) ([]tools.InfomaniakRecord, error) {
		return []tools.InfomaniakRecord{{ID: 7, Type: "A", Source: "hub", Target: "1.2.3.4"}, {ID: 8, Type: "A", Source: "x", Target: "9.9.9.9"}}, nil
	}
	return &calls
}

func TestSelectionIsPerZoneAndDefaultsToInfomaniak(t *testing.T) {
	m := memMeta{}
	if ProviderFor(m, "example.com") != Infomaniak {
		t.Fatal("default must be infomaniak")
	}
	if err := SetProvider(m, "Example.com.", Cloudflare); err != nil {
		t.Fatal(err)
	}
	if ProviderFor(m, "example.com") != Cloudflare || ProviderFor(m, "autre.ch") != Infomaniak {
		t.Fatal("switch must apply to the zone only")
	}
	if SetProvider(m, "example.com", "route53") == nil || SetProvider(m, "bad zone", Cloudflare) == nil {
		t.Fatal("unknown provider / zone must be refused")
	}
	_ = SetProvider(m, "example.com", Infomaniak)
	if m[metaKey] != "{}" {
		t.Fatalf("back to default must clear the entry, got %s", m[metaKey])
	}
}

func TestSameCallsDifferentProviders(t *testing.T) {
	calls := fake(t)
	ctx := context.Background()
	rec := Record{Type: "A", Name: "hub.example.com", Content: "1.2.3.4", TTL: 300}

	ik, _ := New(Infomaniak, map[string]string{"api_token": "ik"})
	if _, err := ik.Create(ctx, "example.com", rec); err != nil {
		t.Fatal(err)
	}
	if c := (*calls)[0]; c.typ != Infomaniak || c.args["source"] != "hub" || c.args["domain"] != "example.com" {
		t.Fatalf("infomaniak create: %+v", c)
	}
	if _, err := ik.Delete(ctx, "example.com", rec); err != nil {
		t.Fatal(err)
	}
	if c := (*calls)[1]; c.action != "infomaniak_delete_dns_record" || c.args["record_id"] != 7 || len(*calls) != 2 {
		t.Fatalf("infomaniak delete must hit only the matching record: %+v", *calls)
	}

	*calls = nil
	cf, _ := New(Cloudflare, map[string]string{"api_token": "cf"})
	if _, err := cf.Create(ctx, "example.com", rec); err != nil {
		t.Fatal(err)
	}
	c := (*calls)[0]
	if c.typ != Cloudflare || c.args["zone_id"] != "z1" || c.args["name"] != "hub.example.com" || c.args["proxied"] != false || c.args["user_confirmed"] != true {
		t.Fatalf("cloudflare create: %+v", c)
	}
	*calls = nil
	if _, err := cf.Delete(ctx, "example.com", rec); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].args["record_id"] != "r1" {
		t.Fatalf("cloudflare delete: %+v", *calls)
	}
	if _, err := cf.Create(ctx, "example.com", Record{Type: "A", Name: "hub.autre.ch", Content: "1.1.1.1"}); err == nil {
		t.Fatal("a record outside the zone must be refused")
	}
}

func TestChallengePerProvider(t *testing.T) {
	ik, _ := New(Infomaniak, map[string]string{"api_token": "tk1"})
	cf, _ := New(Cloudflare, map[string]string{"api_token": "tk2"})
	a, _ := ik.Challenge()
	b, _ := cf.Challenge()
	if a.Hook != "dns_infomaniak" || a.Env[0] != "INFOMANIAK_API_TOKEN=tk1" || b.Hook != "dns_cf" || b.Env[0] != "CF_Token=tk2" {
		t.Fatalf("%+v %+v", a, b)
	}
	if _, err := New(Cloudflare, map[string]string{}); err == nil {
		t.Fatal("no token must be refused")
	}
}

func TestDryRunSendsNothingAndNeedsNoToken(t *testing.T) {
	calls := fake(t)
	m := memMeta{}
	_ = SetProvider(m, "example.com", Cloudflare)
	none := func(string) map[string]string { return nil }
	p, err := DryRun(m, none, "example.com", "create", Record{Type: "A", Name: "acces.example.com", Content: "1.2.3.4"})
	if err != nil || p.Provider != Cloudflare || p.Configured || len(p.Warnings) == 0 || len(p.Steps) != 2 || !strings.Contains(p.Steps[1].Detail, "DNS only") {
		t.Fatalf("%+v %v", p, err)
	}
	off := func(string) map[string]string { return map[string]string{"api_token": "x"} }
	p, _ = DryRun(m, off, "example.com", "cert", Record{})
	if p.Steps[0].Do != "acme.sh --issue --dns dns_cf" || strings.Contains(p.Steps[0].Detail, "CF_Token") {
		t.Fatalf("%+v", p)
	}
	p, _ = DryRun(m, off, "example.com", "delete", Record{Type: "A", Name: "hub.example.com"})
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "Allow changes") {
		t.Fatalf("a write with allow_writes off must warn: %+v", p)
	}
	if _, err := DryRun(m, off, "example.com", "create", Record{Type: "A", Name: "hub.autre.ch", Content: "1"}); err == nil {
		t.Fatal("record outside the zone")
	}
	if len(*calls) != 0 {
		t.Fatal("dry-run must not call any provider")
	}
}
