package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The collaborator contract: full working access, none of the ownership acts.
func TestCollaboratorAccessAndLimits(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	collab := newActiveTestUser(t, s, "collab@example.com")
	stranger := newActiveTestUser(t, s, "stranger@example.com")
	ownerToken := accountToken(t, s, owner)
	collabToken := accountToken(t, s, collab)
	strangerToken := accountToken(t, s, stranger)
	rr := request(t, s.API(), "POST", "/api/apps", `{"name":"blog"}`, ownerToken)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())

	// Only the owner (or an admin) may grant; a collaborator-to-be cannot.
	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "POST", "/api/apps/blog/collaborators", `{"email":"collab@example.com"}`, strangerToken).Code, "strangers must not learn the app exists")
	require.Equal(t, http.StatusOK, request(t, s.API(), "POST", "/api/apps/blog/collaborators", `{"email":"collab@example.com"}`, ownerToken).Code)
	// Unknown or inactive emails are rejected clearly.
	assert.Equal(t, http.StatusBadRequest, request(t, s.API(), "POST", "/api/apps/blog/collaborators", `{"email":"nosuch@example.com"}`, ownerToken).Code)

	// The collaborator now sees and works the app...
	assert.Equal(t, http.StatusOK, request(t, s.API(), "GET", "/api/apps/blog", "", collabToken).Code)
	assert.Equal(t, http.StatusCreated, request(t, s.API(), "PUT", "/api/apps/blog/files/hello.txt", "hi", collabToken).Code)
	var list []*apiAppResponse
	rr = request(t, s.API(), "GET", "/api/apps", "", collabToken)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	require.Len(t, list, 1)
	assert.Equal(t, "blog", list[0].Name)
	assert.False(t, list[0].IsOwner, "the response says whose app it is")

	// ...but cannot perform ownership acts: delete, rename, collaborator management.
	assert.Equal(t, http.StatusForbidden, request(t, s.API(), "DELETE", "/api/apps/blog", "", collabToken).Code)
	assert.Equal(t, http.StatusForbidden, request(t, s.API(), "POST", "/api/apps/blog/rename", `{"new_name":"mine"}`, collabToken).Code)
	assert.Equal(t, http.StatusForbidden, request(t, s.API(), "POST", "/api/apps/blog/collaborators", fmt.Sprintf(`{"email":%q}`, stranger.Email), collabToken).Code)
	assert.Equal(t, http.StatusForbidden, request(t, s.API(), "DELETE", "/api/apps/blog/collaborators/"+owner.ID, "", collabToken).Code)

	// A collaborator may remove THEMSELVES (leave), and access ends with the grant.
	require.Equal(t, http.StatusOK, request(t, s.API(), "DELETE", "/api/apps/blog/collaborators/"+collab.ID, "", collabToken).Code)
	assert.Equal(t, http.StatusNotFound, request(t, s.API(), "GET", "/api/apps/blog", "", collabToken).Code)

	// The owner sees IsOwner and an up-to-date collaborator list.
	rr = request(t, s.API(), "GET", "/api/apps", "", ownerToken)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	require.Len(t, list, 1)
	assert.True(t, list[0].IsOwner)
	var collabs []*apiCollaboratorResponse
	rr = request(t, s.API(), "GET", "/api/apps/blog/collaborators", "", ownerToken)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &collabs))
	assert.Empty(t, collabs)
}

// A collaborator's profile SSH keys ride along on the app while granted.
func TestCollaboratorProfileKeysSyncToTheApp(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	collab := newActiveTestUser(t, s, "collab@example.com")
	ownerToken := accountToken(t, s, owner)
	require.NoError(t, s.users.Update(owner))
	_, err := s.users.AddKey(owner.ID, "o", testPublicKey)
	require.NoError(t, err)
	_, err = s.users.AddKey(collab.ID, "c", testPublicKey2)
	require.NoError(t, err)
	rr := request(t, s.API(), "POST", "/api/apps", `{"name":"blog"}`, ownerToken)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())

	require.Equal(t, http.StatusOK, request(t, s.API(), "POST", "/api/apps/blog/collaborators", `{"email":"collab@example.com"}`, ownerToken).Code)
	keys := appAuthorizedKeys(t, s, "blog")
	assert.Contains(t, keys, testPublicKey, "the owner's key stays")
	assert.Contains(t, keys, testPublicKey2, "the collaborator's key joins on grant")

	require.Equal(t, http.StatusOK, request(t, s.API(), "DELETE", "/api/apps/blog/collaborators/"+collab.ID, "", ownerToken).Code)
	keys = appAuthorizedKeys(t, s, "blog")
	assert.Contains(t, keys, testPublicKey)
	assert.NotContains(t, keys, testPublicKey2, "the key leaves with the grant")
}

// appAuthorizedKeys reads the app's authorized_keys straight from disk (the
// real ssh service writes plain root-owned files since the idmap flip, so the
// test fixture uses it for real).
func appAuthorizedKeys(t *testing.T, s *Server, name string) []string {
	t.Helper()
	a, err := s.apps.Store().App(name)
	require.NoError(t, err)
	b, err := os.ReadFile(filepath.Join(s.config.AppsDir, a.ID, "home", "app", ".ssh", "authorized_keys"))
	require.NoError(t, err)
	var keys []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "ssh-") {
			keys = append(keys, line)
		}
	}
	return keys
}
