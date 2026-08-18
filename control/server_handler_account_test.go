package control

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/store"
)

// accountToken mints an account-wide API token for a user, so a test can drive
// the account endpoints as that user without a browser session.
func accountToken(t *testing.T, s *Server, u *store.User) string {
	t.Helper()
	token, _, err := s.users.CreateAppToken(u.ID, "", "test")
	require.NoError(t, err)
	return token
}

func TestProfileKeyRoundTrip(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)

	rr := request(t, s.API(), "POST", "/api/account/keys", fmt.Sprintf(`{"label":"laptop","key":%q}`, testPublicKey), token)
	require.Equal(t, http.StatusCreated, rr.Code)

	rr = request(t, s.API(), "GET", "/api/account/keys", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	keys, err := s.users.Keys(u.ID)
	require.NoError(t, err)
	require.Len(t, keys, 1)

	rr = request(t, s.API(), "DELETE", "/api/account/keys/"+keys[0].ID, "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	keys, err = s.users.Keys(u.ID)
	require.NoError(t, err)
	assert.Empty(t, keys)
}

// A malformed key must be refused as a bad request, never a 500, and must not be
// stored -- the same key is installed on every one of the owner's apps.
func TestAddKeyRejectsGarbage(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	rr := request(t, s.API(), "POST", "/api/account/keys", `{"label":"bad","key":"not-a-key"}`, token)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	keys, err := s.users.Keys(u.ID)
	require.NoError(t, err)
	assert.Empty(t, keys)
}

// One user must never revoke another's credentials: the delete is scoped to the
// caller, so a foreign id is simply not found and the victim's key survives.
func TestOneUserCannotDeleteAnothersKeyOrToken(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	victim := newActiveTestUser(t, s, "victim@example.com")
	attacker := newActiveTestUser(t, s, "attacker@example.com")
	victimToken := accountToken(t, s, victim)
	attackerToken := accountToken(t, s, attacker)

	// The victim adds a key and mints an account token
	require.Equal(t, http.StatusCreated,
		request(t, s.API(), "POST", "/api/account/keys", fmt.Sprintf(`{"label":"k","key":%q}`, testPublicKey), victimToken).Code)
	victimKeys, err := s.users.Keys(victim.ID)
	require.NoError(t, err)
	require.Len(t, victimKeys, 1)
	victimTokens, err := s.users.Tokens(victim.ID)
	require.NoError(t, err)
	require.NotEmpty(t, victimTokens)

	// The attacker aims their own credentials at the victim's ids
	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "DELETE", "/api/account/keys/"+victimKeys[0].ID, "", attackerToken).Code)
	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "DELETE", "/api/account/tokens/"+victimTokens[0].ID, "", attackerToken).Code)

	// Both survive
	keys, err := s.users.Keys(victim.ID)
	require.NoError(t, err)
	assert.Len(t, keys, 1)
	tokens, err := s.users.Tokens(victim.ID)
	require.NoError(t, err)
	assert.NotEmpty(t, tokens)
}

// An app token may only be minted for an app the caller owns, or one tenant
// could hand out access to another's app.
func TestAppTokenMintedOnlyForAnOwnedApp(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: owner.ID}))
	s.apps.PushMirror()
	attacker := newActiveTestUser(t, s, "attacker@example.com")

	rr := request(t, s.API(), "POST", "/api/account/tokens", `{"app_name":"blog","label":"x"}`, accountToken(t, s, attacker))
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// The global admin token authenticates but has no user record, so profile
// endpoints must reject it clearly rather than panic on a nil user.
func TestGlobalAdminTokenHasNoProfile(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	for _, path := range []string{"/api/account/keys", "/api/account/tokens"} {
		rr := request(t, s.API(), "POST", path, `{"label":"x"}`, testToken)
		assert.Equal(t, http.StatusBadRequest, rr.Code, "path %s", path)
	}
}
