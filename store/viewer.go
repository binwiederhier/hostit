package store

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
		SELECT u.id, u.email, u.name, u.role, u.status, u.app_limit, u.memory_mb, u.disk_mb, u.memory_pool_mb, u.disk_pool_mb, u.created_at
		FROM app_viewer v JOIN user u ON u.id = v.user_id
		WHERE v.app_id = ? ORDER BY u.email
	`
	// One row per app that has any viewers, so a list of apps costs one query
	// rather than one per app.
	selectViewerCountsQuery  = `SELECT app_id, COUNT(*) FROM app_viewer GROUP BY app_id`
	deleteViewersByAppQuery  = `DELETE FROM app_viewer WHERE app_id = ?`
	deleteViewersByUserQuery = `DELETE FROM app_viewer WHERE user_id = ?`
)

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
