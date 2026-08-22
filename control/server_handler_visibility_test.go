package control

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/store"
)

// Creating an app private must be one step. Creating it public and flipping it
// afterwards leaves a window, however short, in which the routing table
// publishes it -- and that window is the whole thing this feature prevents.
func TestAppCanBeCreatedPrivate(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	rr := request(t, s.API(), "POST", "/api/apps", `{"name":"dash","private":true}`, testToken)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())

	var resp apiAppResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.True(t, resp.Private, "the response says so")

	a, err := s.apps.App("dash")
	require.NoError(t, err)
	assert.True(t, a.Private, "and so does the registry")
}

func TestAppsAreCreatedPublicByDefault(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	rr := request(t, s.API(), "POST", "/api/apps", `{"name":"blog"}`, testToken)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())

	a, err := s.apps.App("blog")
	require.NoError(t, err)
	assert.False(t, a.Private)
}

func TestVisibilityCanBeChanged(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal}))

	rr := request(t, s.API(), "PUT", "/api/apps/dash/visibility", `{"private":true}`, testToken)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	a, err := s.apps.App("dash")
	require.NoError(t, err)
	assert.True(t, a.Private)

	rr = request(t, s.API(), "PUT", "/api/apps/dash/visibility", `{"private":false}`, testToken)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	a, err = s.apps.App("dash")
	require.NoError(t, err)
	assert.False(t, a.Private, "and back again")
}

// Visibility is an ownership act: it decides who may see the app, so a
// collaborator -- who can already deploy and read the files -- still does not
// get to publish it to the world.
func TestOnlyTheOwnerMayChangeVisibility(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	friend := newActiveTestUser(t, s, "friend@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: owner.ID, Private: true}))
	require.NoError(t, s.apps.Store().AddAppCollaborator("a1", friend.ID))

	token, _, err := s.users.CreateToken(friend.ID, "laptop")
	require.NoError(t, err)
	rr := request(t, s.API(), "PUT", "/api/apps/dash/visibility", `{"private":false}`, token)
	assert.Equal(t, http.StatusForbidden, rr.Code)

	a, err := s.apps.App("dash")
	require.NoError(t, err)
	assert.True(t, a.Private, "still private")
}

// A stranger must not learn whether the app exists, let alone change it.
func TestAStrangerCannotChangeVisibility(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	stranger := newActiveTestUser(t, s, "stranger@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: owner.ID}))

	token, _, err := s.users.CreateToken(stranger.ID, "laptop")
	require.NoError(t, err)
	rr := request(t, s.API(), "PUT", "/api/apps/dash/visibility", `{"private":true}`, token)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// The viewer surface is deliberately thinner than the collaborator one: it
// grants sight of the app and no way into it.
func TestViewersAreManagedByTheOwner(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	guest := newActiveTestUser(t, s, "guest@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: owner.ID, Private: true}))
	ownerToken, _, err := s.users.CreateToken(owner.ID, "laptop")
	require.NoError(t, err)

	rr := request(t, s.API(), "POST", "/api/apps/dash/viewers", `{"email":"guest@example.com"}`, ownerToken)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.True(t, s.apps.Store().IsAppViewer("a1", guest.ID))

	rr = request(t, s.API(), "GET", "/api/apps/dash/viewers", "", ownerToken)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "guest@example.com")

	rr = request(t, s.API(), "DELETE", "/api/apps/dash/viewers/"+guest.ID, "", ownerToken)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.False(t, s.apps.Store().IsAppViewer("a1", guest.ID))
}

// A collaborator can already open the app, so a viewer row on top would be a
// grant that changes nothing while looking like it does something.
func TestACollaboratorCannotAlsoBeAddedAsAViewer(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	friend := newActiveTestUser(t, s, "friend@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: owner.ID, Private: true}))
	require.NoError(t, s.apps.Store().AddAppCollaborator("a1", friend.ID))
	ownerToken, _, err := s.users.CreateToken(owner.ID, "laptop")
	require.NoError(t, err)

	rr := request(t, s.API(), "POST", "/api/apps/dash/viewers", `{"email":"friend@example.com"}`, ownerToken)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "already a collaborator")
}

// Being able to see an app is not being able to work on it: a viewer gets no
// API access at all, so the app's files and terminal stay out of reach.
func TestAViewerGetsNoAPIAccess(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	guest := newActiveTestUser(t, s, "guest@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: owner.ID, Private: true}))
	require.NoError(t, s.apps.Store().AddAppViewer("a1", guest.ID))
	guestToken, _, err := s.users.CreateToken(guest.ID, "laptop")
	require.NoError(t, err)

	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "GET", "/api/apps/dash", "", guestToken).Code)
	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "GET", "/api/apps/dash/viewers", "", guestToken).Code)
	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "PUT", "/api/apps/dash/visibility", `{"private":false}`, guestToken).Code)

	// And the app does not turn up in their dashboard: a view grant is a URL
	// they can open, not a workspace they own.
	rr := request(t, s.API(), "GET", "/api/apps", "", guestToken)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.NotContains(t, rr.Body.String(), `"dash"`)
}
