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
	rr := request(t, s.API(), "PATCH", "/v1/users/"+admin.ID, `{"role":"user"}`, token)
	assert.Equal(t, http.StatusBadRequest, rr.Code, "an admin must not demote themselves")
	rr = request(t, s.API(), "DELETE", "/v1/users/"+admin.ID, "", token)
	assert.Equal(t, http.StatusBadRequest, rr.Code, "an admin must not delete themselves")

	// Still an admin afterwards
	rr = request(t, s.API(), "GET", "/v1/users", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	var users []*apiUserResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &users))
	require.Len(t, users, 1)
	assert.Equal(t, store.RoleAdmin, users[0].Role)
}
