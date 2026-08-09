package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppEventLog(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	userToken, _, err := s.users.CreateToken(u.ID, "setup")
	require.NoError(t, err)
	rr := request(t, s.API(), "POST", "/api/apps", `{"name":"blog"}`, userToken)
	require.Equal(t, http.StatusCreated, rr.Code)

	// Creating the app recorded an activity-log entry, attributed to the caller.
	rr = request(t, s.API(), "GET", "/api/apps/blog/events", "", userToken)
	require.Equal(t, http.StatusOK, rr.Code)
	var events []apiEventResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &events))
	require.NotEmpty(t, events)
	assert.Equal(t, "created", events[0].Action)
	assert.Equal(t, "owner@example.com", events[0].Actor)
	assert.Equal(t, "info", events[0].Level)

	// A restart adds another, newest-first.
	rr = request(t, s.API(), "POST", "/api/apps/blog/restart", "", userToken)
	require.Equal(t, http.StatusOK, rr.Code)
	rr = request(t, s.API(), "GET", "/api/apps/blog/events", "", userToken)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &events))
	require.GreaterOrEqual(t, len(events), 2)
	assert.Equal(t, "restart", events[0].Action)

	// Another user cannot read this app's log.
	other := newActiveTestUser(t, s, "other@example.com")
	otherToken, _, err := s.users.CreateToken(other.ID, "laptop")
	require.NoError(t, err)
	rr = request(t, s.API(), "GET", "/api/apps/blog/events", "", otherToken)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}
