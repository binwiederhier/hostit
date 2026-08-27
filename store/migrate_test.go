package store

import (
	"database/sql"
	"path/filepath"
	"strings"
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
	require.Len(t, migrations, 44)
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

// The Slack rename migration rewrites existing bot connections from the bare
// "slack" id to "slack-bot", and leaves the personal "slack-user" alone.
func TestSlackRenameMigration(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "hostit.db")
	db, err := sql.Open("sqlite", file)
	require.NoError(t, err)
	// Seed the database at the version just before the slack-bot rename, wherever
	// it sits, so later appended migrations do not shift this test.
	slackBotIdx := 0
	for i, m := range migrations {
		if strings.Contains(m, "SET provider = 'slack-bot'") {
			slackBotIdx = i
			break
		}
	}
	for i := 0; i < slackBotIdx; i++ {
		_, err := db.Exec(migrations[i])
		require.NoError(t, err, "migration %d", i+1)
	}
	_, err = db.Exec(`INSERT INTO connection (id, user_id, slug, provider, kind, secret, created_at) VALUES
		('c1','u1','team','slack','oauth','x',0),
		('c2','u1','me','slack-user','oauth','y',0)`)
	require.NoError(t, err)
	_, err = db.Exec(createSchemaVersionTableQuery)
	require.NoError(t, err)
	_, err = db.Exec(insertSchemaVersionQuery, slackBotIdx) // recorded just before the rename
	require.NoError(t, err)
	require.NoError(t, db.Close())

	s, err := NewStore(file) // opening applies the final migration
	require.NoError(t, err)
	defer s.Close()

	var bot, personal string
	require.NoError(t, s.db.QueryRow(`SELECT provider FROM connection WHERE id='c1'`).Scan(&bot))
	require.NoError(t, s.db.QueryRow(`SELECT provider FROM connection WHERE id='c2'`).Scan(&personal))
	assert.Equal(t, "slack-bot", bot, "the bot connection is rewritten to the new id")
	assert.Equal(t, "slack-user", personal, "the personal connection is left alone")
}

// migrationIndex finds a migration by a substring of its SQL. last picks the
// final match (e.g. the heal re-add) rather than the first. Content-based so
// appended migrations never shift what these upgrade tests target.
func migrationIndex(t *testing.T, substr string, last bool) int {
	t.Helper()
	idx := -1
	for i, m := range migrations {
		if strings.Contains(m, substr) {
			idx = i
			if !last {
				break
			}
		}
	}
	require.GreaterOrEqual(t, idx, 0, "no migration contains %q", substr)
	return idx
}

// A database that has run every migration EXCEPT the last (status) must get the
// status column when opened. Guards the append-only rule: the status migration
// was once inserted mid-slice, so a version-N DB skipped it (no such column).
func TestStatusColumnAddedOnUpgrade(t *testing.T) {
	t.Parallel()
	// Locate the status add by CONTENT, not position: later migrations are
	// appended after it, so "the last migration" is no longer the status one.
	statusIdx := migrationIndex(t, "ADD COLUMN status", false)
	file := filepath.Join(t.TempDir(), "hostit.db")
	db, err := sql.Open("sqlite", file)
	require.NoError(t, err)
	for i := 0; i < statusIdx; i++ {
		_, err := db.Exec(migrations[i])
		require.NoError(t, err, "migration %d", i+1)
	}
	_, err = db.Exec(createSchemaVersionTableQuery)
	require.NoError(t, err)
	_, err = db.Exec(insertSchemaVersionQuery, statusIdx) // everything before the status add is applied
	require.NoError(t, err)
	require.NoError(t, db.Close())

	s, err := NewStore(file) // opening must run the status migration and everything after it
	require.NoError(t, err)
	defer s.Close()
	_, err = s.db.Exec(`INSERT INTO connection (id, user_id, slug, provider, kind, secret, created_at) VALUES ('c1','u1','s','p','oauth','x',0)`)
	require.NoError(t, err, "the status column must exist")
	var status string
	require.NoError(t, s.db.QueryRow(`SELECT status FROM connection WHERE id='c1'`).Scan(&status))
	assert.Equal(t, "ok", status, "status defaults to ok")
}

// The heal case: a database whose connection table is MISSING status yet whose
// recorded version is already past the status migration (what the mis-ordered
// migration left behind). Opening must re-add the column via the heal migration.
func TestHealReaddsSkippedStatusColumn(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "hostit.db")
	db, err := sql.Open("sqlite", file)
	require.NoError(t, err)
	// Run through the slack-bot rename but NOT the status add: the connection
	// table exists without a status column.
	slackBotIdx := migrationIndex(t, "SET provider = 'slack-bot'", false)
	healIdx := migrationIndex(t, "ADD COLUMN status", true) // the heal is the LAST status add
	for i := 0; i <= slackBotIdx; i++ {
		_, err := db.Exec(migrations[i])
		require.NoError(t, err, "migration %d", i+1)
	}
	// Claim a version that already ran the status add (the mis-order left this):
	// only the heal, and anything appended after it, remains to run.
	_, err = db.Exec(createSchemaVersionTableQuery)
	require.NoError(t, err)
	_, err = db.Exec(insertSchemaVersionQuery, healIdx)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	s, err := NewStore(file) // the heal migration must add the missing column
	require.NoError(t, err)
	defer s.Close()
	_, err = s.db.Exec(`INSERT INTO connection (id, user_id, slug, provider, kind, secret, created_at) VALUES ('c1','u1','s','p','oauth','x',0)`)
	require.NoError(t, err, "status column must exist after the heal")
	var status string
	require.NoError(t, s.db.QueryRow(`SELECT status FROM connection WHERE id='c1'`).Scan(&status))
	assert.Equal(t, "ok", status)
}
