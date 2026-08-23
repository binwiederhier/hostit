package connections

import (
	"net/url"
	"strings"
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

// Everything the platform can attach, and the shape each one is. The point of
// the catalog is that most of it needs no OAuth client at all.
func TestTheStaticCatalog(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"imap", "smtp", "caldav", "carddav", "generic",
		"postgres", "mysql", "opensearch", "ntfy", "s3", "home-assistant",
	} {
		p, ok := Lookup(name)
		require.True(t, ok, "%s is offered", name)
		assert.Equal(t, KindStatic, p.Kind, "%s needs no OAuth client", name)
		assert.NotEmpty(t, p.Fields, "%s asks for something", name)
		assert.NotEmpty(t, p.SecretField, "%s names its secret", name)
		assert.NotEmpty(t, p.Help, "%s says what it is for", name)

		// The named secret must actually be one of the fields, and marked secret
		// -- otherwise it is stored in the clear as meta and shown in the UI.
		var found bool
		for _, f := range p.Fields {
			if f.Name == p.SecretField {
				found, _ = true, f
				assert.True(t, f.Secret, "%s: the secret field is marked secret", name)
			}
		}
		assert.True(t, found, "%s: SecretField names a real field", name)
		assert.True(t, p.Configured("", ""), "%s is always available", name)
	}
}

// The OAuth half of the catalog, including the two added for CRM and issues.
func TestTheOAuthCatalogCovers(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"google-calendar", "gmail", "slack", "discord", "github", "jira", "hubspot", "linear"} {
		p, ok := Lookup(name)
		require.True(t, ok, "%s is offered", name)
		assert.Equal(t, KindOAuth, p.Kind, name)
		assert.NotEmpty(t, p.AuthURL, name)
		assert.NotEmpty(t, p.TokenURL, name)
	}
}

// A database connection is one pasted URL: it is what every managed provider
// hands you, and splitting it into host/port/sslmode fields only invites the
// app to reassemble it wrongly.
func TestDatabaseConnectionsTakeAURL(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"postgres", "mysql"} {
		p, _ := Lookup(name)
		assert.Equal(t, "url", p.SecretField, "%s: the whole DSN is the credential", name)
		require.NoError(t, p.Validate(map[string]string{"url": "postgres://u:p@h:5432/db"}))
		assert.Error(t, p.Validate(map[string]string{}), "%s needs its URL", name)
	}
}

// Splitting matters where the non-secret half is genuinely useful on its own:
// a CalDAV URL is not a credential, and an app needs it beside the password.
func TestCaldavKeepsItsURLOutOfTheSecret(t *testing.T) {
	t.Parallel()
	p, ok := Lookup("caldav")
	require.True(t, ok)
	assert.Equal(t, "password", p.SecretField)
	require.NoError(t, p.Validate(map[string]string{
		"url": "https://caldav.fastmail.com/dav/calendars", "username": "me@fastmail.com", "password": "app-pw",
	}))
	assert.Error(t, p.Validate(map[string]string{"url": "https://x", "password": "p"}), "the username is required")
}

// ntfy needs only a token; the server defaults and the topic is the app's
// business, so both are optional.
func TestNtfyNeedsOnlyAToken(t *testing.T) {
	t.Parallel()
	p, ok := Lookup("ntfy")
	require.True(t, ok)
	assert.Equal(t, "token", p.SecretField)
	require.NoError(t, p.Validate(map[string]string{"token": "tk_abc"}))
	assert.Error(t, p.Validate(map[string]string{}))
}

// Gmail over IMAP with an app password sidesteps OAuth entirely -- no
// verification, no CASA, no 100-user cap and no 7-day token expiry. The imap
// provider should say so, because nobody would think to try it.
func TestImapMentionsGmail(t *testing.T) {
	t.Parallel()
	p, _ := Lookup("imap")
	assert.Contains(t, strings.ToLower(p.Help), "gmail")
}

// A Discord user token lists the servers someone is in and nothing inside them;
// channels and messages need a bot invited to the server. Two credentials, two
// providers, rather than one that half-works.
func TestDiscordUserAndBotAreSeparateProviders(t *testing.T) {
	t.Parallel()
	user, ok := Lookup("discord")
	require.True(t, ok)
	assert.Equal(t, KindOAuth, user.Kind)
	assert.Contains(t, strings.ToLower(user.Help), "bot token", "it says what it cannot do")

	bot, ok := Lookup("discord-bot")
	require.True(t, ok)
	assert.Equal(t, KindStatic, bot.Kind, "a bot token is pasted, not consented to")
	assert.Equal(t, "token", bot.SecretField)
	require.NoError(t, bot.Validate(map[string]string{"token": "MTIz.abc.def"}))
}

// An SSH key an app uses to reach another machine or a git remote. Multiline,
// because a key does not fit in a text input.
func TestSSHKeyCredential(t *testing.T) {
	t.Parallel()
	p, ok := Lookup("ssh-key")
	require.True(t, ok)
	assert.Equal(t, KindStatic, p.Kind)
	assert.Equal(t, "private-key", p.SecretField)

	var key Field
	for _, f := range p.Fields {
		if f.Name == "private-key" {
			key = f
		}
	}
	assert.True(t, key.Secret, "it is the credential")
	assert.True(t, key.Multiline, "a key does not fit on one line")

	require.NoError(t, p.Validate(map[string]string{
		"private-key": "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----",
	}))
}

// Pasting a PUBLIC key into the private key box is the mistake worth catching
// at paste time: it validates as "some text" and then fails much later, in an
// app, with an error that says nothing about what went wrong.
func TestSSHKeyRejectsAPublicKey(t *testing.T) {
	t.Parallel()
	p, _ := Lookup("ssh-key")
	err := p.Validate(map[string]string{"private-key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB1 me@laptop"})
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "private key")
	assert.Error(t, p.Validate(map[string]string{"private-key": "not a key at all"}))
}
