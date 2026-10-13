// Package certman manages TLS certificates via acme.sh with DNS-01 challenge.
// It auto-installs acme.sh into ~/.acme.sh/ on first use.
package certman

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"system/internal/dnsprov"
)

// CertPaths holds the paths to the certificate and key files for a domain.
type CertPaths struct {
	FullChain string // PEM bundle (leaf + intermediates)
	Key       string // Private key
}

// EnsureCert issues or renews a certificate for domain using the DNS-01 challenge
// through the zone's DNS provider (ch.Hook = acme.sh dnsapi hook, ch.Env = its
// credentials, see dnsprov.Provider.Challenge).
// email is used for the ACME account registration (Let's Encrypt ToS).
func EnsureCert(ctx context.Context, domain string, ch dnsprov.Challenge, email string) (CertPaths, error) {
	acmeBin, err := ensureAcmeSh(ctx)
	if err != nil {
		return CertPaths{}, fmt.Errorf("install acme.sh: %w", err)
	}

	// If already deployed and readable by Caddy, return immediately.
	deployed := Paths(domain)
	if certExists(deployed.FullChain, deployed.Key) {
		return deployed, nil
	}

	certDir := acmeCertDir(domain)
	certFile := filepath.Join(certDir, "fullchain.cer")
	keyFile := filepath.Join(certDir, domain+".key")

	// If acme.sh cert exists but hasn't been deployed yet, deploy now.
	if certExists(certFile, keyFile) {
		return deployCerts(domain, certFile, keyFile)
	}

	// Register account (idempotent) and issue cert.
	env := append(os.Environ(), ch.Env...)

	// Use staging=false (production Let's Encrypt). For a first run set --staging
	// to avoid rate limits during development, then re-run without.
	args := []string{
		"--issue",
		"--dns", ch.Hook,
		"-d", domain,
		"--server", "letsencrypt",
		"--email", email,
		"--force", // re-issue if cert already exists but we ended up here
	}

	ctx2, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx2, acmeBin, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return CertPaths{}, fmt.Errorf("acme.sh --issue: %w\nOutput:\n%s", err, string(out))
	}

	if !certExists(certFile, keyFile) {
		return CertPaths{}, fmt.Errorf("acme.sh completed but cert files not found at %s", certDir)
	}

	// Copy certs to a world-readable location so Caddy (running as a different
	// user) can load them. The .acme.sh dir is 0700 and inaccessible to caddy.
	dest, err := deployCerts(domain, certFile, keyFile)
	if err != nil {
		return CertPaths{}, fmt.Errorf("deploy certs: %w", err)
	}
	return dest, nil
}

// Renew renews the certificate for domain if it expires within 30 days.
// Designed to be called from a periodic job.
func Renew(ctx context.Context, domain string, ch dnsprov.Challenge) error {
	acmeBin, err := ensureAcmeSh(ctx)
	if err != nil {
		return fmt.Errorf("acme.sh not found: %w", err)
	}

	env := append(os.Environ(), ch.Env...)
	ctx2, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx2, acmeBin, "--renew", "-d", domain, "--server", "letsencrypt")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	// exit 2 = cert not yet due for renewal — that's fine.
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 2 {
			return nil
		}
		return fmt.Errorf("acme.sh --renew: %w\nOutput:\n%s", err, string(out))
	}
	return nil
}

// Paths returns the deployed cert file paths for a domain.
func Paths(domain string) CertPaths {
	dir := certsDeployDir(domain)
	return CertPaths{
		FullChain: filepath.Join(dir, "fullchain.cer"),
		Key:       filepath.Join(dir, domain+".key"),
	}
}

// ---- internal ----

func acmeSHDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".acme.sh")
}

// acmeCertDir returns the directory where acme.sh stores certs for domain.
// acme.sh defaults to ECC (dir suffix _ecc); falls back to RSA path.
func acmeCertDir(domain string) string {
	base := acmeSHDir()
	eccDir := filepath.Join(base, domain+"_ecc")
	if _, err := os.Stat(eccDir); err == nil {
		return eccDir
	}
	return filepath.Join(base, domain)
}

func ensureAcmeSh(ctx context.Context) (string, error) {
	bin := filepath.Join(acmeSHDir(), "acme.sh")
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}

	// Download acme.sh script directly and install it non-interactively.
	script := filepath.Join(os.TempDir(), "acme.sh")
	dl := exec.CommandContext(ctx, "curl", "-fsSL",
		"https://raw.githubusercontent.com/acmesh-official/acme.sh/master/acme.sh",
		"-o", script)
	if out, err := dl.CombinedOutput(); err != nil {
		return "", fmt.Errorf("download acme.sh: %w\n%s", err, string(out))
	}
	_ = os.Chmod(script, 0o755)

	home, _ := os.UserHomeDir()
	install := exec.CommandContext(ctx, "bash", script, "--install",
		"--home", filepath.Join(home, ".acme.sh"),
		"--no-cron")
	install.Dir = os.TempDir()
	if out, err := install.CombinedOutput(); err != nil {
		return "", fmt.Errorf("install acme.sh: %w\n%s", err, string(out))
	}

	if _, err := os.Stat(bin); err != nil {
		return "", fmt.Errorf("acme.sh not found after install at %s", bin)
	}
	return bin, nil
}

func certExists(certFile, keyFile string) bool {
	for _, f := range []string{certFile, keyFile} {
		if _, err := os.Stat(f); err != nil {
			return false
		}
	}
	return true
}

// certsDeployDir returns the directory where deployed (world-readable) certs live.
func certsDeployDir(domain string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "system", "certs", domain)
}

// deployCerts copies cert+key to a world-readable location for Caddy.
func deployCerts(domain, certFile, keyFile string) (CertPaths, error) {
	dir := certsDeployDir(domain)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return CertPaths{}, err
	}
	destCert := filepath.Join(dir, "fullchain.cer")
	destKey := filepath.Join(dir, domain+".key")
	if err := copyFile(certFile, destCert, 0o644); err != nil {
		return CertPaths{}, err
	}
	if err := copyFile(keyFile, destKey, 0o644); err != nil {
		return CertPaths{}, err
	}
	return CertPaths{FullChain: destCert, Key: destKey}, nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, mode)
}

// AcmeOutput sanitizes acme.sh output for display (strips ANSI escape codes).
func AcmeOutput(raw []byte) string {
	s := string(raw)
	// strip ANSI color sequences
	for strings.Contains(s, "\x1b[") {
		start := strings.Index(s, "\x1b[")
		end := strings.IndexAny(s[start:], "ABCDEFGHJKSTfmnsulhp")
		if end < 0 {
			break
		}
		s = s[:start] + s[start+end+1:]
	}
	return strings.TrimSpace(s)
}
