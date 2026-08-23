package control

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/store"
)

// addCredential posts a pasted credential the way the UI does.
func addCredential(t *testing.T, s *Server, token, slug, label string) *apiConnectionResponse {
	t.Helper()
	body := fmt.Sprintf(`{"provider":"generic","slug":%q,"label":%q,"values":{"secret":"sk-%s"}}`, slug, label, slug)
	rr := request(t, s.API(), "POST", "/api/connections", body, token)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	var out apiConnectionResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &out))
	return &out
}

func TestConnectionsCrud(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)

	rr := request(t, s.API(), "GET", "/api/connections", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	var list apiConnectionsResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	assert.Empty(t, list.Connections)
	assert.NotEmpty(t, list.Providers, "the instance says what it can connect")

	c := addCredential(t, s, token, "openai", "OpenAI key")
	assert.Equal(t, "openai", c.Slug)
	assert.Equal(t, "generic", c.Provider)
	assert.Equal(t, "static", c.Kind)

	// The stored secret is never echoed back, on create or on list
	rr = request(t, s.API(), "GET", "/api/connections", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.NotContains(t, rr.Body.String(), "sk-openai", "a stored secret is never readable through the API")

	// Rename
	rr = request(t, s.API(), "PUT", "/api/connections/openai", `{"slug":"openai-work","label":"Work key"}`, token)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	rr = request(t, s.API(), "GET", "/api/connections", "", token)
	assert.Contains(t, rr.Body.String(), "openai-work")

	rr = request(t, s.API(), "DELETE", "/api/connections/openai-work", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	rr = request(t, s.API(), "DELETE", "/api/connections/openai-work", "", token)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// The reshape, through the API: two of the same provider, told apart by slug.
func TestTwoConnectionsOfTheSameProvider(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)

	addCredential(t, s, token, "work-key", "Work")
	addCredential(t, s, token, "home-key", "Home")

	rr := request(t, s.API(), "GET", "/api/connections", "", token)
	var list apiConnectionsResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	require.Len(t, list.Connections, 2)

	// A slug cannot be reused by its owner
	body := `{"provider":"generic","slug":"work-key","label":"again","values":{"secret":"sk-x"}}`
	rr = request(t, s.API(), "POST", "/api/connections", body, token)
	assert.Equal(t, http.StatusConflict, rr.Code)
}

func TestConnectionSlugValidation(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)

	for _, slug := range []string{"", "Has Spaces", "has/slash", "-leading", "trailing-", "x", "under_score"} {
		body := fmt.Sprintf(`{"provider":"generic","slug":%q,"values":{"secret":"sk-x"}}`, slug)
		rr := request(t, s.API(), "POST", "/api/connections", body, token)
		assert.Equal(t, http.StatusBadRequest, rr.Code, "should reject slug %q", slug)
	}
	// Case is normalised rather than refused: a slug is an identifier, and
	// rejecting "Work-Cal" teaches nothing that lowercasing it does not.
	rr := request(t, s.API(), "POST", "/api/connections", `{"provider":"generic","slug":"Work-Cal","values":{"secret":"sk-x"}}`, token)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	var made apiConnectionResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &made))
	assert.Equal(t, "work-cal", made.Slug)
	// A provider this instance cannot offer is refused rather than half-created
	rr = request(t, s.API(), "POST", "/api/connections", `{"provider":"slack","slug":"work-slack"}`, token)
	assert.Equal(t, http.StatusBadRequest, rr.Code, "no Slack client is configured here")
}

// Connections belong to their owner and nobody else can see or touch them.
func TestConnectionsAreOwnerScoped(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	mine := accountToken(t, s, newActiveTestUser(t, s, "me@example.com"))
	theirs := accountToken(t, s, newActiveTestUser(t, s, "them@example.com"))
	addCredential(t, s, mine, "secret-thing", "Mine")

	rr := request(t, s.API(), "GET", "/api/connections", "", theirs)
	require.Equal(t, http.StatusOK, rr.Code)
	var list apiConnectionsResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	assert.Empty(t, list.Connections, "another account sees none of mine")

	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "DELETE", "/api/connections/secret-thing", "", theirs).Code)
	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "PUT", "/api/connections/secret-thing", `{"slug":"stolen"}`, theirs).Code)
}

// Granting is per app and per connection: an app gets the one it was given.
func TestAppGrants(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	s.apps.PushMirror()
	addCredential(t, s, token, "work-key", "Work")
	addCredential(t, s, token, "home-key", "Home")

	rr := request(t, s.API(), "GET", "/api/apps/dash/connections", "", token)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var view apiAppConnectionsResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &view))
	assert.Empty(t, view.Granted)
	assert.Len(t, view.Available, 2, "both of the owner's, offered to grant")

	rr = request(t, s.API(), "PUT", "/api/apps/dash/connections/work-key", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	rr = request(t, s.API(), "GET", "/api/apps/dash/connections", "", token)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &view))
	require.Len(t, view.Granted, 1)
	assert.Equal(t, "work-key", view.Granted[0].Slug)

	rr = request(t, s.API(), "DELETE", "/api/apps/dash/connections/work-key", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	rr = request(t, s.API(), "GET", "/api/apps/dash/connections", "", token)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &view))
	assert.Empty(t, view.Granted)

	// You cannot grant an app a connection that is not yours
	other := accountToken(t, s, newActiveTestUser(t, s, "them@example.com"))
	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "PUT", "/api/apps/dash/connections/work-key", "", other).Code)
}

// Disconnecting cuts every app off, rather than leaving a grant pointing at
// nothing.
func TestDisconnectingRevokesEveryGrant(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token := accountToken(t, s, u)
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	s.apps.PushMirror()
	addCredential(t, s, token, "work-key", "Work")
	require.Equal(t, http.StatusOK, request(t, s.API(), "PUT", "/api/apps/dash/connections/work-key", "", token).Code)

	require.Equal(t, http.StatusOK, request(t, s.API(), "DELETE", "/api/connections/work-key", "", token).Code)
	rr := request(t, s.API(), "GET", "/api/apps/dash/connections", "", token)
	var view apiAppConnectionsResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &view))
	assert.Empty(t, view.Granted)
}
