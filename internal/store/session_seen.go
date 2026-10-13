package store

import (
	"database/sql"
	"errors"
	"time"
)

// SeedSessionSeen marks every pre-existing session as read, once, so enabling the
// unread feature does not flag the whole history.
func (db *DB) SeedSessionSeen() error {
	if v, _ := db.GetAppMeta("session_seen_seeded"); v != "" {
		return nil
	}
	now := time.Now().UTC().Format(rfc)
	if _, err := db.Exec(`INSERT OR IGNORE INTO session_seen(session_id, seen_at) SELECT id, ? FROM sessions`, now); err != nil {
		return err
	}
	return db.SetAppMeta("session_seen_seeded", "1")
}

// MarkSessionSeen records that the user has now seen the session's latest output.
func (db *DB) MarkSessionSeen(sessionID string) error {
	_, err := db.Exec(`INSERT INTO session_seen(session_id, seen_at) VALUES(?,?)
		ON CONFLICT(session_id) DO UPDATE SET seen_at=excluded.seen_at`,
		sessionID, time.Now().UTC().Format(rfc))
	return err
}

// SessionSeenAt returns when the session was last seen; ok=false if never.
func (db *DB) SessionSeenAt(sessionID string) (t time.Time, ok bool) {
	var s string
	err := db.QueryRow(`SELECT seen_at FROM session_seen WHERE session_id=?`, sessionID).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return time.Time{}, false
	}
	t, err = time.Parse(rfc, s)
	return t, err == nil
}
