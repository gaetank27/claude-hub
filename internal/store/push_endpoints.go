package store

import "time"

// PushEndpoint is one phone's WebPush subscription.
type PushEndpoint struct {
	Endpoint string
	P256dh   string
	Auth     string
	Device   string
}

// UpsertPushEndpoint records a subscription, replacing the keys of a known endpoint.
func (db *DB) UpsertPushEndpoint(e PushEndpoint) error {
	_, err := db.Exec(`INSERT INTO push_endpoints(endpoint,p256dh,auth,device,created_at) VALUES(?,?,?,?,?)
		ON CONFLICT(endpoint) DO UPDATE SET p256dh=excluded.p256dh, auth=excluded.auth, device=excluded.device`,
		e.Endpoint, e.P256dh, e.Auth, e.Device, time.Now().Unix())
	return err
}

func (db *DB) DeletePushEndpoint(endpoint string) error {
	_, err := db.Exec(`DELETE FROM push_endpoints WHERE endpoint=?`, endpoint)
	return err
}

func (db *DB) ListPushEndpoints() ([]PushEndpoint, error) {
	rows, err := db.Query(`SELECT endpoint,p256dh,auth,device FROM push_endpoints ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushEndpoint
	for rows.Next() {
		var e PushEndpoint
		if err := rows.Scan(&e.Endpoint, &e.P256dh, &e.Auth, &e.Device); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
