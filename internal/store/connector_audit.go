package store

import "time"

// ConnectorAudit is one journal line of a sensitive connector action (Cloudflare…).
type ConnectorAudit struct {
	ID        int64     `json:"id"`
	TS        time.Time `json:"ts"`
	Connector string    `json:"connector"`
	Action    string    `json:"action"`
	Source    string    `json:"source"`
	Args      string    `json:"args"`
	Outcome   string    `json:"outcome"`
	Detail    string    `json:"detail"`
}

func (db *DB) AddConnectorAudit(a ConnectorAudit) {
	_, _ = db.DB.Exec(`INSERT INTO connector_audit(ts,connector,action,source,args,outcome,detail) VALUES(?,?,?,?,?,?,?)`,
		time.Now().Unix(), a.Connector, a.Action, a.Source, a.Args, a.Outcome, a.Detail)
}

func (db *DB) ListConnectorAudit(connector string, limit int) ([]ConnectorAudit, error) {
	rows, err := db.DB.Query(`SELECT id,ts,connector,action,source,args,outcome,detail FROM connector_audit WHERE connector=? ORDER BY id DESC LIMIT ?`, connector, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConnectorAudit{}
	for rows.Next() {
		var a ConnectorAudit
		var ts int64
		if err := rows.Scan(&a.ID, &ts, &a.Connector, &a.Action, &a.Source, &a.Args, &a.Outcome, &a.Detail); err != nil {
			return nil, err
		}
		a.TS = time.Unix(ts, 0)
		out = append(out, a)
	}
	return out, rows.Err()
}

// PurgeConnectorAudit drops journal lines older than keep.
func (db *DB) PurgeConnectorAudit(keep time.Duration) {
	_, _ = db.DB.Exec(`DELETE FROM connector_audit WHERE ts<?`, time.Now().Add(-keep).Unix())
}
