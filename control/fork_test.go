package control

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/store"
)

// A fork counts against the owner's app limit exactly like a create (both gate on
// checkAppLimit before anything else), so someone at their limit cannot fork their
// way past it. This is checked before the btrfs requirement, so it holds even on
// the non-btrfs test host.
func TestForkHonorsAppLimit(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "u@example.com")
	one := 1
	u.AppLimit = &one // cap at one app...
	require.NoError(t, s.users.Update(u))
	// ...and give them one, so they are exactly at the limit.
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))
	token, _, err := s.users.CreateAppToken(u.ID, "", "test")
	require.NoError(t, err)

	rr := request(t, s.API(), "POST", "/api/apps/blog/fork", `{"new_name":"blog2"}`, token)
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "app limit reached")
}
