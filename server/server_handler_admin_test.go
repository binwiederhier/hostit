package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/store"
)

func TestAdminCannotLockEveryoneOut(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	admin := newActiveTestUser(t, s, "admin@example.com")
	admin.Role = store.RoleAdmin
	require.NoError(t, s.users.Update(admin))
	token, _, err := s.users.CreateToken(admin.ID, "t")
	require.NoError(t, err)

	// The only things standing between one confused click and an instance
	// nobody can administer
	rr := request(t, s.API(), "PATCH", "/api/users/"+admin.ID, `{"role":"user"}`, token)
	assert.Equal(t, http.StatusBadRequest, rr.Code, "an admin must not demote themselves")
	rr = request(t, s.API(), "DELETE", "/api/users/"+admin.ID, "", token)
	assert.Equal(t, http.StatusBadRequest, rr.Code, "an admin must not delete themselves")

	// Still an admin afterwards
	rr = request(t, s.API(), "GET", "/api/users", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	var users []*apiUserResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &users))
	require.Len(t, users, 1)
	assert.Equal(t, store.RoleAdmin, users[0].Role)
}

func TestDeleteUserTransfersOrDeletesTheirApps(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	leaving := newActiveTestUser(t, s, "leaving@example.com")
	staying := newActiveTestUser(t, s, "staying@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: leaving.ID}))

	// Deleting a person should not silently take their work with them, so the
	// caller has to say which they meant
	rr := request(t, s.API(), "DELETE", "/api/users/"+leaving.ID+"?apps=transfer&transfer_to="+staying.ID, "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	app, err := s.apps.Store().App("blog")
	require.NoError(t, err)
	assert.Equal(t, staying.ID, app.OwnerID, "the app must have a live owner")
	_, err = s.users.User(leaving.ID)
	assert.ErrorIs(t, err, store.ErrUserNotFound)

	// The other choice really does remove them
	other := newActiveTestUser(t, s, "other@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "wiki", Port: 10001, Host: store.HostLocal, OwnerID: other.ID}))
	rr = request(t, s.API(), "DELETE", "/api/users/"+other.ID+"?apps=delete", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	_, err = s.apps.Store().App("wiki")
	assert.ErrorIs(t, err, store.ErrAppNotFound)
}

func TestSettingsGetAndUpdate(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	// The defaults are readable, then a partial PATCH changes only what it names
	rr := request(t, s.API(), "GET", "/api/settings", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var before apiSettingsResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &before))

	rr = request(t, s.API(), "PATCH", "/api/settings", `{"default_app_limit":7,"default_memory_mb":512}`, testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var after apiSettingsResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &after))
	assert.Equal(t, 7, after.DefaultAppLimit)
	assert.Equal(t, 512, after.DefaultMemoryMB)
	assert.Equal(t, before.DefaultDiskMB, after.DefaultDiskMB, "an omitted field is left untouched")

	// And it persisted
	rr = request(t, s.API(), "GET", "/api/settings", "", testToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var reread apiSettingsResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &reread))
	assert.Equal(t, 7, reread.DefaultAppLimit)
	assert.Equal(t, 512, reread.DefaultMemoryMB)
}

func TestSettingsUpdateRejectsGarbage(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "PATCH", "/api/settings", `not json`, testToken)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// Every admin endpoint sits behind requireAdmin: an approved but non-admin user
// is refused with a 403 (not a 404, not a 401), across the whole admin surface.
func TestAdminEndpointsRefuseNonAdmins(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "member@example.com")
	token, _, err := s.users.CreateAppToken(u.ID, "", "t")
	require.NoError(t, err)

	for _, tc := range []struct {
		method, path, body string
	}{
		{"GET", "/api/users", ""},
		{"POST", "/api/users", `{"email":"x@example.com"}`},
		{"GET", "/api/domains", ""},
		{"POST", "/api/domains", `{"domain":"example.com"}`},
		{"GET", "/api/settings", ""},
		{"PATCH", "/api/settings", `{"default_app_limit":1}`},
	} {
		rr := request(t, s.API(), tc.method, tc.path, tc.body, token)
		assert.Equal(t, http.StatusForbidden, rr.Code, "%s %s must be 403 for a non-admin", tc.method, tc.path)
	}
}

func TestDeleteUserRefusesAnUnusableTransfer(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	leaving := newActiveTestUser(t, s, "leaving@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: leaving.ID}))

	for _, query := range []string{
		"?apps=transfer",                           // to nobody
		"?apps=transfer&transfer_to=u_nosuch",      // to someone who does not exist
		"?apps=transfer&transfer_to=" + leaving.ID, // to the person being deleted
		"?apps=nonsense",
	} {
		rr := request(t, s.API(), "DELETE", "/api/users/"+leaving.ID+query, "", testToken)
		assert.Equal(t, http.StatusBadRequest, rr.Code, "query %q", query)
	}
	// Nothing happened
	_, err := s.users.User(leaving.ID)
	assert.NoError(t, err)
}
