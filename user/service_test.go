package user

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/config"
	"heckel.io/hostit/store"
)

const (
	testPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIC24brF98CyUY18aeOGGQY3+wILYYnUUBQqICmMTvTGL test@host"
)

func TestLoginCreatesPendingUser(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	u, err := m.Login("someone@example.com", "Someone")
	require.NoError(t, err)
	assert.Equal(t, store.RoleUser, u.Role)
	assert.Equal(t, store.StatusPending, u.Status)
	// Logging in again returns the same user and refreshes the name
	u2, err := m.Login("someone@example.com", "Someone Else")
	require.NoError(t, err)
	assert.Equal(t, u.ID, u2.ID)
	assert.Equal(t, "Someone Else", u2.Name)
	assert.Equal(t, store.StatusPending, u2.Status)
}

func TestLoginAdminEmailIsAutoApproved(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	u, err := m.Login("phil@heckel.io", "Phil")
	require.NoError(t, err)
	assert.Equal(t, store.RoleAdmin, u.Role)
	assert.Equal(t, store.StatusActive, u.Status)
}

func TestLoginAdminEmailCaseInsensitive(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	u, err := m.Login("PHIL@Heckel.IO", "Phil")
	require.NoError(t, err)
	assert.Equal(t, store.RoleAdmin, u.Role)
	assert.Equal(t, "phil@heckel.io", u.Email) // Normalized
}

func TestLoginPromotesExistingUserToAdmin(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	require.NoError(t, m.store.AddUser(&store.User{Email: "phil@heckel.io", Role: store.RoleUser, Status: store.StatusPending}))
	u, err := m.Login("phil@heckel.io", "Phil")
	require.NoError(t, err)
	assert.Equal(t, store.RoleAdmin, u.Role)
	assert.Equal(t, store.StatusActive, u.Status)
}

func TestLoginDeniedStaysDenied(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	u, err := m.Login("bad@example.com", "Bad")
	require.NoError(t, err)
	u.Status = store.StatusDenied
	require.NoError(t, m.Update(u))
	u2, err := m.Login("bad@example.com", "Bad")
	require.NoError(t, err)
	assert.Equal(t, store.StatusDenied, u2.Status)
}

func TestLimitsFallBackToDefaults(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	u, err := m.Login("someone@example.com", "Someone")
	require.NoError(t, err)
	limits, err := m.Limits(u)
	require.NoError(t, err)
	assert.Equal(t, defaultAppLimit, limits.AppLimit)
	assert.Equal(t, defaultMemoryMB, limits.MemoryMB)
	assert.Equal(t, defaultDiskMB, limits.DiskMB)
	// Global defaults override the built-ins
	require.NoError(t, m.SetDefaults(&Limits{AppLimit: 7, MemoryMB: 128, DiskMB: 512}))
	limits, err = m.Limits(u)
	require.NoError(t, err)
	assert.Equal(t, 7, limits.AppLimit)
	assert.Equal(t, 128, limits.MemoryMB)
	// Per-user overrides win over globals
	appLimit := 3
	u.AppLimit = &appLimit
	require.NoError(t, m.Update(u))
	limits, err = m.Limits(u)
	require.NoError(t, err)
	assert.Equal(t, 3, limits.AppLimit)
	assert.Equal(t, 128, limits.MemoryMB) // Still global
}

func TestDefaults(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	defaults, err := m.Defaults()
	require.NoError(t, err)
	assert.Equal(t, defaultAppLimit, defaults.AppLimit)
	require.NoError(t, m.SetDefaults(&Limits{AppLimit: 4, MemoryMB: 256, DiskMB: 1024}))
	defaults, err = m.Defaults()
	require.NoError(t, err)
	assert.Equal(t, 4, defaults.AppLimit)
	assert.Equal(t, 256, defaults.MemoryMB)
	assert.Equal(t, 1024, defaults.DiskMB)
}

func TestTokenRoundTrip(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	u := newActiveUser(t, m, "someone@example.com")
	token, tk, err := m.CreateToken(u.ID, "claude")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(token, tokenPrefix))
	assert.Equal(t, "claude", tk.Label)
	assert.NotContains(t, tk.Prefix, token[len(token)-4:]) // Prefix is not the whole token
	got, err := m.UserByToken(token)
	require.NoError(t, err)
	assert.Equal(t, u.ID, got.ID)
	// Wrong tokens are rejected
	_, err = m.UserByToken("hostit_deadbeef")
	require.ErrorIs(t, err, store.ErrTokenNotFound)
	// Tokens can be listed and revoked
	tokens, err := m.Tokens(u.ID)
	require.NoError(t, err)
	require.Len(t, tokens, 1)
	require.NoError(t, m.DeleteToken(u.ID, tk.ID))
	_, err = m.UserByToken(token)
	require.ErrorIs(t, err, store.ErrTokenNotFound)
}

func TestTokenOfPendingUserRejected(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	u, err := m.Login("pending@example.com", "Pending")
	require.NoError(t, err)
	token, _, err := m.CreateToken(u.ID, "claude")
	require.NoError(t, err)
	_, err = m.UserByToken(token)
	require.ErrorIs(t, err, ErrNotActive)
}

func TestTokenOfDeniedUserRejected(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	u := newActiveUser(t, m, "someone@example.com")
	token, _, err := m.CreateToken(u.ID, "claude")
	require.NoError(t, err)
	u.Status = store.StatusDenied
	require.NoError(t, m.Update(u))
	_, err = m.UserByToken(token)
	require.ErrorIs(t, err, ErrNotActive)
}

func TestProfileKeys(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	u, err := m.Login("someone@example.com", "Someone")
	require.NoError(t, err)
	key, err := m.AddKey(u.ID, "laptop", testPublicKey)
	require.NoError(t, err)
	assert.Equal(t, "laptop", key.Label)
	keys, err := m.Keys(u.ID)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	// Invalid keys are rejected
	_, err = m.AddKey(u.ID, "junk", "not-a-key")
	require.Error(t, err)
	require.NoError(t, m.DeleteKey(u.ID, key.ID))
	keys, err = m.Keys(u.ID)
	require.NoError(t, err)
	assert.Empty(t, keys)
}

func TestApprove(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	u, err := m.Login("someone@example.com", "Someone")
	require.NoError(t, err)
	u.Status = store.StatusActive
	require.NoError(t, m.Update(u))
	got, err := m.User(u.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusActive, got.Status)
	users, err := m.Users()
	require.NoError(t, err)
	require.Len(t, users, 1)
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	conf := config.NewConfig()
	conf.BaseDomain = "apps.example.com"
	conf.AdminToken = "secr3t"
	conf.AdminEmails = []string{"phil@heckel.io"}
	s, err := store.NewStore(filepath.Join(t.TempDir(), "hostit.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = s.Close()
	})
	return NewManager(conf, s)
}

func newActiveUser(t *testing.T, m *Manager, email string) *store.User {
	t.Helper()
	u, err := m.Login(email, "Test User")
	require.NoError(t, err)
	u.Status = store.StatusActive
	require.NoError(t, m.Update(u))
	return u
}
