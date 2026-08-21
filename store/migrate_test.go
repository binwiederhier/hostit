package store

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrationFromV1Schema(t *testing.T) {
	t.Parallel()
	filename := filepath.Join(t.TempDir(), "hostit.db")
	// Simulate a v0.1/v0.2 database: app table only, no schema_version
	old, err := newRawDB(filename)
	require.NoError(t, err)
	_, err = old.Exec(`CREATE TABLE app (name TEXT PRIMARY KEY, port INTEGER NOT NULL UNIQUE, host TEXT NOT NULL, created_at INTEGER NOT NULL)`)
	require.NoError(t, err)
	_, err = old.Exec(`INSERT INTO app (name, port, host, created_at) VALUES ('hello', 10000, 'local', 1700000000)`)
	require.NoError(t, err)
	require.NoError(t, old.Close())
	// Opening it with the current code must migrate in place, keeping the app
	s, err := NewStore(filename)
	require.NoError(t, err)
	defer s.Close()
	app, err := s.App("hello")
	require.NoError(t, err)
	assert.Equal(t, 10000, app.Port)
	assert.Empty(t, app.OwnerID)
	require.NoError(t, s.AddUser(&User{Email: "a@b.c", Role: RoleUser, Status: StatusActive}))
}

func TestMigrationIsIdempotent(t *testing.T) {
	t.Parallel()
	// A pre-versioning database must migrate exactly once: reopening it may not
	// re-run migrations (which would fail with "table already exists")
	filename := filepath.Join(t.TempDir(), "hostit.db")
	old, err := newRawDB(filename)
	require.NoError(t, err)
	_, err = old.Exec(`CREATE TABLE app (name TEXT PRIMARY KEY, port INTEGER NOT NULL UNIQUE, host TEXT NOT NULL, created_at INTEGER NOT NULL)`)
	require.NoError(t, err)
	require.NoError(t, old.Close())
	for i := 0; i < 3; i++ {
		s, err := NewStore(filename)
		require.NoError(t, err, "open %d must succeed", i+1)
		require.NoError(t, s.Close())
	}
	// Same for a database created from scratch
	fresh := filepath.Join(t.TempDir(), "fresh.db")
	for i := 0; i < 3; i++ {
		s, err := NewStore(fresh)
		require.NoError(t, err, "fresh open %d must succeed", i+1)
		require.NoError(t, s.Close())
	}
}

func TestMigrationRecordsVersion(t *testing.T) {
	t.Parallel()
	filename := filepath.Join(t.TempDir(), "hostit.db")
	s, err := NewStore(filename)
	require.NoError(t, err)
	defer s.Close()
	var version int
	require.NoError(t, s.db.QueryRow(selectSchemaVersionQuery).Scan(&version))
	assert.Equal(t, len(migrations), version)
	// Exactly one row, always
	var count int
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM schema_version`).Scan(&count))
	assert.Equal(t, 1, count)
}

// The migration list's tail is pinned: slot 23 (index 22) is BURNED -- the
// abandoned connections PoC ran on stage with its own entry there, so those
// databases already record version 23. A no-op in that slot keeps every
// history aligned; the limits columns must sit at index 23, where both a
// PoC-touched database (at 23) and a clean one (at 22) still reach them.
func TestBurnedSlotKeepsHistoriesAligned(t *testing.T) {
	t.Parallel()
	require.Len(t, migrations, 24)
	assert.Contains(t, migrations[22], "SELECT 1", "index 22 is the burned no-op slot")
	assert.Contains(t, migrations[23], "memory_limit_mb", "the limits columns follow the burned slot")
}
