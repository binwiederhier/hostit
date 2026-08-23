package connections

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every provider the platform offers, and the shape each one is.
func TestTheCatalog(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"google-calendar", "gmail", "slack", "discord", "github", "jira"} {
		p, ok := Lookup(name)
		require.True(t, ok, "%s is offered", name)
		assert.Equal(t, KindOAuth, p.Kind, "%s is an OAuth connection", name)
		assert.NotEmpty(t, p.AuthURL, "%s needs a consent URL", name)
		assert.NotEmpty(t, p.TokenURL, "%s needs a token URL", name)
		assert.NotEmpty(t, p.Scopes, "%s asks for something", name)
		assert.NotEmpty(t, p.Help, "%s tells the owner what it is for", name)
	}
	for _, name := range []string{"imap", "generic"} {
		p, ok := Lookup(name)
		require.True(t, ok, "%s is offered", name)
		assert.Equal(t, KindStatic, p.Kind, "%s is a pasted credential", name)
		assert.NotEmpty(t, p.Fields)
		assert.NotEmpty(t, p.SecretField)
	}
}

// Calendar and Gmail are separate providers on purpose: granting an app the
// calendar must never imply its mail.
func TestCalendarAndMailAreSeparateProviders(t *testing.T) {
	t.Parallel()
	cal, _ := Lookup("google-calendar")
	mail, _ := Lookup("gmail")
	for _, s := range cal.Scopes {
		assert.NotContains(t, s, "gmail", "the calendar provider asks for no mail scope")
	}
	for _, s := range mail.Scopes {
		assert.NotContains(t, s, "calendar", "the mail provider asks for no calendar scope")
	}
}

// Not every provider issues a refresh token. Slack's bot token and a GitHub
// OAuth App's token do not expire and there is nothing to refresh -- treating
// them like Google would refuse the connection outright.
func TestProvidersThatIssueLongLivedTokens(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"slack", "github"} {
		p, _ := Lookup(name)
		assert.True(t, p.LongLivedToken, "%s returns a token that does not expire", name)
	}
	for _, name := range []string{"google-calendar", "gmail", "discord", "jira"} {
		p, _ := Lookup(name)
		assert.False(t, p.LongLivedToken, "%s issues a refresh token", name)
	}
}

// Each provider's consent URL carries what that provider actually needs, not
// Google's parameters copied everywhere.
func TestConsentURLsCarryPerProviderParameters(t *testing.T) {
	t.Parallel()
	params := func(name string) url.Values {
		p, ok := Lookup(name)
		require.True(t, ok)
		u, err := url.Parse(p.AuthCodeURL("cid", "https://hostit.example/cb", "st4te"))
		require.NoError(t, err)
		return u.Query()
	}

	g := params("google-calendar")
	assert.Equal(t, "cid", g.Get("client_id"))
	assert.Equal(t, "https://hostit.example/cb", g.Get("redirect_uri"))
	assert.Equal(t, "st4te", g.Get("state"))
	assert.Equal(t, "code", g.Get("response_type"))
	assert.Equal(t, "offline", g.Get("access_type"), "or Google returns no refresh token")
	assert.Equal(t, "consent", g.Get("prompt"))

	j := params("jira")
	assert.Equal(t, "api.atlassian.com", j.Get("audience"), "Atlassian 3LO needs its audience")
	assert.Equal(t, "consent", j.Get("prompt"))
	assert.Contains(t, j.Get("scope"), "offline_access", "which is how Jira grants a refresh token")

	// Slack and GitHub take none of Google's parameters
	for _, name := range []string{"slack", "github"} {
		q := params(name)
		assert.Empty(t, q.Get("access_type"), "%s: no Google parameters", name)
		assert.Empty(t, q.Get("include_granted_scopes"), "%s: no Google parameters", name)
	}
}

func TestGenericCredentialTakesAnyKeyValue(t *testing.T) {
	t.Parallel()
	p, ok := Lookup("generic")
	require.True(t, ok)
	require.NoError(t, p.Validate(map[string]string{"secret": "sk-abc123"}))
	assert.Error(t, p.Validate(map[string]string{}), "the secret is required")
}

func TestConfiguredGatesOnAClient(t *testing.T) {
	t.Parallel()
	oauth, _ := Lookup("slack")
	assert.False(t, oauth.Configured("", ""), "not offered without a client")
	assert.False(t, oauth.Configured("id", ""), "half a client is not a client")
	assert.True(t, oauth.Configured("id", "secret"))

	static, _ := Lookup("imap")
	assert.True(t, static.Configured("", ""), "a pasted credential needs no client")
}
