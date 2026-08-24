package store

import (
	"database/sql"
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
	require.Len(t, migrations, 33)
	assert.Contains(t, migrations[22], "SELECT 1", "index 22 is the burned no-op slot")
	assert.Contains(t, migrations[23], "memory_limit_mb", "the limits columns follow the burned slot")
	assert.Contains(t, migrations[24], "memory_pool_mb", "then the per-user pools")
	assert.Contains(t, migrations[25], "512", "then the old-default pinning for pre-existing apps")
	assert.Contains(t, migrations[26], "stats", "then the member machine-stats blob")
	assert.Contains(t, migrations[27], "private", "then per-app visibility")
	assert.Contains(t, migrations[28], "app_viewer", "then the view-only grant")
	// 30 and 31 are burned the same way 23 is: the reverted redirect work ran on
	// stage, so a stage database already counts them. Real work must sit after.
	assert.Contains(t, migrations[29], "SELECT 1", "slot 30 is burned (redirect_to)")
	assert.Contains(t, migrations[30], "SELECT 1", "slot 31 is burned (app_redirect)")
	assert.Contains(t, migrations[31], "CREATE TABLE connection", "connections lands after the burned slots")
	assert.Contains(t, migrations[32], "CREATE TABLE provider", "then provider definitions")
}

// A database that ran the abandoned PoC still holds its differently-shaped
// connection tables. The new migration has to reclaim those names rather than
// trip over them, or every such instance fails to start.
func TestConnectionsMigrationReclaimsThePoCTables(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "hostit.db")
	db, err := sql.Open("sqlite", file)
	require.NoError(t, err)
	for i := 0; i < 29; i++ {
		_, err := db.Exec(migrations[i])
		require.NoError(t, err, "migration %d", i+1)
	}
	// The PoC's shape, as a stage database would still have it
	_, err = db.Exec(`CREATE TABLE connection (user_id TEXT NOT NULL, provider TEXT NOT NULL, PRIMARY KEY (user_id, provider));
	                  CREATE TABLE app_connection (app_id TEXT NOT NULL, provider TEXT NOT NULL, PRIMARY KEY (app_id, provider));
	                  INSERT INTO connection (user_id, provider) VALUES ('u1', 'google')`)
	require.NoError(t, err)
	_, err = db.Exec(createSchemaVersionTableQuery)
	require.NoError(t, err)
	_, err = db.Exec(insertSchemaVersionQuery, 31) // as stage records it
	require.NoError(t, err)
	require.NoError(t, db.Close())

	s, err := NewStore(file)
	require.NoError(t, err, "a stage-shaped database must still open")
	defer s.Close()

	// The new shape is in place, and the PoC's row is gone with its old table
	var n int
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM connection`).Scan(&n))
	assert.Equal(t, 0, n)
	_, err = s.db.Exec(`INSERT INTO connection (id, user_id, slug, provider, kind, secret, created_at) VALUES ('c1','u1','work-cal','google-calendar','oauth','x',0)`)
	assert.NoError(t, err, "the new columns exist")
}

// Migration 28 defaults every existing app to public, and 29 adds the viewer
// table empty. Both matter more than they look: an app that predates the column
// was reachable by URL, and a migration that changed what any of them mean
// would take apps offline for their own owners.
func TestVisibilityMigrationsLeaveExistingAppsAlone(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "hostit.db")

	// A database at the version just before per-app visibility, with apps in it.
	db, err := sql.Open("sqlite", file)
	require.NoError(t, err)
	for i := 0; i < 27; i++ {
		_, err := db.Exec(migrations[i])
		require.NoError(t, err, "migration %d", i+1)
	}
	_, err = db.Exec(createSchemaVersionTableQuery)
	require.NoError(t, err)
	_, err = db.Exec(insertSchemaVersionQuery, 27)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO app (id, name, port, host, owner_id, created_at, image_tag, uid) VALUES ('a1', 'blog', 10000, '', 'u1', 0, '', 0)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	s, err := NewStore(file)
	require.NoError(t, err)
	defer s.Close()

	a, err := s.App("blog")
	require.NoError(t, err)
	assert.False(t, a.Private, "an app that predates the column stays public")

	viewers, err := s.AppViewers("a1")
	require.NoError(t, err)
	assert.Empty(t, viewers, "and starts with nobody granted access")
}
