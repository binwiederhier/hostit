package store

import (
	"database/sql"
	"errors"
	"time"
)

const (
	// The transcript is keyed on app_id so it follows a rename. app_name stays as
	// the row's PK (and is updated on rename) to keep upsert conflict handling and
	// to stop a reused name from colliding with a stale row.
	selectAssistantSessionQuery = `SELECT transcript FROM assistant_session WHERE app_id = (SELECT id FROM app WHERE name = ? AND id != '')`
	upsertAssistantSessionQuery = `
		INSERT INTO assistant_session (app_name, app_id, transcript, updated_at) VALUES (?, COALESCE((SELECT id FROM app WHERE name = ? AND id != ''), ''), ?, ?)
		ON CONFLICT(app_name) DO UPDATE SET transcript = excluded.transcript, updated_at = excluded.updated_at
	`
	deleteAssistantSessionByNameQuery = `DELETE FROM assistant_session WHERE app_id = (SELECT id FROM app WHERE name = ? AND id != '')`
	deleteAssistantSessionQuery       = `DELETE FROM assistant_session WHERE app_id = ? OR (app_id = '' AND app_name = ?)`
)

// LoadAssistantSession returns the app's stored assistant transcript (a JSON
// blob), or an empty string if there is none yet
func (s *Store) LoadAssistantSession(appName string) (string, error) {
	var transcript string
	err := s.db.QueryRow(selectAssistantSessionQuery, appName).Scan(&transcript)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return transcript, err
}

// SaveAssistantSession stores (upserts) the app's assistant transcript
func (s *Store) SaveAssistantSession(appName, transcript string) error {
	_, err := s.db.Exec(upsertAssistantSessionQuery, appName, appName, transcript, time.Now().Unix())
	return err
}

// DeleteAssistantSession forgets the app's assistant transcript
func (s *Store) DeleteAssistantSession(appName string) error {
	_, err := s.db.Exec(deleteAssistantSessionByNameQuery, appName)
	return err
}
