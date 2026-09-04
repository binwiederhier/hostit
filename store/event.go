package store

import "time"

const (
	// eventName resolves the app's current name from its id, so the activity log
	// follows a rename; events are keyed on app_id.
	eventName            = `COALESCE((SELECT name FROM app a WHERE a.id = app_event.app_id), app_event.app_name)`
	insertEventQuery     = `INSERT INTO app_event (app_name, app_id, created_at, actor, level, action, detail) VALUES (?, COALESCE((SELECT id FROM app WHERE name = ? AND id != ''), ''), ?, ?, ?, ?, ?)`
	selectEventsQuery    = `SELECT id, ` + eventName + `, created_at, actor, level, action, detail FROM app_event WHERE app_id = (SELECT id FROM app WHERE name = ? AND id != '') ORDER BY created_at DESC, id DESC LIMIT ?`
	deleteAppEventsQuery = `DELETE FROM app_event WHERE app_id = ? OR (app_id = '' AND app_name = ?)`
	// Keep an app's activity log bounded: drop everything but the newest rows.
	trimAppEventsQuery = `DELETE FROM app_event WHERE app_id = (SELECT id FROM app WHERE name = ? AND id != '') AND id NOT IN (SELECT id FROM app_event WHERE app_id = (SELECT id FROM app WHERE name = ? AND id != '') ORDER BY id DESC LIMIT ?)`
)

// maxAppEvents bounds how many activity-log rows an app keeps.
const (
	maxAppEvents = 500
)

// Event is one entry in an app's activity log (the Logs tab).
type Event struct {
	ID        int64
	AppName   string
	CreatedAt time.Time
	Actor     string // email that did it; empty for the system/admin token
	Level     string // "info" | "error"
	Action    string
	Detail    string
}

// AddEvent appends one activity-log entry, then trims the app's log to the newest
// maxAppEvents so it cannot grow without bound.
func (s *Store) AddEvent(e *Event) error {
	if _, err := s.db.Exec(insertEventQuery, e.AppName, e.AppName, e.CreatedAt.Unix(), e.Actor, e.Level, e.Action, e.Detail); err != nil {
		return err
	}
	_, err := s.db.Exec(trimAppEventsQuery, e.AppName, e.AppName, maxAppEvents)
	return err
}

// AppEvents returns an app's most recent activity-log entries, newest first.
func (s *Store) AppEvents(appName string, limit int) ([]*Event, error) {
	rows, err := s.db.Query(selectEventsQuery, appName, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []*Event
	for rows.Next() {
		var e Event
		var created int64
		if err := rows.Scan(&e.ID, &e.AppName, &created, &e.Actor, &e.Level, &e.Action, &e.Detail); err != nil {
			return nil, err
		}
		e.CreatedAt = time.Unix(created, 0)
		events = append(events, &e)
	}
	return events, rows.Err()
}
