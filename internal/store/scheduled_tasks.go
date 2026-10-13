package store

import (
	"database/sql"
	"errors"
	"time"
)

// ScheduledTask mirrors a row of the scheduled_tasks table.
type ScheduledTask struct {
	ID             string
	Name           string
	ProjectID      string // "" = global (runs under the system project)
	Prompt         string
	Schedule       string // 5-field cron expression
	Enabled        bool
	PermissionMode string
	Model          string
	LastRunAt      *time.Time
	NextRunAt      time.Time
	LastSessionID  string
	OneShot        bool   // true => disable task after first fire
	Command        string // non-empty => script task: run this shell command, no Claude session
	LastStatus     string // script tasks: "" (never ran) | running | ok | error
	LastOutput     string // script tasks: tail of the last run's combined output
	CreatedAt      time.Time
}

const taskCols = `id,name,project_id,prompt,schedule,enabled,permission_mode,model,last_run_at,next_run_at,last_session_id,one_shot,created_at,command,last_status,last_output`

func (db *DB) CreateScheduledTask(t ScheduledTask) error {
	_, err := db.Exec(
		`INSERT INTO scheduled_tasks(`+taskCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Name, t.ProjectID, t.Prompt, t.Schedule, b2i(t.Enabled),
		t.PermissionMode, t.Model, nullTime(t.LastRunAt), t.NextRunAt.Format(rfc),
		t.LastSessionID, b2i(t.OneShot), t.CreatedAt.Format(rfc),
		t.Command, t.LastStatus, t.LastOutput,
	)
	return err
}

func (db *DB) GetScheduledTask(id string) (ScheduledTask, error) {
	row := db.QueryRow(`SELECT `+taskCols+` FROM scheduled_tasks WHERE id=?`, id)
	return scanTask(row)
}

func (db *DB) ListScheduledTasks() ([]ScheduledTask, error) {
	rows, err := db.Query(`SELECT ` + taskCols + ` FROM scheduled_tasks ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectTasks(rows)
}

func (db *DB) ListScheduledTasksByProject(projectID string) ([]ScheduledTask, error) {
	rows, err := db.Query(`SELECT `+taskCols+` FROM scheduled_tasks WHERE project_id=? ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectTasks(rows)
}

// ListDueTasks returns enabled tasks whose next_run_at is at or before now.
func (db *DB) ListDueTasks(now time.Time) ([]ScheduledTask, error) {
	rows, err := db.Query(`SELECT `+taskCols+`
		FROM scheduled_tasks WHERE enabled=1 AND next_run_at <= ?
		ORDER BY next_run_at ASC`, now.Format(rfc))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectTasks(rows)
}

func (db *DB) UpdateScheduledTask(t ScheduledTask) error {
	_, err := db.Exec(`UPDATE scheduled_tasks
		SET name=?,project_id=?,prompt=?,schedule=?,enabled=?,permission_mode=?,model=?,next_run_at=?,one_shot=?,command=?
		WHERE id=?`,
		t.Name, t.ProjectID, t.Prompt, t.Schedule, b2i(t.Enabled),
		t.PermissionMode, t.Model, t.NextRunAt.Format(rfc), b2i(t.OneShot), t.Command, t.ID,
	)
	return err
}

func (db *DB) SetTaskEnabled(id string, enabled bool) error {
	_, err := db.Exec(`UPDATE scheduled_tasks SET enabled=? WHERE id=?`, b2i(enabled), id)
	return err
}

func (db *DB) SetTaskLastRun(id, sessionID string, ran, next time.Time) error {
	_, err := db.Exec(`UPDATE scheduled_tasks
		SET last_run_at=?,last_session_id=?,next_run_at=? WHERE id=?`,
		ran.Format(rfc), sessionID, next.Format(rfc), id)
	return err
}

// SetTaskResult records the outcome of a script task's last run.
func (db *DB) SetTaskResult(id, status, output string) error {
	_, err := db.Exec(`UPDATE scheduled_tasks SET last_status=?,last_output=? WHERE id=?`, status, output, id)
	return err
}

func (db *DB) DeleteScheduledTask(id string) error {
	_, err := db.Exec(`DELETE FROM scheduled_tasks WHERE id=?`, id)
	return err
}

func nullTime(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return t.Format(rfc)
}

func scanTask(s scanner) (ScheduledTask, error) {
	var t ScheduledTask
	var created, nextRun string
	var lastRun sql.NullString
	var enabled, oneShot int
	err := s.Scan(&t.ID, &t.Name, &t.ProjectID, &t.Prompt, &t.Schedule,
		&enabled, &t.PermissionMode, &t.Model, &lastRun, &nextRun,
		&t.LastSessionID, &oneShot, &created,
		&t.Command, &t.LastStatus, &t.LastOutput)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	t.Enabled = enabled != 0
	t.OneShot = oneShot != 0
	t.CreatedAt, _ = time.Parse(rfc, created)
	t.NextRunAt, _ = time.Parse(rfc, nextRun)
	if lastRun.Valid {
		v, _ := time.Parse(rfc, lastRun.String)
		t.LastRunAt = &v
	}
	return t, nil
}

func collectTasks(rows *sql.Rows) ([]ScheduledTask, error) {
	var out []ScheduledTask
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
