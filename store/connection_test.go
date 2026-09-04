package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testConn(userID, slug, provider string) *Connection {
	return &Connection{UserID: userID, Slug: slug, Provider: provider, Kind: ConnectionOAuth,
		Label: slug, Secret: "cipher-" + slug, CreatedAt: time.Now()}
}

func TestConnectionRoundTrip(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	c := testConn("u1", "work-cal", "google-calendar")
	require.NoError(t, s.AddConnection(c))
	assert.NotEmpty(t, c.ID, "the store assigns an id")

	got, err := s.ConnectionBySlug("u1", "work-cal")
	require.NoError(t, err)
	assert.Equal(t, "google-calendar", got.Provider)
	assert.Equal(t, "cipher-work-cal", got.Secret)

	byID, err := s.Connection(c.ID)
	require.NoError(t, err)
	assert.Equal(t, got.Slug, byID.Slug)

	require.NoError(t, s.DeleteConnection(c.ID))
	_, err = s.ConnectionBySlug("u1", "work-cal")
	assert.ErrorIs(t, err, ErrConnectionNotFound)
	assert.ErrorIs(t, s.DeleteConnection(c.ID), ErrConnectionNotFound)
}

// The whole reason this shape changed: one owner, two Google Calendars, told
// apart by the slug they chose. The old schema keyed on (user, provider) and
// could not express it at all.
func TestOneOwnerCanHoldSeveralOfTheSameProvider(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddConnection(testConn("u1", "work-cal", "google-calendar")))
	require.NoError(t, s.AddConnection(testConn("u1", "personal-cal", "google-calendar")))
	require.NoError(t, s.AddConnection(testConn("u1", "work-slack", "slack")))

	list, err := s.Connections("u1")
	require.NoError(t, err)
	require.Len(t, list, 3)

	work, err := s.ConnectionBySlug("u1", "work-cal")
	require.NoError(t, err)
	personal, err := s.ConnectionBySlug("u1", "personal-cal")
	require.NoError(t, err)
	assert.Equal(t, personal.Provider, work.Provider, "same provider")
	assert.NotEqual(t, personal.ID, work.ID, "different connections")
	assert.NotEqual(t, personal.Secret, work.Secret, "each holds its own credential")
}

func TestSlugsAreUniquePerOwnerButNotAcrossOwners(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddConnection(testConn("u1", "cal", "google-calendar")))

	err := s.AddConnection(testConn("u1", "cal", "gmail"))
	assert.ErrorIs(t, err, ErrConnectionSlugExists, "the same owner cannot reuse a slug")

	// A different owner is a different namespace entirely
	require.NoError(t, s.AddConnection(testConn("u2", "cal", "google-calendar")))
	mine, err := s.ConnectionBySlug("u1", "cal")
	require.NoError(t, err)
	theirs, err := s.ConnectionBySlug("u2", "cal")
	require.NoError(t, err)
	assert.NotEqual(t, mine.ID, theirs.ID)

	// And one owner's slug never resolves for another
	_, err = s.ConnectionBySlug("u3", "cal")
	assert.ErrorIs(t, err, ErrConnectionNotFound)
}

// A grant names a CONNECTION, not a provider, so granting the work calendar
// never hands over the personal one.
func TestGrantsNameOneConnection(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{ID: "a1", Name: "dash", Port: 10000, Host: HostLocal, OwnerID: "u1"}))
	work := testConn("u1", "work-cal", "google-calendar")
	personal := testConn("u1", "personal-cal", "google-calendar")
	require.NoError(t, s.AddConnection(work))
	require.NoError(t, s.AddConnection(personal))

	require.NoError(t, s.GrantConnection("a1", work.ID))
	granted, err := s.AppConnections("a1")
	require.NoError(t, err)
	require.Len(t, granted, 1)
	assert.Equal(t, "work-cal", granted[0].Slug, "only the one that was granted")

	// Revoking leaves the connection itself alone
	require.NoError(t, s.RevokeConnection("a1", work.ID))
	granted, err = s.AppConnections("a1")
	require.NoError(t, err)
	assert.Empty(t, granted)
	_, err = s.ConnectionBySlug("u1", "work-cal")
	assert.NoError(t, err, "revoking a grant does not disconnect the account")
}

func TestDeletingAConnectionTakesItsGrants(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{ID: "a1", Name: "dash", Port: 10000, Host: HostLocal, OwnerID: "u1"}))
	c := testConn("u1", "work-cal", "google-calendar")
	require.NoError(t, s.AddConnection(c))
	require.NoError(t, s.GrantConnection("a1", c.ID))

	require.NoError(t, s.DeleteConnection(c.ID))
	granted, err := s.AppConnections("a1")
	require.NoError(t, err)
	assert.Empty(t, granted, "disconnecting cuts every app off at once")
}

func TestDeletingAnAppTakesItsGrants(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{ID: "a1", Name: "dash", Port: 10000, Host: HostLocal, OwnerID: "u1"}))
	c := testConn("u1", "work-cal", "google-calendar")
	require.NoError(t, s.AddConnection(c))
	require.NoError(t, s.GrantConnection("a1", c.ID))

	require.NoError(t, s.RemoveApp("dash"))
	n, err := s.CountGrants(c.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "a deleted app leaves no grant behind to be revived by an id reuse")
}

// Updating in place is what a re-consent does: same slug, fresh secret.
func TestUpdateConnectionSecret(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	c := testConn("u1", "work-cal", "google-calendar")
	require.NoError(t, s.AddConnection(c))

	require.NoError(t, s.UpdateConnectionSecret(c.ID, "cipher-v2", "a b c", `{"email":"x@y"}`))
	got, err := s.Connection(c.ID)
	require.NoError(t, err)
	assert.Equal(t, "cipher-v2", got.Secret)
	assert.Equal(t, "a b c", got.Scopes)
	assert.Equal(t, `{"email":"x@y"}`, got.Meta)
	assert.ErrorIs(t, s.UpdateConnectionSecret("nosuch", "x", "", ""), ErrConnectionNotFound)
}

func TestRenameConnection(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	c := testConn("u1", "work-cal", "google-calendar")
	require.NoError(t, s.AddConnection(c))
	other := testConn("u1", "personal-cal", "google-calendar")
	require.NoError(t, s.AddConnection(other))

	require.NoError(t, s.RenameConnection(c.ID, "office-cal", "Office calendar"))
	got, err := s.ConnectionBySlug("u1", "office-cal")
	require.NoError(t, err)
	assert.Equal(t, "Office calendar", got.Label)

	assert.ErrorIs(t, s.RenameConnection(c.ID, "personal-cal", "x"), ErrConnectionSlugExists)
}

func TestGrantedAppNames(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{ID: "a1", Name: "zeta", Port: 10000, Host: HostLocal, OwnerID: "u1"}))
	require.NoError(t, s.AddApp(&App{ID: "a2", Name: "alpha", Port: 10001, Host: HostLocal, OwnerID: "u1"}))
	require.NoError(t, s.AddApp(&App{ID: "a3", Name: "mid", Port: 10002, Host: HostLocal, OwnerID: "u1"}))
	c := testConn("u1", "work-cal", "google-calendar")
	require.NoError(t, s.AddConnection(c))
	require.NoError(t, s.GrantConnection("a1", c.ID))
	require.NoError(t, s.GrantConnection("a2", c.ID))

	names, err := s.GrantedAppNames(c.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "zeta"}, names, "only granted apps, sorted by name")

	// A connection nobody holds lists nothing (never nil, so the JSON is [] not null).
	other := testConn("u1", "spare", "google-calendar")
	require.NoError(t, s.AddConnection(other))
	names, err = s.GrantedAppNames(other.ID)
	require.NoError(t, err)
	assert.Empty(t, names)
	assert.NotNil(t, names)
}

// A connection grant is one OWNER lending one app their credential, so it must
// not survive the app changing hands: the new owner would otherwise act as the
// old one. Enforced in the query, so every caller is safe by construction --
// the assistant reads grants through this same path.
func TestAppConnectionsAreScopedToTheAppsCurrentOwner(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{ID: "a1", Name: "dash", Port: 10000, Host: HostLocal, OwnerID: "u1"}))
	c := testConn("u1", "work-cal", "google-calendar")
	require.NoError(t, s.AddConnection(c))
	require.NoError(t, s.GrantConnection("a1", c.ID))

	granted, err := s.AppConnections("a1")
	require.NoError(t, err)
	require.Len(t, granted, 1, "the owner's own connection resolves")

	// Hand the app to somebody else: the previous owner's credential must not
	// come with it.
	require.NoError(t, s.SetAppOwner("dash", "u2"))
	granted, err = s.AppConnections("a1")
	require.NoError(t, err)
	assert.Empty(t, granted, "a stale grant from the previous owner does not resolve")
}

// Both transfer paths drop the app's connection grants: the single-app handover
// and the bulk move that deleting a user can do. The rows must go, not merely
// stop resolving, so the table never carries grants that mean nothing.
func TestTransfersDropConnectionGrants(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{ID: "a1", Name: "one", Port: 10000, Host: HostLocal, OwnerID: "u1"}))
	require.NoError(t, s.AddApp(&App{ID: "a2", Name: "two", Port: 10001, Host: HostLocal, OwnerID: "u1"}))
	c := testConn("u1", "work-cal", "google-calendar")
	require.NoError(t, s.AddConnection(c))
	require.NoError(t, s.GrantConnection("a1", c.ID))
	require.NoError(t, s.GrantConnection("a2", c.ID))

	// Single-app handover.
	require.NoError(t, s.SetAppOwner("one", "u2"))
	names, err := s.GrantedAppNames(c.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"two"}, names, "the handed-over app's grant is gone")

	// Bulk move (what deleting a user with ?apps=transfer does).
	_, err = s.TransferApps("u1", "u3")
	require.NoError(t, err)
	names, err = s.GrantedAppNames(c.ID)
	require.NoError(t, err)
	assert.Empty(t, names, "the bulk transfer drops them too")
}
