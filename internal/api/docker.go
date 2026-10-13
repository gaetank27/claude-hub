package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"system/internal/caddy"
	"system/internal/docker"
	"system/internal/store"
)

// containerDTO is one container as shown in the Sandbox/Live UI.
type containerDTO struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Image         string    `json:"image"`
	Project       string    `json:"project"`     // claudehub.project label
	Environment   string    `json:"environment"` // sandbox | live
	State         string    `json:"state"`       // running | exited | created | ...
	Status        string    `json:"status"`      // human-readable ("Up 2 minutes")
	Ports         []portDTO `json:"ports"`
	WebURL        string    `json:"webUrl,omitempty"`    // internal Hub proxy link (/x/...)
	DirectURL     string    `json:"directUrl,omitempty"` // custom domain when provisioned
	DomainID      string    `json:"domainId,omitempty"`  // domain record id (for status polling)
	DomainStatus  string    `json:"domainStatus,omitempty"` // pending|active|error
	CPUPercent    float64   `json:"cpuPercent"`
	MemUsed       int64     `json:"memUsed"`
	MemLimit      int64     `json:"memLimit"`
	RestartPolicy string    `json:"restartPolicy,omitempty"`
	CreatedAt     int64     `json:"createdAt"`
}

type portDTO struct {
	Private int    `json:"private"`
	Public  int    `json:"public"`
	Type    string `json:"type"`
}

// dockerStatus reports daemon availability so the UI can degrade gracefully
// instead of erroring (mirrors the Ollama supervision pattern: never 5xx).
func (s *Server) dockerStatus(w http.ResponseWriter, r *http.Request) {
	if s.docker == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "error": "docker not configured"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	ver, err := s.docker.Ping(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "version": ver})
}

// listContainers returns hub-managed containers, optionally filtered by
// environment (?env=sandbox|live), enriched with live CPU/RAM stats.
func (s *Server) listContainers(w http.ResponseWriter, r *http.Request) {
	if s.docker == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("docker not configured"))
		return
	}
	env := r.URL.Query().Get("env")
	if env != "" && env != "sandbox" && env != "live" {
		writeErr(w, http.StatusBadRequest, errors.New("env must be sandbox or live"))
		return
	}
	// Only hub-managed containers (those carrying a project label).
	filters := []string{docker.LabelProject}
	if env != "" {
		filters = append(filters, docker.LabelEnv+"="+env)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	cs, err := s.docker.List(ctx, filters...)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}

	// Preload domain map (containerName → domain) to avoid N DB queries.
	domainByContainer := map[string]store.Domain{}
	if doms, err := s.db.ListDomains(); err == nil {
		for _, d := range doms {
			if d.ContainerName != "" {
				domainByContainer[d.ContainerName] = d
			}
		}
	}

	out := make([]containerDTO, len(cs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8) // bound concurrent stats calls
	for i, c := range cs {
		out[i] = s.toContainerDTO(c, domainByContainer[c.Name()])
		if c.State != "running" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, id string) {
			defer wg.Done()
			defer func() { <-sem }()
			sctx, scancel := context.WithTimeout(ctx, 8*time.Second)
			defer scancel()
			if st, err := s.docker.Stats(sctx, id); err == nil {
				out[i].CPUPercent = st.CPUPercent()
				out[i].MemUsed = st.MemUsed()
				out[i].MemLimit = st.MemLimit()
			}
		}(i, c.ID)
	}
	wg.Wait()

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) toContainerDTO(c docker.Container, dom store.Domain) containerDTO {
	dto := containerDTO{
		ID:          c.ID,
		Name:        c.Name(),
		Image:       c.Image,
		Project:     c.Labels[docker.LabelProject],
		Environment: c.Labels[docker.LabelEnv],
		State:       c.State,
		Status:      c.Status,
		CreatedAt:   c.Created,
	}
	published := false
	for _, p := range c.Ports {
		dto.Ports = append(dto.Ports, portDTO{Private: p.PrivatePort, Public: p.PublicPort, Type: p.Type})
		if p.PublicPort != 0 && p.Type == "tcp" {
			published = true
		}
	}
	if published {
		dto.WebURL = strings.TrimRight(s.mgr.GetPublicBaseURL(), "/") + "/x/" + dto.Name + "/"
	}
	if dom.ID != "" {
		dto.DomainID = dom.ID
		dto.DomainStatus = dom.Status
		if dom.Status == "active" {
			dto.DirectURL = "https://" + dom.Domain
		}
	}
	return dto
}

// containerAction drives a container's lifecycle (start|stop|restart).
func (s *Server) containerAction(w http.ResponseWriter, r *http.Request) {
	if s.docker == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("docker not configured"))
		return
	}
	var req struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	id := r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	var err error
	switch req.Action {
	case "start":
		err = s.docker.Start(ctx, id)
	case "stop":
		err = s.docker.Stop(ctx, id)
	case "restart":
		err = s.docker.Restart(ctx, id)
	default:
		writeErr(w, http.StatusBadRequest, errors.New("action must be start, stop or restart"))
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// removeContainer force-removes a container (stops it first if running).
func (s *Server) removeContainer(w http.ResponseWriter, r *http.Request) {
	if s.docker == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("docker not configured"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := s.docker.Remove(ctx, r.PathValue("id")); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// createContainer pulls an image and launches a container bound to a project and
// environment. Live containers get restart=unless-stopped (survive host reboot);
// sandbox containers get none. Ports are published on 127.0.0.1 only — sandbox
// apps are reachable solely through the authenticated /x/ reverse proxy.
func (s *Server) createContainer(w http.ResponseWriter, r *http.Request) {
	if s.docker == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("docker not configured"))
		return
	}
	var req struct {
		Project string            `json:"project"` // project slug
		Env     string            `json:"env"`     // sandbox | live
		Image   string            `json:"image"`
		Name    string            `json:"name"`
		Ports   []int             `json:"ports"`   // container-side tcp ports to expose
		EnvVars map[string]string `json:"envVars"` // environment variables
		WebPort int               `json:"webPort"` // port routed by the proxy
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	req.Env = strings.TrimSpace(req.Env)
	if req.Env != "sandbox" && req.Env != "live" {
		writeErr(w, http.StatusBadRequest, errors.New("env must be sandbox or live"))
		return
	}
	if strings.TrimSpace(req.Image) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("image is required"))
		return
	}
	if strings.TrimSpace(req.Project) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("project is required"))
		return
	}
	// Validate the project exists so containers can't orphan from the UI.
	if _, err := s.db.GetProjectBySlug(req.Project); err != nil {
		writeNotFoundOr500(w, err)
		return
	}

	labels := map[string]string{
		docker.LabelProject: req.Project,
		docker.LabelEnv:     req.Env,
	}
	if req.WebPort > 0 {
		labels[docker.LabelWebPort] = strconv.Itoa(req.WebPort)
	}
	ports := make([]docker.PortSpec, 0, len(req.Ports))
	for _, p := range req.Ports {
		ports = append(ports, docker.PortSpec{Container: p, Proto: "tcp"})
	}
	restart := "no"
	if req.Env == "live" {
		restart = "unless-stopped"
	}
	spec := docker.RunSpec{
		Name:          strings.TrimSpace(req.Name),
		Image:         strings.TrimSpace(req.Image),
		Env:           req.EnvVars,
		Ports:         ports,
		Labels:        labels,
		Network:       "claudehub_" + req.Env,
		RestartPolicy: restart,
		PublishHost:   "127.0.0.1",
	}

	// Image pulls can be slow; give them room.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	if err := s.docker.Pull(ctx, spec.Image); err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Errorf("pull: %w", err))
		return
	}
	id, err := s.docker.Create(ctx, spec)
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Errorf("create: %w", err))
		return
	}
	if err := s.docker.Start(ctx, id); err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Errorf("start: %w", err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// containerProvisionDomain handles POST /api/docker/containers/{name}/domain.
// Body: {"domain":"myapp.example.com","serverIP":"192.0.2.10"}
// Resolves the container's published port, creates a domain record, and starts
// async provisioning (DNS A record + Let's Encrypt cert + Caddy route).
func (s *Server) containerProvisionDomain(w http.ResponseWriter, r *http.Request) {
	if s.docker == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("docker not configured"))
		return
	}
	name := r.PathValue("name")

	var req struct {
		Domain   string `json:"domain"`   // e.g. myapp.example.com
		ServerIP string `json:"serverIP"` // e.g. 192.0.2.10
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Domain == "" {
		writeErr(w, http.StatusBadRequest, errors.New("domain is required"))
		return
	}

	// If a domain is already linked to this container, return it.
	if existing, err := s.db.GetDomainByContainer(name); err == nil {
		writeJSON(w, http.StatusOK, toDomainDTO(existing))
		return
	}

	// Resolve the container's published web port.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	ins, err := s.docker.Inspect(ctx, name)
	cancel()
	if err != nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("container %q not found", name))
		return
	}
	hostPort := pickHostPort(ins)
	if hostPort == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("container %q has no published port — set webPort when creating", name))
		return
	}
	upstream := "127.0.0.1:" + hostPort

	// Parse zone and source from domain: "sub.zone.tld" → zone="zone.tld", source="sub"
	dnsZone, dnsSource := splitDomain(req.Domain)

	d, err := s.db.CreateDomainFor(req.Domain, upstream, name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	job := &domainJob{}
	domainJobsMu.Lock()
	domainJobs[d.ID] = job
	domainJobsMu.Unlock()

	email := s.adminEmail()

	serverIP := req.ServerIP
	if serverIP == "" {
		ip, err := s.resolveDnsServerIP(r.Context())
		if err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		serverIP = ip
	}

	var dns *domainDNSReq
	if dnsSource != "" && dnsZone != "" {
		dns = &domainDNSReq{ZoneDomain: dnsZone, Source: dnsSource, IP: serverIP}
	}

	go s.provisionDomain(d, dns, s.dnsProviderFor(req.Domain, dns), email, job)

	writeJSON(w, http.StatusAccepted, toDomainDTO(d))
}

// containerRemoveDomain handles DELETE /api/docker/containers/{name}/domain.
func (s *Server) containerRemoveDomain(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	d, err := s.db.GetDomainByContainer(name)
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("no domain linked to this container"))
		return
	}
	cc := caddy.New("")
	if err := cc.RemoveDomain(r.Context(), d.Domain); err != nil {
		// log but don't fail — the DB record should still be removed
		fmt.Printf("containerRemoveDomain: caddy remove %s: %v\n", d.Domain, err)
	}
	_ = s.db.DeleteDomain(d.ID)
	w.WriteHeader(http.StatusNoContent)
}

// splitDomain splits "sub.zone.tld" into ("zone.tld", "sub").
// Handles multi-part subdomains: "a.b.zone.tld" → ("zone.tld", "a.b").
func splitDomain(fqdn string) (zone, source string) {
	parts := strings.Split(strings.TrimSuffix(fqdn, "."), ".")
	if len(parts) < 3 {
		return fqdn, ""
	}
	// Assume zone = last 2 labels (works for .me, .com, .fr, etc.)
	zone = strings.Join(parts[len(parts)-2:], ".")
	source = strings.Join(parts[:len(parts)-2], ".")
	return zone, source
}
