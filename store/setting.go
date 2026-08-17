package store

const (
	// SettingRoutesSeq is the routing table's version, persisted because the
	// proxy compares it across control restarts (see control/proxies.go).
	SettingRoutesSeq = "routes_seq"
	// SettingNextPort is where port allocation resumes, persisted so a restart
	// does not send it back to the bottom of the range and reuse the most
	// recently freed ports first (see Manager.allocatePort).
	SettingNextPort = "next_port"

	upsertSettingQuery  = `INSERT INTO setting (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`
	selectSettingsQuery = `SELECT key, value FROM setting`
)

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
