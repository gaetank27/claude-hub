package store

import (
	"database/sql"
	"errors"
	"time"
)

// Tool connection statuses.
const (
	ToolConnected    = "connected"
	ToolDisconnected = "disconnected"
	ToolError        = "error"
)

// Tool mirrors a row of the tools table: one configured external integration.
// ConfigEnc is the AES-GCM encrypted JSON of the connector's field map; the API
// layer decrypts it only when needed (test/use) and never returns secrets.
type Tool struct {
	ID           string
	Type         string
	Name         string
	Status       string
	Enabled      bool
	Error        string
	ConfigEnc    string
	LastTestedAt *time.Time
	CreatedAt    time.Time
	CategoryID   string // '' = automatic (default category of the connector type)
}

const toolCols = `id,type,name,status,enabled,error,config_enc,last_tested_at,created_at,category_id`

// CreateTool inserts a configured tool durably (FULL sync): it is a root entity
// holding credentials that must survive a crash.
func (db *DB) CreateTool(t Tool) error {
	return db.WithFullSync(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO tools(id,type,name,status,enabled,error,config_enc,created_at)
			 VALUES(?,?,?,?,?,?,?,?)`,
			t.ID, t.Type, t.Name, t.Status, boolToInt(t.Enabled), t.Error, t.ConfigEnc,
			t.CreatedAt.Format(rfc),
		)
		return err
	})
}

func (db *DB) GetTool(id string) (Tool, error) {
	row := db.QueryRow(`SELECT `+toolCols+` FROM tools WHERE id=?`, id)
	return scanTool(row)
}

func (db *DB) ListTools() ([]Tool, error) {
	rows, err := db.Query(`SELECT ` + toolCols + ` FROM tools ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tool
	for rows.Next() {
		t, err := scanTool(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpdateTool persists edits to a tool's label, config and enabled flag durably
// (the config carries credentials).
func (db *DB) UpdateTool(id, name, configEnc string, enabled bool) error {
	return db.WithFullSync(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`UPDATE tools SET name=?, config_enc=?, enabled=? WHERE id=?`,
			name, configEnc, boolToInt(enabled), id)
		return err
	})
}

// SetToolStatus records the outcome of a connection test/use. NORMAL sync —
// purely derived, re-establishable by testing again.
func (db *DB) SetToolStatus(id, status, errMsg string, testedAt *time.Time) error {
	var ts any
	if testedAt != nil {
		ts = testedAt.Format(rfc)
	}
	_, err := db.Exec(`UPDATE tools SET status=?, error=?, last_tested_at=? WHERE id=?`,
		status, errMsg, ts, id)
	return err
}

func (db *DB) DeleteTool(id string) error {
	return db.WithFullSync(func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM tools WHERE id=?`, id)
		return err
	})
}

func scanTool(s scanner) (Tool, error) {
	var (
		t       Tool
		enabled int
		created string
		tested  sql.NullString
	)
	err := s.Scan(&t.ID, &t.Type, &t.Name, &t.Status, &enabled, &t.Error, &t.ConfigEnc,
		&tested, &created, &t.CategoryID)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	t.Enabled = enabled != 0
	t.CreatedAt, _ = time.Parse(rfc, created)
	if tested.Valid {
		if tt, err := time.Parse(rfc, tested.String); err == nil {
			t.LastTestedAt = &tt
		}
	}
	return t, nil
}
