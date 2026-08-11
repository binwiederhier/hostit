package store

import (
	"database/sql"
	"errors"
	"strings"
)

const (
	// A user's explicit assistant permissions. A missing row means the user
	// inherits the global defaults; a present row overrides them.
	selectUserAssistantQuery = `SELECT external_allowed, allowed_models FROM user_assistant WHERE user_id = ?`
	upsertUserAssistantQuery = `
		INSERT INTO user_assistant (user_id, external_allowed, allowed_models) VALUES (?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET external_allowed = excluded.external_allowed, allowed_models = excluded.allowed_models
	`
	deleteUserAssistantQuery = `DELETE FROM user_assistant WHERE user_id = ?`

	// An app's last-used assistant mode, keyed on app_id so a rename keeps it.
	selectAppModeQuery = `SELECT mode FROM app_assistant WHERE app_id = (SELECT id FROM app WHERE name = ?)`
	upsertAppModeQuery = `
		INSERT INTO app_assistant (app_id, mode) SELECT id, ? FROM app WHERE name = ?
		ON CONFLICT(app_id) DO UPDATE SET mode = excluded.mode
	`
	deleteAppAssistantQuery = `DELETE FROM app_assistant WHERE app_id = ?`

	// Global defaults for users without an explicit override (stored in setting).
	SettingAssistantDefaultExternal = "assistant_default_external_allowed" // "1"/"0"
	SettingAssistantDefaultModels   = "assistant_default_allowed_models"   // csv; empty = all
	SettingAssistantDefaultMode     = "assistant_default_mode"             // external-claude or a model id
)

// UserAssistant is a user's explicit assistant permissions: whether they may use
// the External Claude mode, and which API models they may pick (empty = all).
type UserAssistant struct {
	ExternalAllowed bool
	AllowedModels   []string
}

// UserAssistant returns a user's explicit permission override, or nil if they
// have none (in which case the caller applies the global defaults).
func (s *Store) UserAssistant(userID string) (*UserAssistant, error) {
	var ext int
	var models string
	err := s.db.QueryRow(selectUserAssistantQuery, userID).Scan(&ext, &models)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return &UserAssistant{ExternalAllowed: ext != 0, AllowedModels: splitModels(models)}, nil
}

// SetUserAssistant records a user's explicit permission override.
func (s *Store) SetUserAssistant(userID string, externalAllowed bool, allowedModels []string) error {
	_, err := s.db.Exec(upsertUserAssistantQuery, userID, boolToInt(externalAllowed), joinModels(allowedModels))
	return err
}

// DeleteUserAssistant drops a user's override so they inherit the global default.
func (s *Store) DeleteUserAssistant(userID string) error {
	_, err := s.db.Exec(deleteUserAssistantQuery, userID)
	return err
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

// splitModels parses the stored comma-separated allowlist; empty means "all".
func splitModels(csv string) []string {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func joinModels(models []string) string {
	return strings.Join(models, ",")
}
