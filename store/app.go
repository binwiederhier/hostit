package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

const (
	insertAppQuery = `INSERT INTO app (id, name, port, host, owner_id, created_at, image_tag, uid, private) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	selectAppQuery = `
		SELECT id, name, port, host, owner_id, disk_mb, created_at, image_tag, powered_off, uid, archived, private, memory_limit_mb, disk_limit_mb, cpu_milli, tabs, soft_deleted_at
		FROM app WHERE name = ?
	`
	selectAppByUIDQuery = `
		SELECT id, name, port, host, owner_id, disk_mb, created_at, image_tag, powered_off, uid, archived, private, memory_limit_mb, disk_limit_mb, cpu_milli, tabs, soft_deleted_at
		FROM app WHERE uid = ?
	`
	selectAppsQuery = `
		SELECT id, name, port, host, owner_id, disk_mb, created_at, image_tag, powered_off, uid, archived, private, memory_limit_mb, disk_limit_mb, cpu_milli, tabs, soft_deleted_at
		FROM app ORDER BY name
	`
	selectAppsByOwnerQuery = `
		SELECT id, name, port, host, owner_id, disk_mb, created_at, image_tag, powered_off, uid, archived, private, memory_limit_mb, disk_limit_mb, cpu_milli, tabs, soft_deleted_at
		FROM app WHERE owner_id = ? ORDER BY name
	`
	selectAppHostQuery         = `SELECT host FROM app WHERE name = ?`
	selectAppCountByOwnerQuery = `SELECT COUNT(*) FROM app WHERE owner_id = ? AND soft_deleted_at = 0`
	selectPortsQuery           = `SELECT port FROM app ORDER BY port`
	updateAppUsageQuery        = `UPDATE app SET disk_mb = ? WHERE name = ?`
	updateAppPoweredOffQuery   = `UPDATE app SET powered_off = ? WHERE name = ?`
	updateAppArchivedQuery     = `UPDATE app SET archived = ? WHERE name = ?`
	updateAppSoftDeletedQuery  = `UPDATE app SET soft_deleted_at = ? WHERE name = ?`
	updateAppPrivateQuery      = `UPDATE app SET private = ? WHERE name = ?`
	updateAppTabsQuery         = `UPDATE app SET tabs = ? WHERE name = ?`
	updateAppUIDQuery          = `UPDATE app SET uid = ? WHERE name = ?`
	deleteAppQuery             = `DELETE FROM app WHERE name = ?`
	renameAppQuery             = `UPDATE app SET name = ? WHERE name = ?`
	setAppOwnerQuery           = `UPDATE app SET owner_id = ? WHERE name = ?`
	updateAppLimitsQuery       = `UPDATE app SET memory_limit_mb = ?, disk_limit_mb = ?, cpu_milli = ? WHERE name = ?`
	// After the app is renamed, keep assistant_session's name mirror (its primary
	// key) truthful, so a later app that reuses the freed name cannot collide with a
	// stale row. Every other per-app table keys on app_id and needs no update.
	renameAssistantMirrorQuery = `UPDATE assistant_session SET app_name = ? WHERE app_id = (SELECT id FROM app WHERE name = ?)`
	// Every tag still in use, so image GC never removes one an app is pinned to.
	imageTagsInUseQuery = `SELECT DISTINCT image_tag FROM app WHERE image_tag != ''`
)

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
	_, err := s.db.Exec(insertAppQuery, app.ID, app.Name, app.Port, app.Host, app.OwnerID, app.CreatedAt.Unix(), app.ImageTag, app.UID, app.Private)
	return err
}

// SetAppUID records the base uid the hosting node allocated for the app; used
// by the one-time backfill for rows created before uid recording.
// AppByUID resolves an app from the uid its unix account and container run as.
// That uid is recorded here, so this answers for an app on ANY node, where the
// local passwd file only knows the apps that live on this host.
func (s *Store) AppByUID(uid int) (*App, error) {
	app := &App{}
	var createdAt, softDeletedAt int64
	err := s.db.QueryRow(selectAppByUIDQuery, uid).Scan(&app.ID, &app.Name, &app.Port, &app.Host, &app.OwnerID, &app.DiskMB, &createdAt, &app.ImageTag, &app.PoweredOff, &app.UID, &app.Archived, &app.Private, &app.MemoryLimitMB, &app.DiskLimitMB, &app.CPUMilli, &app.Tabs, &softDeletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAppNotFound
	}
	if err != nil {
		return nil, err
	}
	app.CreatedAt = time.Unix(createdAt, 0)
	if softDeletedAt != 0 {
		app.SoftDeletedAt = time.Unix(softDeletedAt, 0)
	}
	return app, nil
}

func (s *Store) SetAppUID(name string, uid int) error {
	_, err := s.db.Exec(updateAppUIDQuery, uid, name)
	return err
}

// SetAppOwner moves an app to a new owner; collaborator grants are the
// caller's concern (the server keeps the old owner on as a collaborator).
func (s *Store) SetAppOwner(name, ownerID string) error {
	_, err := s.db.Exec(setAppOwnerQuery, ownerID, name)
	return err
}

// SetAppArchived shelves an app, or brings it back. Archiving is deliberately
// not the same switch as poweroff: the two are independent columns so an app
// leaving the archive returns to the power state it had, rather than to a
// guess.
func (s *Store) SetAppArchived(name string, archived bool) error {
	_, err := s.db.Exec(updateAppArchivedQuery, archived, name)
	return err
}

// SetAppSoftDeleted stamps (or clears, with the zero time) when an app was
// soft-deleted. A non-zero stamp hides it from its owner and schedules its real
// deletion; the zero time restores it.
func (s *Store) SetAppSoftDeleted(name string, at time.Time) error {
	var v int64
	if !at.IsZero() {
		v = at.Unix()
	}
	res, err := s.db.Exec(updateAppSoftDeletedQuery, v, name)
	if err != nil {
		return err
	}
	return checkAffected(res, ErrAppNotFound)
}

// SetAppPrivate records (or clears) an app's private visibility.
// SetAppTabs stores the owner's per-app tab override (CSV of tab keys); empty
// clears the override so each viewer's profile default applies again.
func (s *Store) SetAppTabs(name, tabs string) error {
	res, err := s.db.Exec(updateAppTabsQuery, tabs, name)
	if err != nil {
		return err
	}
	return checkAffected(res, ErrAppNotFound)
}

func (s *Store) SetAppPrivate(name string, private bool) error {
	_, err := s.db.Exec(updateAppPrivateQuery, private, name)
	return err
}

// SetAppPoweredOff records (or clears) an app's deliberate poweroff.
func (s *Store) SetAppPoweredOff(name string, off bool) error {
	_, err := s.db.Exec(updateAppPoweredOffQuery, off, name)
	return err
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

// App returns the app with the given name, or ErrAppNotFound
func (s *Store) App(name string) (*App, error) {
	var app App
	var createdAt, softDeletedAt int64
	err := s.db.QueryRow(selectAppQuery, name).Scan(&app.ID, &app.Name, &app.Port, &app.Host, &app.OwnerID, &app.DiskMB, &createdAt, &app.ImageTag, &app.PoweredOff, &app.UID, &app.Archived, &app.Private, &app.MemoryLimitMB, &app.DiskLimitMB, &app.CPUMilli, &app.Tabs, &softDeletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAppNotFound
	} else if err != nil {
		return nil, err
	}
	app.CreatedAt = time.Unix(createdAt, 0)
	if softDeletedAt != 0 {
		app.SoftDeletedAt = time.Unix(softDeletedAt, 0)
	}
	return &app, nil
}

// AppHost returns the id of the node an app is hosted on, or ErrAppNotFound.
// Cheap single-column read, used to scope node callbacks to their own apps.
func (s *Store) AppHost(name string) (string, error) {
	var host string
	err := s.db.QueryRow(selectAppHostQuery, name).Scan(&host)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrAppNotFound
	}
	return host, err
}

// Apps returns all registered apps, sorted by name
func (s *Store) Apps() ([]*App, error) {
	return s.queryApps(selectAppsQuery)
}

// UpdateAppLimits records the app's admin-set limit overrides; zeros clear
// them (back to owner defaults / uncapped CPU).
func (s *Store) UpdateAppLimits(name string, memoryLimitMB, diskLimitMB, cpuMilli int) error {
	res, err := s.db.Exec(updateAppLimitsQuery, memoryLimitMB, diskLimitMB, cpuMilli, name)
	if err != nil {
		return err
	}
	return checkAffected(res, ErrAppNotFound)
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
	if _, err := s.db.Exec(deleteCollaboratorsByAppQuery, app.ID); err != nil {
		return err
	}
	if _, err := s.db.Exec(deletePendingViewersByAppQuery, app.ID); err != nil {
		return err
	}
	if _, err := s.db.Exec(deleteViewersByAppQuery, app.ID); err != nil {
		return err
	}
	if _, err := s.db.Exec(deleteGrantsByAppIDQuery, app.ID); err != nil {
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
		var createdAt, softDeletedAt int64
		if err := rows.Scan(&app.ID, &app.Name, &app.Port, &app.Host, &app.OwnerID, &app.DiskMB, &createdAt, &app.ImageTag, &app.PoweredOff, &app.UID, &app.Archived, &app.Private, &app.MemoryLimitMB, &app.DiskLimitMB, &app.CPUMilli, &app.Tabs, &softDeletedAt); err != nil {
			return nil, err
		}
		app.CreatedAt = time.Unix(createdAt, 0)
		if softDeletedAt != 0 {
			app.SoftDeletedAt = time.Unix(softDeletedAt, 0)
		}
		apps = append(apps, &app)
	}
	return apps, rows.Err()
}
