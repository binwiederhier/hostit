// Package store persists the app registry (name, port, runner host) in a SQLite database.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // SQLite driver (pure Go, no cgo)
)

const (
	createTablesQuery = `
		CREATE TABLE IF NOT EXISTS app (
			name TEXT PRIMARY KEY,
			port INTEGER NOT NULL UNIQUE,
			host TEXT NOT NULL,
			created_at INTEGER NOT NULL
		);
	`
	insertAppQuery   = `INSERT INTO app (name, port, host, created_at) VALUES (?, ?, ?, ?)`
	selectAppQuery   = `SELECT name, port, host, created_at FROM app WHERE name = ?`
	selectAppsQuery  = `SELECT name, port, host, created_at FROM app ORDER BY name`
	selectPortsQuery = `SELECT port FROM app ORDER BY port`
	deleteAppQuery   = `DELETE FROM app WHERE name = ?`
)

var (
	// ErrAppNotFound is returned when an app does not exist in the registry
	ErrAppNotFound = errors.New("app not found")
)

// Store is the SQLite-backed app registry
type Store struct {
	db *sql.DB
}

// NewStore opens (and if necessary creates) the registry database at filename
func NewStore(filename string) (*Store, error) {
	db, err := sql.Open("sqlite", filename)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(createTablesQuery); err != nil {
		return nil, fmt.Errorf("cannot create tables: %w", err)
	}
	return &Store{db: db}, nil
}

// AddApp inserts a new app; name and port must be unique
func (s *Store) AddApp(app *App) error {
	if app.CreatedAt.IsZero() {
		app.CreatedAt = time.Now()
	}
	_, err := s.db.Exec(insertAppQuery, app.Name, app.Port, app.Host, app.CreatedAt.Unix())
	return err
}

// App returns the app with the given name, or ErrAppNotFound
func (s *Store) App(name string) (*App, error) {
	row := s.db.QueryRow(selectAppQuery, name)
	return scanApp(row)
}

// Apps returns all registered apps, sorted by name
func (s *Store) Apps() ([]*App, error) {
	rows, err := s.db.Query(selectAppsQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	apps := make([]*App, 0)
	for rows.Next() {
		var app App
		var createdAt int64
		if err := rows.Scan(&app.Name, &app.Port, &app.Host, &createdAt); err != nil {
			return nil, err
		}
		app.CreatedAt = time.Unix(createdAt, 0)
		apps = append(apps, &app)
	}
	return apps, rows.Err()
}

// UsedPorts returns all allocated ports, sorted ascending
func (s *Store) UsedPorts() ([]int, error) {
	rows, err := s.db.Query(selectPortsQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ports := make([]int, 0)
	for rows.Next() {
		var port int
		if err := rows.Scan(&port); err != nil {
			return nil, err
		}
		ports = append(ports, port)
	}
	return ports, rows.Err()
}

// RemoveApp deletes the app with the given name, or returns ErrAppNotFound
func (s *Store) RemoveApp(name string) error {
	result, err := s.db.Exec(deleteAppQuery, name)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrAppNotFound
	}
	return nil
}

// Close closes the underlying database
func (s *Store) Close() error {
	return s.db.Close()
}

func scanApp(row *sql.Row) (*App, error) {
	var app App
	var createdAt int64
	err := row.Scan(&app.Name, &app.Port, &app.Host, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAppNotFound
	} else if err != nil {
		return nil, err
	}
	app.CreatedAt = time.Unix(createdAt, 0)
	return &app, nil
}
