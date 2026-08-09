package store

import (
	"errors"
	"time"
)

const (
	insertAllowedDomainQuery = `
		INSERT INTO allowed_domain (domain, created_at) VALUES (?, ?)
		ON CONFLICT (domain) DO UPDATE SET created_at = created_at
		RETURNING created_at
	`
	selectAllowedDomainsQuery = `SELECT domain, created_at FROM allowed_domain ORDER BY domain`
	selectAllowedDomainQuery  = `SELECT COUNT(*) FROM allowed_domain WHERE domain = ?`
	deleteAllowedDomainQuery  = `DELETE FROM allowed_domain WHERE domain = ?`
)

var (
	// ErrDomainNotFound is returned when an allowed domain does not exist
	ErrDomainNotFound = errors.New("domain not found")
)

// AddAllowedDomain allows an email domain, filling in CreatedAt; adding one
// twice keeps the original time rather than moving it
func (s *Store) AddAllowedDomain(d *AllowedDomain) error {
	var createdAt int64
	if err := s.db.QueryRow(insertAllowedDomainQuery, d.Domain, time.Now().Unix()).Scan(&createdAt); err != nil {
		return err
	}
	d.CreatedAt = time.Unix(createdAt, 0)
	return nil
}

// AllowedDomains returns every allowed email domain, sorted
func (s *Store) AllowedDomains() ([]*AllowedDomain, error) {
	rows, err := s.db.Query(selectAllowedDomainsQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	domains := make([]*AllowedDomain, 0)
	for rows.Next() {
		var d AllowedDomain
		var createdAt int64
		if err := rows.Scan(&d.Domain, &createdAt); err != nil {
			return nil, err
		}
		d.CreatedAt = time.Unix(createdAt, 0)
		domains = append(domains, &d)
	}
	return domains, rows.Err()
}

// DomainAllowed reports whether an email domain skips the approval queue
func (s *Store) DomainAllowed(domain string) (bool, error) {
	var count int
	if err := s.db.QueryRow(selectAllowedDomainQuery, domain).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// RemoveAllowedDomain stops auto-approving a domain; users already approved
// under it keep their accounts
func (s *Store) RemoveAllowedDomain(domain string) error {
	res, err := s.db.Exec(deleteAllowedDomainQuery, domain)
	if err != nil {
		return err
	}
	return checkAffected(res, ErrDomainNotFound)
}
