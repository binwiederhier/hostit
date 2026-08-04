package store

import (
	"database/sql"
	"fmt"
)

const (
	createSchemaVersionTableQuery = `
		CREATE TABLE IF NOT EXISTS schema_version (
			version INTEGER PRIMARY KEY
		);
	`
	selectSchemaVersionQuery = `SELECT version FROM schema_version LIMIT 1`
	deleteSchemaVersionQuery = `DELETE FROM schema_version`
	insertSchemaVersionQuery = `INSERT INTO schema_version (version) VALUES (?)`
	appTableExistsQuery      = `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'app'`
)

// migrations are applied in order; ALWAYS append, never insert or edit, since
// deployed databases record how far they got (see STYLEGUIDE.md)
var migrations = []string{
	// 1: the original v0.1 schema
	`
		CREATE TABLE IF NOT EXISTS app (
			name TEXT PRIMARY KEY,
			port INTEGER NOT NULL UNIQUE,
			host TEXT NOT NULL,
			created_at INTEGER NOT NULL
		);
	`,
	// 2: users, tokens, keys, settings, app ownership and usage accounting
	`
		CREATE TABLE user (
			id TEXT PRIMARY KEY,
			email TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL,
			role TEXT NOT NULL,
			status TEXT NOT NULL,
			app_limit INTEGER,
			memory_mb INTEGER,
			disk_mb INTEGER,
			created_at INTEGER NOT NULL
		);
		CREATE TABLE token (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			hash TEXT NOT NULL UNIQUE,
			prefix TEXT NOT NULL,
			label TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			last_used INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX idx_token_user ON token (user_id);
		CREATE TABLE user_key (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			label TEXT NOT NULL,
			key TEXT NOT NULL,
			created_at INTEGER NOT NULL
		);
		CREATE INDEX idx_user_key_user ON user_key (user_id);
		CREATE TABLE app_key (
			app_name TEXT NOT NULL,
			key TEXT NOT NULL
		);
		CREATE INDEX idx_app_key_app ON app_key (app_name);
		CREATE TABLE setting (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
		ALTER TABLE app ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';
		ALTER TABLE app ADD COLUMN disk_mb INTEGER NOT NULL DEFAULT 0;
		ALTER TABLE app ADD COLUMN over_quota INTEGER NOT NULL DEFAULT 0;
		CREATE INDEX idx_app_owner ON app (owner_id);
	`,
}

// migrate brings the database up to the current schema version, creating it if
// necessary. Databases from before schema_version existed are detected by the
// presence of the app table and treated as version 1.
func migrate(db *sql.DB) error {
	if _, err := db.Exec(createSchemaVersionTableQuery); err != nil {
		return err
	}
	version, err := currentVersion(db)
	if err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		if err := applyMigration(db, i); err != nil {
			return err
		}
	}
	return nil
}

// applyMigration runs one migration and records the new version in the same
// transaction, so a failure leaves the database untouched and a success can
// never be replayed (which would fail with "table already exists")
func applyMigration(db *sql.DB, index int) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // No-op once committed
	if _, err := tx.Exec(migrations[index]); err != nil {
		return fmt.Errorf("migration %d failed: %w", index+1, err)
	}
	if _, err := tx.Exec(deleteSchemaVersionQuery); err != nil {
		return err
	}
	if _, err := tx.Exec(insertSchemaVersionQuery, index+1); err != nil {
		return err
	}
	return tx.Commit()
}

// currentVersion returns the schema version, inferring 1 for pre-versioning
// databases that already have an app table, and 0 for empty ones
func currentVersion(db *sql.DB) (int, error) {
	var version int
	err := db.QueryRow(selectSchemaVersionQuery).Scan(&version)
	if err == nil {
		return version, nil
	} else if err != sql.ErrNoRows {
		return 0, err
	}
	var count int
	if err := db.QueryRow(appTableExistsQuery).Scan(&count); err != nil {
		return 0, err
	}
	if count > 0 {
		return 1, nil
	}
	return 0, nil
}
