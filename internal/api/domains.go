package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

	"system/internal/caddy"
	"system/internal/certman"
	"system/internal/dnsprov"
	"system/internal/store"
)

// domainJob tracks the progress of an async domain provisioning task.
type domainJob struct {
	mu   sync.Mutex
	logs []string
	done bool
	err  string
}

func (j *domainJob) log(msg string) {
	j.mu.Lock()
	j.logs = append(j.logs, msg)
	j.mu.Unlock()
	log.Printf("domain: %s", msg)
}

var (
	domainJobsMu sync.Mutex
	domainJobs   = map[string]*domainJob{}
)

// domainDTO is returned by list/get endpoints.
type domainDTO struct {
	ID       string `json:"id"`
	Domain   string `json:"domain"`
	Upstream string `json:"upstream"`
	Status   string `json:"status"` // pending|active|error
	Error    string `json:"error,omitempty"`
}

func toDomainDTO(d store.Domain) domainDTO {
	return domainDTO{ID: d.ID, Domain: d.Domain, Upstream: d.Upstream, Status: d.Status, Error: d.Error}
}

func (s *Server) listDomains(w http.ResponseWriter, r *http.Request) {
	domains, err := s.db.ListDomains()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]domainDTO, len(domains))
	for i, d := range domains {
		out[i] = toDomainDTO(d)
	}
	writeJSON(w, http.StatusOK, out)
}

// createDomain handles POST /api/domains.
// Body: {"domain":"hub.example.com","upstream":"127.0.0.1:8787","dnsRecord":{"domain":"example.com","source":"hub","ip":"192.0.2.10"}}
// If dnsRecord is provided, the A record is created through the DNS provider of its zone first
// (Infomaniak unless the zone was switched, see internal/dnsprov).
// The cert issuance (acme.sh DNS-01) runs asynchronously; poll GET /api/domains/{id}/status.
func (s *Server) createDomain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain   string         `json:"domain"`
		Upstream string         `json:"upstream"`
		DNS      *domainDNSReq  `json:"dnsRecord,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Domain == "" || req.Upstream == "" {
		writeErr(w, http.StatusBadRequest, errors.New("domain and upstream required"))
		return
	}

	// Reject duplicate.
	if existing, err := s.db.GetDomainByName(req.Domain); err == nil {
		writeJSON(w, http.StatusOK, toDomainDTO(existing))
		return
	}

	d, err := s.db.CreateDomain(req.Domain, req.Upstream)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	job := &domainJob{}
	domainJobsMu.Lock()
	domainJobs[d.ID] = job
	domainJobsMu.Unlock()

	email := s.adminEmail()
	go s.provisionDomain(d, req.DNS, s.dnsProviderFor(req.Domain, req.DNS), email, job)

	writeJSON(w, http.StatusAccepted, toDomainDTO(d))
}

// domainStatus handles GET /api/domains/{id}/status
func (s *Server) domainStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	domainJobsMu.Lock()
	job := domainJobs[id]
	domainJobsMu.Unlock()

	d, err := s.db.GetDomain(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	dto := toDomainDTO(d)

	var logs []string
	var done bool
	if job != nil {
		job.mu.Lock()
		logs = append([]string{}, job.logs...)
		done = job.done
		job.mu.Unlock()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"domain": dto,
		"logs":   logs,
		"done":   done,
	})
}

// deleteDomain handles DELETE /api/domains/{id}
func (s *Server) deleteDomain(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d, err := s.db.GetDomain(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}

	cc := caddy.New("")
	if err := cc.RemoveDomain(r.Context(), d.Domain); err != nil {
		log.Printf("domain: remove Caddy route for %s: %v", d.Domain, err)
	}

	_ = s.db.DeleteDomain(id)
	w.WriteHeader(http.StatusNoContent)
}

// ---- async provisioning ----

type domainDNSReq struct {
	ZoneDomain string `json:"zoneDomain"`
	Source     string `json:"source"`
	IP         string `json:"ip"`
}

func (s *Server) provisionDomain(d store.Domain, dns *domainDNSReq, prov dnsprov.Provider, email string, job *domainJob) {
	ctx := context.Background()
	defer func() {
		job.mu.Lock()
		job.done = true
		job.mu.Unlock()
	}()

	// Step 1: create DNS A record if requested.
	if dns != nil && dns.ZoneDomain != "" && dns.Source != "" && dns.IP != "" && prov != nil {
		fqdn := dns.Source + "." + dns.ZoneDomain
		job.log(fmt.Sprintf("Creating A record %s → %s via %s …", fqdn, dns.IP, prov.Name()))
		out, err := prov.Create(ctx, dns.ZoneDomain, dnsprov.Record{Type: "A", Name: fqdn, Content: dns.IP, TTL: 300})
		if err != nil {
			msg := "DNS record creation failed: " + err.Error()
			job.log("ERROR: " + msg)
			_ = s.db.SetDomainError(d.ID, msg)
			return
		}
		job.log("DNS: " + out)
	}

	// Step 2: issue TLS cert via acme.sh DNS-01.
	if prov == nil {
		job.log("WARNING: no usable DNS provider connector — skipping cert issuance. Add manually and call /api/domains/{id}/activate")
	} else {
		job.log(fmt.Sprintf("Issuing Let's Encrypt cert for %s via DNS-01 through %s (this may take ~1 min) …", d.Domain, prov.Name()))
		ch, err := prov.Challenge()
		var paths certman.CertPaths
		if err == nil {
			paths, err = certman.EnsureCert(ctx, d.Domain, ch, email)
		}
		if err != nil {
			msg := "cert issuance failed: " + err.Error()
			job.log("ERROR: " + msg)
			_ = s.db.SetDomainError(d.ID, msg)
			return
		}
		job.log(fmt.Sprintf("Cert issued: %s", paths.FullChain))

		if err := s.db.SetDomainCerts(d.ID, paths.FullChain, paths.Key); err != nil {
			job.log("ERROR: save cert paths: " + err.Error())
			return
		}
		d.CertFile = paths.FullChain
		d.KeyFile = paths.Key
	}

	// Step 3: update Caddy config.
	job.log(fmt.Sprintf("Updating Caddy: adding route %s → %s …", d.Domain, d.Upstream))
	cc := caddy.New("")
	if err := cc.AddDomain(ctx, d.Domain, d.Upstream, d.CertFile, d.KeyFile); err != nil {
		msg := "Caddy update failed: " + err.Error()
		job.log("ERROR: " + msg)
		_ = s.db.SetDomainError(d.ID, msg)
		return
	}
	job.log("Caddy updated. Domain is live at https://" + d.Domain)
	_ = s.db.SetDomainCerts(d.ID, d.CertFile, d.KeyFile) // ensure status=active
}

// ---- helpers ----

// dnsProviderFor instantiates the DNS provider of the zone of a new domain (the
// zone given by the request, else deduced from the name). nil = none usable; the
// provisioning job then skips DNS and certificate and says so.
func (s *Server) dnsProviderFor(domain string, dns *domainDNSReq) dnsprov.Provider {
	zone, _ := splitDomain(domain)
	if dns != nil && dns.ZoneDomain != "" {
		zone = dns.ZoneDomain
	}
	p, err := dnsprov.Resolve(s.db, s.connectorConfig, zone)
	if err != nil {
		log.Printf("domain: %v", err)
		return nil
	}
	return p
}

// connectorConfig returns the decrypted config of the first enabled, connected
// connector of the given type.
func (s *Server) connectorConfig(typ string) (map[string]string, error) {
	all, err := s.db.ListTools()
	if err != nil {
		return nil, err
	}
	for _, t := range all {
		if t.Type != typ || !t.Enabled || t.Status != store.ToolConnected {
			continue
		}
		return s.decryptToolConfig(t.ConfigEnc)
	}
	return nil, fmt.Errorf("no active %s connector found", typ)
}

// adminEmail returns a usable email for ACME account registration.
// Tries (in order): app_meta "acme_email", first connected Claude account email.
func (s *Server) adminEmail() string {
	if v, err := s.db.GetAppMeta("acme_email"); err == nil && strings.Contains(v, "@") {
		return v
	}
	accts, err := s.db.ListClaudeAccounts()
	if err == nil {
		for _, a := range accts {
			if strings.Contains(a.AccountEmail, "@") {
				return a.AccountEmail
			}
		}
	}
	return "admin@hub.local"
}
