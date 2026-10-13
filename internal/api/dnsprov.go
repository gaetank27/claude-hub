package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"system/internal/dnsprov"
	"system/internal/store"
)

// DNS provider endpoints (cookie-only, see auth/apikey.go). Switching a zone
// changes which DNS host the Hub writes to and validates certificates against —
// it never touches name servers or the registrar, so it must only be done once the
// zone really is served by the new provider.

// dnsProviderStatus handles GET /api/dns/providers.
func (s *Server) dnsProviderStatus(w http.ResponseWriter, r *http.Request) {
	conf := map[string]bool{}
	for _, n := range dnsprov.Names {
		cfg, err := s.connectorConfig(n)
		conf[n] = err == nil && cfg["api_token"] != ""
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"default":    dnsprov.Default,
		"providers":  dnsprov.Names,
		"configured": conf,
		"zones":      dnsprov.ZoneProviders(s.db),
	})
}

// dnsProviderSet handles PUT /api/dns/providers {"zone","provider"}.
func (s *Server) dnsProviderSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Zone     string `json:"zone"`
		Provider string `json:"provider"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.Zone == "" || req.Provider == "" {
		writeErr(w, http.StatusBadRequest, errors.New("zone and provider required"))
		return
	}
	// A zone can only be pointed at a provider that has a working connector.
	if req.Provider != dnsprov.Default {
		if cfg, err := s.connectorConfig(req.Provider); err != nil || cfg["api_token"] == "" {
			writeErr(w, http.StatusConflict, errors.New("configure and enable the "+req.Provider+" connector first (Tools)"))
			return
		}
	}
	before := dnsprov.ProviderFor(s.db, req.Zone)
	if err := dnsprov.SetProvider(s.db, req.Zone, req.Provider); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.db.AddConnectorAudit(store.ConnectorAudit{Connector: "dns", Action: "set_provider", Source: "ui",
		Args: strings.ToLower(req.Zone) + ": " + before + " → " + req.Provider, Outcome: "ok"})
	s.dnsProviderStatus(w, r)
}

// dnsDryRun handles POST /api/dns/dry-run {"zone","op":"create|delete|cert","type","name","content"}.
// No call is made to any provider.
func (s *Server) dnsDryRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Zone string `json:"zone"`
		Op   string `json:"op"`
		dnsprov.Record
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid body"))
		return
	}
	plan, err := dnsprov.DryRun(s.db, func(typ string) map[string]string {
		cfg, err := s.connectorConfig(typ)
		if err != nil {
			return nil
		}
		return cfg
	}, req.Zone, req.Op, req.Record)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

