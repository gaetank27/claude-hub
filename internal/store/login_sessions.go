package store

import (
	"time"

	"github.com/google/uuid"
)

// LoginSession is one signed-in device: the server-side half of a hub_session
// cookie. The cookie is only valid while this row exists.
type LoginSession struct {
	ID         string
	Sub        string
	Email      string
	Method     string // "local" | "oidc"
	UserAgent  string
	IP         string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  *time.Time // nil => "stay signed in"
}

var loginSessionCols = `id,sub,email,method,user_agent,ip,created_at,last_seen_at,expires_at`

func scanLoginSession(row interface{ Scan(...any) error }) (LoginSession, error) {
	var s LoginSession
	var created, seen, exp int64
	err := row.Scan(&s.ID, &s.Sub, &s.Email, &s.Method, &s.UserAgent, &s.IP, &created, &seen, &exp)
	s.CreatedAt, s.LastSeenAt = time.Unix(created, 0), time.Unix(seen, 0)
	if exp > 0 {
		t := time.Unix(exp, 0)
		s.ExpiresAt = &t
	}
	return s, err
}

// CreateLoginSession records a new sign-in. A zero expiresAt means it never
// expires.
func (db *DB) CreateLoginSession(sub, email, method, userAgent, ip string, expiresAt time.Time) (LoginSession, error) {
	now := time.Now()
	s := LoginSession{ID: uuid.NewString(), Sub: sub, Email: email, Method: method, UserAgent: userAgent, IP: ip, CreatedAt: now, LastSeenAt: now}
	var exp int64
	if !expiresAt.IsZero() {
		exp = expiresAt.Unix()
		s.ExpiresAt = &expiresAt
	}
	_, err := db.DB.Exec(
		`INSERT INTO login_sessions(`+loginSessionCols+`) VALUES(?,?,?,?,?,?,?,?,?)`,
		s.ID, s.Sub, s.Email, s.Method, s.UserAgent, s.IP, now.Unix(), now.Unix(), exp,
	)
	return s, err
}

// LoginSessionLive reports whether the sign-in exists and has not expired.
func (db *DB) LoginSessionLive(id string) bool {
	var n int
	err := db.DB.QueryRow(
		`SELECT COUNT(*) FROM login_sessions WHERE id=? AND (expires_at=0 OR expires_at>=?)`,
		id, time.Now().Unix(),
	).Scan(&n)
	return err == nil && n == 1
}

// TouchLoginSession records that the device was just seen.
func (db *DB) TouchLoginSession(id, userAgent, ip string) error {
	_, err := db.DB.Exec(
		`UPDATE login_sessions SET last_seen_at=?, ip=?, user_agent=CASE WHEN ?='' THEN user_agent ELSE ? END WHERE id=?`,
		time.Now().Unix(), ip, userAgent, userAgent, id)
	return err
}

// ListLoginSessions drops expired sign-ins and returns the rest, most recently
// seen first.
func (db *DB) ListLoginSessions() ([]LoginSession, error) {
	if _, err := db.DB.Exec(`DELETE FROM login_sessions WHERE expires_at>0 AND expires_at<?`, time.Now().Unix()); err != nil {
		return nil, err
	}
	rows, err := db.DB.Query(`SELECT ` + loginSessionCols + ` FROM login_sessions ORDER BY last_seen_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LoginSession
	for rows.Next() {
		s, err := scanLoginSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DeleteLoginSession revokes one sign-in.
func (db *DB) DeleteLoginSession(id string) error {
	res, err := db.DB.Exec(`DELETE FROM login_sessions WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteOtherLoginSessions revokes every sign-in except keepID.
func (db *DB) DeleteOtherLoginSessions(keepID string) error {
	_, err := db.DB.Exec(`DELETE FROM login_sessions WHERE id<>?`, keepID)
	return err
}
