package store

import (
	"database/sql"
	"errors"
	"time"
)

const (
	insertUserQuery = `
		INSERT INTO user (id, email, name, role, status, app_limit, memory_mb, disk_mb, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	selectUserQuery = `
		SELECT id, email, name, role, status, app_limit, memory_mb, disk_mb, created_at
		FROM user WHERE id = ?
	`
	selectUserByEmailQuery = `
		SELECT id, email, name, role, status, app_limit, memory_mb, disk_mb, created_at
		FROM user WHERE email = ?
	`
	selectUsersQuery = `
		SELECT id, email, name, role, status, app_limit, memory_mb, disk_mb, created_at
		FROM user ORDER BY email
	`
	updateUserQuery = `
		UPDATE user SET name = ?, role = ?, status = ?, app_limit = ?, memory_mb = ?, disk_mb = ?
		WHERE id = ?
	`
	deleteUserQuery = `DELETE FROM user WHERE id = ?`

	insertTokenQuery        = `INSERT INTO token (id, user_id, hash, prefix, label, created_at) VALUES (?, ?, ?, ?, ?, ?)`
	selectTokenByHashQuery  = `SELECT id, user_id, hash, prefix, label, created_at, last_used FROM token WHERE hash = ?`
	selectTokensByUserQuery = `SELECT id, user_id, hash, prefix, label, created_at, last_used FROM token WHERE user_id = ? ORDER BY created_at`
	updateTokenUsedQuery    = `UPDATE token SET last_used = ? WHERE id = ?`
	deleteTokenQuery        = `DELETE FROM token WHERE user_id = ? AND id = ?`
	deleteTokensByUserQuery = `DELETE FROM token WHERE user_id = ?`

	insertUserKeyQuery  = `INSERT INTO user_key (id, user_id, label, key, created_at) VALUES (?, ?, ?, ?, ?)`
	selectUserKeysQuery = `SELECT id, user_id, label, key, created_at FROM user_key WHERE user_id = ? ORDER BY created_at`
	deleteUserKeyQuery  = `DELETE FROM user_key WHERE user_id = ? AND id = ?`
	deleteUserKeysQuery = `DELETE FROM user_key WHERE user_id = ?`

	insertAppKeyQuery  = `INSERT INTO app_key (app_name, key) VALUES (?, ?)`
	selectAppKeysQuery = `SELECT key FROM app_key WHERE app_name = ?`
	deleteAppKeysQuery = `DELETE FROM app_key WHERE app_name = ?`

	upsertSettingQuery  = `INSERT INTO setting (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`
	selectSettingsQuery = `SELECT key, value FROM setting`

	userIDPrefix  = "u_"
	tokenIDPrefix = "tk_"
	keyIDPrefix   = "k_"
	idLength      = 12
)

var (
	// ErrUserNotFound is returned when a user does not exist
	ErrUserNotFound = errors.New("user not found")
	// ErrTokenNotFound is returned when a token does not exist
	ErrTokenNotFound = errors.New("token not found")
	// ErrKeyNotFound is returned when an SSH key does not exist
	ErrKeyNotFound = errors.New("key not found")
)

// AddUser inserts a new user, assigning an ID and creation time
func (s *Store) AddUser(u *User) error {
	if u.ID == "" {
		u.ID = userIDPrefix + randomID()
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now()
	}
	_, err := s.db.Exec(insertUserQuery, u.ID, u.Email, u.Name, u.Role, u.Status,
		nullableInt(u.AppLimit), nullableInt(u.MemoryMB), nullableInt(u.DiskMB), u.CreatedAt.Unix())
	return err
}

// User returns a user by ID, or ErrUserNotFound
func (s *Store) User(id string) (*User, error) {
	return scanUser(s.db.QueryRow(selectUserQuery, id))
}

// UserByEmail returns a user by email address, or ErrUserNotFound
func (s *Store) UserByEmail(email string) (*User, error) {
	return scanUser(s.db.QueryRow(selectUserByEmailQuery, email))
}

// Users returns all users, sorted by email
func (s *Store) Users() ([]*User, error) {
	rows, err := s.db.Query(selectUsersQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := make([]*User, 0)
	for rows.Next() {
		u, err := scanUserRow(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// UpdateUser persists name, role, status and limit overrides
func (s *Store) UpdateUser(u *User) error {
	result, err := s.db.Exec(updateUserQuery, u.Name, u.Role, u.Status,
		nullableInt(u.AppLimit), nullableInt(u.MemoryMB), nullableInt(u.DiskMB), u.ID)
	if err != nil {
		return err
	}
	return checkAffected(result, ErrUserNotFound)
}

// RemoveUser deletes a user along with their tokens and profile keys; their
// apps are deleted by the caller (which must also remove the Unix users)
func (s *Store) RemoveUser(id string) error {
	result, err := s.db.Exec(deleteUserQuery, id)
	if err != nil {
		return err
	}
	if err := checkAffected(result, ErrUserNotFound); err != nil {
		return err
	}
	if _, err := s.db.Exec(deleteTokensByUserQuery, id); err != nil {
		return err
	}
	_, err = s.db.Exec(deleteUserKeysQuery, id)
	return err
}

// AddToken stores a new API token (hash only)
func (s *Store) AddToken(t *Token) error {
	if t.ID == "" {
		t.ID = tokenIDPrefix + randomID()
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}
	_, err := s.db.Exec(insertTokenQuery, t.ID, t.UserID, t.Hash, t.Prefix, t.Label, t.CreatedAt.Unix())
	return err
}

// TokenByHash looks up a token by its SHA-256 hash, or ErrTokenNotFound
func (s *Store) TokenByHash(hash string) (*Token, error) {
	return scanToken(s.db.QueryRow(selectTokenByHashQuery, hash))
}

// TokensByUser returns all tokens of a user, oldest first
func (s *Store) TokensByUser(userID string) ([]*Token, error) {
	rows, err := s.db.Query(selectTokensByUserQuery, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tokens := make([]*Token, 0)
	for rows.Next() {
		t, err := scanTokenRow(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// TouchToken records that a token was just used
func (s *Store) TouchToken(id string) error {
	_, err := s.db.Exec(updateTokenUsedQuery, time.Now().Unix(), id)
	return err
}

// RemoveToken deletes one of the user's own tokens
func (s *Store) RemoveToken(userID, id string) error {
	result, err := s.db.Exec(deleteTokenQuery, userID, id)
	if err != nil {
		return err
	}
	return checkAffected(result, ErrTokenNotFound)
}

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
	if _, err := s.db.Exec(deleteAppKeysQuery, appName); err != nil {
		return err
	}
	for _, key := range keys {
		if _, err := s.db.Exec(insertAppKeyQuery, appName, key); err != nil {
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

// SetSetting stores a global setting (upsert)
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(upsertSettingQuery, key, value)
	return err
}

// Settings returns all global settings
func (s *Store) Settings() (map[string]string, error) {
	rows, err := s.db.Query(selectSettingsQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	settings := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		settings[key] = value
	}
	return settings, rows.Err()
}

func scanUser(row *sql.Row) (*User, error) {
	u, err := scanUserValues(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	return u, err
}

func scanUserRow(rows *sql.Rows) (*User, error) {
	return scanUserValues(rows.Scan)
}

func scanUserValues(scan func(dest ...any) error) (*User, error) {
	var u User
	var appLimit, memoryMB, diskMB sql.NullInt64
	var createdAt int64
	err := scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Status, &appLimit, &memoryMB, &diskMB, &createdAt)
	if err != nil {
		return nil, err
	}
	u.AppLimit = intFromNull(appLimit)
	u.MemoryMB = intFromNull(memoryMB)
	u.DiskMB = intFromNull(diskMB)
	u.CreatedAt = time.Unix(createdAt, 0)
	return &u, nil
}

func scanToken(row *sql.Row) (*Token, error) {
	t, err := scanTokenValues(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTokenNotFound
	}
	return t, err
}

func scanTokenRow(rows *sql.Rows) (*Token, error) {
	return scanTokenValues(rows.Scan)
}

func scanTokenValues(scan func(dest ...any) error) (*Token, error) {
	var t Token
	var createdAt, lastUsed int64
	if err := scan(&t.ID, &t.UserID, &t.Hash, &t.Prefix, &t.Label, &createdAt, &lastUsed); err != nil {
		return nil, err
	}
	t.CreatedAt = time.Unix(createdAt, 0)
	if lastUsed > 0 {
		t.LastUsed = time.Unix(lastUsed, 0)
	}
	return &t, nil
}

func nullableInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func intFromNull(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	i := int(v.Int64)
	return &i
}
