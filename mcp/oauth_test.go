package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAuthServer records the token requests it is sent, so the tests can assert
// on what hostit proves rather than only on what it gets back.
type fakeAuthServer struct {
	*httptest.Server
	forms       []url.Values
	rotate      bool
	refuse      string
	wantAudChk  bool
	issuedCount int
}

func newFakeAuthServer(t *testing.T, f *fakeAuthServer) *fakeAuthServer {
	t.Helper()
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, err := url.ParseQuery(string(body))
		require.NoError(t, err)
		f.forms = append(f.forms, form)
		w.Header().Set("Content-Type", "application/json")
		if f.refuse != "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": f.refuse})
			return
		}
		f.issuedCount++
		out := map[string]any{"access_token": "at-1", "expires_in": 3600, "refresh_token": "rt-1"}
		if f.rotate {
			out["refresh_token"] = "rt-2"
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(f.Server.Close)
	return f
}

func testDiscovery(f *fakeAuthServer) Discovery {
	return Discovery{
		NeedsAuth:             true,
		CanAuthorize:          true,
		Issuer:                f.URL,
		AuthorizationEndpoint: f.URL + "/authorize",
		TokenEndpoint:         f.URL + "/token",
		SupportsCIMD:          true,
		Resource:              "https://mcp.example.com/mcp",
		Scopes:                []string{"tools:read"},
	}
}

func TestTheConsentURLCarriesPKCEAndTheResource(t *testing.T) {
	d := testDiscovery(&fakeAuthServer{Server: httptest.NewServer(nil)})
	d.AuthorizationEndpoint = "https://as.example.com/authorize"
	pkce, err := NewPKCE()
	require.NoError(t, err)

	raw := AuthCodeURL(d, "https://hostit.example/client.json", "https://hostit.example/cb", "state-1", pkce)
	u, err := url.Parse(raw)
	require.NoError(t, err)
	q := u.Query()

	assert.Equal(t, "https://as.example.com/authorize", u.Scheme+"://"+u.Host+u.Path)
	assert.Equal(t, "code", q.Get("response_type"))
	assert.Equal(t, pkce.Challenge, q.Get("code_challenge"))
	assert.Equal(t, "S256", q.Get("code_challenge_method"))
	assert.Equal(t, "https://mcp.example.com/mcp", q.Get("resource"),
		"RFC 8707: the token is minted for this server and is useless against another")
	assert.Equal(t, "tools:read", q.Get("scope"))
	assert.Equal(t, "https://hostit.example/client.json", q.Get("client_id"),
		"CIMD: hostit identifies itself by a URL the server can fetch, so there is nothing to register")
}

func TestTheCodeExchangeProvesPossessionOfTheVerifier(t *testing.T) {
	f := newFakeAuthServer(t, &fakeAuthServer{})
	pkce, err := NewPKCE()
	require.NoError(t, err)

	tok, err := Exchange(context.Background(), http.DefaultClient, testDiscovery(f),
		"https://hostit.example/client.json", "https://hostit.example/cb", "the-code", pkce)
	require.NoError(t, err)
	assert.Equal(t, "at-1", tok.AccessToken)
	assert.Equal(t, "rt-1", tok.RefreshToken)
	require.NotNil(t, tok.ExpiresAt)

	require.Len(t, f.forms, 1)
	assert.Equal(t, "authorization_code", f.forms[0].Get("grant_type"))
	assert.Equal(t, pkce.Verifier, f.forms[0].Get("code_verifier"))
	assert.Equal(t, "https://mcp.example.com/mcp", f.forms[0].Get("resource"))
	assert.Empty(t, f.forms[0].Get("client_secret"),
		"hostit is a public client here: there is no per-server secret to have")
}

// MCP mandates refresh-token rotation, so the old token dies on first use. A
// client that keeps the original works exactly once.
func TestARotatedRefreshTokenIsReturnedSoItCanBeStored(t *testing.T) {
	f := newFakeAuthServer(t, &fakeAuthServer{rotate: true})

	tok, err := Refresh(context.Background(), http.DefaultClient, testDiscovery(f),
		"https://hostit.example/client.json", "rt-1")
	require.NoError(t, err)
	assert.Equal(t, "at-1", tok.AccessToken)
	assert.Equal(t, "rt-2", tok.RefreshToken, "the new one, or the next refresh fails as invalid_grant")
	assert.Equal(t, "refresh_token", f.forms[0].Get("grant_type"))
	assert.Equal(t, "https://mcp.example.com/mcp", f.forms[0].Get("resource"))
}

func TestARefusedRefreshSaysWhoRefusedAndWhy(t *testing.T) {
	f := newFakeAuthServer(t, &fakeAuthServer{refuse: "invalid_grant"})

	_, err := Refresh(context.Background(), http.DefaultClient, testDiscovery(f),
		"https://hostit.example/client.json", "rt-old")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid_grant")
	assert.ErrorIs(t, err, ErrUnauthorized, "so the UI can say 'reconnect' rather than 'something went wrong'")
}

// The client metadata document is the thing an authorization server fetches at
// the client_id URL. If it does not describe the redirect hostit actually uses,
// every consent is refused.
func TestTheClientMetadataDocumentDescribesTheRealRedirect(t *testing.T) {
	doc := ClientMetadata("https://hostit.example/api/connections/callback", "https://hostit.example/client.json")

	var got map[string]any
	require.NoError(t, json.Unmarshal(doc, &got))
	assert.Equal(t, "https://hostit.example/client.json", got["client_id"])
	assert.Contains(t, got["redirect_uris"], "https://hostit.example/api/connections/callback")
	assert.Equal(t, "none", got["token_endpoint_auth_method"], "public client, no secret")
	assert.Contains(t, got["grant_types"], "authorization_code")
	assert.Contains(t, got["grant_types"], "refresh_token")
}

// Not every authorization server accepts a URL as a client_id. The next best
// thing is dynamic registration -- deprecated by MCP in favour of CIMD, but
// still what a server that predates it offers.
func TestAServerWithoutCIMDIsRegisteredDynamically(t *testing.T) {
	var seen map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&seen))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "generated-id"})
	}))
	t.Cleanup(srv.Close)

	d := Discovery{TokenEndpoint: "https://as.example.com/token", RegistrationEndpoint: srv.URL, SupportsCIMD: false}
	id, err := Register(context.Background(), http.DefaultClient, d,
		"https://hostit.example/cb", "https://hostit.example/client.json")
	require.NoError(t, err)
	assert.Equal(t, "generated-id", id)
	assert.Equal(t, "none", seen["token_endpoint_auth_method"], "hostit registers as the public client it is")
	assert.Contains(t, seen["redirect_uris"], "https://hostit.example/cb")
}

// A server that issues a secret is refusing to treat hostit as a public client.
// hostit has nowhere safe to put one, so this is said plainly rather than half
// worked around.
func TestARegistrationThatIssuesASecretIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "x", "client_secret": "shh"})
	}))
	t.Cleanup(srv.Close)

	d := Discovery{TokenEndpoint: "https://as.example.com/token", RegistrationEndpoint: srv.URL}
	_, err := Register(context.Background(), http.DefaultClient, d, "https://hostit.example/cb", "https://hostit.example/client.json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secret")
}

// The third case, which a real server (GitHub's MCP endpoint) actually is:
// neither a URL client_id nor registration. There is nothing to try, and
// guessing would produce a consent error nobody could diagnose.
func TestAServerWithNeitherIsReportedAsUnusable(t *testing.T) {
	d := Discovery{NeedsAuth: true, CanAuthorize: true, Issuer: "https://github.com/login/oauth",
		TokenEndpoint: "https://github.com/login/oauth/access_token"}
	_, err := ClientIDFor(context.Background(), http.DefaultClient, d,
		"https://hostit.example/cb", "https://hostit.example/client.json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "github.com/login/oauth", "name the server, so an operator knows what to go and register")
}

// The happy path stays a no-op: a CIMD server needs no registration at all.
func TestACIMDServerNeedsNoRegistration(t *testing.T) {
	d := Discovery{NeedsAuth: true, CanAuthorize: true, SupportsCIMD: true, TokenEndpoint: "https://as/token"}
	id, err := ClientIDFor(context.Background(), http.DefaultClient, d, "https://hostit.example/cb", "https://hostit.example/client.json")
	require.NoError(t, err)
	assert.Equal(t, "https://hostit.example/client.json", id)
}

// A server that refuses to register hostit is not the user mistyping something,
// and the error must not read as if it were. Fastmail's MCP server does exactly
// this: it allowlists the redirect URIs it will register, so claude.ai and
// localhost are accepted and every self-hosted instance is refused.
func TestARegistrationRefusedForTheRedirectSaysWhoRefusedAndWhy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		// Exactly Fastmail's shape: the code repeated inside the description.
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":             "invalid_redirect_uri",
			"error_description": "invalid_redirect_uri redirect_uri not a valid scheme or host",
		})
	}))
	t.Cleanup(srv.Close)

	d := Discovery{TokenEndpoint: "https://as.example.com/token", RegistrationEndpoint: srv.URL, Issuer: "https://api.example.com"}
	_, err := Register(context.Background(), http.DefaultClient, d,
		"https://hostit.example/auth/callback", "https://hostit.example/client.json")
	require.Error(t, err)
	msg := err.Error()

	assert.Contains(t, msg, "api.example.com", "name who refused")
	assert.Contains(t, msg, "https://hostit.example/auth/callback", "and which redirect it refused")
	assert.Contains(t, msg, "approved in advance",
		"and say what it MEANS -- an allowlist, not a typo the owner can fix")
	assert.Equal(t, 1, strings.Count(msg, "invalid_redirect_uri"),
		"the provider repeats its own code inside the description; saying it twice reads like a bug")
}
