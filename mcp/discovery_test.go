package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeMCPServer stands in for a protected MCP server and its authorization
// server, so the discovery chain can be walked without the internet.
type fakeMCPServer struct {
	*httptest.Server
	// challenge is what the 401 advertises. Empty means no auth at all.
	challengeResourceMetadata bool
	scopes                    []string
	// authMeta chooses which metadata document the authorization server serves.
	oidcStyle bool
}

func newFakeMCPServer(t *testing.T, f *fakeMCPServer) *fakeMCPServer {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.Server = srv

	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		// A server that wants nothing answers normally. Only one that demands
		// authorization refuses, and it says where to arrange it.
		if !f.challengeResourceMetadata || r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("WWW-Authenticate",
			`Bearer resource_metadata="`+srv.URL+`/.well-known/oauth-protected-resource", scope="tools:read"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resource":              srv.URL + "/mcp",
			"authorization_servers": []string{srv.URL},
			"scopes_supported":      f.scopes,
		})
	})
	meta := map[string]any{
		"issuer":                                srv.URL,
		"authorization_endpoint":                srv.URL + "/authorize",
		"token_endpoint":                        srv.URL + "/token",
		"code_challenge_methods_supported":      []string{"S256"},
		"client_id_metadata_document_supported": true,
	}
	path := "/.well-known/oauth-authorization-server"
	if f.oidcStyle {
		path = "/.well-known/openid-configuration"
	}
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(meta)
	})
	return f
}

// A server that wants no authentication says so by answering, and discovery
// must report that rather than inventing a flow.
func TestDiscoverAnUnauthenticatedServer(t *testing.T) {
	t.Parallel()
	f := newFakeMCPServer(t, &fakeMCPServer{})
	got, err := Discover(context.Background(), http.DefaultClient, f.URL+"/mcp")
	require.NoError(t, err)
	assert.False(t, got.NeedsAuth, "no challenge means no authorization to arrange")
}

// The full chain an MCP client walks: a 401 names the resource metadata, which
// names the authorization server, which describes its own endpoints.
func TestDiscoverWalksTheChainFromA401(t *testing.T) {
	t.Parallel()
	f := newFakeMCPServer(t, &fakeMCPServer{challengeResourceMetadata: true, scopes: []string{"tools:read", "tools:write"}})

	got, err := Discover(context.Background(), http.DefaultClient, f.URL+"/mcp")
	require.NoError(t, err)
	require.True(t, got.NeedsAuth)
	assert.Equal(t, f.URL, got.Issuer)
	assert.Equal(t, f.URL+"/authorize", got.AuthorizationEndpoint)
	assert.Equal(t, f.URL+"/token", got.TokenEndpoint)
	assert.True(t, got.SupportsCIMD, "so hostit can identify itself by URL instead of registering")
	// The resource identifier is what the token gets bound to (RFC 8707)
	assert.Equal(t, f.URL+"/mcp", got.Resource)
	assert.Contains(t, got.Scopes, "tools:read")
}

// Some authorization servers publish only the OpenID document. A client that
// tries just one of the two finds nothing and reports the server as broken.
func TestDiscoverFallsBackToTheOpenIDDocument(t *testing.T) {
	t.Parallel()
	f := newFakeMCPServer(t, &fakeMCPServer{challengeResourceMetadata: true, oidcStyle: true})

	got, err := Discover(context.Background(), http.DefaultClient, f.URL+"/mcp")
	require.NoError(t, err)
	require.True(t, got.NeedsAuth)
	assert.Equal(t, f.URL+"/token", got.TokenEndpoint)
}

// A server that demands auth but advertises nothing cannot be connected
// automatically, and saying so beats a a half-built flow that 401s later.
func TestDiscoverReportsAnUnhelpfulChallenge(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	got, err := Discover(context.Background(), http.DefaultClient, srv.URL)
	require.NoError(t, err)
	assert.True(t, got.NeedsAuth)
	assert.False(t, got.CanAuthorize, "nothing was advertised, so nothing can be arranged for the owner")
}
