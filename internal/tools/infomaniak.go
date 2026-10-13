package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func init() { register(infomaniak{}) }

// infomaniak talks to the Infomaniak REST API (v1) to manage DNS records.
// Auth: Bearer token obtained from the Infomaniak Manager → Developer → API keys.
// The connector auto-resolves domain name → numeric domain_id on every call.
type infomaniak struct{}

func (infomaniak) Type() string  { return "infomaniak" }
func (infomaniak) Label() string { return "Infomaniak DNS" }

func (infomaniak) Fields() []Field {
	return []Field{
		{Key: "api_token", Label: "API token", Type: FieldPassword, Secret: true, Required: true,
			Placeholder: "token from Manager → Developer → API Keys"},
	}
}

func (infomaniak) Test(ctx context.Context, cfg map[string]string) error {
	if err := missing(cfg, "api_token"); err != nil {
		return err
	}
	// List domains — succeeds only if the token is valid and has at least domain scope.
	domains, err := ikListDomains(ctx, cfg)
	if err != nil {
		return err
	}
	if len(domains) == 0 {
		return fmt.Errorf("authenticated but no domain found under this account")
	}
	return nil
}

func (infomaniak) Tools() []ToolSpec {
	domainArg := ArgSpec{Name: "domain", Type: "string", Description: "Domain name, e.g. example.com", Required: true}
	return []ToolSpec{
		{
			Name:        "infomaniak_list_dns_records",
			Description: "List all DNS records for an Infomaniak domain.",
			Args:        []ArgSpec{domainArg},
		},
		{
			Name:        "infomaniak_create_dns_record",
			Description: "Create a DNS record on an Infomaniak domain. source is the subdomain (e.g. hub, _acme-challenge.hub) or @ for the apex.",
			Args: []ArgSpec{
				domainArg,
				{Name: "type", Type: "string", Description: "Record type: A, AAAA, CNAME, TXT, MX, NS, SRV, CAA…", Required: true},
				{Name: "source", Type: "string", Description: "Subdomain or @ for the apex, e.g. hub or _acme-challenge.hub", Required: true},
				{Name: "target", Type: "string", Description: "Record value, e.g. an IP address or TXT string", Required: true},
				{Name: "ttl", Type: "integer", Description: "TTL in seconds (default 300)"},
			},
		},
		{
			Name:        "infomaniak_delete_dns_record",
			Description: "Delete a DNS record by its numeric ID (use infomaniak_list_dns_records to find the id).",
			Args: []ArgSpec{
				domainArg,
				{Name: "record_id", Type: "integer", Description: "Numeric record ID returned by infomaniak_list_dns_records", Required: true},
			},
		},
		{
			Name:        "infomaniak_update_dns_record",
			Description: "Update an existing DNS record (replace its type, source, target and/or ttl).",
			Args: []ArgSpec{
				domainArg,
				{Name: "record_id", Type: "integer", Description: "Numeric record ID", Required: true},
				{Name: "type", Type: "string", Description: "New record type", Required: true},
				{Name: "source", Type: "string", Description: "New subdomain or @", Required: true},
				{Name: "target", Type: "string", Description: "New record value", Required: true},
				{Name: "ttl", Type: "integer", Description: "New TTL in seconds (default 300)"},
			},
		},
	}
}

func (infomaniak) Call(ctx context.Context, cfg map[string]string, action string, args map[string]any) (string, error) {
	domain := argString(args, "domain", "")
	if domain == "" {
		return "", fmt.Errorf("domain is required")
	}

	switch action {
	case "infomaniak_list_dns_records":
		return ikListRecords(ctx, cfg, domain)

	case "infomaniak_create_dns_record":
		typ := argString(args, "type", "")
		source := argString(args, "source", "")
		target := argString(args, "target", "")
		if typ == "" || source == "" || target == "" {
			return "", fmt.Errorf("type, source and target are required")
		}
		ttl := 300
		if v, ok := argInt(args, "ttl"); ok && v > 0 {
			ttl = v
		}
		return ikCreateRecord(ctx, cfg, domain, typ, source, target, ttl)

	case "infomaniak_delete_dns_record":
		id, ok := argInt(args, "record_id")
		if !ok {
			return "", fmt.Errorf("record_id is required")
		}
		return ikDeleteRecord(ctx, cfg, domain, id)

	case "infomaniak_update_dns_record":
		id, ok := argInt(args, "record_id")
		if !ok {
			return "", fmt.Errorf("record_id is required")
		}
		typ := argString(args, "type", "")
		source := argString(args, "source", "")
		target := argString(args, "target", "")
		if typ == "" || source == "" || target == "" {
			return "", fmt.Errorf("type, source and target are required")
		}
		ttl := 300
		if v, ok := argInt(args, "ttl"); ok && v > 0 {
			ttl = v
		}
		return ikUpdateRecord(ctx, cfg, domain, id, typ, source, target, ttl)

	default:
		return "", fmt.Errorf("unknown action %q", action)
	}
}

// ---- Infomaniak API helpers ----

const ikBase = "https://api.infomaniak.com"

// ikProduct is one entry from GET /1/product?service_name=domain.
type ikProduct struct {
	ID           int    `json:"id"`
	CustomerName string `json:"customer_name"`
}

// ikDNSRecord is one DNS record returned by the API.
// The id field may be a string or integer depending on API version.
type ikDNSRecord struct {
	ID        json.Number `json:"id"`
	Source    string      `json:"source"`
	SourceIDN string      `json:"source_idn"`
	Type      string      `json:"type"`
	Target    string      `json:"target"`
	TTL       int         `json:"ttl"`
}

func ikListDomains(ctx context.Context, cfg map[string]string) ([]ikProduct, error) {
	var resp struct {
		Result string      `json:"result"`
		Data   []ikProduct `json:"data"`
	}
	if err := ikDo(ctx, cfg, http.MethodGet, "/1/product?service_name=domain", nil, &resp); err != nil {
		return nil, err
	}
	if resp.Result != "success" {
		return nil, fmt.Errorf("infomaniak: API returned result=%q", resp.Result)
	}
	return resp.Data, nil
}

// ikDomainID resolves a domain name (e.g. "example.com") to its numeric ID.
func ikDomainID(ctx context.Context, cfg map[string]string, name string) (int, error) {
	domains, err := ikListDomains(ctx, cfg)
	if err != nil {
		return 0, err
	}
	name = strings.ToLower(strings.TrimRight(name, "."))
	for _, d := range domains {
		if strings.ToLower(d.CustomerName) == name {
			return d.ID, nil
		}
	}
	return 0, fmt.Errorf("domain %q not found in this Infomaniak account", name)
}

func ikListRecords(ctx context.Context, cfg map[string]string, domain string) (string, error) {
	domID, err := ikDomainID(ctx, cfg, domain)
	if err != nil {
		return "", err
	}
	// The Infomaniak DNS API returns data as a flat array of records.
	var resp struct {
		Result string        `json:"result"`
		Data   []ikDNSRecord `json:"data"`
	}
	if err := ikDo(ctx, cfg, http.MethodGet, fmt.Sprintf("/1/domain/%d/dns/record", domID), nil, &resp); err != nil {
		return "", err
	}
	if resp.Result != "success" {
		return "", fmt.Errorf("infomaniak: API returned result=%q", resp.Result)
	}
	if len(resp.Data) == 0 {
		return fmt.Sprintf("No DNS records found for %s.", domain), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "DNS records for %s (%d total):\n", domain, len(resp.Data))
	for _, r := range resp.Data {
		src := r.Source
		if r.SourceIDN != "" {
			src = r.SourceIDN
		}
		fmt.Fprintf(&b, "  id=%-8s %-8s %-40s → %s (ttl=%d)\n", r.ID.String(), r.Type, src, r.Target, r.TTL)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func ikCreateRecord(ctx context.Context, cfg map[string]string, domain, typ, source, target string, ttl int) (string, error) {
	domID, err := ikDomainID(ctx, cfg, domain)
	if err != nil {
		return "", err
	}
	body := map[string]any{
		"type":   strings.ToUpper(typ),
		"source": source,
		"target": target,
		"ttl":    ttl,
	}
	var resp struct {
		Result string              `json:"result"`
		Data   json.RawMessage     `json:"data"`
	}
	if err := ikDo(ctx, cfg, http.MethodPost, fmt.Sprintf("/1/domain/%d/dns/record", domID), body, &resp); err != nil {
		return "", err
	}
	if resp.Result != "success" {
		return "", fmt.Errorf("infomaniak: API returned result=%q", resp.Result)
	}
	// data may be {"id": N} or the created record object
	var created struct{ ID int `json:"id"` }
	_ = json.Unmarshal(resp.Data, &created)
	return fmt.Sprintf("Created %s record for %s.%s → %s (id=%d, ttl=%d).", strings.ToUpper(typ), source, domain, target, created.ID, ttl), nil
}

func ikDeleteRecord(ctx context.Context, cfg map[string]string, domain string, recordID int) (string, error) {
	domID, err := ikDomainID(ctx, cfg, domain)
	if err != nil {
		return "", err
	}
	var resp struct {
		Result string `json:"result"`
	}
	if err := ikDo(ctx, cfg, http.MethodDelete, fmt.Sprintf("/1/domain/%d/dns/record/%d", domID, recordID), nil, &resp); err != nil {
		return "", err
	}
	if resp.Result != "success" {
		return "", fmt.Errorf("infomaniak: API returned result=%q", resp.Result)
	}
	return fmt.Sprintf("Deleted DNS record %d from %s.", recordID, domain), nil
}

func ikUpdateRecord(ctx context.Context, cfg map[string]string, domain string, recordID int, typ, source, target string, ttl int) (string, error) {
	domID, err := ikDomainID(ctx, cfg, domain)
	if err != nil {
		return "", err
	}
	body := map[string]any{
		"type":   strings.ToUpper(typ),
		"source": source,
		"target": target,
		"ttl":    ttl,
	}
	var resp struct {
		Result string `json:"result"`
	}
	if err := ikDo(ctx, cfg, http.MethodPut, fmt.Sprintf("/1/domain/%d/dns/record/%d", domID, recordID), body, &resp); err != nil {
		return "", err
	}
	if resp.Result != "success" {
		return "", fmt.Errorf("infomaniak: API returned result=%q", resp.Result)
	}
	return fmt.Sprintf("Updated DNS record %d on %s: %s %s.%s → %s (ttl=%d).", recordID, domain, strings.ToUpper(typ), source, domain, target, ttl), nil
}

func ikDo(ctx context.Context, cfg map[string]string, method, path string, in any, out any) error {
	var bodyReader io.Reader = http.NoBody
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, ikBase+path, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg["api_token"])
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient(false).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("infomaniak %s %s: %s — %s", method, path, resp.Status, strings.TrimSpace(string(body)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// InfomaniakRecord is one zone record, for callers that need ids (dnsprov).
type InfomaniakRecord struct {
	ID     int
	Type   string
	Source string
	Target string
}

// InfomaniakRecords lists the records of a zone with their numeric ids.
func InfomaniakRecords(ctx context.Context, cfg map[string]string, domain string) ([]InfomaniakRecord, error) {
	domID, err := ikDomainID(ctx, cfg, domain)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Result string        `json:"result"`
		Data   []ikDNSRecord `json:"data"`
	}
	if err := ikDo(ctx, cfg, http.MethodGet, fmt.Sprintf("/1/domain/%d/dns/record", domID), nil, &resp); err != nil {
		return nil, err
	}
	if resp.Result != "success" {
		return nil, fmt.Errorf("infomaniak: API returned result=%q", resp.Result)
	}
	out := make([]InfomaniakRecord, 0, len(resp.Data))
	for _, r := range resp.Data {
		id, _ := r.ID.Int64()
		src := r.Source
		if r.SourceIDN != "" {
			src = r.SourceIDN
		}
		out = append(out, InfomaniakRecord{ID: int(id), Type: r.Type, Source: src, Target: r.Target})
	}
	return out, nil
}
