package session

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"system/internal/store"
)

// refreshSkew is how far before expiry a token is considered due for refresh, so
// the meter never hits the exact expiry instant.
const refreshSkew = 10 * time.Minute

// RefreshIdleTokens refreshes the OAuth access token of every connected account
// whose on-disk token is expired or about to expire (and that has a refresh
// token). It deliberately does NOT look at running sessions: an account that is
// actively making API calls has the CLI refreshing its token in place, so its
// on-disk token is never observed expiring — `tokenDueForRefresh` is therefore
// itself the idle detector. A token found expired on disk proves nothing is
// keeping it warm (a running-but-idle session does not refresh it), so the hub
// can safely refresh it without racing the CLI. Best-effort: every failure is
// logged and skipped, never fatal.
func (m *Manager) RefreshIdleTokens(ctx context.Context) {
	if m.cfg.OAuthTokenURL == "" {
		return
	}
	accounts, err := m.db.ListClaudeAccounts()
	if err != nil {
		return
	}
	for _, a := range accounts {
		if a.Kind != store.AccountKindOAuth || a.Status != store.AccountConnected {
			continue
		}
		due, err := tokenDueForRefresh(credentialsPath(a))
		if err != nil || !due {
			continue
		}
		if err := m.refreshAccountToken(ctx, a); err != nil {
			log.Printf("token refresh %s (%s): %v", a.ID, a.Label, err)
		} else {
			log.Printf("token refresh %s (%s): ok", a.ID, a.Label)
		}
	}
}

// oauthCreds is the on-disk credentials file shape. We decode into a generic map
// to preserve fields we don't manage (subscriptionType, rateLimitTier, scopes…)
// and only rewrite the three token fields.
type oauthCreds struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    int64  `json:"expiresAt"` // unix ms
}

// tokenDueForRefresh reports whether the credentials file has a refresh token and
// an access token expiring within refreshSkew (or already expired).
func tokenDueForRefresh(path string) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var doc struct {
		OAuth oauthCreds `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return false, err
	}
	if doc.OAuth.RefreshToken == "" {
		return false, nil // can't refresh without a refresh token
	}
	if doc.OAuth.ExpiresAt == 0 {
		return false, nil // unknown expiry — leave it alone
	}
	deadline := time.UnixMilli(doc.OAuth.ExpiresAt).Add(-refreshSkew)
	return time.Now().After(deadline), nil
}

// tokenResponse is the OAuth token endpoint's reply to a refresh_token grant.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"` // seconds
}

// refreshAccountToken exchanges the account's stored refresh token for a fresh
// access token and writes the updated credentials back atomically, preserving
// every other field in the file. The refresh token is only consumed by the
// upstream on success, so a failed call leaves the stored credentials intact.
func (m *Manager) refreshAccountToken(ctx context.Context, a store.ClaudeAccount) error {
	path := credentialsPath(a)
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read credentials: %w", err)
	}
	// Preserve the whole document; we only touch claudeAiOauth's token fields.
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse credentials: %w", err)
	}
	var oauth map[string]json.RawMessage
	if err := json.Unmarshal(doc["claudeAiOauth"], &oauth); err != nil {
		return fmt.Errorf("parse claudeAiOauth: %w", err)
	}
	var cur oauthCreds
	_ = json.Unmarshal(doc["claudeAiOauth"], &cur)
	if cur.RefreshToken == "" {
		return fmt.Errorf("no refresh token")
	}

	tok, err := m.doRefresh(ctx, cur.RefreshToken)
	if err != nil {
		return err
	}

	// Merge the new token fields back, keeping everything else as-is. The upstream
	// rotates the refresh token; fall back to the previous one if it omits it.
	newRefresh := tok.RefreshToken
	if newRefresh == "" {
		newRefresh = cur.RefreshToken
	}
	expiresAt := time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).UnixMilli()
	oauth["accessToken"], _ = json.Marshal(tok.AccessToken)
	oauth["refreshToken"], _ = json.Marshal(newRefresh)
	oauth["expiresAt"], _ = json.Marshal(expiresAt)
	doc["claudeAiOauth"], _ = json.Marshal(oauth)

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, out, 0o600)
}

// doRefresh performs the refresh_token OAuth grant against the configured token
// endpoint with the Claude Code public client id.
func (m *Manager) doRefresh(ctx context.Context, refreshToken string) (tokenResponse, error) {
	body, _ := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     m.cfg.OAuthClientID,
	})
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.cfg.OAuthTokenURL, bytes.NewReader(body))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return tokenResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return tokenResponse{}, fmt.Errorf("token endpoint: HTTP %d", resp.StatusCode)
	}
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return tokenResponse{}, fmt.Errorf("decode token response: %w", err)
	}
	if tr.AccessToken == "" {
		return tokenResponse{}, fmt.Errorf("token endpoint returned no access_token")
	}
	return tr, nil
}

// writeFileAtomic writes data to a temp file in the same dir then renames it over
// path, so a crash mid-write never leaves a truncated credentials file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".credentials-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
