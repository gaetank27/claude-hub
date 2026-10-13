package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Domain is a Hub-managed domain: a Caddy route with a Let's Encrypt cert.
type Domain struct {
	ID            string
	Domain        string // e.g. hub.example.com
	Upstream      string // e.g. 127.0.0.1:8787
	CertFile      string
	KeyFile       string
	Status        string // pending | active | error
	Error         string
	ContainerName string // non-empty when linked to a Docker container
	CreatedAt     time.Time
}

var domainCols = `id,domain,upstream,cert_file,key_file,status,error,container_name,created_at`

func scanDomain(row interface{ Scan(...any) error }) (Domain, error) {
	var d Domain
	var ts int64
	err := row.Scan(&d.ID, &d.Domain, &d.Upstream, &d.CertFile, &d.KeyFile, &d.Status, &d.Error, &d.ContainerName, &ts)
	d.CreatedAt = time.Unix(ts, 0)
	return d, err
}

// ListDomains returns all managed domains.
func (db *DB) ListDomains() ([]Domain, error) {
	rows, err := db.DB.Query(`SELECT ` + domainCols + ` FROM domains ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Domain
	for rows.Next() {
		d, err := scanDomain(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDomain returns a domain by its id.
func (db *DB) GetDomain(id string) (Domain, error) {
	row := db.DB.QueryRow(`SELECT `+domainCols+` FROM domains WHERE id=?`, id)
	d, err := scanDomain(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Domain{}, ErrNotFound
	}
	return d, err
}

// GetDomainByName returns a domain by its DNS name.
func (db *DB) GetDomainByName(name string) (Domain, error) {
	row := db.DB.QueryRow(`SELECT `+domainCols+` FROM domains WHERE domain=?`, name)
	d, err := scanDomain(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Domain{}, ErrNotFound
	}
	return d, err
}

// CreateDomain inserts a new managed domain with status "pending".
func (db *DB) CreateDomain(domain, upstream string) (Domain, error) {
	return db.CreateDomainFor(domain, upstream, "")
}

// CreateDomainFor inserts a domain linked to a specific container (containerName may be "").
func (db *DB) CreateDomainFor(domain, upstream, containerName string) (Domain, error) {
	d := Domain{
		ID:            uuid.NewString(),
		Domain:        domain,
		Upstream:      upstream,
		ContainerName: containerName,
		Status:        "pending",
		CreatedAt:     time.Now(),
	}
	_, err := db.DB.Exec(
		`INSERT INTO domains(id,domain,upstream,cert_file,key_file,status,error,container_name,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		d.ID, d.Domain, d.Upstream, "", "", d.Status, "", d.ContainerName, d.CreatedAt.Unix(),
	)
	return d, err
}

// GetDomainByContainer returns the active domain linked to a container name.
func (db *DB) GetDomainByContainer(containerName string) (Domain, error) {
	row := db.DB.QueryRow(`SELECT `+domainCols+` FROM domains WHERE container_name=? ORDER BY created_at DESC LIMIT 1`, containerName)
	d, err := scanDomain(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Domain{}, ErrNotFound
	}
	return d, err
}

// SetDomainCerts updates the cert paths and marks the domain active.
func (db *DB) SetDomainCerts(id, certFile, keyFile string) error {
	_, err := db.DB.Exec(
		`UPDATE domains SET cert_file=?,key_file=?,status='active',error='' WHERE id=?`,
		certFile, keyFile, id,
	)
	return err
}

// SetDomainError records a provisioning error.
func (db *DB) SetDomainError(id, errMsg string) error {
	_, err := db.DB.Exec(`UPDATE domains SET status='error',error=? WHERE id=?`, errMsg, id)
	return err
}

// DeleteDomain removes a managed domain record.
func (db *DB) DeleteDomain(id string) error {
	_, err := db.DB.Exec(`DELETE FROM domains WHERE id=?`, id)
	return err
}
