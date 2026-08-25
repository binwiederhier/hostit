package connections

import (
	"context"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// userScopeProvider is a stand-in for the Slack (personal) provider: a user
// token, a baseline scope, and three read options.
func userScopeProvider(tokenURL string) Provider {
	return Provider{
		Name: "slack-user", Label: "Slack (personal)", Kind: KindOAuth,
		UserToken: true, LongLivedToken: true, TokenURL: tokenURL,
		AuthURL: "https://slack.com/oauth/v2/authorize",
		Scopes:  []string{"users:read"},
		ScopeOptions: []ScopeOption{
			{Key: "public", Scopes: []string{"channels:read", "channels:history"}, Default: true},
			{Key: "private", Scopes: []string{"groups:read", "groups:history"}, Default: true},
			{Key: "search", Scopes: []string{"search:read"}, Default: true},
		},
	}
}

// A user-token provider requests its scopes as user_scope (not scope), so Slack
// issues a token that acts as the person rather than a bot that must be invited.
func TestAuthCodeURLUsesUserScope(t *testing.T) {
	t.Parallel()
	p := userScopeProvider("")
	p.Scopes = []string{"users:read", "search:read"} // caller has set the effective scopes
	u, err := url.Parse(p.AuthCodeURL("cid", "https://cb", "st"))
	require.NoError(t, err)
	q := u.Query()
	assert.Equal(t, "users:read search:read", q.Get("user_scope"))
	assert.Empty(t, q.Get("scope"), "a user-token provider must not send the bot scope param")
}

// A normal (bot/app) provider still uses the plain scope param.
func TestAuthCodeURLUsesScopeForBotToken(t *testing.T) {
	t.Parallel()
	p := Provider{Name: "slack", Kind: KindOAuth, AuthURL: "https://slack.com/oauth/v2/authorize", Scopes: []string{"channels:read"}}
	u, err := url.Parse(p.AuthCodeURL("cid", "https://cb", "st"))
	require.NoError(t, err)
	assert.Equal(t, "channels:read", u.Query().Get("scope"))
	assert.Empty(t, u.Query().Get("user_scope"))
}

// ResolveScopes maps the option keys the dialog sends to scopes, always
// including the baseline, in order, and rejects an unknown key.
func TestResolveScopes(t *testing.T) {
	t.Parallel()
	p := userScopeProvider("")

	got, err := p.ResolveScopes([]string{"public", "search"})
	require.NoError(t, err)
	assert.Equal(t, []string{"users:read", "channels:read", "channels:history", "search:read"}, got)

	_, err = p.ResolveScopes([]string{"public", "nonsense"})
	require.Error(t, err, "an unknown key must be refused, not trusted")

	// No options selected still yields the baseline.
	base, err := p.ResolveScopes(nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"users:read"}, base)
}

// DefaultScopeKeys are the options checked when the dialog opens.
func TestDefaultScopeKeys(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []string{"public", "private", "search"}, userScopeProvider("").DefaultScopeKeys())
}

// A user token comes back under authed_user.access_token, NOT the top-level
// access_token (which is the unused bot token); Exchange must store the former.
func TestExchangeReadsUserToken(t *testing.T) {
	t.Parallel()
	srv := tokenServer(t, map[string]any{
		"access_token": "xoxb-bot-ignored",
		"authed_user":  map[string]any{"access_token": "xoxp-user-1"},
	}, nil)
	p := userScopeProvider(srv.URL)

	secret, err := p.Exchange(context.Background(), srv.Client(), "cid", "sec", "https://cb", "code")
	require.NoError(t, err)
	assert.Equal(t, "xoxp-user-1", secret)
}
