package store

import (
	"database/sql"
	"time"
)

// User is a local hub operator authenticated by login/password + TOTP. It is not
// a Claude subscription (claude_accounts) nor an OIDC identity (the IdP's
// concern). Roles are reserved for later; today every user is an admin.
type User struct {
	ID            string
	Username      string
	Email         string
	PasswordHash  string // bcrypt
	TOTPSecretEnc string // AES-256-GCM ciphertext of the base32 TOTP secret
	TOTPConfirmed bool
	Role          string
	CreatedAt     time.Time
}

const userCols = `id,username,email,password_hash,totp_secret_enc,totp_confirmed,role,created_at`

// CreateUser inserts a user durably (FULL sync): it is a root identity that must
// survive power loss the moment it is created during setup.
func (db *DB) CreateUser(u User) error {
	return db.WithFullSync(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO users(`+userCols+`) VALUES(?,?,?,?,?,?,?,?)`,
			u.ID, u.Username, u.Email, u.PasswordHash, u.TOTPSecretEnc,
			b2i(u.TOTPConfirmed), u.Role, u.CreatedAt.Format(rfc),
		)
		return err
	})
}

func (db *DB) GetUserByUsername(username string) (User, error) {
	row := db.QueryRow(`SELECT `+userCols+` FROM users WHERE username=?`, username)
	return scanUser(row)
}

func (db *DB) GetUserByID(id string) (User, error) {
	row := db.QueryRow(`SELECT `+userCols+` FROM users WHERE id=?`, id)
	return scanUser(row)
}

// CountUsers reports how many local users exist; 0 means the setup wizard has
// never created an admin (first boot).
func (db *DB) CountUsers() (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// SetUserTOTP stores a freshly generated, not-yet-confirmed TOTP secret,
// resetting the confirmed flag so the user must validate a code again.
func (db *DB) SetUserTOTP(id, secretEnc string) error {
	_, err := db.Exec(`UPDATE users SET totp_secret_enc=?, totp_confirmed=0 WHERE id=?`, secretEnc, id)
	return err
}

// ConfirmUserTOTP marks the user's TOTP enrolment as validated (a first code
// matched). Durable: losing it would lock the operator out of 2FA.
func (db *DB) ConfirmUserTOTP(id string) error {
	return db.WithFullSync(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE users SET totp_confirmed=1 WHERE id=?`, id)
		return err
	})
}

// DeleteUnconfirmedUsers removes accounts that never validated their TOTP. It
// lets the first-boot wizard's "create admin" step be re-run safely (e.g. after
// a page reload) without ever touching a fully enrolled, confirmed admin.
func (db *DB) DeleteUnconfirmedUsers() error {
	_, err := db.Exec(`DELETE FROM users WHERE totp_confirmed=0`)
	return err
}

func scanUser(s scanner) (User, error) {
	var (
		u         User
		confirmed int
		created   string
	)
	err := s.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.TOTPSecretEnc, &confirmed, &u.Role, &created)
	if err == sql.ErrNoRows {
		return u, ErrNotFound
	}
	if err != nil {
		return u, err
	}
	u.TOTPConfirmed = confirmed == 1
	u.CreatedAt, _ = time.Parse(rfc, created)
	return u, nil
}

// GetAppMeta reads an application-metadata value, returning ErrNotFound when the
// key was never set.
func (db *DB) GetAppMeta(key string) (string, error) {
	var v string
	err := db.QueryRow(`SELECT value FROM app_meta WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", ErrNotFound
	}
	return v, err
}

// SetAppMeta upserts an application-metadata value.
func (db *DB) SetAppMeta(key, value string) error {
	_, err := db.Exec(
		`INSERT INTO app_meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		key, value,
	)
	return err
}
