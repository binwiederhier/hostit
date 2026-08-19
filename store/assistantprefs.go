package store

import (
	"database/sql"
	"errors"
)

const (
	// A user's explicit assistant permissions. A missing row means the user
	// inherits the global defaults; a present row overrides them.

	// An app's last-used assistant mode, keyed on app_id so a rename keeps it.
	selectAppModeQuery = `SELECT mode FROM app_assistant WHERE app_id = (SELECT id FROM app WHERE name = ?)`
	upsertAppModeQuery = `
		INSERT INTO app_assistant (app_id, mode) SELECT id, ? FROM app WHERE name = ?
		ON CONFLICT(app_id) DO UPDATE SET mode = excluded.mode
	`
	deleteAppAssistantQuery = `DELETE FROM app_assistant WHERE app_id = ?`

	// Global defaults for users without an explicit override (stored in setting).
)

// UserAssistant is a user's explicit assistant permissions: whether they may use
// the External Claude mode, and which API models they may pick (empty = all).
type UserAssistant struct {
	ExternalAllowed bool
	AllowedModels   []string
}

// AppAssistantMode returns the mode an app last used, or "" if it never set one
// (in which case the caller applies the global default mode).
func (s *Store) AppAssistantMode(appName string) (string, error) {
	var mode string
	err := s.db.QueryRow(selectAppModeQuery, appName).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return mode, err
}

// SetAppAssistantMode records the mode an app is using, so it is remembered.
func (s *Store) SetAppAssistantMode(appName, mode string) error {
	_, err := s.db.Exec(upsertAppModeQuery, mode, appName)
	return err
}
