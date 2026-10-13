// deploy-webhook: tiny webhook receiver for GitHub push events.
// Validates X-Hub-Signature-256, then runs /opt/site/deploy.sh on push to main.
// Secret read from /opt/site/webhook.secret at startup.
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

var secret string

func main() {
	raw, err := os.ReadFile("/opt/site/webhook.secret")
	if err != nil {
		log.Fatalf("read secret: %v", err)
	}
	secret = strings.TrimSpace(string(raw))

	http.HandleFunc("/", handle)
	log.Println("deploy-webhook listening on :9000")
	log.Fatal(http.ListenAndServe(":9000", nil))
}

func handle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		w.WriteHeader(http.StatusOK)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}

	if !validSig(body, r.Header.Get("X-Hub-Signature-256")) {
		log.Printf("invalid signature from %s", r.RemoteAddr)
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

	go func() {
		log.Println("push to main — running deploy.sh")
		cmd := exec.Command("/opt/site/deploy.sh")
		out, err := cmd.CombinedOutput()
		if err != nil {
			log.Printf("deploy failed: %v\n%s", err, out)
		} else {
			log.Printf("deploy OK")
		}
	}()
}

func validSig(body []byte, sig string) bool {
	if !strings.HasPrefix(sig, "sha256=") {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(sig), []byte(expected))
}
