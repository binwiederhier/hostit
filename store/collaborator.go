package store

const (
	insertCollaboratorQuery = `INSERT OR IGNORE INTO app_collaborator (app_id, user_id, created_at) VALUES (?, ?, strftime('%s', 'now'))`
	deleteCollaboratorQuery = `DELETE FROM app_collaborator WHERE app_id = ? AND user_id = ?`
	selectCollaboratorQuery = `SELECT COUNT(*) FROM app_collaborator WHERE app_id = ? AND user_id = ?`
	// The list joins the user table: callers show emails, never raw ids.
	selectCollaboratorsQuery = `
		SELECT u.id, u.email, u.name, u.role, u.status, u.app_limit, u.memory_mb, u.disk_mb, u.created_at
		FROM app_collaborator c JOIN user u ON u.id = c.user_id
		WHERE c.app_id = ? ORDER BY u.email
	`
	selectAppsByCollaboratorQuery = `
		SELECT a.id, a.name, a.port, a.host, a.owner_id, a.disk_mb, a.created_at, a.image_tag, a.powered_off
		FROM app_collaborator c JOIN app a ON a.id = c.app_id
		WHERE c.user_id = ? ORDER BY a.name
	`
	deleteCollaboratorsByAppQuery  = `DELETE FROM app_collaborator WHERE app_id = ?`
	deleteCollaboratorsByUserQuery = `DELETE FROM app_collaborator WHERE user_id = ?`
)

// AddAppCollaborator grants a user access to an app; granting twice is a no-op
// (the grant already holds), never an error.
func (s *Store) AddAppCollaborator(appID, userID string) error {
	_, err := s.db.Exec(insertCollaboratorQuery, appID, userID)
	return err
}

// RemoveAppCollaborator revokes a user's access to an app.
func (s *Store) RemoveAppCollaborator(appID, userID string) error {
	_, err := s.db.Exec(deleteCollaboratorQuery, appID, userID)
	return err
}

// IsAppCollaborator reports whether the user holds a grant on the app.
func (s *Store) IsAppCollaborator(appID, userID string) bool {
	var count int
	if err := s.db.QueryRow(selectCollaboratorQuery, appID, userID).Scan(&count); err != nil {
		return false
	}
	return count > 0
}

// AppCollaborators returns the users holding a grant on the app, by email.
func (s *Store) AppCollaborators(appID string) ([]*User, error) {
	rows, err := s.db.Query(selectCollaboratorsQuery, appID)
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

// AppsByCollaborator returns the apps a user holds a grant on (their
// collaborated apps, as opposed to AppsByOwner), sorted by name.
func (s *Store) AppsByCollaborator(userID string) ([]*App, error) {
	return s.queryApps(selectAppsByCollaboratorQuery, userID)
}
