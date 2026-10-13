package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// MCPClient is an OAuth client registered through DCR.
type MCPClient struct {
	ID           string
	Name         string
	RedirectURIs []string
	CreatedAt    time.Time
}

// MCPGrant is one authorised connection of a remote MCP client.
type MCPGrant struct {
	ID         string     `json:"id"`
	ClientID   string     `json:"clientId"`
	ClientName string     `json:"clientName"`
	UserEmail  string     `json:"userEmail"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	LastIP     string     `json:"lastIp"`
	RefreshExp time.Time  `json:"expiresAt"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
}

// MCPAudit is one audit-journal line.
type MCPAudit struct {
	ID         int64     `json:"id"`
	TS         time.Time `json:"ts"`
	GrantID    string    `json:"grantId"`
	ClientName string    `json:"client"`
	IP         string    `json:"ip"`
	Method     string    `json:"method"`
	Tool       string    `json:"tool"`
	Args       string    `json:"args"`
	Outcome    string    `json:"outcome"`
	Detail     string    `json:"detail"`
	MS         int64     `json:"ms"`
}

// HashMCPToken is the stored form of an access/refresh token.
func HashMCPToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

const maxMCPClients = 200

func (db *DB) CreateMCPClient(name string, uris []string) (MCPClient, error) {
	b, _ := json.Marshal(uris)
	c := MCPClient{ID: "mcpc_" + uuid.NewString(), Name: name, RedirectURIs: uris, CreatedAt: time.Now()}
	if _, err := db.DB.Exec(`INSERT INTO mcp_clients(id,name,redirect_uris,created_at) VALUES(?,?,?,?)`, c.ID, c.Name, string(b), c.CreatedAt.Unix()); err != nil {
		return c, err
	}
	// DCR is anonymous: keep the table bounded by dropping the oldest clients
	// that never obtained a grant.
	_, _ = db.DB.Exec(`DELETE FROM mcp_clients WHERE id NOT IN (SELECT client_id FROM mcp_grants)
		AND id NOT IN (SELECT id FROM mcp_clients ORDER BY created_at DESC LIMIT ?)`, maxMCPClients)
	return c, nil
}

func (db *DB) GetMCPClient(id string) (MCPClient, error) {
	var c MCPClient
	var raw string
	var ts int64
	err := db.DB.QueryRow(`SELECT id,name,redirect_uris,created_at FROM mcp_clients WHERE id=?`, id).Scan(&c.ID, &c.Name, &raw, &ts)
	if err != nil {
		return c, err
	}
	c.CreatedAt = time.Unix(ts, 0)
	_ = json.Unmarshal([]byte(raw), &c.RedirectURIs)
	return c, nil
}

// CreateMCPGrant stores a new connection with its first token pair.
func (db *DB) CreateMCPGrant(clientID, clientName, email, accessHash string, accessExp time.Time, refreshHash string, refreshExp time.Time) (string, error) {
	id := "mcpg_" + uuid.NewString()
	_, err := db.DB.Exec(`INSERT INTO mcp_grants(id,client_id,client_name,user_email,created_at,access_hash,access_exp,refresh_hash,refresh_exp) VALUES(?,?,?,?,?,?,?,?,?)`,
		id, clientID, clientName, email, time.Now().Unix(), accessHash, accessExp.Unix(), refreshHash, refreshExp.Unix())
	return id, err
}

// MCPGrantByAccess returns the live grant owning an access-token hash.
func (db *DB) MCPGrantByAccess(hash string, now time.Time) (MCPGrant, error) {
	var g MCPGrant
	var created, used, rexp, revoked, aexp int64
	err := db.DB.QueryRow(`SELECT id,client_id,client_name,user_email,created_at,last_used_at,last_ip,refresh_exp,revoked_at,access_exp FROM mcp_grants WHERE access_hash=? AND access_hash<>''`, hash).
		Scan(&g.ID, &g.ClientID, &g.ClientName, &g.UserEmail, &created, &used, &g.LastIP, &rexp, &revoked, &aexp)
	if err != nil {
		return g, err
	}
	if revoked != 0 || aexp < now.Unix() || rexp < now.Unix() {
		return g, sql.ErrNoRows
	}
	g.CreatedAt = time.Unix(created, 0)
	return g, nil
}

// RotateMCPRefresh swaps a grant's tokens when the presented refresh hash is the
// current one. A reuse of the previous (already rotated) refresh token is a
// theft signal: the grant is revoked and ErrMCPRefreshReuse returned.
var ErrMCPRefreshReuse = errors.New("refresh token reuse")

func (db *DB) RotateMCPRefresh(refreshHash, newAccess string, accessExp time.Time, newRefresh string, refreshExp time.Time, now time.Time) (MCPGrant, error) {
	var g MCPGrant
	var created, rexp, revoked int64
	err := db.DB.QueryRow(`SELECT id,client_id,client_name,user_email,created_at,refresh_exp,revoked_at FROM mcp_grants WHERE refresh_hash=? AND refresh_hash<>''`, refreshHash).
		Scan(&g.ID, &g.ClientID, &g.ClientName, &g.UserEmail, &created, &rexp, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		var id string
		if db.DB.QueryRow(`SELECT id FROM mcp_grants WHERE prev_refresh_hash=? AND prev_refresh_hash<>''`, refreshHash).Scan(&id) == nil {
			_ = db.RevokeMCPGrant(id)
			return g, ErrMCPRefreshReuse
		}
		return g, sql.ErrNoRows
	}
	if err != nil {
		return g, err
	}
	if revoked != 0 || rexp < now.Unix() {
		return g, sql.ErrNoRows
	}
	_, err = db.DB.Exec(`UPDATE mcp_grants SET prev_refresh_hash=refresh_hash, refresh_hash=?, access_hash=?, access_exp=?, refresh_exp=? WHERE id=?`,
		newRefresh, newAccess, accessExp.Unix(), refreshExp.Unix(), g.ID)
	return g, err
}

func (db *DB) TouchMCPGrant(id, ip string) {
	_, _ = db.DB.Exec(`UPDATE mcp_grants SET last_used_at=?, last_ip=? WHERE id=?`, time.Now().Unix(), ip, id)
}

func (db *DB) ListMCPGrants() ([]MCPGrant, error) {
	rows, err := db.DB.Query(`SELECT id,client_id,client_name,user_email,created_at,last_used_at,last_ip,refresh_exp,revoked_at FROM mcp_grants WHERE revoked_at=0 ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MCPGrant{}
	for rows.Next() {
		var g MCPGrant
		var created, used, rexp, revoked int64
		if err := rows.Scan(&g.ID, &g.ClientID, &g.ClientName, &g.UserEmail, &created, &used, &g.LastIP, &rexp, &revoked); err != nil {
			return nil, err
		}
		g.CreatedAt = time.Unix(created, 0)
		g.RefreshExp = time.Unix(rexp, 0)
		if used > 0 {
			t := time.Unix(used, 0)
			g.LastUsedAt = &t
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// RevokeMCPGrant kills a connection at once: tokens are cleared so neither the
// access nor the refresh token can ever match again.
func (db *DB) RevokeMCPGrant(id string) error {
	res, err := db.DB.Exec(`UPDATE mcp_grants SET revoked_at=?, access_hash='', refresh_hash='', prev_refresh_hash='' WHERE id=? AND revoked_at=0`, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (db *DB) RevokeAllMCPGrants() (int64, error) {
	res, err := db.DB.Exec(`UPDATE mcp_grants SET revoked_at=?, access_hash='', refresh_hash='', prev_refresh_hash='' WHERE revoked_at=0`, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (db *DB) AddMCPAudit(a MCPAudit) {
	_, _ = db.DB.Exec(`INSERT INTO mcp_audit(ts,grant_id,client_name,ip,method,tool,args,outcome,detail,ms) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		time.Now().Unix(), a.GrantID, a.ClientName, a.IP, a.Method, a.Tool, a.Args, a.Outcome, a.Detail, a.MS)
}

func (db *DB) ListMCPAudit(limit int) ([]MCPAudit, error) {
	rows, err := db.DB.Query(`SELECT id,ts,grant_id,client_name,ip,method,tool,args,outcome,detail,ms FROM mcp_audit ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MCPAudit{}
	for rows.Next() {
		var a MCPAudit
		var ts int64
		if err := rows.Scan(&a.ID, &ts, &a.GrantID, &a.ClientName, &a.IP, &a.Method, &a.Tool, &a.Args, &a.Outcome, &a.Detail, &a.MS); err != nil {
			return nil, err
		}
		a.TS = time.Unix(ts, 0)
		out = append(out, a)
	}
	return out, rows.Err()
}

// PurgeMCP drops audit lines older than keep and long-dead grants.
func (db *DB) PurgeMCP(keep time.Duration) {
	cut := time.Now().Add(-keep).Unix()
	_, _ = db.DB.Exec(`DELETE FROM mcp_audit WHERE ts<?`, cut)
	_, _ = db.DB.Exec(`DELETE FROM mcp_grants WHERE (revoked_at>0 AND revoked_at<?) OR (refresh_exp<?)`, cut, cut)
}
