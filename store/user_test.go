package store

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddAndGetUser(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	u := &User{Email: "phil@example.com", Name: "Phil", Role: RoleAdmin, Status: StatusActive}
	require.NoError(t, s.AddUser(u))
	assert.NotEmpty(t, u.ID)
	assert.False(t, u.CreatedAt.IsZero())
	got, err := s.UserByEmail("phil@example.com")
	require.NoError(t, err)
	assert.Equal(t, u.ID, got.ID)
	assert.Equal(t, RoleAdmin, got.Role)
	assert.Equal(t, StatusActive, got.Status)
	got, err = s.User(u.ID)
	require.NoError(t, err)
	assert.Equal(t, "Phil", got.Name)
}

func TestUserNotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	_, err := s.UserByEmail("nobody@example.com")
	require.ErrorIs(t, err, ErrUserNotFound)
	_, err = s.User("u_nope")
	require.ErrorIs(t, err, ErrUserNotFound)
}

func TestAddUserDuplicateEmail(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddUser(&User{Email: "a@b.c", Role: RoleUser, Status: StatusPending}))
	require.Error(t, s.AddUser(&User{Email: "a@b.c", Role: RoleUser, Status: StatusPending}))
}

func TestUsersSortedByEmail(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddUser(&User{Email: "z@b.c", Role: RoleUser, Status: StatusPending}))
	require.NoError(t, s.AddUser(&User{Email: "a@b.c", Role: RoleUser, Status: StatusActive}))
	users, err := s.Users()
	require.NoError(t, err)
	require.Len(t, users, 2)
	assert.Equal(t, "a@b.c", users[0].Email)
}

func TestUpdateUser(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	u := &User{Email: "a@b.c", Role: RoleUser, Status: StatusPending}
	require.NoError(t, s.AddUser(u))
	u.Role = RoleAdmin
	u.Status = StatusActive
	u.AppLimit = intPtr(9)
	u.MemoryMB = intPtr(512)
	u.DiskMB = nil
	require.NoError(t, s.UpdateUser(u))
	got, err := s.User(u.ID)
	require.NoError(t, err)
	assert.Equal(t, RoleAdmin, got.Role)
	assert.Equal(t, StatusActive, got.Status)
	require.NotNil(t, got.AppLimit)
	assert.Equal(t, 9, *got.AppLimit)
	require.NotNil(t, got.MemoryMB)
	assert.Equal(t, 512, *got.MemoryMB)
	assert.Nil(t, got.DiskMB)
}

func TestRemoveUser(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	u := &User{Email: "a@b.c", Role: RoleUser, Status: StatusActive}
	require.NoError(t, s.AddUser(u))
	require.NoError(t, s.RemoveUser(u.ID))
	_, err := s.User(u.ID)
	require.ErrorIs(t, err, ErrUserNotFound)
	require.ErrorIs(t, s.RemoveUser(u.ID), ErrUserNotFound)
}

func TestAppOwnership(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	u := &User{Email: "a@b.c", Role: RoleUser, Status: StatusActive}
	require.NoError(t, s.AddUser(u))
	require.NoError(t, s.AddApp(&App{Name: "blog", Port: 10000, Host: HostLocal, OwnerID: u.ID}))
	require.NoError(t, s.AddApp(&App{Name: "orphan", Port: 10001, Host: HostLocal}))
	app, err := s.App("blog")
	require.NoError(t, err)
	assert.Equal(t, u.ID, app.OwnerID)
	own, err := s.AppsByOwner(u.ID)
	require.NoError(t, err)
	require.Len(t, own, 1)
	assert.Equal(t, "blog", own[0].Name)
	count, err := s.AppCountByOwner(u.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	// Unowned apps come back with an empty owner
	orphan, err := s.App("orphan")
	require.NoError(t, err)
	assert.Empty(t, orphan.OwnerID)
}

func TestAppDiskUsage(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{Name: "blog", Port: 10000, Host: HostLocal}))
	require.NoError(t, s.UpdateAppUsage("blog", 42, true))
	app, err := s.App("blog")
	require.NoError(t, err)
	assert.Equal(t, 42, app.DiskMB)
	assert.True(t, app.OverQuota)
}

func TestTokens(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	u := &User{Email: "a@b.c", Role: RoleUser, Status: StatusActive}
	require.NoError(t, s.AddUser(u))
	tk := &Token{UserID: u.ID, Hash: "hash1", Prefix: "hostit_ab", Label: "claude"}
	require.NoError(t, s.AddToken(tk))
	assert.NotEmpty(t, tk.ID)
	got, err := s.TokenByHash("hash1")
	require.NoError(t, err)
	assert.Equal(t, u.ID, got.UserID)
	assert.Equal(t, "claude", got.Label)
	_, err = s.TokenByHash("nope")
	require.ErrorIs(t, err, ErrTokenNotFound)
	tokens, err := s.TokensByUser(u.ID)
	require.NoError(t, err)
	require.Len(t, tokens, 1)
	require.NoError(t, s.TouchToken(tk.ID))
	got, err = s.TokenByHash("hash1")
	require.NoError(t, err)
	assert.False(t, got.LastUsed.IsZero())
	require.NoError(t, s.RemoveToken(u.ID, tk.ID))
	_, err = s.TokenByHash("hash1")
	require.ErrorIs(t, err, ErrTokenNotFound)
	// Removing another user's token must not work
	require.ErrorIs(t, s.RemoveToken("u_other", tk.ID), ErrTokenNotFound)
}

func TestUserKeys(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	u := &User{Email: "a@b.c", Role: RoleUser, Status: StatusActive}
	require.NoError(t, s.AddUser(u))
	key := &UserKey{UserID: u.ID, Label: "laptop", Key: "ssh-ed25519 AAAA laptop"}
	require.NoError(t, s.AddUserKey(key))
	keys, err := s.UserKeys(u.ID)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, "laptop", keys[0].Label)
	require.NoError(t, s.RemoveUserKey(u.ID, key.ID))
	keys, err = s.UserKeys(u.ID)
	require.NoError(t, err)
	assert.Empty(t, keys)
	require.ErrorIs(t, s.RemoveUserKey(u.ID, key.ID), ErrKeyNotFound)
}

func TestAppKeys(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{Name: "blog", Port: 10000, Host: HostLocal}))
	require.NoError(t, s.SetAppKeys("blog", []string{"ssh-ed25519 AAAA one", "ssh-ed25519 AAAA two"}))
	keys, err := s.AppKeys("blog")
	require.NoError(t, err)
	require.Len(t, keys, 2)
	require.NoError(t, s.SetAppKeys("blog", []string{"ssh-ed25519 AAAA three"}))
	keys, err = s.AppKeys("blog")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, "ssh-ed25519 AAAA three", keys[0])
}

func TestSettings(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	settings, err := s.Settings()
	require.NoError(t, err)
	assert.Empty(t, settings)
	require.NoError(t, s.SetSetting("default_app_limit", "5"))
	require.NoError(t, s.SetSetting("default_app_limit", "7")) // Upsert
	require.NoError(t, s.SetSetting("default_memory_mb", "256"))
	settings, err = s.Settings()
	require.NoError(t, err)
	assert.Equal(t, "7", settings["default_app_limit"])
	assert.Equal(t, "256", settings["default_memory_mb"])
}

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

func intPtr(i int) *int {
	return &i
}
