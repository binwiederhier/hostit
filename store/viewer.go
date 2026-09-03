package store

import "strings"

// Viewers are the weaker of the two per-app grants. A COLLABORATOR can deploy,
// edit files, use the terminal and SSH in; a VIEWER can only open the app's
// URL. The split exists so that sharing a private dashboard with somebody does
// not also hand them the keys to the app that serves it.
//
// The two are separate tables rather than a role column, so neither query has
// to filter and neither grant can be mistaken for the other. Holding a
// collaborator grant implies viewing (control's mayViewApp checks both), so
// nobody needs to be added twice.

const (
	insertViewerQuery = `INSERT OR IGNORE INTO app_viewer (app_id, user_id, created_at) VALUES (?, ?, strftime('%s', 'now'))`
	deleteViewerQuery = `DELETE FROM app_viewer WHERE app_id = ? AND user_id = ?`
	selectViewerQuery = `SELECT COUNT(*) FROM app_viewer WHERE app_id = ? AND user_id = ?`
	// The list joins the user table: callers show emails, never raw ids.
	selectViewersQuery = `
		SELECT u.id, u.email, u.name, u.role, u.status, u.app_limit, u.memory_mb, u.disk_mb, u.memory_pool_mb, u.disk_pool_mb, u.created_at, u.tech_level, u.assistant_prompt, u.default_tabs, u.onboarded
		FROM app_viewer v JOIN user u ON u.id = v.user_id
		WHERE v.app_id = ? ORDER BY u.email
	`
	// One row per app that has any viewers, so a list of apps costs one query
	// rather than one per app.
	selectViewerCountsQuery = `SELECT app_id, COUNT(*) FROM app_viewer GROUP BY app_id`
	// Suspended accounts are filtered out here rather than by the caller: these
	// feed the set the proxy enforces on, and a suspended user must not be in it.
	selectAllViewersQuery = `
		SELECT v.app_id, v.user_id FROM app_viewer v JOIN user u ON u.id = v.user_id WHERE u.status = ?
	`
	selectAllCollaboratorsQuery = `
		SELECT c.app_id, c.user_id FROM app_collaborator c JOIN user u ON u.id = c.user_id WHERE u.status = ?
	`
	selectActiveAdminsQuery = `SELECT id FROM user WHERE role = ? AND status = ?`
	// The owners whose accounts are active. An owner is not a row in either
	// grant table, so they are collected separately -- but by the same rule: a
	// suspended owner may not open their own app either.
	selectActiveOwnersQuery = `
		SELECT a.id, a.owner_id FROM app a JOIN user u ON u.id = a.owner_id WHERE u.status = ?
	`
	deleteViewersByAppQuery = `DELETE FROM app_viewer WHERE app_id = ?`
	// Pending viewer invites, keyed by email until the person has an account.
	insertPendingViewerQuery       = `INSERT OR IGNORE INTO pending_viewer (app_id, email, created_at) VALUES (?, ?, strftime('%s','now'))`
	deletePendingViewerQuery       = `DELETE FROM pending_viewer WHERE app_id = ? AND email = ?`
	selectPendingViewersQuery      = `SELECT email FROM pending_viewer WHERE app_id = ? ORDER BY email`
	deletePendingViewersByAppQuery = `DELETE FROM pending_viewer WHERE app_id = ?`
	selectPendingViewerAppsQuery   = `SELECT app_id FROM pending_viewer WHERE email = ?`
	deleteViewersByUserQuery       = `DELETE FROM app_viewer WHERE user_id = ?`
	// Distinct emails of everyone the owner has ever granted view access to, on
	// any of their apps, so a new app can offer them as picks rather than making
	// the owner retype an address they have used before.
	selectViewerEmailsForOwnerQuery = `
		SELECT DISTINCT u.email FROM app_viewer v
		JOIN user u ON u.id = v.user_id
		JOIN app a ON a.id = v.app_id
		WHERE a.owner_id = ? ORDER BY u.email
	`
	// The apps a person can OPEN as a viewer -- the "shared with you" list for a
	// viewer-only account. Mirrors selectAppsByCollaboratorQuery.
	selectAppsByViewerQuery = `
		SELECT a.id, a.name, a.port, a.host, a.owner_id, a.disk_mb, a.created_at, a.image_tag, a.powered_off, a.uid, a.archived, a.private, a.memory_limit_mb, a.disk_limit_mb, a.cpu_milli, a.tabs, a.soft_deleted_at
		FROM app_viewer v JOIN app a ON a.id = v.app_id
		WHERE v.user_id = ? ORDER BY a.name
	`
)

// AppsByViewer returns the apps a user holds a view grant on, so a viewer-only
// account can be shown the apps shared with them.
func (s *Store) AppsByViewer(userID string) ([]*App, error) {
	return s.queryApps(selectAppsByViewerQuery, userID)
}

// AddAppViewer lets a user open the app's URL; granting twice is a no-op (the
// grant already holds), never an error.
func (s *Store) AddAppViewer(appID, userID string) error {
	_, err := s.db.Exec(insertViewerQuery, appID, userID)
	return err
}

// RemoveAppViewer revokes a user's view grant on an app.
func (s *Store) RemoveAppViewer(appID, userID string) error {
	_, err := s.db.Exec(deleteViewerQuery, appID, userID)
	return err
}

// AddPendingViewer records a view invite for an email that has no account yet;
// it becomes a real grant when they first sign in (ResolvePendingViewers).
func (s *Store) AddPendingViewer(appID, email string) error {
	_, err := s.db.Exec(insertPendingViewerQuery, appID, strings.ToLower(strings.TrimSpace(email)))
	return err
}

// RemovePendingViewer withdraws a pending invite.
func (s *Store) RemovePendingViewer(appID, email string) error {
	_, err := s.db.Exec(deletePendingViewerQuery, appID, strings.ToLower(strings.TrimSpace(email)))
	return err
}

// PendingViewers lists the emails invited to view an app who have not signed in
// yet, sorted.
func (s *Store) PendingViewers(appID string) ([]string, error) {
	rows, err := s.db.Query(selectPendingViewersQuery, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, err
		}
		out = append(out, email)
	}
	return out, rows.Err()
}

// ResolvePendingViewers turns every pending invite for this email into a real
// view grant for the user and clears the invites. Called when a person signs in,
// so an owner can invite someone before they have an account. Returns how many
// apps were resolved.
func (s *Store) ResolvePendingViewers(email, userID string) (int, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	rows, err := s.db.Query(selectPendingViewerAppsQuery, email)
	if err != nil {
		return 0, err
	}
	var appIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		appIDs = append(appIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, appID := range appIDs {
		if _, err := s.db.Exec(insertViewerQuery, appID, userID); err != nil {
			return 0, err
		}
		if _, err := s.db.Exec(deletePendingViewerQuery, appID, email); err != nil {
			return 0, err
		}
	}
	return len(appIDs), nil
}

// ViewerEmailsForOwner lists the distinct emails the owner has granted view
// access to across their apps, sorted, for the new-app dialog to offer as picks.
func (s *Store) ViewerEmailsForOwner(ownerID string) ([]string, error) {
	rows, err := s.db.Query(selectViewerEmailsForOwnerQuery, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	emails := make([]string, 0)
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, err
		}
		emails = append(emails, email)
	}
	return emails, rows.Err()
}

// IsAppViewer reports whether the user holds a view grant on the app. It does
// NOT consider collaborators; the caller decides that a collaborator may also
// view, so that the two grants stay independently readable.
func (s *Store) IsAppViewer(appID, userID string) bool {
	var count int
	if err := s.db.QueryRow(selectViewerQuery, appID, userID).Scan(&count); err != nil {
		return false
	}
	return count > 0
}

// AppViewers returns the users holding a view grant on the app, by email.
func (s *Store) AppViewers(appID string) ([]*User, error) {
	rows, err := s.db.Query(selectViewersQuery, appID)
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

// ViewerCounts returns how many viewers each app has, keyed by app id. Apps
// with none are absent from the map.
func (s *Store) ViewerCounts() (map[string]int, error) {
	rows, err := s.db.Query(selectViewerCountsQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var appID string
		var count int
		if err := rows.Scan(&appID, &count); err != nil {
			return nil, err
		}
		counts[appID] = count
	}
	return counts, rows.Err()
}

// AccessSets returns, per app id, the ACTIVE users who may open that app: its
// owner, its collaborators and its viewers together, since all three include
// looking at it. Admins are not in here -- they are global rather than per-app.
// Suspended accounts are excluded throughout, the owner's included.
//
// One query per grant table for the whole registry, not one per app: this
// feeds the routing table, which is re-derived every half second.
func (s *Store) AccessSets() (map[string][]string, error) {
	sets := make(map[string][]string)
	for _, query := range []string{selectAllCollaboratorsQuery, selectAllViewersQuery, selectActiveOwnersQuery} {
		rows, err := s.db.Query(query, StatusActive)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var appID, userID string
			if err := rows.Scan(&appID, &userID); err != nil {
				rows.Close()
				return nil, err
			}
			sets[appID] = append(sets[appID], userID)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return sets, nil
}

// ActiveAdmins returns the ids of admins, who may open any app. Global rather
// than copied into every app's set, so one admin does not appear once per app
// in the table the proxy holds.
func (s *Store) ActiveAdmins() ([]string, error) {
	rows, err := s.db.Query(selectActiveAdminsQuery, RoleAdmin, StatusActive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
