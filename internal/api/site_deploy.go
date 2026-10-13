package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

const (
	siteDeployKeyPath     = "/home/claude/.local/share/system/site-deploy-key"
	siteWebhookSecretPath = "/home/claude/.local/share/system/deploy-webhook.secret"
	siteHostKeyPath       = "/home/claude/.local/share/system/site-host-key.pub"
	siteSSHHost           = "192.0.2.12:22"
	siteDeployScript      = "/opt/site/deploy.sh"
	siteDeployTimeout     = 20 * time.Minute
)

// siteDeploy serializes deploys and coalesces bursts of pushes: at most one
// deploy runs at a time, and any number of pushes received meanwhile trigger a
// single follow-up run.
var siteDeploy struct {
	mu      sync.Mutex
	running bool
	pending bool
}

// siteDeployWebhook receives GitHub push webhooks and triggers a deploy on
// LXC 109 (site) asynchronously. The endpoint is exempt from Hub auth;
// it validates via HMAC-SHA256 (X-Hub-Signature-256) instead.
func (s *Server) siteDeployWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}

	raw, err := os.ReadFile(siteWebhookSecretPath)
	secret := strings.TrimSpace(string(raw))
	if err != nil || secret == "" {
		// An empty secret would make the HMAC forgeable by anyone: refuse.
		log.Printf("deploy-webhook: secret unavailable or empty: %v", err)
		http.Error(w, "webhook not configured", http.StatusServiceUnavailable)
		return
	}

	if !verifyGitHubSig(body, secret, r.Header.Get("X-Hub-Signature-256")) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	if r.Header.Get("X-GitHub-Event") != "push" {
		w.WriteHeader(http.StatusOK)
		return
	}

	var payload struct {
		Ref string `json:"ref"`
	}
	json.Unmarshal(body, &payload)
	if payload.Ref != "refs/heads/main" {
		w.WriteHeader(http.StatusOK)
		return
	}

	w.WriteHeader(http.StatusOK)
	triggerSiteDeploy()
}

// triggerSiteDeploy starts a deploy in the background, or flags a follow-up
// run when one is already in flight.
func triggerSiteDeploy() {
	siteDeploy.mu.Lock()
	defer siteDeploy.mu.Unlock()
	if siteDeploy.running {
		siteDeploy.pending = true
		log.Printf("deploy-webhook: deploy already running — follow-up queued")
		return
	}
	siteDeploy.running = true
	go func() {
		for {
			log.Printf("deploy-webhook: push to main — triggering deploy")
			if err := sshRunDeploy(); err != nil {
				log.Printf("deploy-webhook: deploy failed: %v", err)
			} else {
				log.Printf("deploy-webhook: deploy OK")
			}
			siteDeploy.mu.Lock()
			if !siteDeploy.pending {
				siteDeploy.running = false
				siteDeploy.mu.Unlock()
				return
			}
			siteDeploy.pending = false
			siteDeploy.mu.Unlock()
		}
	}()
}

func verifyGitHubSig(body []byte, secret, sig string) bool {
	if secret == "" || !strings.HasPrefix(sig, "sha256=") {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(sig), []byte(expected))
}

// siteHostKeyCallback pins the LXC's SSH host key (trust on first use): the
// first connection records the key in siteHostKeyPath, every later one must
// present the same key. cmd/create-lxc removes the file when it recreates the
// container, since a fresh LXC gets a fresh host key.
func siteHostKeyCallback(_ string, _ net.Addr, key gossh.PublicKey) error {
	pinned, err := os.ReadFile(siteHostKeyPath)
	if errors.Is(err, os.ErrNotExist) {
		log.Printf("deploy-webhook: pinning host key %s", gossh.FingerprintSHA256(key))
		return os.WriteFile(siteHostKeyPath, gossh.MarshalAuthorizedKey(key), 0600)
	}
	if err != nil {
		return fmt.Errorf("read pinned host key: %w", err)
	}
	want, _, _, _, err := gossh.ParseAuthorizedKey(pinned)
	if err != nil {
		return fmt.Errorf("parse pinned host key: %w", err)
	}
	if !bytes.Equal(want.Marshal(), key.Marshal()) {
		return fmt.Errorf("host key mismatch: got %s, pinned %s (remove %s if the LXC was recreated)",
			gossh.FingerprintSHA256(key), gossh.FingerprintSHA256(want), siteHostKeyPath)
	}
	return nil
}

func sshRunDeploy() error {
	keyData, err := os.ReadFile(siteDeployKeyPath)
	if err != nil {
		return fmt.Errorf("read deploy key: %w", err)
	}
	signer, err := gossh.ParsePrivateKey(keyData)
	if err != nil {
		return fmt.Errorf("parse deploy key: %w", err)
	}
	cfg := &gossh.ClientConfig{
		User:            "root",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: siteHostKeyCallback,
		Timeout:         30 * time.Second,
	}
	client, err := gossh.Dial("tcp", siteSSHHost, cfg)
	if err != nil {
		return fmt.Errorf("ssh dial: %w", err)
	}
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("ssh session: %w", err)
	}
	defer sess.Close()

	// Closing the client aborts a deploy that hangs past the timeout.
	timer := time.AfterFunc(siteDeployTimeout, func() { client.Close() })
	defer timer.Stop()
	return sess.Run(siteDeployScript)
}
