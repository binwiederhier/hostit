package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

const (
	// domainName resolves the app's current name from its id, so routing and the
	// UI follow a rename; domains are keyed on app_id.
	domainName              = `COALESCE((SELECT name FROM app a WHERE a.id = app_domain.app_id), app_domain.app_name)`
	domainCols              = `domain, ` + domainName + `, status, last_error, created_at, active_at`
	insertDomainQuery       = `INSERT INTO app_domain (domain, app_name, app_id, status, last_error, created_at) VALUES (?, ?, COALESCE((SELECT id FROM app WHERE name = ?), ''), ?, '', ?)`
	selectDomainsByAppQuery = `SELECT ` + domainCols + ` FROM app_domain WHERE app_id = (SELECT id FROM app WHERE name = ?) ORDER BY created_at`
	selectDomainQuery       = `SELECT ` + domainCols + ` FROM app_domain WHERE domain = ?`
	selectAllDomainsQuery   = `SELECT ` + domainCols + ` FROM app_domain`
	activeDomainsQuery      = `SELECT ` + domainName + `, domain FROM app_domain WHERE status = ? ORDER BY created_at`
	updateDomainStatusQuery = `UPDATE app_domain SET status = ?, last_error = ?, active_at = ? WHERE domain = ?`
	deleteDomainQuery       = `DELETE FROM app_domain WHERE domain = ?`
	deleteAppDomainsQuery   = `DELETE FROM app_domain WHERE app_id = ? OR (app_id = '' AND app_name = ?)`
)

var (
	// ErrAppDomainNotFound is returned when a custom app domain does not exist
	ErrAppDomainNotFound = errors.New("domain not found")
	// ErrAppDomainExists is returned when a domain is already registered to an app
	ErrAppDomainExists = errors.New("domain already in use")
)

// AddDomain registers a custom domain for an app, or ErrAppDomainExists if the
// domain already belongs to some app
func (s *Store) AddDomain(d *Domain) error {
	_, err := s.db.Exec(insertDomainQuery, d.Domain, d.AppName, d.AppName, string(d.Status), d.CreatedAt.Unix())
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrAppDomainExists
	}
	return err
}

// Domains lists an app's custom domains, oldest first
func (s *Store) Domains(appName string) ([]*Domain, error) {
	return s.queryDomains(selectDomainsByAppQuery, appName)
}

// AllDomains lists every custom domain, for building the routing cache and TLS
// management at startup
func (s *Store) AllDomains() ([]*Domain, error) {
	return s.queryDomains(selectAllDomainsQuery)
}

// ActiveDomains returns each app's first verified (active) custom domain in one
// query, so the app-list endpoint does not run a domain lookup per app.
func (s *Store) ActiveDomains() (map[string]string, error) {
	rows, err := s.db.Query(activeDomainsQuery, DomainActive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byApp := make(map[string]string)
	for rows.Next() {
		var app, domain string
		if err := rows.Scan(&app, &domain); err != nil {
			return nil, err
		}
		if _, seen := byApp[app]; !seen { // oldest active wins (ORDER BY created_at)
			byApp[app] = domain
		}
	}
	return byApp, rows.Err()
}

// Domain returns one domain by hostname, or ErrAppDomainNotFound
func (s *Store) Domain(domain string) (*Domain, error) {
	d, err := scanDomain(s.db.QueryRow(selectDomainQuery, domain))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAppDomainNotFound
	}
	return d, err
}

// SetDomainStatus updates a domain's issuance state; activeAt is nil unless the
// domain just became active
func (s *Store) SetDomainStatus(domain string, status DomainStatus, lastErr string, activeAt *time.Time) error {
	var active *int64
	if activeAt != nil {
		unix := activeAt.Unix()
		active = &unix
	}
	res, err := s.db.Exec(updateDomainStatusQuery, string(status), lastErr, active, domain)
	if err != nil {
		return err
	}
	return checkAffected(res, ErrAppDomainNotFound)
}

// DeleteDomain forgets a custom domain (the caller drops its certificate)
func (s *Store) DeleteDomain(domain string) error {
	_, err := s.db.Exec(deleteDomainQuery, domain)
	return err
}

func scanDomain(row scanner) (*Domain, error) {
	var d Domain
	var status string
	var createdAt int64
	var activeAt *int64
	if err := row.Scan(&d.Domain, &d.AppName, &status, &d.LastError, &createdAt, &activeAt); err != nil {
		return nil, err
	}
	d.Status = DomainStatus(status)
	d.CreatedAt = time.Unix(createdAt, 0)
	if activeAt != nil {
		t := time.Unix(*activeAt, 0)
		d.ActiveAt = &t
	}
	return &d, nil
}
