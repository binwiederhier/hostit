package store

import (
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
	require.NoError(t, s.UpdateAppUsage("blog", 42))
	app, err := s.App("blog")
	require.NoError(t, err)
	assert.Equal(t, 42, app.DiskMB)
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
	assert.Nil(t, got.LastUsed, "an unused token has no last-used time")
	_, err = s.TokenByHash("nope")
	require.ErrorIs(t, err, ErrTokenNotFound)
	tokens, err := s.TokensByUser(u.ID)
	require.NoError(t, err)
	require.Len(t, tokens, 1)
	require.NoError(t, s.TouchToken(tk.ID))
	got, err = s.TokenByHash("hash1")
	require.NoError(t, err)
	require.NotNil(t, got.LastUsed, "a used token records when")
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

func TestAllowedDomains(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	domains, err := s.AllowedDomains()
	require.NoError(t, err)
	assert.Empty(t, domains)
	first := &AllowedDomain{Domain: "example.org"}
	require.NoError(t, s.AddAllowedDomain(first))
	assert.False(t, first.CreatedAt.IsZero(), "the caller gets the stored time back")
	require.NoError(t, s.AddAllowedDomain(&AllowedDomain{Domain: "example.com"}))
	domains, err = s.AllowedDomains()
	require.NoError(t, err)
	require.Len(t, domains, 2)
	assert.Equal(t, "example.com", domains[0].Domain) // Sorted
	assert.False(t, domains[0].CreatedAt.IsZero())
	allowed, err := s.DomainAllowed("example.org")
	require.NoError(t, err)
	assert.True(t, allowed)
	allowed, err = s.DomainAllowed("nope.com")
	require.NoError(t, err)
	assert.False(t, allowed)
	// Adding the same domain twice is not an error; the admin just repeated it
	require.NoError(t, s.AddAllowedDomain(&AllowedDomain{Domain: "example.org"}))
	domains, err = s.AllowedDomains()
	require.NoError(t, err)
	assert.Len(t, domains, 2)
	require.NoError(t, s.RemoveAllowedDomain("example.org"))
	allowed, err = s.DomainAllowed("example.org")
	require.NoError(t, err)
	assert.False(t, allowed)
	require.ErrorIs(t, s.RemoveAllowedDomain("example.org"), ErrDomainNotFound)
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

func intPtr(i int) *int {
	return &i
}

func TestTransferApps(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	from := &User{Email: "leaving@b.c", Role: RoleUser, Status: StatusActive}
	to := &User{Email: "staying@b.c", Role: RoleUser, Status: StatusActive}
	require.NoError(t, s.AddUser(from))
	require.NoError(t, s.AddUser(to))
	require.NoError(t, s.AddApp(&App{Name: "blog", Port: 10000, Host: HostLocal, OwnerID: from.ID}))
	require.NoError(t, s.AddApp(&App{Name: "wiki", Port: 10001, Host: HostLocal, OwnerID: from.ID}))
	require.NoError(t, s.AddApp(&App{Name: "other", Port: 10002, Host: HostLocal, OwnerID: to.ID}))
	require.NoError(t, s.AddToken(&Token{UserID: from.ID, Hash: "h1", Prefix: "p", Label: "agent", AppName: "blog"}))
	require.NoError(t, s.AddToken(&Token{UserID: from.ID, Hash: "h2", Prefix: "p", Label: "account"}))

	moved, err := s.TransferApps(from.ID, to.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"blog", "wiki"}, moved)

	// The apps belong to the new owner...
	apps, err := s.AppsByOwner(to.ID)
	require.NoError(t, err)
	require.Len(t, apps, 3)
	own, err := s.AppsByOwner(from.ID)
	require.NoError(t, err)
	assert.Empty(t, own)

	// ...and so do their agent tokens, or deleting the old owner would take the
	// tokens of apps that are no longer theirs
	tk, err := s.TokenByHash("h1")
	require.NoError(t, err)
	assert.Equal(t, to.ID, tk.UserID)
	// An account-wide token is personal and stays behind, to die with the user
	tk, err = s.TokenByHash("h2")
	require.NoError(t, err)
	assert.Equal(t, from.ID, tk.UserID)
}
