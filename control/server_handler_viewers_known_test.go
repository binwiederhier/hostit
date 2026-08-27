package control

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/store"
)

// The new-app dialog offers emails the owner has shared with before. The
// endpoint returns them distinct and this-owner-only, so one person's viewers
// never leak into another owner's suggestions.
func TestKnownViewersEndpoint(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	token, _, err := s.users.CreateToken(owner.ID, "t")
	require.NoError(t, err)

	bob := newActiveTestUser(t, s, "bob@example.com")
	ann := newActiveTestUser(t, s, "ann@example.com")
	stranger := newActiveTestUser(t, s, "stranger@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: owner.ID}))
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a2", Name: "wiki", Port: 10001, Host: store.HostLocal, OwnerID: owner.ID}))
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a3", Name: "theirs", Port: 10002, Host: store.HostLocal, OwnerID: stranger.ID}))
	require.NoError(t, s.apps.Store().AddAppViewer("a1", bob.ID))
	require.NoError(t, s.apps.Store().AddAppViewer("a2", bob.ID)) // same person twice -> one email
	require.NoError(t, s.apps.Store().AddAppViewer("a2", ann.ID))
	require.NoError(t, s.apps.Store().AddAppViewer("a3", ann.ID)) // on the stranger's app -> excluded from owner's known set here

	rr := request(t, s.API(), "GET", "/api/viewers/known", "", token)
	require.Equal(t, http.StatusOK, rr.Code)
	var body struct {
		Emails []string `json:"emails"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(t, []string{"ann@example.com", "bob@example.com"}, body.Emails)
}
