package store

import "time"

const (
	// Accumulate a turn's usage onto the app's running totals. INSERT ... SELECT so
	// nothing is recorded for an app that does not exist (no NULL primary key), and
	// the app is addressed by name but keyed on its id, so a rename keeps the totals.
	insertUsageQuery = `
		INSERT INTO app_usage (app_id, input_tokens, output_tokens, cache_write_tokens, cache_read_tokens, updated_at)
		SELECT id, ?, ?, ?, ?, ? FROM app WHERE name = ?
		ON CONFLICT(app_id) DO UPDATE SET
			input_tokens = input_tokens + excluded.input_tokens,
			output_tokens = output_tokens + excluded.output_tokens,
			cache_write_tokens = cache_write_tokens + excluded.cache_write_tokens,
			cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
			updated_at = excluded.updated_at
	`
	// Sum every owner's assistant usage across their apps, for the admin view.
	selectUsageByOwnerQuery = `
		SELECT a.owner_id,
			SUM(u.input_tokens), SUM(u.output_tokens), SUM(u.cache_write_tokens), SUM(u.cache_read_tokens)
		FROM app_usage u JOIN app a ON a.id = u.app_id
		GROUP BY a.owner_id
	`
	deleteAppUsageQuery = `DELETE FROM app_usage WHERE app_id = ?`
)

// AssistantUsage is a running count of built-in-assistant token usage. Cache
// tokens are separate because they are priced differently from fresh input.
type AssistantUsage struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
}

// AddAssistantUsage adds one turn's token usage to an app's running totals. A
// no-op if the app does not exist.
func (s *Store) AddAssistantUsage(appName string, u AssistantUsage) error {
	_, err := s.db.Exec(insertUsageQuery, u.InputTokens, u.OutputTokens, u.CacheWriteTokens, u.CacheReadTokens, time.Now().Unix(), appName)
	return err
}

// UsageByOwner returns each owner's summed assistant usage, keyed by owner id.
func (s *Store) UsageByOwner() (map[string]AssistantUsage, error) {
	rows, err := s.db.Query(selectUsageByOwnerQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byOwner := make(map[string]AssistantUsage)
	for rows.Next() {
		var owner string
		var u AssistantUsage
		if err := rows.Scan(&owner, &u.InputTokens, &u.OutputTokens, &u.CacheWriteTokens, &u.CacheReadTokens); err != nil {
			return nil, err
		}
		byOwner[owner] = u
	}
	return byOwner, rows.Err()
}
