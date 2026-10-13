package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Client talks to the Docker Engine API over a unix socket. It carries no global
// timeout: callers pass a context deadline suited to the operation (short for
// list/stats/actions, longer for image pulls).
type Client struct {
	http *http.Client
	base string
}

// New returns a client for the given docker host. Only the unix:// scheme is
// supported (the local daemon socket); the host part of the URL is irrelevant
// because every request is dialed over the socket.
func New(host string) *Client {
	socket := strings.TrimPrefix(host, "unix://")
	if socket == "" {
		socket = "/var/run/docker.sock"
	}
	return &Client{
		base: "http://docker",
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socket)
				},
			},
		},
	}
}

// APIError carries the daemon's HTTP status so callers can branch on it (e.g.
// treat 409 "already exists" as success).
type APIError struct {
	Status int
	Msg    string
}

func (e *APIError) Error() string {
	if e.Msg != "" {
		return fmt.Sprintf("docker: %s (%d)", e.Msg, e.Status)
	}
	return fmt.Sprintf("docker: HTTP %d", e.Status)
}

// do issues a request and decodes a JSON response into out (when non-nil).
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &APIError{Status: resp.StatusCode, Msg: dockerMessage(msg)}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// dockerMessage extracts the daemon's {"message":"..."} error text when present.
func dockerMessage(b []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &e) == nil && e.Message != "" {
		return e.Message
	}
	return strings.TrimSpace(string(b))
}

// Ping returns the daemon version, or an error if it is unreachable.
func (c *Client) Ping(ctx context.Context) (string, error) {
	var v struct {
		Version string `json:"Version"`
	}
	if err := c.do(ctx, http.MethodGet, "/version", nil, &v); err != nil {
		return "", err
	}
	return v.Version, nil
}

// List returns containers (including stopped) matching the given label filters,
// e.g. List(ctx, "claudehub.environment=sandbox").
func (c *Client) List(ctx context.Context, labels ...string) ([]Container, error) {
	q := url.Values{"all": {"1"}}
	if len(labels) > 0 {
		fb, _ := json.Marshal(map[string][]string{"label": labels})
		q.Set("filters", string(fb))
	}
	var out []Container
	err := c.do(ctx, http.MethodGet, "/containers/json?"+q.Encode(), nil, &out)
	return out, err
}

// Inspect returns the detailed view of a container.
func (c *Client) Inspect(ctx context.Context, id string) (Inspect, error) {
	var out Inspect
	err := c.do(ctx, http.MethodGet, "/containers/"+id+"/json", nil, &out)
	return out, err
}

// Stats returns a single resource sample (stream=false yields one reading with
// precpu populated, so CPU% is computable from one call).
func (c *Client) Stats(ctx context.Context, id string) (Stats, error) {
	var out Stats
	err := c.do(ctx, http.MethodGet, "/containers/"+id+"/stats?stream=false", nil, &out)
	return out, err
}

// Start, Stop and Restart drive a container's lifecycle. Stop/Restart let the
// daemon apply its default grace period before SIGKILL.
func (c *Client) Start(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+id+"/start", nil, nil)
}
func (c *Client) Stop(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+id+"/stop", nil, nil)
}
func (c *Client) Restart(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+id+"/restart", nil, nil)
}

// Remove force-removes a container (stops it first if running).
func (c *Client) Remove(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/containers/"+id+"?force=1", nil, nil)
}

// EnsureNetwork creates a bridge network, treating "already exists" as success.
func (c *Client) EnsureNetwork(ctx context.Context, name string) error {
	err := c.do(ctx, http.MethodPost, "/networks/create",
		map[string]any{"Name": name, "Driver": "bridge"}, nil)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict {
		return nil
	}
	return err
}

// Pull fetches an image, draining the progress stream until the pull completes.
// Give it a generous context deadline (downloads can be slow).
func (c *Client) Pull(ctx context.Context, image string) error {
	repo, tag := splitImage(image)
	q := url.Values{"fromImage": {repo}, "tag": {tag}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/images/create?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &APIError{Status: resp.StatusCode, Msg: dockerMessage(msg)}
	}
	_, err = io.Copy(io.Discard, resp.Body) // block until the pull finishes
	return err
}

// createBody and friends mirror the /containers/create request schema.
type createBody struct {
	Image        string              `json:"Image"`
	Env          []string            `json:"Env,omitempty"`
	Labels       map[string]string   `json:"Labels,omitempty"`
	ExposedPorts map[string]struct{} `json:"ExposedPorts,omitempty"`
	HostConfig   hostConfig          `json:"HostConfig"`
}

type hostConfig struct {
	PortBindings  map[string][]portBinding `json:"PortBindings,omitempty"`
	NetworkMode   string                   `json:"NetworkMode,omitempty"`
	RestartPolicy restartPolicy            `json:"RestartPolicy"`
}

type portBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"` // "" => ephemeral host port
}

type restartPolicy struct {
	Name string `json:"Name"`
}

// Create creates a container from the spec and returns its id (not started).
func (c *Client) Create(ctx context.Context, spec RunSpec) (string, error) {
	exposed := map[string]struct{}{}
	bindings := map[string][]portBinding{}
	for _, p := range spec.Ports {
		key := fmt.Sprintf("%d/%s", p.Container, proto(p.Proto))
		exposed[key] = struct{}{}
		bindings[key] = []portBinding{{HostIP: spec.PublishHost, HostPort: ""}}
	}
	body := createBody{
		Image:        spec.Image,
		Env:          envSlice(spec.Env),
		Labels:       spec.Labels,
		ExposedPorts: exposed,
		HostConfig: hostConfig{
			PortBindings:  bindings,
			NetworkMode:   spec.Network,
			RestartPolicy: restartPolicy{Name: spec.RestartPolicy},
		},
	}
	path := "/containers/create"
	if spec.Name != "" {
		path += "?name=" + url.QueryEscape(spec.Name)
	}
	var res struct {
		ID string `json:"Id"`
	}
	if err := c.do(ctx, http.MethodPost, path, body, &res); err != nil {
		return "", err
	}
	return res.ID, nil
}

func proto(p string) string {
	if p == "udp" {
		return "udp"
	}
	return "tcp"
}

func envSlice(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

// splitImage separates an image reference into repository and tag, defaulting to
// "latest". A trailing ":tag" only counts when it comes after the last "/", so a
// registry "host:port/repo" is not mistaken for a tag.
func splitImage(image string) (repo, tag string) {
	i := strings.LastIndex(image, ":")
	if i < 0 || i < strings.LastIndex(image, "/") {
		return image, "latest"
	}
	return image[:i], image[i+1:]
}
