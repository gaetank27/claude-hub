// Package store wraps the SQLite database holding the hub's derived state.
// It is opened in WAL mode so it survives a hard kill: committed transactions
// stay durable and the DB reopens cleanly without manual recovery.
package store

import (
	"database/sql"
	_ "embed"
	"fmt"

	_ "modernc.org/sqlite" // pure-Go driver, no cgo — keeps the binary self-contained
)

//go:embed schema.sql
var schemaSQL string

// DB is the handle to the hub store.
type DB struct {
	*sql.DB
}

// Open opens (creating if needed) the SQLite database at path, applies the
// durability PRAGMAs and runs the idempotent schema migration.
//
// journal_mode=WAL + synchronous=NORMAL gives durability across process crashes
// (kill -9) while staying fast. Critical writes bump synchronous to FULL via
// WithFullSync to also survive an OS-level power loss.
func Open(path string) (*DB, error) {
	// _txlock=immediate avoids surprise write-deadlocks under concurrency.
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_txlock=immediate", path)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// modernc serializes connections per-handle; a single conn keeps PRAGMA
	// state coherent and is plenty for a single-node hub.
	sqlDB.SetMaxOpenConns(1)

	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
	}
	for _, p := range pragmas {
		if _, err := sqlDB.Exec(p); err != nil {
			sqlDB.Close()
			return nil, fmt.Errorf("pragma %q: %w", p, err)
		}
	}

	if _, err := sqlDB.Exec(schemaSQL); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	if err := migrate(sqlDB); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("migrate columns: %w", err)
	}

	return &DB{sqlDB}, nil
}

// migrate brings an older database up to the current schema. CREATE TABLE IF NOT
// EXISTS in schema.sql only covers fresh installs; columns added later must be
// patched onto existing tables. SQLite has no ADD COLUMN IF NOT EXISTS, so we
// probe table_info and add only what is missing — making this idempotent across
// boots.
func migrate(db *sql.DB) error {
	want := map[string]map[string]string{
		"sessions": {
			"comment":           "TEXT NOT NULL DEFAULT ''",
			"pr_url":            "TEXT NOT NULL DEFAULT ''",
			"claude_account_id": "TEXT NOT NULL DEFAULT ''",
			"model":             "TEXT NOT NULL DEFAULT ''",
			"source":            "TEXT NOT NULL DEFAULT ''",
			"archived":          "INTEGER NOT NULL DEFAULT 0",
			"stopped_at":        "TEXT",
		},
		"projects": {
			"starred":            "INTEGER NOT NULL DEFAULT 0",
			"category":           "TEXT NOT NULL DEFAULT ''",
			"hidden":             "INTEGER NOT NULL DEFAULT 0",
			"archived":           "INTEGER NOT NULL DEFAULT 0",
			"comment":            "TEXT NOT NULL DEFAULT ''",
			"claude_account_id":  "TEXT NOT NULL DEFAULT ''",
			"model":              "TEXT NOT NULL DEFAULT ''",
			"show_auto_sessions": "INTEGER NOT NULL DEFAULT 0",
		},
		"usage_snapshots": {
			"account_id":        "TEXT NOT NULL DEFAULT ''",
			"session_reset":     "TEXT",
			"week_reset":        "TEXT",
			"week_opus_reset":   "TEXT",
			"week_sonnet_reset": "TEXT",
		},
		"rag_documents": {
			"collection_id": "TEXT NOT NULL DEFAULT ''",
			"content_hash":  "TEXT NOT NULL DEFAULT ''",
		},
		"rag_collections": {
			"project_id": "TEXT NOT NULL DEFAULT ''",
			"category":   "TEXT NOT NULL DEFAULT ''",
		},
		"scheduled_tasks": {
			"one_shot":    "INTEGER NOT NULL DEFAULT 0",
			"command":     "TEXT NOT NULL DEFAULT ''",
			"last_status": "TEXT NOT NULL DEFAULT ''",
			"last_output": "TEXT NOT NULL DEFAULT ''",
		},
		"assistant_messages": {
			"held_until": "INTEGER NOT NULL DEFAULT 0",
			"held_tool":  "TEXT NOT NULL DEFAULT ''",
			"held_jid":   "TEXT NOT NULL DEFAULT ''",
		},
		"assistant_attachments": {
			"direction": "TEXT NOT NULL DEFAULT 'in'", // in = sent to the assistant, out = image sent by it
			"width":     "INTEGER NOT NULL DEFAULT 0",
			"height":    "INTEGER NOT NULL DEFAULT 0",
			"caption":   "TEXT NOT NULL DEFAULT ''",
		},
		"tools": {
			"category_id": "TEXT NOT NULL DEFAULT ''", // '' = automatic (see tool_categories)
		},
		"domains": {
			"container_name": "TEXT NOT NULL DEFAULT ''",
		},
		"script_keys": {
			"allowed_commands": "TEXT NOT NULL DEFAULT ''",   // comma-separated command names the script may be sent
			"allow_images":     "INTEGER NOT NULL DEFAULT 0", // 1 = the key may upload screenshots
		},
	}
	for table, cols := range want {
		have, err := tableColumns(db, table)
		if err != nil {
			return err
		}
		for col, def := range cols {
			if have[col] {
				continue
			}
			if _, err := db.Exec("ALTER TABLE " + table + " ADD COLUMN " + col + " " + def); err != nil {
				return fmt.Errorf("add %s.%s: %w", table, col, err)
			}
		}
	}
	// Indexes on migrate-added columns must be created here, after the column is
	// guaranteed to exist (schema.sql runs before migrate, so it cannot reference
	// collection_id on a pre-existing rag_documents table).
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_rag_documents_collection ON rag_documents(collection_id)`); err != nil {
		return fmt.Errorf("index rag_documents.collection_id: %w", err)
	}
	return nil
}

func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := make(map[string]bool)
	for rows.Next() {
		var (
			cid         int
			name, ctype string
			notnull, pk int
			dflt        sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// WithFullSync runs fn inside a transaction committed with synchronous=FULL,
// for state transitions we must not lose even on power loss (e.g. session
// creation and lifecycle changes).
func (db *DB) WithFullSync(fn func(tx *sql.Tx) error) error {
	if _, err := db.Exec("PRAGMA synchronous=FULL"); err != nil {
		return err
	}
	defer db.Exec("PRAGMA synchronous=NORMAL")

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
