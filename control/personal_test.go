package control

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The global admin token is an OPERATOR credential, not a person. It has no
// user record, so caller.userID() is the empty string -- and every endpoint
// scoped to a person then quietly operates on a namespace nobody owns.
//
// That is not a read-only curiosity. Writes land in it: a connection stored
// with user_id "" can never be resolved by any app (an app resolves in its
// OWNER's namespace), and it is invisible to every person on the instance. The
// bug that found this was two cleanup passes that reported success while
// deleting nothing, because they were looking at that namespace.
func TestTheAdminTokenIsRefusedTheSurfacesThatBelongToAPerson(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	for _, c := range []struct {
		method string
		path   string
		body   string
	}{
		{"GET", "/api/connections", ""},
		{"POST", "/api/connections", `{"provider":"generic","slug":"orphan","values":{"secret":"x"}}`},
		{"PUT", "/api/connections/orphan", `{"label":"x"}`},
		{"DELETE", "/api/connections/orphan", ""},
		{"POST", "/api/connections/orphan/reconnect", ""},
		{"GET", "/api/connections/orphan/mcp/tools", ""},
		{"GET", "/api/account/keys", ""},
		{"POST", "/api/account/keys", `{"label":"laptop","key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB1 x@y"}`},
		{"GET", "/api/account/tokens", ""},
		{"POST", "/api/account/tokens", `{"label":"t"}`},
	} {
		rr := request(t, s.API(), c.method, c.path, c.body, testToken)
		assert.Equal(t, http.StatusForbidden, rr.Code, "%s %s", c.method, c.path)
		assert.Contains(t, rr.Body.String(), "belongs to a person", "%s %s", c.method, c.path)
	}
}

// The operator half is still an operator's to do. Defining a provider for the
// WHOLE INSTANCE is exactly what an operator credential is for, so it stays --
// and listing, which shows what the instance offers, is harmless.
func TestTheAdminTokenCanStillDoTheOperatorHalf(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	list := request(t, s.API(), "GET", "/api/providers", "", testToken)
	assert.Equal(t, http.StatusOK, list.Code, list.Body.String())

	instance := request(t, s.API(), "POST", "/api/providers",
		`{"name":"acme","label":"Acme","scope":"instance","client_id":"c","client_secret":"s","auth_url":"https://a/x","token_url":"https://a/t"}`,
		testToken)
	assert.Equal(t, http.StatusCreated, instance.Code, instance.Body.String())
}

// A PERSONAL provider from the admin token used to be stored with owner_id ""
// -- which is the marker for an INSTANCE provider. So a request that said
// "personal" silently defined one for everybody. Refused rather than guessed.
func TestAPersonalProviderFromTheAdminTokenIsRefusedRatherThanMadeInstanceWide(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	rr := request(t, s.API(), "POST", "/api/providers",
		`{"name":"acme","label":"Acme","client_id":"c","client_secret":"s","auth_url":"https://a/x","token_url":"https://a/t"}`,
		testToken)
	require.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), "scope")
}
