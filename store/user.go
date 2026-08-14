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

	// The no-op UPDATE keeps the original created_at while still letting
	// RETURNING report it, so re-adding a domain is idempotent and truthful
	updateAppOwnerQuery        = `UPDATE app SET owner_id = ? WHERE owner_id = ?`
	updateTokenOwnerByAppQuery = `UPDATE token SET user_id = ? WHERE app_name = ?`

	userIDPrefix = "u_"
)

var (
	// ErrUserNotFound is returned when a user does not exist
	ErrUserNotFound = errors.New("user not found")
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
	if _, err := s.db.Exec(deleteUserKeysQuery, id); err != nil {
		return err
	}
	if _, err := s.db.Exec(deleteCollaboratorsByUserQuery, id); err != nil {
		return err
	}
	_, err = s.db.Exec(deleteUserAssistantQuery, id)
	return err
}

// TransferApps hands every app of one user to another, together with the
// app-scoped tokens that go with them, and returns the names it moved.
//
// The tokens have to move too: they belong to the app, not the person, and
// deleting the old user afterwards would otherwise take the credentials of apps
// that are no longer theirs. Account-wide tokens stay behind and die with them.
func (s *Store) TransferApps(fromUserID, toUserID string) ([]string, error) {
	apps, err := s.AppsByOwner(fromUserID)
	if err != nil {
		return nil, err
	}
	moved := make([]string, 0, len(apps))
	for _, a := range apps {
		if _, err := s.db.Exec(updateTokenOwnerByAppQuery, toUserID, a.Name); err != nil {
			return nil, err
		}
		moved = append(moved, a.Name)
	}
	if _, err := s.db.Exec(updateAppOwnerQuery, toUserID, fromUserID); err != nil {
		return nil, err
	}
	return moved, nil
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
