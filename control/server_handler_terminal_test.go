package control

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/store"
)

// The terminal is a shell in the app's container, so it must be reachable only
// by the app's owner (or an admin). A stranger is turned away before any upgrade.
func TestTerminalRequiresAppOwner(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: owner.ID}))
	s.apps.PushMirror()
	stranger := newActiveTestUser(t, s, "stranger@example.com")

	// A non-owner is refused, and the app's existence is not revealed
	rr := request(t, s.API(), "GET", "/api/apps/blog/terminal", "", accountToken(t, s, stranger))
	assert.Equal(t, http.StatusNotFound, rr.Code)

	// The owner passes the ownership gate; the upgrade then fails only because this
	// plain request carries no WebSocket headers (proving auth was not the blocker).
	rr = request(t, s.API(), "GET", "/api/apps/blog/terminal", "", accountToken(t, s, owner))
	assert.NotEqual(t, http.StatusNotFound, rr.Code)
}
