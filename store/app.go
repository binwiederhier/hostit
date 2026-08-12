package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

const (
	insertAppQuery = `INSERT INTO app (id, name, port, host, owner_id, created_at, image_tag) VALUES (?, ?, ?, ?, ?, ?, ?)`
	selectAppQuery = `
		SELECT id, name, port, host, owner_id, disk_mb, created_at, image_tag
		FROM app WHERE name = ?
	`
	selectAppsQuery = `
		SELECT id, name, port, host, owner_id, disk_mb, created_at, image_tag
		FROM app ORDER BY name
	`
	selectAppsByOwnerQuery = `
		SELECT id, name, port, host, owner_id, disk_mb, created_at, image_tag
		FROM app WHERE owner_id = ? ORDER BY name
	`
	selectAppCountByOwnerQuery = `SELECT COUNT(*) FROM app WHERE owner_id = ?`
	selectPortsQuery           = `SELECT port FROM app ORDER BY port`
	updateAppUsageQuery        = `UPDATE app SET disk_mb = ? WHERE name = ?`
	deleteAppQuery             = `DELETE FROM app WHERE name = ?`
	renameAppQuery             = `UPDATE app SET name = ? WHERE name = ?`
	// After the app is renamed, keep assistant_session's name mirror (its primary
	// key) truthful, so a later app that reuses the freed name cannot collide with a
	// stale row. Every other per-app table keys on app_id and needs no update.
	renameAssistantMirrorQuery = `UPDATE assistant_session SET app_name = ? WHERE app_id = (SELECT id FROM app WHERE name = ?)`
	// Image pinning: backfill unpinned apps, and list every tag still in use so
	// image GC never removes one an app is pinned to.
	pinImageTagsQuery      = `UPDATE app SET image_tag = ? WHERE image_tag = ''`
	imageTagsInUseQuery    = `SELECT DISTINCT image_tag FROM app WHERE image_tag != ''`
	updateAppImageTagQuery = `UPDATE app SET image_tag = ? WHERE name = ?`
	// App-id backfill: name the unpinned apps so the daemon can assign each a fresh
	// id on startup (SQL can't generate a distinct random id per row).
	selectAppsWithoutIDQuery = `SELECT name FROM app WHERE id = ''`
	setAppIDQuery            = `UPDATE app SET id = ? WHERE name = ? AND id = ''`
)

// fkAppIDBackfillQueries fill each per-app table's app_id from the app it points
// at by name, for rows that predate the app_id columns. Only rows whose app still
// exists are touched; account-wide tokens (empty app_name) and orphans are left
// with an empty app_id. All are idempotent (WHERE app_id = ”).
var fkAppIDBackfillQueries = []string{
	`UPDATE app_key SET app_id = (SELECT id FROM app WHERE app.name = app_key.app_name) WHERE app_id = '' AND EXISTS (SELECT 1 FROM app WHERE app.name = app_key.app_name)`,
	`UPDATE token SET app_id = (SELECT id FROM app WHERE app.name = token.app_name) WHERE app_id = '' AND app_name != '' AND EXISTS (SELECT 1 FROM app WHERE app.name = token.app_name)`,
	`UPDATE assistant_session SET app_id = (SELECT id FROM app WHERE app.name = assistant_session.app_name) WHERE app_id = '' AND EXISTS (SELECT 1 FROM app WHERE app.name = assistant_session.app_name)`,
	`UPDATE snapshot SET app_id = (SELECT id FROM app WHERE app.name = snapshot.app_name) WHERE app_id = '' AND EXISTS (SELECT 1 FROM app WHERE app.name = snapshot.app_name)`,
	`UPDATE app_domain SET app_id = (SELECT id FROM app WHERE app.name = app_domain.app_name) WHERE app_id = '' AND EXISTS (SELECT 1 FROM app WHERE app.name = app_domain.app_name)`,
	`UPDATE app_event SET app_id = (SELECT id FROM app WHERE app.name = app_event.app_name) WHERE app_id = '' AND EXISTS (SELECT 1 FROM app WHERE app.name = app_event.app_name)`,
}

var (
	// ErrAppNotFound is returned when an app does not exist in the registry
	ErrAppNotFound = errors.New("app not found")
	// ErrAppNameTaken is returned when a rename targets a name already in use
	ErrAppNameTaken = errors.New("app name already in use")
)

// AddApp inserts a new app; name and port must be unique. It assigns the app a
// fresh id when one is not set, so every app has a stable identity from birth.
func (s *Store) AddApp(app *App) error {
	if app.CreatedAt.IsZero() {
		app.CreatedAt = time.Now()
	}
	if app.ID == "" {
		app.ID = randomID()
	}
	_, err := s.db.Exec(insertAppQuery, app.ID, app.Name, app.Port, app.Host, app.OwnerID, app.CreatedAt.Unix(), app.ImageTag)
	return err
}

// BackfillAppIDs assigns an id to every app that has none (apps created before
// app ids), so id becomes a complete, unique identity. Idempotent: once every
// app has an id there is nothing to do.
func (s *Store) BackfillAppIDs() error {
	rows, err := s.db.Query(selectAppsWithoutIDQuery)
	if err != nil {
		return err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, name := range names {
		if _, err := s.db.Exec(setAppIDQuery, randomID(), name); err != nil {
			return err
		}
	}
	return nil
}

// RenameApp changes an app's name in one transaction. Everything durable keys on
// app_id, so this is a pure metadata update: the app row's name, plus the
// assistant_session name mirror (its PK). Returns ErrAppNotFound if the old name
// is unknown, or ErrAppNameTaken if the new name is already in use.
func (s *Store) RenameApp(oldName, newName string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // No-op once committed
	res, err := tx.Exec(renameAppQuery, newName, oldName)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrAppNameTaken
		}
		return err
	}
	if err := checkAffected(res, ErrAppNotFound); err != nil {
		return err
	}
	if _, err := tx.Exec(renameAssistantMirrorQuery, newName, newName); err != nil {
		return err
	}
	return tx.Commit()
}

// BackfillFKAppIDs fills app_id on every per-app table from the app it points at
// by name, for rows created before those columns existed. Must run after
// BackfillAppIDs so the app ids it copies are present. Idempotent.
func (s *Store) BackfillFKAppIDs() error {
	for _, query := range fkAppIDBackfillQueries {
		if _, err := s.db.Exec(query); err != nil {
			return err
		}
	}
	return nil
}

// PinImageTags backfills every app that has no pinned image tag with the given
// one, so apps from before image pinning stay on the image they are running.
func (s *Store) PinImageTags(tag string) error {
	_, err := s.db.Exec(pinImageTagsQuery, tag)
	return err
}

// ImageTagsInUse returns the set of workspace image tags apps are pinned to, so
// image GC keeps them even when their container is momentarily gone.
func (s *Store) ImageTagsInUse() (map[string]bool, error) {
	rows, err := s.db.Query(imageTagsInUseQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tags := make(map[string]bool)
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		tags[tag] = true
	}
	return tags, rows.Err()
}

// SetAppImageTag repins one app (used when an app is deliberately moved onto a
// rebuilt image).
func (s *Store) SetAppImageTag(name, tag string) error {
	_, err := s.db.Exec(updateAppImageTagQuery, tag, name)
	return err
}

// App returns the app with the given name, or ErrAppNotFound
func (s *Store) App(name string) (*App, error) {
	var app App
	var createdAt int64
	err := s.db.QueryRow(selectAppQuery, name).Scan(&app.ID, &app.Name, &app.Port, &app.Host, &app.OwnerID, &app.DiskMB, &createdAt, &app.ImageTag)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAppNotFound
	} else if err != nil {
		return nil, err
	}
	app.CreatedAt = time.Unix(createdAt, 0)
	return &app, nil
}

// Apps returns all registered apps, sorted by name
func (s *Store) Apps() ([]*App, error) {
	return s.queryApps(selectAppsQuery)
}

// AppsByOwner returns the apps owned by a user, sorted by name
func (s *Store) AppsByOwner(ownerID string) ([]*App, error) {
	return s.queryApps(selectAppsByOwnerQuery, ownerID)
}

// AppCountByOwner counts a user's apps, for limit enforcement
func (s *Store) AppCountByOwner(ownerID string) (int, error) {
	var count int
	err := s.db.QueryRow(selectAppCountByOwnerQuery, ownerID).Scan(&count)
	return count, err
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

// UpdateAppUsage records an app's measured disk usage (in MB) for the dashboard
func (s *Store) UpdateAppUsage(name string, diskMB int) error {
	result, err := s.db.Exec(updateAppUsageQuery, diskMB, name)
	if err != nil {
		return err
	}
	return checkAffected(result, ErrAppNotFound)
}

// RemoveApp deletes the app with the given name and everything attached to it, or
// returns ErrAppNotFound. The app is looked up first so its id is known before its
// row is gone: the per-app tables key on app_id, so the cascade deletes by id (and
// falls back to the name for any row not yet backfilled).
func (s *Store) RemoveApp(name string) error {
	app, err := s.App(name)
	if err != nil {
		return err // ErrAppNotFound if it does not exist
	}
	if _, err := s.db.Exec(deleteAppQuery, name); err != nil {
		return err
	}
	if _, err := s.db.Exec(deleteAppKeysQuery, app.ID, name); err != nil {
		return err
	}
	if _, err := s.db.Exec(deleteAssistantSessionQuery, app.ID, name); err != nil {
		return err
	}
	if _, err := s.db.Exec(deleteAppSnapshotsQuery, app.ID, name); err != nil {
		return err
	}
	if _, err := s.db.Exec(deleteAppDomainsQuery, app.ID, name); err != nil {
		return err
	}
	if _, err := s.db.Exec(deleteAppEventsQuery, app.ID, name); err != nil {
		return err
	}
	if _, err := s.db.Exec(deleteAppUsageQuery, app.ID); err != nil {
		return err
	}
	if _, err := s.db.Exec(deleteAppAssistantQuery, app.ID); err != nil {
		return err
	}
	return s.RemoveTokensByApp(app.ID, name)
}

func (s *Store) queryApps(query string, args ...any) ([]*App, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	apps := make([]*App, 0)
	for rows.Next() {
		var app App
		var createdAt int64
		if err := rows.Scan(&app.ID, &app.Name, &app.Port, &app.Host, &app.OwnerID, &app.DiskMB, &createdAt, &app.ImageTag); err != nil {
			return nil, err
		}
		app.CreatedAt = time.Unix(createdAt, 0)
		apps = append(apps, &app)
	}
	return apps, rows.Err()
}
