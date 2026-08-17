package store

import (
	"errors"
	"time"
)

const (
	// ProxyLocal is the proxy that shares control's host. Like the colocated
	// node it exists implicitly, so a single-box install needs no enrollment.
	ProxyLocal = "local"

	proxyCols            = `name, registered_at, last_seen`
	ensureProxyQuery     = `INSERT INTO proxy (name, registered_at) VALUES (?, ?) ON CONFLICT (name) DO NOTHING`
	selectProxyQuery     = `SELECT ` + proxyCols + ` FROM proxy WHERE name = ?`
	selectProxiesQuery   = `SELECT ` + proxyCols + ` FROM proxy ORDER BY name`
	updateProxySeenQuery = `UPDATE proxy SET last_seen = ? WHERE name = ?`
	deleteProxyQuery     = `DELETE FROM proxy WHERE name = ?`
)

var (
	// ErrProxyNotFound is returned when a proxy name is not registered
	ErrProxyNotFound = errors.New("proxy not found")
)

// EnsureProxy registers a proxy if it is not already; safe to call at every
// control start (which is how the colocated proxy comes to exist).
func (s *Store) EnsureProxy(name string) error {
	_, err := s.db.Exec(ensureProxyQuery, name, time.Now().Unix())
	return err
}

// Proxy returns one proxy by name, or ErrProxyNotFound.
func (s *Store) Proxy(name string) (*Proxy, error) {
	row := s.db.QueryRow(selectProxyQuery, name)
	return scanProxy(row)
}

// Proxies returns all registered proxies, sorted by name.
func (s *Store) Proxies() ([]*Proxy, error) {
	rows, err := s.db.Query(selectProxiesQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	proxies := make([]*Proxy, 0)
	for rows.Next() {
		p, err := scanProxy(rows)
		if err != nil {
			return nil, err
		}
		proxies = append(proxies, p)
	}
	return proxies, rows.Err()
}

// SetProxySeen records a connect/heartbeat timestamp, so an operator can tell
// a configured proxy from a serving one.
func (s *Store) SetProxySeen(name string, seen time.Time) error {
	_, err := s.db.Exec(updateProxySeenQuery, seen.Unix(), name)
	return err
}

// DeleteProxy unregisters a proxy; its certificate stops being accepted at the
// next connect, and the daemon drops the session it holds.
func (s *Store) DeleteProxy(name string) error {
	res, err := s.db.Exec(deleteProxyQuery, name)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrProxyNotFound
	}
	return nil
}

func scanProxy(row rowScanner) (*Proxy, error) {
	var registeredAt, lastSeen int64
	p := &Proxy{}
	if err := row.Scan(&p.Name, &registeredAt, &lastSeen); err != nil {
		return nil, ErrProxyNotFound
	}
	if registeredAt > 0 {
		p.RegisteredAt = time.Unix(registeredAt, 0)
	}
	if lastSeen > 0 {
		p.LastSeen = time.Unix(lastSeen, 0)
	}
	return p, nil
}
