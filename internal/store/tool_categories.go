package store

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/google/uuid"
)

// OtherToolCategory is the catch-all category: it always exists and cannot be
// deleted, so a tool whose category is unknown or removed always has a home.
const OtherToolCategory = "other"

// DefaultToolCategoryKeys is the set seeded once, in display order. The label of
// an unrenamed default is localised by the UI from its key.
var DefaultToolCategoryKeys = []string{"home", "messaging", "mail", "infra", "scripts", "assistant", OtherToolCategory}

// ToolCategory is a group of tools on the Tools page. Name is empty for an
// unrenamed default (the UI then shows the localised label of Key).
type ToolCategory struct {
	ID       string `json:"id"`
	Key      string `json:"key"`
	Name     string `json:"name"`
	Position int    `json:"position"`
}

const toolCatSeededMeta = "tool_categories_seeded"

// ListToolCategories returns the categories in display order. On first use it
// seeds the defaults (once: a default the user deletes is not recreated), and it
// guarantees "other" exists.
func (db *DB) ListToolCategories() ([]ToolCategory, error) {
	if _, err := db.GetAppMeta(toolCatSeededMeta); errors.Is(err, ErrNotFound) {
		for i, k := range DefaultToolCategoryKeys {
			if _, err := db.Exec(`INSERT OR IGNORE INTO tool_categories(id,key,name,position) VALUES(?,?,?,?)`, k, k, "", i); err != nil {
				return nil, err
			}
		}
		if err := db.SetAppMeta(toolCatSeededMeta, "1"); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tool_categories WHERE id=?`, OtherToolCategory).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		if _, err := db.Exec(`INSERT INTO tool_categories(id,key,name,position) VALUES(?,?,?, (SELECT COALESCE(MAX(position),0)+1 FROM tool_categories))`,
			OtherToolCategory, OtherToolCategory, ""); err != nil {
			return nil, err
		}
	}
	rows, err := db.Query(`SELECT id,key,name,position FROM tool_categories ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ToolCategory{}
	for rows.Next() {
		var c ToolCategory
		if err := rows.Scan(&c.ID, &c.Key, &c.Name, &c.Position); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CreateToolCategory appends a user category.
func (db *DB) CreateToolCategory(name string) (ToolCategory, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ToolCategory{}, errors.New("name is required")
	}
	if _, err := db.ListToolCategories(); err != nil { // make sure defaults are seeded first
		return ToolCategory{}, err
	}
	c := ToolCategory{ID: uuid.NewString(), Name: name}
	if err := db.QueryRow(`SELECT COALESCE(MAX(position),0)+1 FROM tool_categories`).Scan(&c.Position); err != nil {
		return c, err
	}
	_, err := db.Exec(`INSERT INTO tool_categories(id,key,name,position) VALUES(?,?,?,?)`, c.ID, "", c.Name, c.Position)
	return c, err
}

// RenameToolCategory sets a category's name. An empty name is allowed only for a
// default (it restores the localised label).
func (db *DB) RenameToolCategory(id, name string) error {
	name = strings.TrimSpace(name)
	var key string
	if err := db.QueryRow(`SELECT key FROM tool_categories WHERE id=?`, id).Scan(&key); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if name == "" && key == "" {
		return errors.New("name is required")
	}
	_, err := db.Exec(`UPDATE tool_categories SET name=? WHERE id=?`, name, id)
	return err
}

// ErrProtectedCategory is returned when deleting the catch-all category.
var ErrProtectedCategory = errors.New("the \"other\" category cannot be deleted")

// DeleteToolCategory removes a category; its tools go back to "automatic".
func (db *DB) DeleteToolCategory(id string) error {
	if id == OtherToolCategory {
		return ErrProtectedCategory
	}
	return db.WithFullSync(func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM tool_categories WHERE id=?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		_, err = tx.Exec(`UPDATE tools SET category_id='' WHERE category_id=?`, id)
		return err
	})
}

// ReorderToolCategories sets the display order: ids listed come first, in that
// order; categories not listed keep their relative order after them.
func (db *DB) ReorderToolCategories(ids []string) error {
	cur, err := db.ListToolCategories()
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, c := range cur {
		known[c.ID] = true
	}
	var order []string
	seen := map[string]bool{}
	for _, id := range ids {
		if known[id] && !seen[id] {
			seen[id] = true
			order = append(order, id)
		}
	}
	for _, c := range cur {
		if !seen[c.ID] {
			order = append(order, c.ID)
		}
	}
	return db.WithFullSync(func(tx *sql.Tx) error {
		for i, id := range order {
			if _, err := tx.Exec(`UPDATE tool_categories SET position=? WHERE id=?`, i, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetToolCategory moves a tool to a category ("" = automatic).
func (db *DB) SetToolCategory(toolID, categoryID string) error {
	if categoryID != "" {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM tool_categories WHERE id=?`, categoryID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
	}
	res, err := db.Exec(`UPDATE tools SET category_id=? WHERE id=?`, categoryID, toolID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
