package store

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// ProjectAccess is the per-project access policy: which configured tools the
// project's sessions may use, and which other projects' code indexes they may
// search. The zero value is the default (all tools, own project only).
type ProjectAccess struct {
	ToolsMode       string   // "all" | "list"
	AllowedTools    []string // tool ids, honoured when ToolsMode == "list"
	AllowedProjects []string // other project ids whose code index is searchable
}

// ToolAllowed reports whether the tool with the given id may be used.
func (a ProjectAccess) ToolAllowed(toolID string) bool {
	if a.ToolsMode != "list" {
		return true
	}
	for _, id := range a.AllowedTools {
		if id == toolID {
			return true
		}
	}
	return false
}

// GetProjectAccess returns the project's policy, or the default when none is set.
func (db *DB) GetProjectAccess(projectID string) (ProjectAccess, error) {
	var mode, tools, projs string
	err := db.QueryRow(`SELECT tools_mode, allowed_tools, allowed_projects FROM project_access WHERE project_id=?`,
		projectID).Scan(&mode, &tools, &projs)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectAccess{ToolsMode: "all", AllowedTools: []string{}, AllowedProjects: []string{}}, nil
	}
	if err != nil {
		return ProjectAccess{}, err
	}
	a := ProjectAccess{ToolsMode: mode, AllowedTools: []string{}, AllowedProjects: []string{}}
	_ = json.Unmarshal([]byte(tools), &a.AllowedTools)
	_ = json.Unmarshal([]byte(projs), &a.AllowedProjects)
	return a, nil
}

// SetProjectAccess upserts the project's policy.
func (db *DB) SetProjectAccess(projectID string, a ProjectAccess) error {
	if a.AllowedTools == nil {
		a.AllowedTools = []string{}
	}
	if a.AllowedProjects == nil {
		a.AllowedProjects = []string{}
	}
	tools, _ := json.Marshal(a.AllowedTools)
	projs, _ := json.Marshal(a.AllowedProjects)
	_, err := db.Exec(`INSERT INTO project_access(project_id, tools_mode, allowed_tools, allowed_projects)
		VALUES(?,?,?,?) ON CONFLICT(project_id) DO UPDATE SET
		tools_mode=excluded.tools_mode, allowed_tools=excluded.allowed_tools, allowed_projects=excluded.allowed_projects`,
		projectID, a.ToolsMode, string(tools), string(projs))
	return err
}
