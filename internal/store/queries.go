package store

import (
	"database/sql"
	"errors"
	"time"
)

// ErrNotFound is returned when a row lookup yields nothing.
var ErrNotFound = errors.New("not found")

// Session lifecycle states.
const (
	StateStarting = "starting"
	StateRunning  = "running"
	StateStopped  = "stopped"
	StateError    = "error"
)

// Project mirrors a row of the projects table.
type Project struct {
	ID        string
	Name      string
	Slug      string
	Dir       string
	IsSystem  bool
	Protected bool
	CreatedAt time.Time
	Starred   bool   // pinned/favorite, sorted first
	Category  string // free-text grouping label
	Hidden    bool   // excluded from the projects list (e.g. the Skills workspace)
	Archived  bool   // soft-archived: hidden from the board, restorable from Archivage
	Comment   string // free-text human note shown on the project tile/page
	// ClaudeAccountID is the subscription this project's sessions run under.
	// Empty => inherit the default (active) account at launch time.
	ClaudeAccountID string
	// Model is the Claude model alias this project's new sessions run on.
	// Empty => inherit the global runtime-settings model at launch time.
	Model string
	// ShowAutoSessions: when true, this project's automatic bot/gateway sessions
	// (WhatsApp, API key) also appear in the Dashboard and recent-sessions bar.
	ShowAutoSessions bool
}

// Session mirrors a row of the sessions table.
type Session struct {
	ID              string
	ProjectID       string
	JournalPath     string
	SocketPath      string
	State           string
	PermissionMode  string
	CreatedAt       time.Time
	LastEventAt     *time.Time
	Comment         string // free-text human note/label
	PrURL           string // PR opened for approved autonomous work
	ClaudeAccountID string // Claude subscription the session ran under
	Model           string // Claude model alias the session was launched on ("" => resolved at launch)
	Source          string // "" = manual, "scheduler" = from scheduled task
	Archived        bool   // soft-archived: hidden from normal view, restorable
}

// Claude account kinds and statuses.
const (
	AccountKindOAuth  = "oauth"  // Pro/Max subscription via `claude auth login` / setup-token
	AccountKindAPIKey = "apikey" // ANTHROPIC_API_KEY injected at launch

	AccountConnected = "connected" // credentials present, usable
	AccountLoggedOut = "loggedout" // credentials archived/removed
)

// ClaudeAccount mirrors a row of the claude_accounts table: one connected Claude
// Code subscription, isolated in its own CLAUDE_CONFIG_DIR.
type ClaudeAccount struct {
	ID           string
	Label        string
	ConfigDir    string
	Kind         string
	OrgID        string
	AccountEmail string
	Status       string
	Active       bool
	CreatedAt    time.Time
}

// Skill statuses. A skill is `draft` while its authoring session runs, becomes
// `enabled` once validated (and thus loaded by Claude), and can be `disabled`.
const (
	SkillDraft    = "draft"
	SkillEnabled  = "enabled"
	SkillDisabled = "disabled"
)

// Skill mirrors a row of the skills table: a global, reusable Claude Code skill.
// The definition lives on disk (~/.claude/skills/<slug>/SKILL.md); this is the
// searchable index over it.
type Skill struct {
	ID          string
	Name        string
	Slug        string
	Description string
	Keywords    string
	Status      string
	SessionID   string // background session that authored it
	CreatedAt   time.Time
}

const rfc = time.RFC3339Nano

// CreateProject inserts a project. Durable (FULL sync) since it is a root entity.
func (db *DB) CreateProject(p Project) error {
	return db.WithFullSync(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO projects(id,name,slug,dir,is_system,protected,created_at,starred,category,hidden,archived,comment,claude_account_id,model)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			p.ID, p.Name, p.Slug, p.Dir, b2i(p.IsSystem), b2i(p.Protected),
			p.CreatedAt.Format(rfc), b2i(p.Starred), p.Category, b2i(p.Hidden), b2i(p.Archived), p.Comment, p.ClaudeAccountID, p.Model,
		)
		return err
	})
}

const projectCols = `id,name,slug,dir,is_system,protected,created_at,starred,category,hidden,archived,comment,claude_account_id,model,show_auto_sessions`

func (db *DB) GetProject(id string) (Project, error) {
	row := db.QueryRow(`SELECT `+projectCols+` FROM projects WHERE id=?`, id)
	return scanProject(row)
}

func (db *DB) GetProjectBySlug(slug string) (Project, error) {
	row := db.QueryRow(`SELECT `+projectCols+` FROM projects WHERE slug=?`, slug)
	return scanProject(row)
}

func (db *DB) ListProjects() ([]Project, error) {
	// system project first, then starred, then most-recent. Hidden projects (the
	// Skills workspace) and archived projects are excluded — they are not part of
	// the active board.
	return db.queryProjects(`SELECT ` + projectCols + `
		FROM projects WHERE hidden=0 AND archived=0 ORDER BY is_system DESC, starred DESC, created_at DESC`)
}

// ListArchivedProjects returns soft-archived projects (most-recent first), shown
// on the Archivage page where they can be restored or permanently deleted.
func (db *DB) ListArchivedProjects() ([]Project, error) {
	return db.queryProjects(`SELECT ` + projectCols + `
		FROM projects WHERE hidden=0 AND archived=1 ORDER BY created_at DESC`)
}

func (db *DB) queryProjects(query string, args ...any) ([]Project, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetProjectArchived flips a project's archived flag. NORMAL sync: reconstructible
// (the data on disk is untouched), losing the flag on power loss is harmless.
func (db *DB) SetProjectArchived(id string, archived bool) error {
	_, err := db.Exec(`UPDATE projects SET archived=? WHERE id=?`, b2i(archived), id)
	return err
}

// UpdateProjectMeta sets a project's starred flag, category and free-text comment
// (user-editable metadata). NORMAL sync: losing it on power loss is harmless
// (cosmetic).
func (db *DB) UpdateProjectMeta(id string, starred bool, category, comment string) error {
	_, err := db.Exec(`UPDATE projects SET starred=?, category=?, comment=? WHERE id=?`,
		b2i(starred), category, comment, id)
	return err
}

// SetProjectClaudeAccount assigns the subscription a project's sessions run
// under (” => inherit the default account). NORMAL sync: a lost write only
// reverts the project to the default account, which is harmless.
func (db *DB) SetProjectClaudeAccount(id, accountID string) error {
	_, err := db.Exec(`UPDATE projects SET claude_account_id=? WHERE id=?`, accountID, id)
	return err
}

// SetProjectModel assigns the Claude model a project's new sessions run on
// (” => inherit the global runtime-settings model). NORMAL sync: a lost write
// only reverts the project to the global default model, which is harmless.
func (db *DB) SetProjectModel(id, model string) error {
	_, err := db.Exec(`UPDATE projects SET model=? WHERE id=?`, model, id)
	return err
}

// SetProjectShowAutoSessions toggles whether the project's automatic bot sessions
// appear in the Dashboard / recent bar. NORMAL sync: purely a display preference.
func (db *DB) SetProjectShowAutoSessions(id string, show bool) error {
	_, err := db.Exec(`UPDATE projects SET show_auto_sessions=? WHERE id=?`, b2i(show), id)
	return err
}

// DeleteProject removes a project row durably. The caller must have deleted its
// sessions first (their journals are on disk); this only drops the index entry.
func (db *DB) DeleteProject(id string) error {
	return db.WithFullSync(func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM projects WHERE id=?`, id)
		return err
	})
}

// --- Skills ---------------------------------------------------------------

const skillCols = `id,name,slug,description,keywords,status,session_id,created_at`

// CreateSkill inserts a skill durably (FULL sync): it is a root entity whose
// on-disk definition is keyed by this row's slug.
func (db *DB) CreateSkill(sk Skill) error {
	return db.WithFullSync(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO skills(id,name,slug,description,keywords,status,session_id,created_at)
			 VALUES(?,?,?,?,?,?,?,?)`,
			sk.ID, sk.Name, sk.Slug, sk.Description, sk.Keywords, sk.Status,
			sk.SessionID, sk.CreatedAt.Format(rfc),
		)
		return err
	})
}

func (db *DB) GetSkill(id string) (Skill, error) {
	row := db.QueryRow(`SELECT `+skillCols+` FROM skills WHERE id=?`, id)
	return scanSkill(row)
}

func (db *DB) GetSkillBySlug(slug string) (Skill, error) {
	row := db.QueryRow(`SELECT `+skillCols+` FROM skills WHERE slug=?`, slug)
	return scanSkill(row)
}

func (db *DB) ListSkills() ([]Skill, error) {
	rows, err := db.Query(`SELECT ` + skillCols + ` FROM skills ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Skill
	for rows.Next() {
		sk, err := scanSkill(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sk)
	}
	return out, rows.Err()
}

// UpdateSkillMeta edits user-facing fields. NORMAL sync (re-derivable from the
// on-disk SKILL.md if ever lost).
func (db *DB) UpdateSkillMeta(id, name, description, keywords string) error {
	_, err := db.Exec(`UPDATE skills SET name=?, description=?, keywords=? WHERE id=?`,
		name, description, keywords, id)
	return err
}

func (db *DB) SetSkillStatus(id, status string) error {
	_, err := db.Exec(`UPDATE skills SET status=? WHERE id=?`, status, id)
	return err
}

// SetSkillSession repoints a skill at its current editing session (set when the
// skill is created or evolved, so the UI can stream the live work).
func (db *DB) SetSkillSession(id, sessionID string) error {
	_, err := db.Exec(`UPDATE skills SET session_id=? WHERE id=?`, sessionID, id)
	return err
}

// DeleteSkill drops the index row durably. The caller removes the on-disk
// definition separately.
func (db *DB) DeleteSkill(id string) error {
	return db.WithFullSync(func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM skills WHERE id=?`, id)
		return err
	})
}

func scanSkill(s scanner) (Skill, error) {
	var sk Skill
	var created string
	err := s.Scan(&sk.ID, &sk.Name, &sk.Slug, &sk.Description, &sk.Keywords,
		&sk.Status, &sk.SessionID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return sk, ErrNotFound
	}
	if err != nil {
		return sk, err
	}
	sk.CreatedAt, _ = time.Parse(rfc, created)
	return sk, nil
}

const sessionCols = `id,project_id,journal_path,socket_path,state,permission_mode,created_at,last_event_at,comment,pr_url,claude_account_id,model,source,archived`

// CreateSession inserts a session row durably.
func (db *DB) CreateSession(s Session) error {
	return db.WithFullSync(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO sessions(id,project_id,journal_path,socket_path,state,permission_mode,created_at,claude_account_id,model,comment,source)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			s.ID, s.ProjectID, s.JournalPath, s.SocketPath, s.State,
			s.PermissionMode, s.CreatedAt.Format(rfc), s.ClaudeAccountID, s.Model, s.Comment, s.Source,
		)
		return err
	})
}

func (db *DB) GetSession(id string) (Session, error) {
	row := db.QueryRow(`SELECT `+sessionCols+` FROM sessions WHERE id=?`, id)
	return scanSession(row)
}

// ListSessionsByProject returns non-archived sessions for a project (newest first).
func (db *DB) ListSessionsByProject(projectID string) ([]Session, error) {
	rows, err := db.Query(`SELECT `+sessionCols+`
		FROM sessions WHERE project_id=? AND archived=0 ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectSessions(rows)
}

// ListArchivedSessionsByProject returns archived sessions for a project (newest first).
func (db *DB) ListArchivedSessionsByProject(projectID string) ([]Session, error) {
	rows, err := db.Query(`SELECT `+sessionCols+`
		FROM sessions WHERE project_id=? AND archived=1 ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectSessions(rows)
}

// ListSessions returns every session across all projects, newest first. Includes
// archived sessions so usage history stays complete.
func (db *DB) ListSessions() ([]Session, error) {
	rows, err := db.Query(`SELECT ` + sessionCols + `
		FROM sessions ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectSessions(rows)
}

// ListActiveSessions returns only non-archived sessions across all projects,
// newest first. Powers the observability dashboard.
func (db *DB) ListActiveSessions() ([]Session, error) {
	rows, err := db.Query(`SELECT ` + sessionCols + `
		FROM sessions WHERE archived=0 ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectSessions(rows)
}

// ListSessionsByState powers crash recovery at boot (state="running").
func (db *DB) ListSessionsByState(state string) ([]Session, error) {
	rows, err := db.Query(`SELECT `+sessionCols+`
		FROM sessions WHERE state=?`, state)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectSessions(rows)
}

// SetSessionState transitions a session durably (lifecycle change).
func (db *DB) SetSessionState(id, state string) error {
	return db.WithFullSync(func(tx *sql.Tx) error {
		// stopped_at feeds the bot-session auto-archive (closed for N hours).
		_, err := tx.Exec(`UPDATE sessions SET state=?,
			stopped_at=CASE WHEN ? IN ('stopped','error') THEN ? ELSE NULL END WHERE id=?`,
			state, state, time.Now().UTC().Format(rfc), id)
		return err
	})
}

// SessionStoppedAt returns when the session last stopped; ok=false if unknown
// (still live, or closed before stopped_at was tracked).
func (db *DB) SessionStoppedAt(id string) (t time.Time, ok bool) {
	var s sql.NullString
	if err := db.QueryRow(`SELECT stopped_at FROM sessions WHERE id=?`, id).Scan(&s); err != nil || !s.Valid {
		return time.Time{}, false
	}
	t, err := time.Parse(rfc, s.String)
	return t, err == nil
}

// SetSessionPermissionMode updates a session's claude permission mode durably
// (FULL sync): it gates autonomous execution (plan → bypassPermissions), so the
// change must survive a crash.
func (db *DB) SetSessionPermissionMode(id, mode string) error {
	return db.WithFullSync(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE sessions SET permission_mode=? WHERE id=?`, mode, id)
		return err
	})
}

// TouchSession records the last event timestamp (cheap, NORMAL sync).
func (db *DB) TouchSession(id string, at time.Time) error {
	_, err := db.Exec(`UPDATE sessions SET last_event_at=? WHERE id=?`, at.Format(rfc), id)
	return err
}

// SetSessionComment updates a session's free-text note (cheap, NORMAL sync — an
// annotation, not lifecycle state).
func (db *DB) SetSessionComment(id, comment string) error {
	_, err := db.Exec(`UPDATE sessions SET comment=? WHERE id=?`, comment, id)
	return err
}

// SetSessionPrURL records the pull-request URL opened for an approved autonomous
// session (cheap, NORMAL sync). Unlike the branch name this is not derivable, so
// it must be persisted.
func (db *DB) SetSessionPrURL(id, url string) error {
	_, err := db.Exec(`UPDATE sessions SET pr_url=? WHERE id=?`, url, id)
	return err
}

// SetSessionArchived flips a session's archived flag. Archived sessions are
// hidden from the normal view but their journals are preserved.
func (db *DB) SetSessionArchived(id string, archived bool) error {
	_, err := db.Exec(`UPDATE sessions SET archived=? WHERE id=?`, b2i(archived), id)
	return err
}

// DeleteSession removes a session row durably. The caller deletes the on-disk
// journal/socket; this only drops the derived index entry.
func (db *DB) DeleteSession(id string) error {
	return db.WithFullSync(func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM sessions WHERE id=?`, id)
		return err
	})
}

// --- scan helpers ---

type scanner interface{ Scan(...any) error }

func scanProject(s scanner) (Project, error) {
	var p Project
	var isSys, prot, starred, hidden, archived, showAuto int
	var created string
	err := s.Scan(&p.ID, &p.Name, &p.Slug, &p.Dir, &isSys, &prot, &created, &starred, &p.Category, &hidden, &archived, &p.Comment, &p.ClaudeAccountID, &p.Model, &showAuto)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	p.IsSystem, p.Protected, p.Starred, p.Hidden = isSys != 0, prot != 0, starred != 0, hidden != 0
	p.Archived = archived != 0
	p.ShowAutoSessions = showAuto != 0
	p.CreatedAt, _ = time.Parse(rfc, created)
	return p, nil
}

func scanSession(s scanner) (Session, error) {
	var sess Session
	var created string
	var lastEvent sql.NullString
	var archived int
	err := s.Scan(&sess.ID, &sess.ProjectID, &sess.JournalPath, &sess.SocketPath,
		&sess.State, &sess.PermissionMode, &created, &lastEvent, &sess.Comment, &sess.PrURL,
		&sess.ClaudeAccountID, &sess.Model, &sess.Source, &archived)
	if errors.Is(err, sql.ErrNoRows) {
		return sess, ErrNotFound
	}
	if err != nil {
		return sess, err
	}
	sess.CreatedAt, _ = time.Parse(rfc, created)
	if lastEvent.Valid {
		t, _ := time.Parse(rfc, lastEvent.String)
		sess.LastEventAt = &t
	}
	sess.Archived = archived != 0
	return sess, nil
}

func collectSessions(rows *sql.Rows) ([]Session, error) {
	var out []Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// GetSettings returns all key/value runtime settings as a map.
func (db *DB) GetSettings() (map[string]string, error) {
	rows, err := db.Query(`SELECT key,value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SetSetting upserts a single runtime setting. NORMAL sync: a derived preference,
// harmless to lose on power loss (re-set from the UI).
func (db *DB) SetSetting(key, value string) error {
	_, err := db.Exec(
		`INSERT INTO settings(key,value) VALUES(?,?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		key, value)
	return err
}

// UsageSnapshot is one periodic sample of the plan usage windows. A nil pointer
// is an untracked window (stored as SQL NULL), distinct from 0% utilization.
type UsageSnapshot struct {
	TS         time.Time
	Session    *float64
	Week       *float64
	WeekOpus   *float64
	WeekSonnet *float64
	AccountID  string // Claude subscription this sample belongs to
	// Window reset times, persisted so the meter's countdown survives a live-fetch
	// failure (the stale fallback then still shows "réinit. in N h"). nil = untracked.
	SessionReset    *time.Time
	WeekReset       *time.Time
	WeekOpusReset   *time.Time
	WeekSonnetReset *time.Time
}

// InsertUsageSnapshot appends a plan-usage sample. NORMAL sync: a lost sample on
// power loss just leaves a small gap in a monitoring chart.
func (db *DB) InsertUsageSnapshot(s UsageSnapshot) error {
	_, err := db.Exec(
		`INSERT INTO usage_snapshots(ts,session,week,week_opus,week_sonnet,account_id,
			session_reset,week_reset,week_opus_reset,week_sonnet_reset)
		 VALUES(?,?,?,?,?,?,?,?,?,?)`,
		s.TS.Format(rfc), nullF(s.Session), nullF(s.Week), nullF(s.WeekOpus), nullF(s.WeekSonnet), s.AccountID,
		nullT(s.SessionReset), nullT(s.WeekReset), nullT(s.WeekOpusReset), nullT(s.WeekSonnetReset))
	return err
}

// LatestUsageSnapshot returns the most recent sample for an account. It backs the
// live meter when an upstream fetch fails (briefly-expired token, backend just
// restarted, slow upstream) so the meter shows the last-known usage instead of
// vanishing. Returns ErrNotFound when the account has no samples yet.
func (db *DB) LatestUsageSnapshot(accountID string) (UsageSnapshot, error) {
	row := db.QueryRow(
		`SELECT ts,session,week,week_opus,week_sonnet,account_id,
			session_reset,week_reset,week_opus_reset,week_sonnet_reset
		 FROM usage_snapshots WHERE account_id=? ORDER BY ts DESC LIMIT 1`, accountID)
	var (
		ts                 string
		se, wk, wo, ws     sql.NullFloat64
		acct               string
		seR, wkR, woR, wsR sql.NullString
	)
	if err := row.Scan(&ts, &se, &wk, &wo, &ws, &acct, &seR, &wkR, &woR, &wsR); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return UsageSnapshot{}, ErrNotFound
		}
		return UsageSnapshot{}, err
	}
	t, _ := time.Parse(rfc, ts)
	return UsageSnapshot{
		TS: t, Session: f2p(se), Week: f2p(wk), WeekOpus: f2p(wo), WeekSonnet: f2p(ws), AccountID: acct,
		SessionReset: s2t(seR), WeekReset: s2t(wkR), WeekOpusReset: s2t(woR), WeekSonnetReset: s2t(wsR),
	}, nil
}

// ListUsageSnapshots returns the samples in [from, to), oldest first. When
// accountID is non-empty only that account's samples are returned (so plan-usage
// history follows the active subscription); empty returns all accounts.
func (db *DB) ListUsageSnapshots(from, to time.Time, accountID string) ([]UsageSnapshot, error) {
	query := `SELECT ts,session,week,week_opus,week_sonnet,account_id FROM usage_snapshots
		 WHERE ts >= ? AND ts < ?`
	args := []any{from.Format(rfc), to.Format(rfc)}
	if accountID != "" {
		query += ` AND account_id = ?`
		args = append(args, accountID)
	}
	query += ` ORDER BY ts`
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageSnapshot
	for rows.Next() {
		var (
			ts             string
			se, wk, wo, ws sql.NullFloat64
			acct           string
		)
		if err := rows.Scan(&ts, &se, &wk, &wo, &ws, &acct); err != nil {
			return nil, err
		}
		t, _ := time.Parse(rfc, ts)
		out = append(out, UsageSnapshot{
			TS:         t,
			Session:    f2p(se),
			Week:       f2p(wk),
			WeekOpus:   f2p(wo),
			WeekSonnet: f2p(ws),
			AccountID:  acct,
		})
	}
	return out, rows.Err()
}

// PruneUsageSnapshots deletes samples older than the cutoff to bound table size.
func (db *DB) PruneUsageSnapshots(before time.Time) error {
	_, err := db.Exec(`DELETE FROM usage_snapshots WHERE ts < ?`, before.Format(rfc))
	return err
}

// nullF maps an optional float to a SQL NULL-or-value argument.
func nullF(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// f2p maps a nullable SQL float back to an optional float.
func f2p(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	v := n.Float64
	return &v
}

// nullT maps an optional time to a SQL NULL-or-RFC3339-string argument.
func nullT(p *time.Time) any {
	if p == nil {
		return nil
	}
	return p.Format(rfc)
}

// s2t parses a nullable SQL RFC3339 string back to an optional time.
func s2t(n sql.NullString) *time.Time {
	if !n.Valid || n.String == "" {
		return nil
	}
	t, err := time.Parse(rfc, n.String)
	if err != nil {
		return nil
	}
	return &t
}

// --- claude_accounts ---

const accountCols = `id,label,config_dir,kind,org_id,account_email,status,active,created_at`

// CreateClaudeAccount inserts an account durably (a root entity for isolation).
func (db *DB) CreateClaudeAccount(a ClaudeAccount) error {
	return db.WithFullSync(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO claude_accounts(`+accountCols+`) VALUES(?,?,?,?,?,?,?,?,?)`,
			a.ID, a.Label, a.ConfigDir, a.Kind, a.OrgID, a.AccountEmail,
			a.Status, b2i(a.Active), a.CreatedAt.Format(rfc),
		)
		return err
	})
}

func (db *DB) GetClaudeAccount(id string) (ClaudeAccount, error) {
	row := db.QueryRow(`SELECT `+accountCols+` FROM claude_accounts WHERE id=?`, id)
	return scanAccount(row)
}

// GetActiveClaudeAccount returns the single active account, or ErrNotFound when
// none is active (e.g. every account was logged out).
func (db *DB) GetActiveClaudeAccount() (ClaudeAccount, error) {
	row := db.QueryRow(`SELECT ` + accountCols + ` FROM claude_accounts WHERE active=1 LIMIT 1`)
	return scanAccount(row)
}

// ListClaudeAccounts returns all accounts, active first then newest.
func (db *DB) ListClaudeAccounts() ([]ClaudeAccount, error) {
	rows, err := db.Query(`SELECT ` + accountCols + `
		FROM claude_accounts ORDER BY active DESC, created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClaudeAccount
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (db *DB) CountClaudeAccounts() (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM claude_accounts`).Scan(&n)
	return n, err
}

// SetActiveClaudeAccount makes id the single active account, atomically clearing
// the previous one. FULL sync: which subscription new work bills against must not
// be lost on power loss.
func (db *DB) SetActiveClaudeAccount(id string) error {
	return db.WithFullSync(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE claude_accounts SET active=0 WHERE active=1`); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE claude_accounts SET active=1 WHERE id=?`, id)
		return err
	})
}

// ClearActiveClaudeAccount unsets every active flag (used when the active account
// is logged out and no connected account remains to take over).
func (db *DB) ClearActiveClaudeAccount() error {
	_, err := db.Exec(`UPDATE claude_accounts SET active=0 WHERE active=1`)
	return err
}

// SetClaudeAccountLabel renames an account's display label.
func (db *DB) SetClaudeAccountLabel(id, label string) error {
	_, err := db.Exec(`UPDATE claude_accounts SET label=? WHERE id=?`, label, id)
	return err
}

// SetClaudeAccountStatus flips an account between connected/loggedout.
func (db *DB) SetClaudeAccountStatus(id, status string) error {
	_, err := db.Exec(`UPDATE claude_accounts SET status=? WHERE id=?`, status, id)
	return err
}

// UpdateClaudeAccountMeta records org/email discovered from the credentials.
func (db *DB) UpdateClaudeAccountMeta(id, orgID, email string) error {
	_, err := db.Exec(`UPDATE claude_accounts SET org_id=?, account_email=? WHERE id=?`,
		orgID, email, id)
	return err
}

// DeleteClaudeAccount removes an account row durably. The caller is responsible
// for refusing the active account / one with sessions, and for the on-disk dir.
func (db *DB) DeleteClaudeAccount(id string) error {
	return db.WithFullSync(func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM claude_accounts WHERE id=?`, id)
		return err
	})
}

// AttachOrphanSessions rebinds every session with no account (legacy rows created
// before multi-account support) to accountID. Run once at seed time.
func (db *DB) AttachOrphanSessions(accountID string) error {
	_, err := db.Exec(`UPDATE sessions SET claude_account_id=? WHERE claude_account_id=''`, accountID)
	return err
}

// AttachOrphanSnapshots rebinds account-less usage samples to accountID (seed).
func (db *DB) AttachOrphanSnapshots(accountID string) error {
	_, err := db.Exec(`UPDATE usage_snapshots SET account_id=? WHERE account_id=''`, accountID)
	return err
}

func scanAccount(s scanner) (ClaudeAccount, error) {
	var a ClaudeAccount
	var active int
	var created string
	err := s.Scan(&a.ID, &a.Label, &a.ConfigDir, &a.Kind, &a.OrgID,
		&a.AccountEmail, &a.Status, &active, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	a.Active = active != 0
	a.CreatedAt, _ = time.Parse(rfc, created)
	return a, nil
}
