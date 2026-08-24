package control

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/store"
)

// Everything the web app does, a user's own API token must be able to do too.
//
// The web app is just a client: it holds a session cookie, but nothing it does
// is supposed to be reachable ONLY with one. It is easy to break that by
// accident -- an endpoint that reads the session directly, or one that ends up
// behind requireAdmin because it was written while looking at the admin page --
// and the failure is invisible from the browser, which is the one client that
// still works.
//
// So this walks the endpoints the UI calls and asserts a plain user's token is
// never the reason one is refused. It deliberately does not care whether the
// call SUCCEEDS: with no node attached, a deploy or a file read fails on its
// own merits, and a 400 or a 409 is a fine answer. 401 and 403 are not.
func TestAUserTokenCanDoEverythingTheWebAppCan(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	friend := newActiveTestUser(t, s, "friend@example.com")
	token, _, err := s.users.CreateToken(owner.ID, "laptop")
	require.NoError(t, err)

	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: owner.ID}))
	app := "/api/apps/dash"

	calls := []struct {
		method string
		path   string
		body   string
	}{
		// The account pages
		{"GET", "/api/account", ""},
		{"GET", "/api/connections", ""},
		{"POST", "/api/connections", `{"provider":"generic","slug":"a-key","values":{"secret":"x"}}`},
		{"PUT", "/api/connections/a-key", `{"label":"renamed"}`},
		{"POST", "/api/connections/a-key/reconnect", ""},
		{"GET", "/api/connections/a-key/mcp/tools", ""},
		{"GET", "/api/providers", ""},
		{"POST", "/api/providers", `{"name":"acme","label":"Acme","client_id":"c","client_secret":"s","auth_url":"https://a/x","token_url":"https://a/t"}`},
		{"PUT", "/api/providers/acme", `{"label":"Acme 2","client_id":"c","auth_url":"https://a/x","token_url":"https://a/t"}`},
		{"DELETE", "/api/providers/acme", ""},
		{"DELETE", "/api/connections/a-key", ""},
		{"GET", "/api/account/keys", ""},
		{"POST", "/api/account/keys", `{"label":"laptop","key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB1 x@y"}`},
		{"PUT", "/api/account/keys/k_none", `{"label":"renamed"}`},
		{"GET", "/api/account/tokens", ""},
		{"POST", "/api/account/tokens", `{"label":"another"}`},

		// The dashboard
		{"GET", "/api/apps", ""},
		{"POST", "/api/apps", `{"name":"made","private":true}`},
		{"GET", app, ""},

		// The app page: settings
		{"PUT", app + "/description", `{"description":"what it is"}`},
		{"PUT", app + "/visibility", `{"private":true}`},
		{"PUT", app + "/snapshot-config", `{"interval":"3h"}`},
		{"PUT", app + "/keys", `{"ssh_keys":[]}`},
		{"POST", app + "/token", ""},
		{"PATCH", app + "/limits", `{"memory_mb":128}`},
		{"POST", app + "/rename", `{"name":"dash2"}`},

		// Access
		{"GET", app + "/collaborators", ""},
		{"POST", app + "/collaborators", `{"email":"friend@example.com"}`},
		{"DELETE", app + "/collaborators/" + friend.ID, ""},
		{"GET", app + "/viewers", ""},
		{"POST", app + "/viewers", `{"email":"friend@example.com"}`},
		{"DELETE", app + "/viewers/" + friend.ID, ""},

		// Domains
		{"GET", app + "/connections", ""},
		{"PUT", app + "/connections/a-key", ""},
		{"DELETE", app + "/connections/a-key", ""},
		{"GET", app + "/domains", ""},
		{"POST", app + "/domains", `{"domain":"dash.example.org"}`},
		{"POST", app + "/domains/dash.example.org/verify", ""},
		{"DELETE", app + "/domains/dash.example.org", ""},

		// Snapshots, logs, activity, preview
		{"GET", app + "/snapshots", ""},
		{"POST", app + "/snapshots", `{"label":"before"}`},
		{"GET", app + "/events", ""},
		{"GET", app + "/logs?lines=300", ""},
		{"POST", app + "/preview", ""},

		// Lifecycle and deploy
		{"POST", app + "/deploy", ""},
		{"POST", app + "/start", ""},
		{"POST", app + "/stop", ""},
		{"POST", app + "/restart", ""},
		{"POST", app + "/poweron", ""},
		{"POST", app + "/poweroff", ""},
		{"POST", app + "/reboot", ""},
		{"POST", app + "/archive", ""},
		{"POST", app + "/unarchive", ""},

		// The files the editor and the assistant drive
		{"GET", app + "/files", ""},
		{"POST", app + "/mkdir", `{"path":"src"}`},
		{"POST", app + "/move", `{"from":"a","to":"b"}`},
		{"POST", app + "/run", `{"command":"true"}`},

		// The assistant
		{"GET", app + "/assistant", ""},

		// Ownership acts, last: they change who may do the rest
		{"POST", app + "/fork", `{"name":"forked"}`},
		{"POST", app + "/transfer", `{"email":"friend@example.com"}`},
	}

	for _, c := range calls {
		rr := request(t, s.API(), c.method, c.path, c.body, token)
		assert.NotContains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, rr.Code,
			fmt.Sprintf("%s %s refused a user's own token (%d): %s", c.method, c.path, rr.Code, rr.Body.String()))
	}
}

// The other half of the same rule: a user's token must NOT reach the admin
// surface, however it is spelled. These are the endpoints the web app only
// renders for an admin.
func TestAUserTokenCannotReachTheAdminSurface(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	token, _, err := s.users.CreateToken(owner.ID, "laptop")
	require.NoError(t, err)

	for _, c := range []struct {
		method string
		path   string
		body   string
	}{
		{"GET", "/api/users", ""},
		{"POST", "/api/users", `{"email":"someone@example.com"}`},
		{"PATCH", "/api/users/" + owner.ID, `{"role":"admin"}`},
		{"DELETE", "/api/users/" + owner.ID, ""},
		{"GET", "/api/domains", ""},
		{"POST", "/api/domains", `{"domain":"example.com"}`},
		{"GET", "/api/cluster", ""},
		{"GET", "/api/settings", ""},
		{"PATCH", "/api/settings", `{"memory_mb":256}`},
	} {
		rr := request(t, s.API(), c.method, c.path, c.body, token)
		assert.Equal(t, http.StatusForbidden, rr.Code, "%s %s should be admin-only", c.method, c.path)
	}
}

// An app-scoped token is the one a user pastes into their own agent, so it is
// meant to be narrower: its own app, and nothing else.
func TestAnAppTokenIsConfinedToItsApp(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	owner := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: owner.ID}))
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a2", Name: "other", Port: 10001, Host: store.HostLocal, OwnerID: owner.ID}))
	token, _, err := s.users.CreateAppToken(owner.ID, "dash", "agent")
	require.NoError(t, err)

	assert.NotEqual(t, http.StatusForbidden, request(t, s.API(), "GET", "/api/apps/dash", "", token).Code, "its own app")
	assert.Equal(t, http.StatusForbidden, request(t, s.API(), "GET", "/api/apps/other", "", token).Code, "somebody else's")
	assert.Equal(t, http.StatusForbidden, request(t, s.API(), "GET", "/api/account", "", token).Code,
		"and never the account behind it, which would tell whoever holds it who its owner is")
}
