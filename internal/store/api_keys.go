package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
)

// APIKeyPrefix marks a bearer token as a hub API key.
const APIKeyPrefix = "hubk_"

// APIKey is a credential for machine clients. The secret itself is never stored:
// only its SHA-256, so a key can be shown exactly once, at creation.
type APIKey struct {
	ID         string
	Name       string
	ProjectID  string // "" => full API access; otherwise scoped to that project's sessions
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

var apiKeyCols = `id,name,project_id,created_at,last_used_at`

func scanAPIKey(row interface{ Scan(...any) error }) (APIKey, error) {
	var k APIKey
	var created, used int64
	err := row.Scan(&k.ID, &k.Name, &k.ProjectID, &created, &used)
	k.CreatedAt = time.Unix(created, 0)
	if used > 0 {
		t := time.Unix(used, 0)
		k.LastUsedAt = &t
	}
	return k, err
}

func hashAPIKey(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// CreateAPIKey mints a key and returns it with its one-time secret.
func (db *DB) CreateAPIKey(name, projectID string) (APIKey, string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return APIKey{}, "", err
	}
	secret := APIKeyPrefix + base64.RawURLEncoding.EncodeToString(buf)
	k := APIKey{ID: uuid.NewString(), Name: name, ProjectID: projectID, CreatedAt: time.Now()}
	_, err := db.DB.Exec(
		`INSERT INTO api_keys(id,name,key_hash,project_id,created_at) VALUES(?,?,?,?,?)`,
		k.ID, k.Name, hashAPIKey(secret), k.ProjectID, k.CreatedAt.Unix(),
	)
	return k, secret, err
}

// LookupAPIKey resolves a presented secret to its key and records the use.
func (db *DB) LookupAPIKey(secret string) (APIKey, error) {
	row := db.DB.QueryRow(`SELECT `+apiKeyCols+` FROM api_keys WHERE key_hash=?`, hashAPIKey(secret))
	k, err := scanAPIKey(row)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	if err != nil {
		return APIKey{}, err
	}
	_, _ = db.DB.Exec(`UPDATE api_keys SET last_used_at=? WHERE id=?`, time.Now().Unix(), k.ID)
	return k, nil
}

// ListAPIKeys returns all keys, oldest first.
func (db *DB) ListAPIKeys() ([]APIKey, error) {
	rows, err := db.DB.Query(`SELECT ` + apiKeyCols + ` FROM api_keys ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// DeleteAPIKey revokes a key.
func (db *DB) DeleteAPIKey(id string) error {
	res, err := db.DB.Exec(`DELETE FROM api_keys WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
