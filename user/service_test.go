package user

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/controlconf"
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
	u, err := m.Login("phil@example.com", "Phil")
	require.NoError(t, err)
	assert.Equal(t, store.RoleAdmin, u.Role)
	assert.Equal(t, store.StatusActive, u.Status)
}

func TestLoginAdminEmailCaseInsensitive(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	u, err := m.Login("PHIL@Example.COM", "Phil")
	require.NoError(t, err)
	assert.Equal(t, store.RoleAdmin, u.Role)
	assert.Equal(t, "phil@example.com", u.Email) // Normalized
}

func TestLoginFromAnAllowedDomainSkipsApproval(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	_, err := m.AllowDomain("allowed.example")
	require.NoError(t, err)
	u, err := m.Login("newhire@allowed.example", "New Hire")
	require.NoError(t, err)
	assert.Equal(t, store.StatusActive, u.Status)
	assert.Equal(t, store.RoleUser, u.Role, "an allowed domain approves, it does not promote")
	// Everyone else still waits
	other, err := m.Login("stranger@example.com", "Stranger")
	require.NoError(t, err)
	assert.Equal(t, store.StatusPending, other.Status)
}

func TestAllowingADomainApprovesWhoeverWasWaiting(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	u, err := m.Login("early@allowed.example", "Early Bird")
	require.NoError(t, err)
	require.Equal(t, store.StatusPending, u.Status)
	// Someone the admin explicitly turned away must stay turned away
	denied, err := m.Login("nope@allowed.example", "Nope")
	require.NoError(t, err)
	denied.Status = store.StatusDenied
	require.NoError(t, m.Update(denied))

	_, err = m.AllowDomain("allowed.example")
	require.NoError(t, err)
	u, err = m.Login("early@allowed.example", "Early Bird")
	require.NoError(t, err)
	assert.Equal(t, store.StatusActive, u.Status)
	denied, err = m.Login("nope@allowed.example", "Nope")
	require.NoError(t, err)
	assert.Equal(t, store.StatusDenied, denied.Status)
}

func TestAllowDomainNormalizesWhatAnAdminTypes(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	for _, input := range []string{"*@allowed.example", "@allowed.example", "  ALLOWED.EXAMPLE ", "allowed.example"} {
		d, err := m.AllowDomain(input)
		require.NoError(t, err, "input %q", input)
		assert.Equal(t, "allowed.example", d.Domain, "input %q", input)
	}
	domains, err := m.AllowedDomains()
	require.NoError(t, err)
	assert.Len(t, domains, 1, "the same domain typed four ways is one domain")

	for _, bad := range []string{"", "*@", "slide", "not a domain.com", "someone@allowed.example", "*@*"} {
		_, err := m.AllowDomain(bad)
		assert.ErrorIs(t, err, ErrInvalid, "input %q must be rejected", bad)
	}
	require.NoError(t, m.DisallowDomain("*@allowed.example"), "removal takes the same shapes")
	require.ErrorIs(t, m.DisallowDomain("allowed.example"), store.ErrDomainNotFound)
}

func TestInviteCreatesAnApprovedUser(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	u, err := m.Invite("newhire@allowed.example", store.RoleUser)
	require.NoError(t, err)
	assert.Equal(t, store.StatusActive, u.Status)
	assert.Equal(t, store.RoleUser, u.Role)

	// Their first Google sign-in finds the invite and fills in the real name
	loggedIn, err := m.Login("NewHire@allowed.example", "New Hire")
	require.NoError(t, err)
	assert.Equal(t, u.ID, loggedIn.ID)
	assert.Equal(t, store.StatusActive, loggedIn.Status)
	assert.Equal(t, "New Hire", loggedIn.Name)

	// Admins can be invited too
	admin, err := m.Invite("boss@allowed.example", store.RoleAdmin)
	require.NoError(t, err)
	assert.Equal(t, store.RoleAdmin, admin.Role)

	// Garbage in, and inviting the same person twice, are both refused
	for _, bad := range []string{"", "nope", "  ", "@allowed.example"} {
		_, err := m.Invite(bad, store.RoleUser)
		assert.ErrorIs(t, err, ErrInvalid, "input %q must be rejected", bad)
	}
	_, err = m.Invite("newhire@allowed.example", store.RoleUser)
	assert.ErrorIs(t, err, ErrInvalid)
	_, err = m.Invite("someone@allowed.example", store.Role("wizard"))
	assert.ErrorIs(t, err, ErrInvalid)
}

func TestLoginPromotesExistingUserToAdmin(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	require.NoError(t, m.store.AddUser(&store.User{Email: "phil@example.com", Role: store.RoleUser, Status: store.StatusPending}))
	u, err := m.Login("phil@example.com", "Phil")
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

func TestAppScopedToken(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	u := newActiveUser(t, m, "someone@example.com")
	token, tk, err := m.CreateAppToken(u.ID, "blog", "claude")
	require.NoError(t, err)
	assert.Equal(t, "blog", tk.AppName)
	// Resolving it yields both the user and the app it is limited to
	got, scope, err := m.UserAndScopeByToken(token)
	require.NoError(t, err)
	assert.Equal(t, u.ID, got.ID)
	assert.Equal(t, "blog", scope)
	// An account-wide token has no scope
	wide, _, err := m.CreateToken(u.ID, "laptop")
	require.NoError(t, err)
	_, scope, err = m.UserAndScopeByToken(wide)
	require.NoError(t, err)
	assert.Empty(t, scope)
	// App tokens are listed with their app, and can be revoked like any other
	tokens, err := m.Tokens(u.ID)
	require.NoError(t, err)
	require.Len(t, tokens, 2)
	require.NoError(t, m.DeleteToken(u.ID, tk.ID))
	_, _, err = m.UserAndScopeByToken(token)
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

func TestUpdateActivatesAPendingUser(t *testing.T) {
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
	conf := controlconf.NewConfig()
	conf.BaseDomain = "apps.example.com"
	conf.AdminToken = "secr3t"
	conf.AdminEmails = []string{"phil@example.com"}
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

func TestAllowDomainRefusesPublicMailProviders(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	// Allowing gmail.com means allowing everyone with an email address. It is
	// almost always a slip, and the cost of the slip is the whole instance.
	for _, domain := range []string{
		"gmail.com", "GMAIL.COM", "*@gmail.com", "googlemail.com",
		"hotmail.com", "outlook.com", "yahoo.com", "icloud.com", "proton.me",
		"gmx.de", "web.de", "qq.com", "mail.ru", "yandex.ru",
	} {
		_, err := m.AllowDomain(domain)
		require.ErrorIs(t, err, ErrInvalid, "%q must be refused", domain)
		assert.Contains(t, err.Error(), "public email provider", "%q needs a reason the admin understands", domain)
	}
	// A company domain is the point of the feature
	for _, domain := range []string{"allowed.example", "example.com", "mycompany.co.uk"} {
		_, err := m.AllowDomain(domain)
		assert.NoError(t, err, "%q must be allowed", domain)
	}
}

// Pools bound the SUM of a user's apps' effective limits. A user without an
// explicit pool derives app_limit x the per-app default -- exactly as capable
// as before pools existed, so the feature's arrival changes nobody's budget.
func TestLimitsDerivePools(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	u := &store.User{Email: "a@example.com", Name: "A", Role: store.RoleUser, Status: store.StatusActive}
	require.NoError(t, m.store.AddUser(u))

	limits, err := m.Limits(u)
	require.NoError(t, err)
	assert.Equal(t, 3*512, limits.MemoryPoolMB, "derived: app_limit x per-app default")
	assert.Equal(t, 3*2048, limits.DiskPoolMB)

	// An explicit pool wins over the derivation.
	pool := 4096
	u.MemoryPoolMB = &pool
	require.NoError(t, m.store.UpdateUser(u))
	limits, err = m.Limits(u)
	require.NoError(t, err)
	assert.Equal(t, 4096, limits.MemoryPoolMB)
	assert.Equal(t, 3*2048, limits.DiskPoolMB, "the other pool still derives")

	// A raised per-app default raises the derived pool with it.
	mem := 1024
	u.MemoryPoolMB, u.MemoryMB = nil, &mem
	require.NoError(t, m.store.UpdateUser(u))
	limits, err = m.Limits(u)
	require.NoError(t, err)
	assert.Equal(t, 3*1024, limits.MemoryPoolMB)
}
