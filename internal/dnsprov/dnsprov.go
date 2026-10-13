// Package dnsprov makes the Hub's DNS needs (create/delete a record, DNS-01
// challenge of a certificate) independent of the DNS host. The provider is chosen
// per zone (app_meta "dns_providers", a JSON map zone → provider; Infomaniak when a
// zone is not listed), so moving a zone to Cloudflare is a setting change, not a
// redeploy. Nothing here switches a zone by itself.
package dnsprov

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"system/internal/tools"
)

const (
	Infomaniak = "infomaniak"
	Cloudflare = "cloudflare"
	// Default is the provider of every zone that has no explicit setting.
	Default = Infomaniak

	metaKey = "dns_providers"
)

// Names lists the supported providers.
var Names = []string{Infomaniak, Cloudflare}

var zoneRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// Record is a DNS record with a fully qualified name (Name "hub.example.com").
type Record struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl,omitempty"`
}

// Challenge is what acme.sh needs for a DNS-01 validation.
type Challenge struct {
	Hook string   // acme.sh dnsapi hook, e.g. dns_cf
	Env  []string // KEY=VALUE, contains the secret: never log it
}

// Provider is one DNS host.
type Provider interface {
	Name() string
	Create(ctx context.Context, zone string, r Record) (string, error)
	Delete(ctx context.Context, zone string, r Record) (string, error)
	Challenge() (Challenge, error)
}

// seams for tests (no real network call).
var (
	callConnector = func(ctx context.Context, typ string, cfg map[string]string, action string, args map[string]any) (string, error) {
		c, ok := tools.Get(typ)
		if !ok {
			return "", fmt.Errorf("%s connector not registered", typ)
		}
		return c.Call(ctx, cfg, action, args)
	}
	cfRead = tools.CloudflareRead
	ikList = tools.InfomaniakRecords
)

// New builds the provider from the decrypted connector config.
func New(name string, cfg map[string]string) (Provider, error) {
	if cfg["api_token"] == "" {
		return nil, fmt.Errorf("no %s API token configured", name)
	}
	switch name {
	case Infomaniak:
		return ikProvider{cfg}, nil
	case Cloudflare:
		c := map[string]string{}
		for k, v := range cfg {
			c[k] = v
		}
		c["_source"] = "ui" // initiated by a user action of the Hub UI
		return cfProvider{c}, nil
	}
	return nil, fmt.Errorf("unknown DNS provider %q", name)
}

// Relative returns the record name relative to the zone ("@" for the apex).
func Relative(zone, fqdn string) (string, error) {
	zone = strings.ToLower(strings.TrimSuffix(zone, "."))
	fqdn = strings.ToLower(strings.TrimSuffix(fqdn, "."))
	if fqdn == zone {
		return "@", nil
	}
	if !strings.HasSuffix(fqdn, "."+zone) {
		return "", fmt.Errorf("%s is not inside zone %s", fqdn, zone)
	}
	return strings.TrimSuffix(fqdn, "."+zone), nil
}

// ---- Infomaniak ----

type ikProvider struct{ cfg map[string]string }

func (ikProvider) Name() string { return Infomaniak }

func (p ikProvider) Create(ctx context.Context, zone string, r Record) (string, error) {
	src, err := Relative(zone, r.Name)
	if err != nil {
		return "", err
	}
	ttl := r.TTL
	if ttl <= 0 {
		ttl = 300
	}
	return callConnector(ctx, Infomaniak, p.cfg, "infomaniak_create_dns_record", map[string]any{
		"domain": zone, "type": r.Type, "source": src, "target": r.Content, "ttl": ttl})
}

func (p ikProvider) Delete(ctx context.Context, zone string, r Record) (string, error) {
	src, err := Relative(zone, r.Name)
	if err != nil {
		return "", err
	}
	recs, err := ikList(ctx, p.cfg, zone)
	if err != nil {
		return "", err
	}
	n := 0
	for _, x := range recs {
		if strings.EqualFold(x.Type, r.Type) && strings.EqualFold(x.Source, src) && (r.Content == "" || x.Target == r.Content) {
			if _, err := callConnector(ctx, Infomaniak, p.cfg, "infomaniak_delete_dns_record", map[string]any{"domain": zone, "record_id": x.ID}); err != nil {
				return "", err
			}
			n++
		}
	}
	return fmt.Sprintf("Deleted %d record(s) %s %s.", n, r.Type, r.Name), nil
}

func (p ikProvider) Challenge() (Challenge, error) {
	return Challenge{Hook: "dns_infomaniak", Env: []string{"INFOMANIAK_API_TOKEN=" + p.cfg["api_token"]}}, nil
}

// ---- Cloudflare ----

type cfProvider struct{ cfg map[string]string }

func (cfProvider) Name() string { return Cloudflare }

func (p cfProvider) zoneID(ctx context.Context, zone string) (string, error) {
	data, err := cfRead(ctx, p.cfg, "zones", nil)
	if err != nil {
		return "", err
	}
	zs, _ := data.([]tools.CloudflareZone)
	for _, z := range zs {
		if strings.EqualFold(z.Name, zone) {
			return z.ID, nil
		}
	}
	return "", fmt.Errorf("zone %s not found in this Cloudflare account", zone)
}

// Create always makes a DNS-only (grey cloud) record: the Hub never proxies.
// It goes through the connector's guarded path (allow_writes + audit journal).
func (p cfProvider) Create(ctx context.Context, zone string, r Record) (string, error) {
	if _, err := Relative(zone, r.Name); err != nil {
		return "", err
	}
	zid, err := p.zoneID(ctx, zone)
	if err != nil {
		return "", err
	}
	ttl := r.TTL
	if ttl <= 0 {
		ttl = 1
	}
	return callConnector(ctx, Cloudflare, p.cfg, "cloudflare_create_dns_record", map[string]any{
		"zone_id": zid, "type": r.Type, "name": r.Name, "content": r.Content, "proxied": false, "ttl": ttl, "user_confirmed": true})
}

func (p cfProvider) Delete(ctx context.Context, zone string, r Record) (string, error) {
	if _, err := Relative(zone, r.Name); err != nil {
		return "", err
	}
	zid, err := p.zoneID(ctx, zone)
	if err != nil {
		return "", err
	}
	data, err := cfRead(ctx, p.cfg, "dns", map[string]any{"zone_id": zid})
	if err != nil {
		return "", err
	}
	recs, _ := data.([]tools.CloudflareRecord)
	n := 0
	for _, x := range recs {
		if strings.EqualFold(x.Type, r.Type) && strings.EqualFold(x.Name, strings.TrimSuffix(r.Name, ".")) && (r.Content == "" || x.Content == r.Content) {
			if _, err := callConnector(ctx, Cloudflare, p.cfg, "cloudflare_delete_dns_record", map[string]any{"zone_id": zid, "record_id": x.ID, "user_confirmed": true}); err != nil {
				return "", err
			}
			n++
		}
	}
	return fmt.Sprintf("Deleted %d record(s) %s %s.", n, r.Type, r.Name), nil
}

func (p cfProvider) Challenge() (Challenge, error) {
	return Challenge{Hook: "dns_cf", Env: []string{"CF_Token=" + p.cfg["api_token"]}}, nil
}

// ---- per-zone setting ----

// Settings reads and writes the zone → provider map.
type Settings interface {
	GetAppMeta(key string) (string, error)
	SetAppMeta(key, value string) error
}

// ZoneProviders returns the explicit zone → provider map.
func ZoneProviders(db Settings) map[string]string {
	m := map[string]string{}
	if raw, err := db.GetAppMeta(metaKey); err == nil {
		_ = json.Unmarshal([]byte(raw), &m)
	}
	return m
}

// ProviderFor returns the provider name of a zone (Default when unset).
func ProviderFor(db Settings, zone string) string {
	if p, ok := ZoneProviders(db)[strings.ToLower(strings.TrimSuffix(zone, "."))]; ok && valid(p) {
		return p
	}
	return Default
}

func valid(p string) bool {
	for _, n := range Names {
		if n == p {
			return true
		}
	}
	return false
}

// SetProvider records the provider of a zone. Setting the Default removes the entry.
func SetProvider(db Settings, zone, provider string) error {
	zone = strings.ToLower(strings.TrimSuffix(zone, "."))
	if !zoneRe.MatchString(zone) {
		return errors.New("invalid zone name")
	}
	if !valid(provider) {
		return fmt.Errorf("unknown DNS provider %q (expected one of %s)", provider, strings.Join(Names, ", "))
	}
	m := ZoneProviders(db)
	if provider == Default {
		delete(m, zone)
	} else {
		m[zone] = provider
	}
	b, _ := json.Marshal(m)
	return db.SetAppMeta(metaKey, string(b))
}

// Resolve finds the provider of a zone and instantiates it from the connectors
// (configs are decrypted by load). A nil Provider with an error means the zone's
// provider has no usable connector.
func Resolve(db Settings, load func(typ string) (map[string]string, error), zone string) (Provider, error) {
	name := ProviderFor(db, zone)
	cfg, err := load(name)
	if err != nil {
		return nil, fmt.Errorf("zone %s uses %s: %w", zone, name, err)
	}
	return New(name, cfg)
}

// ---- dry-run ----

// Step is one thing the Hub would do.
type Step struct {
	Do     string `json:"do"`
	Detail string `json:"detail"`
}

// Plan is the result of a dry-run: nothing was sent to any provider.
type Plan struct {
	Zone       string   `json:"zone"`
	Provider   string   `json:"provider"`
	Configured bool     `json:"configured"`
	Steps      []Step   `json:"steps"`
	Warnings   []string `json:"warnings"`
}

// DryRun describes what a create/delete/cert operation would do for the zone's
// current provider, without any network call and without needing a token.
// connectorCfg is nil when the provider's connector is absent. op is
// create | delete | cert.
func DryRun(db Settings, connectorCfg func(typ string) map[string]string, zone, op string, r Record) (Plan, error) {
	zone = strings.ToLower(strings.TrimSuffix(zone, "."))
	if !zoneRe.MatchString(zone) {
		return Plan{}, errors.New("invalid zone name")
	}
	name := ProviderFor(db, zone)
	cfg := connectorCfg(name)
	p := Plan{Zone: zone, Provider: name, Configured: cfg != nil && cfg["api_token"] != "", Steps: []Step{}, Warnings: []string{}}
	if !p.Configured {
		p.Warnings = append(p.Warnings, fmt.Sprintf("no usable %s connector: a real operation would fail", name))
	}
	if name == Cloudflare && cfg != nil && cfg["allow_writes"] != "true" && op != "cert" {
		p.Warnings = append(p.Warnings, "« Allow changes » is off on the Cloudflare connector: a real write would be refused")
	}
	rec := func() error {
		if r.Type == "" || r.Name == "" || r.Content == "" && op == "create" {
			return errors.New("type, name and content are required")
		}
		if _, err := Relative(zone, r.Name); err != nil {
			return err
		}
		return nil
	}
	switch op {
	case "create":
		if err := rec(); err != nil {
			return Plan{}, err
		}
		if name == Cloudflare {
			p.Steps = append(p.Steps, Step{"GET /zones", "find the id of " + zone},
				Step{"POST /zones/<id>/dns_records", fmt.Sprintf("%s %s → %s (DNS only, TTL auto)", strings.ToUpper(r.Type), r.Name, r.Content)})
		} else {
			src, _ := Relative(zone, r.Name)
			p.Steps = append(p.Steps, Step{"GET /1/product", "find the id of " + zone},
				Step{"POST /1/domain/<id>/dns/record", fmt.Sprintf("%s %s → %s (TTL 300)", strings.ToUpper(r.Type), src, r.Content)})
		}
	case "delete":
		if err := rec(); err != nil {
			return Plan{}, err
		}
		p.Steps = append(p.Steps, Step{"list", "find the " + strings.ToUpper(r.Type) + " records named " + r.Name + " at " + name},
			Step{"delete", "remove each match (by value when given)"})
	case "cert":
		hook := "dns_infomaniak"
		if name == Cloudflare {
			hook = "dns_cf"
		}
		p.Steps = append(p.Steps, Step{"acme.sh --issue --dns " + hook, "the _acme-challenge TXT of " + zone + " is written and removed through " + name + " (token passed by environment, never logged)"})
	default:
		return Plan{}, fmt.Errorf("unknown operation %q (create, delete or cert)", op)
	}
	sort.Strings(p.Warnings)
	return p, nil
}
