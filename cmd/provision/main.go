// cmd/provision is a one-shot CLI to provision a domain:
// create cert via acme.sh DNS-01 + update Caddy config.
// Usage: provision -domain hub.example.com -upstream 127.0.0.1:8787
package main

import (
	"strings"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"system/internal/caddy"
	"system/internal/certman"
	"system/internal/dnsprov"
	"system/internal/secretbox"
	"system/internal/store"
)

func main() {
	domain := flag.String("domain", "", "Domain to provision (e.g. hub.example.com)")
	upstream := flag.String("upstream", "127.0.0.1:8787", "Upstream dial address")
	dbPath := flag.String("db", "/home/claude/.local/share/system/system.db", "Path to system.db")
	keyPath := flag.String("key", "/home/claude/.local/share/system/secret.key", "Path to secret key")
	caddyAdmin := flag.String("caddy", "http://localhost:2019", "Caddy admin URL")
	email := flag.String("email", "", "ACME account email (auto-detected from DB if empty)")
	flag.Parse()

	if *domain == "" {
		log.Fatal("--domain is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Open DB and secrets.
	db, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	box, err := secretbox.Open(*keyPath)
	if err != nil {
		log.Fatalf("open secretbox: %v", err)
	}

	// DNS provider of the domain's zone (Infomaniak unless the zone was switched).
	zone := *domain
	if parts := strings.Split(zone, "."); len(parts) > 2 {
		zone = strings.Join(parts[len(parts)-2:], ".")
	}
	prov, err := dnsprov.Resolve(db, func(typ string) (map[string]string, error) { return resolveConfig(db, box, typ) }, zone)
	if err != nil {
		log.Fatalf("dns provider: %v", err)
	}
	ch, err := prov.Challenge()
	if err != nil {
		log.Fatalf("dns challenge: %v", err)
	}
	fmt.Printf("✓ DNS provider for %s: %s\n", zone, prov.Name())

	// Resolve email.
	acmeEmail := *email
	if acmeEmail == "" {
		acmeEmail = resolveEmail(db)
	}
	fmt.Printf("✓ ACME email: %s\n", acmeEmail)

	// Issue/renew cert.
	fmt.Printf("→ Issuing cert for %s via DNS-01 …\n", *domain)
	certs, err := certman.EnsureCert(ctx, *domain, ch, acmeEmail)
	if err != nil {
		log.Fatalf("ensure cert: %v", err)
	}
	fmt.Printf("✓ Cert: %s\n   Key: %s\n", certs.FullChain, certs.Key)

	// Update Caddy.
	fmt.Printf("→ Updating Caddy for %s → %s …\n", *domain, *upstream)
	cc := caddy.New(*caddyAdmin)
	if err := cc.AddDomain(ctx, *domain, *upstream, certs.FullChain, certs.Key); err != nil {
		log.Fatalf("caddy add domain: %v", err)
	}
	fmt.Printf("✓ Caddy updated — %s is live!\n", *domain)
}

func resolveConfig(db *store.DB, box *secretbox.Box, typ string) (map[string]string, error) {
	tools, err := db.ListTools()
	if err != nil {
		return nil, err
	}
	for _, t := range tools {
		if t.Type != typ || !t.Enabled || t.Status != store.ToolConnected {
			continue
		}
		raw, err := box.Decrypt(t.ConfigEnc)
		if err != nil {
			return nil, err
		}
		var cfg map[string]string
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
		return cfg, nil
	}
	return nil, fmt.Errorf("no enabled %s connector found", typ)
}

func resolveEmail(db *store.DB) string {
	if v, err := db.GetAppMeta("acme_email"); err == nil && v != "" {
		return v
	}
	accts, err := db.ListClaudeAccounts()
	if err == nil {
		for _, a := range accts {
			if a.AccountEmail != "" {
				return a.AccountEmail
			}
		}
	}
	hostname, _ := os.Hostname()
	return "admin@" + hostname
}
