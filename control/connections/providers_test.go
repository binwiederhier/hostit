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
	for _, name := range []string{"google-calendar", "gmail", "google-drive", "slack-bot", "discord", "github", "jira"} {
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
	// Drive is its own provider too: connecting a calendar must never hand over
	// the person's files, and connecting Drive must not reach their mail.
	drive, ok := Lookup("google-drive")
	require.True(t, ok, "google-drive is offered")
	for _, s := range drive.Scopes {
		assert.NotContains(t, s, "gmail", "the drive provider asks for no mail scope")
		assert.NotContains(t, s, "calendar", "the drive provider asks for no calendar scope")
	}
	for _, s := range append(append([]string{}, cal.Scopes...), mail.Scopes...) {
		assert.NotContains(t, s, "drive", "neither calendar nor mail asks for drive")
	}
	// Read-only, like the other Google connections.
	assert.Contains(t, strings.Join(drive.Scopes, " "), "drive.readonly", "drive is read-only")
}

// Not every provider issues a refresh token. Slack's bot token does not expire
// and there is nothing to refresh -- treating it like Google would refuse the
// connection outright. GitHub is HYBRID: a classic OAuth App's token never
// expires, but a GitHub App (or an OAuth App with expiring tokens) issues an
// 8h token AND a refresh token, so github must be able to refresh -- treating
// every github connection as long-lived was why one died overnight.
func TestProvidersThatIssueLongLivedTokens(t *testing.T) {
	t.Parallel()
	slack, _ := Lookup("slack-bot")
	assert.True(t, slack.LongLivedToken, "slack-bot returns a token that does not expire")
	assert.False(t, slack.HybridToken)

	// GitHub and Linear are both hybrid: refreshable when the app issues a refresh
	// token, permanent (probe-only) when it does not.
	for _, name := range []string{"github", "linear"} {
		p, _ := Lookup(name)
		assert.False(t, p.LongLivedToken, "%s is not fixed long-lived", name)
		assert.True(t, p.HybridToken, "%s is hybrid: refreshable or permanent by registration", name)
		assert.NotEmpty(t, p.ProbeURL, "%s: the permanent variant still needs a probe", name)
	}

	for _, name := range []string{"google-calendar", "gmail", "google-drive", "discord", "jira"} {
		p, _ := Lookup(name)
		assert.False(t, p.LongLivedToken, "%s issues a refresh token", name)
		assert.False(t, p.HybridToken, "%s is not hybrid", name)
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
	for _, name := range []string{"slack-bot", "github"} {
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
	oauth, _ := Lookup("slack-bot")
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
	for _, name := range []string{"google-calendar", "gmail", "slack-bot", "discord", "github", "jira", "hubspot", "linear"} {
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

// The dialog suggests a NAME for a new connection, and the suggestion is
// provider knowledge -- every static credential once showed "OpenAI key",
// including Home Assistant.
func TestEveryProviderSuggestsItsOwnName(t *testing.T) {
	t.Parallel()
	for _, p := range All() {
		assert.NotEmpty(t, p.NameHint, "%s suggests what to call it", p.Name)
		if p.Name != "generic" {
			assert.NotContains(t, strings.ToLower(p.NameHint), "openai",
				"%s must not borrow the catch-all's example", p.Name)
		}
	}
	// A few that are worth getting right rather than defaulting to the label
	for name, want := range map[string]string{
		"home-assistant":  "Home Assistant",
		"ssh-key":         "Deploy key",
		"google-calendar": "Work calendar",
		"generic":         "OpenAI key",
	} {
		p, ok := Lookup(name)
		require.True(t, ok, name)
		assert.Equal(t, want, p.NameHint, name)
	}
}

// Fastmail's JMAP reaches mail, calendars and contacts through ONE scoped API
// token, which beats a CalDAV credential plus an IMAP credential plus an SMTP
// credential for the same account.
func TestFastmailIsOneCredentialForEverything(t *testing.T) {
	t.Parallel()
	p, ok := Lookup("fastmail")
	require.True(t, ok)
	assert.Equal(t, KindStatic, p.Kind, "an API token, not OAuth")
	assert.Equal(t, "token", p.SecretField)
	require.NoError(t, p.Validate(map[string]string{"token": "fmu1-abc"}))
	// The session endpoint is what a JMAP client starts from, so the app should
	// not have to know it by heart.
	assert.Contains(t, p.Help+" "+fieldPlaceholder(p, "session"), "api.fastmail.com/jmap/session")
}

func fieldPlaceholder(p Provider, name string) string {
	for _, f := range p.Fields {
		if f.Name == name {
			return f.Placeholder
		}
	}
	return ""
}

// GitHub's OAuth App scopes are coarse: "repo" is full read/write on EVERY
// repository, public and private, and GitHub offers nothing finer -- no
// read-only private access and no per-repository limit. So the checkboxes make
// the wide grant a deliberate choice rather than the default, and public-only
// (which is a genuinely smaller thing to hand an app) is what a connection gets
// unless somebody ticks more.
func TestGitHubOAuthAppOffersNarrowerScopes(t *testing.T) {
	t.Parallel()
	p, ok := Lookup("github")
	require.True(t, ok)
	require.NotEmpty(t, p.ScopeOptions, "the coarse grant is opt-in, not the default")

	// The baseline alone identifies the person and reaches no code at all.
	base, err := p.ResolveScopes(nil)
	require.NoError(t, err)
	assert.NotContains(t, base, "repo", "no private-repo access without asking")
	assert.NotContains(t, base, "public_repo", "and no code access at all")

	// The default ticks are public repositories only.
	def, err := p.ResolveScopes(p.DefaultScopeKeys())
	require.NoError(t, err)
	assert.Contains(t, def, "public_repo")
	assert.NotContains(t, def, "repo", "full private access is never a default")

	// Asking for private repos is what pulls in the wide scope.
	all, err := p.ResolveScopes([]string{"private"})
	require.NoError(t, err)
	assert.Contains(t, all, "repo")

	// One OAuth App issues one grant per user, so a second connection on it
	// would only alias the first -- the same reason Slack disallows multiples.
	assert.False(t, p.AllowsMultiple(), "a second GitHub connection would share the first's token")
}

// A GitHub APP is the answer to what OAuth App scopes cannot express: it is
// installed on chosen repositories with per-resource read/write permissions, so
// the grant is configured on GitHub rather than requested here. It therefore
// asks for no scopes at all -- "a user access token only has permissions that
// both the user and the app have".
func TestGitHubAppAsksForNoScopes(t *testing.T) {
	t.Parallel()
	p, ok := Lookup("github-app")
	require.True(t, ok, "github-app is offered")
	assert.Equal(t, KindOAuth, p.Kind)
	assert.Empty(t, p.Scopes, "permissions come from the app's own configuration")
	assert.Empty(t, p.ScopeOptions, "and there is nothing here to tick")
	assert.True(t, p.HybridToken, "user tokens expire only if the app opts in, so treat both as possible")
	assert.False(t, p.AllowsMultiple(), "one app, one grant per user")
	assert.NotEmpty(t, p.Help)
}

// A provider with no scopes must not send an empty scope= parameter.
func TestConsentURLOmitsAnEmptyScope(t *testing.T) {
	t.Parallel()
	p, ok := Lookup("github-app")
	require.True(t, ok)
	u := p.AuthCodeURL("cid", "https://hostit.example/auth/callback", "state123")
	assert.NotContains(t, u, "scope=", "no scope parameter at all, rather than an empty one")
}
