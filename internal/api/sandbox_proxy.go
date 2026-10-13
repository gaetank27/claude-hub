package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"system/internal/docker"
)

// containerProxy reverse-proxies browser traffic to a hub-managed container's
// published port, so sandbox/live web apps are reachable over the hub's HTTPS
// origin (works on mobile Safari) without per-app nginx/DNS changes. It resolves
// a container name to its host port via inspect, cached briefly to avoid an
// inspect on every request.
type containerProxy struct {
	docker *docker.Client
	mu     sync.Mutex
	cache  map[string]cachedTarget
}

type cachedTarget struct {
	url *url.URL
	exp time.Time
}

func newContainerProxy(c *docker.Client) *containerProxy {
	return &containerProxy{docker: c, cache: map[string]cachedTarget{}}
}

// target resolves the upstream URL (127.0.0.1:<hostPort>) for a container name,
// preferring the claudehub.web_port mapping, then any published tcp port.
func (p *containerProxy) target(ctx context.Context, name string) (*url.URL, error) {
	p.mu.Lock()
	if t, ok := p.cache[name]; ok && time.Now().Before(t.exp) {
		p.mu.Unlock()
		return t.url, nil
	}
	p.mu.Unlock()

	ins, err := p.docker.Inspect(ctx, name)
	if err != nil {
		return nil, err
	}
	hostPort := pickHostPort(ins)
	if hostPort == "" {
		return nil, fmt.Errorf("container %q has no published port", name)
	}
	u, err := url.Parse("http://127.0.0.1:" + hostPort)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.cache[name] = cachedTarget{url: u, exp: time.Now().Add(10 * time.Second)}
	p.mu.Unlock()
	return u, nil
}

func pickHostPort(ins docker.Inspect) string {
	if wp := ins.Config.Labels[docker.LabelWebPort]; wp != "" {
		if bind, ok := ins.NetworkSettings.Ports[wp+"/tcp"]; ok && len(bind) > 0 && bind[0].HostPort != "" {
			return bind[0].HostPort
		}
	}
	for key, bind := range ins.NetworkSettings.Ports {
		if strings.HasSuffix(key, "/tcp") && len(bind) > 0 && bind[0].HostPort != "" {
			return bind[0].HostPort
		}
	}
	return ""
}

// sandboxProxy handles /x/{name}/{rest...}: it strips the /x/{name} prefix and
// forwards the request (HTTP and WebSocket upgrades alike) to the container. It
// sits behind the auth middleware, so only authenticated hub users reach it.
func (s *Server) sandboxProxy(w http.ResponseWriter, r *http.Request) {
	if s.dockerProxy == nil {
		http.Error(w, "docker not configured", http.StatusServiceUnavailable)
		return
	}
	name := r.PathValue("name")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	target, err := s.dockerProxy.target(ctx, name)
	cancel()
	if err != nil {
		http.Error(w, "sandbox app unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	prefix := "/x/" + name
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, prefix)
			if pr.Out.URL.Path == "" {
				pr.Out.URL.Path = "/"
			}
			pr.Out.URL.RawPath = ""
			pr.Out.Host = target.Host
			pr.SetXForwarded()
			pr.Out.Header.Set("X-Forwarded-Prefix", prefix)
		},
	}
	rp.ServeHTTP(w, r)
}

// sandboxRedirect normalises /x/{name} to /x/{name}/ so relative asset paths in
// the proxied app resolve correctly.
func (s *Server) sandboxRedirect(w http.ResponseWriter, r *http.Request) {
	dest := "/x/" + r.PathValue("name") + "/"
	if r.URL.RawQuery != "" {
		dest += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, dest, http.StatusMovedPermanently)
}
