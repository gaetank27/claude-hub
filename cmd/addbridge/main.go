// Command addbridge registers a Proton Bridge IMAP connector instance in the Hub.
// Manual fallback: the Hub UI (Tools › Proton Bridge — mail) now does all of this.
// Run after Proton Bridge is installed (deploy/proton-bridge) and the account has
// been added with `proton-bridge-cli` (commands `login`, then `info`).
//
// Usage:
//
//	BRIDGE_PASSWORD=<bridge-imap-password> go run ./cmd/addbridge -email <your@proton.me>
//
// The password can also be passed with -password, but it is then visible in the
// process list.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"system/internal/auth"
)

func main() {
	password := flag.String("password", "", "Proton Bridge IMAP password (prefer the BRIDGE_PASSWORD env var)")
	email := flag.String("email", "", "Your Proton email address (used as IMAP username)")
	port := flag.String("port", "1143", "Bridge IMAP port (default 1143)")
	apiBase := flag.String("api", "http://127.0.0.1:8787", "Hub API base URL")
	secretFile := flag.String("secret", defaultSecretFile(), "Path to session.key")
	flag.Parse()

	if *password == "" {
		*password = os.Getenv("BRIDGE_PASSWORD")
	}
	if *password == "" {
		log.Fatal("Usage: BRIDGE_PASSWORD=<bridge-imap-password> go run ./cmd/addbridge -email <you@proton.me>\n\n" +
			"Get it with: proton-bridge-cli, then `info` (the Bridge-generated password, not your Proton one)")
	}
	if *email == "" {
		log.Fatal("-email is required (your Proton email address)")
	}

	secret, err := os.ReadFile(*secretFile)
	if err != nil {
		log.Fatalf("read session key %s: %v", *secretFile, err)
	}
	cookie := auth.MintSession(secret, "addbridge", "addbridge@hub.local", time.Hour)

	payload := map[string]any{
		"type": "imap",
		"name": "Proton Bridge",
		"config": map[string]string{
			"host":     "127.0.0.1",
			"port":     *port,
			"username": *email,
			"password": *password,
			"tls":      "false",
		},
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest(http.MethodPost, *apiBase+"/api/tools", bytes.NewReader(body))
	if err != nil {
		log.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("POST /api/tools: %v", err)
	}
	defer resp.Body.Close()

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)

	if resp.StatusCode != http.StatusCreated {
		log.Fatalf("failed: %s — %v", resp.Status, result)
	}
	fmt.Printf("✓ Connector created: id=%v name=%q type=imap\n", result["id"], result["name"])
	fmt.Printf("  IMAP host=127.0.0.1:%s user=%s\n", *port, *email)
	fmt.Println()
	fmt.Println("Bridge runs as a systemd user service (systemctl --user status proton-bridge).")
}

func defaultSecretFile() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "system", "session.key")
}
