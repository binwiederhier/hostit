package store

import (
	"errors"
	"time"
)

const (
	insertUserKeyQuery  = `INSERT INTO user_key (id, user_id, label, key, created_at) VALUES (?, ?, ?, ?, ?)`
	selectUserKeysQuery = `SELECT id, user_id, label, key, created_at FROM user_key WHERE user_id = ? ORDER BY created_at`
	deleteUserKeyQuery  = `DELETE FROM user_key WHERE user_id = ? AND id = ?`
	deleteUserKeysQuery = `DELETE FROM user_key WHERE user_id = ?`

	// app_key rows are keyed on app_id so they survive a rename; the app is still
	// addressed by name at the boundary, resolved to its id in each statement.
	insertAppKeyQuery        = `INSERT INTO app_key (app_name, app_id, key) VALUES (?, COALESCE((SELECT id FROM app WHERE name = ?), ''), ?)`
	selectAppKeysQuery       = `SELECT key FROM app_key WHERE app_id = (SELECT id FROM app WHERE name = ?)`
	deleteAppKeysByNameQuery = `DELETE FROM app_key WHERE app_id = (SELECT id FROM app WHERE name = ?)`
	deleteAppKeysQuery       = `DELETE FROM app_key WHERE app_id = ? OR (app_id = '' AND app_name = ?)`

	keyIDPrefix = "k_"
)

var (
	// ErrKeyNotFound is returned when an SSH key does not exist
	ErrKeyNotFound = errors.New("key not found")
)

// AddUserKey adds an SSH public key to a user's profile
func (s *Store) AddUserKey(k *UserKey) error {
	if k.ID == "" {
		k.ID = keyIDPrefix + randomID()
	}
	if k.CreatedAt.IsZero() {
		k.CreatedAt = time.Now()
	}
	_, err := s.db.Exec(insertUserKeyQuery, k.ID, k.UserID, k.Label, k.Key, k.CreatedAt.Unix())
	return err
}

// UserKeys returns a user's profile SSH keys
func (s *Store) UserKeys(userID string) ([]*UserKey, error) {
	rows, err := s.db.Query(selectUserKeysQuery, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := make([]*UserKey, 0)
	for rows.Next() {
		var k UserKey
		var createdAt int64
		if err := rows.Scan(&k.ID, &k.UserID, &k.Label, &k.Key, &createdAt); err != nil {
			return nil, err
		}
		k.CreatedAt = time.Unix(createdAt, 0)
		keys = append(keys, &k)
	}
	return keys, rows.Err()
}

// RemoveUserKey deletes one of the user's own profile keys
func (s *Store) RemoveUserKey(userID, id string) error {
	result, err := s.db.Exec(deleteUserKeyQuery, userID, id)
	if err != nil {
		return err
	}
	return checkAffected(result, ErrKeyNotFound)
}

// SetAppKeys replaces the app-specific SSH keys (e.g. hostit-generated ones)
func (s *Store) SetAppKeys(appName string, keys []string) error {
	if _, err := s.db.Exec(deleteAppKeysByNameQuery, appName); err != nil {
		return err
	}
	for _, key := range keys {
		if _, err := s.db.Exec(insertAppKeyQuery, appName, appName, key); err != nil {
			return err
		}
	}
	return nil
}

// AppKeys returns the app-specific SSH keys
func (s *Store) AppKeys(appName string) ([]string, error) {
	rows, err := s.db.Query(selectAppKeysQuery, appName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}
