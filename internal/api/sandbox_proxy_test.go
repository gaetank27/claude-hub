package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"system/internal/docker"
)

// TestSandboxProxy exercises the /x/{name}/ reverse proxy against a real running
// container. It is an integration test: it skips cleanly when the Docker daemon
// or the expected smoke container is absent, so it never breaks a plain build.
func TestSandboxProxy(t *testing.T) {
	const name = "claudehub-smoke-nginx"
	c := docker.New("unix:///var/run/docker.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := c.Ping(ctx); err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
	if ins, err := c.Inspect(ctx, name); err != nil || !ins.State.Running {
		t.Skipf("smoke container %q not running", name)
	}

	srv := &Server{docker: c, dockerProxy: newContainerProxy(c), publicBaseURL: "http://x"}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/x/" + name + "/")
	if err != nil {
		t.Fatalf("proxy GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(strings.ToLower(string(body)), "nginx") {
		t.Fatalf("body does not look like the nginx welcome page: %.120s", body)
	}
}
