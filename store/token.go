package store

import (
	"database/sql"
	"errors"
	"time"
)

const (
	// An app-scoped token is keyed on app_id so it survives the app's rename;
	// tokenName resolves the app's current name for display. Account-wide tokens
	// have an empty app_id and app_name.
	tokenName               = `COALESCE((SELECT name FROM app a WHERE a.id = token.app_id), token.app_name)`
	tokenCols               = `id, user_id, hash, prefix, label, ` + tokenName + `, secret, created_at, last_used`
	insertTokenQuery        = `INSERT INTO token (id, user_id, hash, prefix, label, app_name, app_id, secret, created_at) VALUES (?, ?, ?, ?, ?, ?, COALESCE((SELECT id FROM app WHERE name = ?), ''), ?, ?)`
	selectTokenByHashQuery  = `SELECT ` + tokenCols + ` FROM token WHERE hash = ?`
	selectTokensByUserQuery = `SELECT ` + tokenCols + ` FROM token WHERE user_id = ? ORDER BY created_at`
	updateTokenUsedQuery    = `UPDATE token SET last_used = ? WHERE id = ?`
	selectTokensByAppQuery  = `SELECT ` + tokenCols + ` FROM token WHERE app_id = (SELECT id FROM app WHERE name = ?) ORDER BY created_at`
	deleteTokenQuery        = `DELETE FROM token WHERE user_id = ? AND id = ?`
	deleteTokensByAppQuery  = `DELETE FROM token WHERE app_id = ? OR (app_id = '' AND app_name = ?)`
	deleteTokensByUserQuery = `DELETE FROM token WHERE user_id = ?`

	tokenIDPrefix = "tk_"
)

var (
	// ErrTokenNotFound is returned when a token does not exist
	ErrTokenNotFound = errors.New("token not found")
)

// AddToken stores a new API token (hash only)
func (s *Store) AddToken(t *Token) error {
	if t.ID == "" {
		t.ID = tokenIDPrefix + randomID()
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}
	_, err := s.db.Exec(insertTokenQuery, t.ID, t.UserID, t.Hash, t.Prefix, t.Label, t.AppName, t.AppName, t.Secret, t.CreatedAt.Unix())
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

// TokensByApp returns the tokens scoped to one app
func (s *Store) TokensByApp(appName string) ([]*Token, error) {
	rows, err := s.db.Query(selectTokensByAppQuery, appName)
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

// RemoveTokensByApp deletes every token scoped to an app, used when it is deleted.
// It keys on the app's id (with a name fallback for tokens not yet backfilled),
// never on the empty app_id that account-wide tokens carry.
func (s *Store) RemoveTokensByApp(appID, appName string) error {
	_, err := s.db.Exec(deleteTokensByAppQuery, appID, appName)
	return err
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
	if err := scan(&t.ID, &t.UserID, &t.Hash, &t.Prefix, &t.Label, &t.AppName, &t.Secret, &createdAt, &lastUsed); err != nil {
		return nil, err
	}
	t.CreatedAt = time.Unix(createdAt, 0)
	if lastUsed > 0 {
		used := time.Unix(lastUsed, 0)
		t.LastUsed = &used
	}
	return &t, nil
}
