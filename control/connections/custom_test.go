package connections

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A custom provider is the catalog entry an operator writes themselves, for a
// service hostit has never heard of. The point is that the catalog entries were
// always PURE DATA -- there is no per-provider code anywhere -- so an operator
// can supply the same data and get the same behaviour.
func TestACustomProviderIsBuiltFromItsSpec(t *testing.T) {
	p, err := CustomProvider("acme", CustomSpec{
		Label:    "Acme",
		Scopes:   []string{"read", "write"},
		AuthURL:  "https://acme.example.com/oauth/authorize",
		TokenURL: "https://acme.example.com/oauth/token",
		Help:     "Your Acme workspace.",
	})
	require.NoError(t, err)

	assert.Equal(t, "acme", p.Name)
	assert.Equal(t, "Acme", p.Label)
	assert.Equal(t, KindOAuth, p.Kind)
	assert.Equal(t, []string{"read", "write"}, p.Scopes)
	assert.Equal(t, "Your Acme workspace.", p.Help)

	// And it behaves exactly like a built-in, because it IS one.
	consent := p.AuthCodeURL("client-1", "https://hostit.example/auth/callback", "state-1")
	assert.Contains(t, consent, "https://acme.example.com/oauth/authorize?")
	assert.Contains(t, consent, "scope=read+write")
	assert.Contains(t, consent, "client_id=client-1")
}

// The vendor quirks the built-ins needed are data too, so a custom provider can
// express them without a line of Go.
func TestACustomProviderCanCarryTheQuirksTheBuiltinsNeeded(t *testing.T) {
	p, err := CustomProvider("acme", CustomSpec{
		Label: "Acme", AuthURL: "https://a/x", TokenURL: "https://a/t",
		AuthParams:     map[string]string{"access_type": "offline"},
		LongLivedToken: true,
	})
	require.NoError(t, err)
	assert.True(t, p.LongLivedToken)
	assert.Contains(t, p.AuthCodeURL("c", "https://r", "s"), "access_type=offline")
}

// A half-written entry must be refused at load, where the operator is looking,
// rather than offered and then failing on somebody's consent screen.
func TestAnIncompleteCustomProviderIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec CustomSpec
		want string
	}{
		{"no label", CustomSpec{AuthURL: "https://a/x", TokenURL: "https://a/t"}, "label"},
		{"no endpoints", CustomSpec{Label: "Acme"}, "auth-url"},
		{"auth only", CustomSpec{Label: "Acme", AuthURL: "https://a/x"}, "token-url"},
		{"token only", CustomSpec{Label: "Acme", TokenURL: "https://a/t"}, "auth-url"},
		{"not a URL", CustomSpec{Label: "Acme", AuthURL: "acme.com/x", TokenURL: "https://a/t"}, "https://"},
	} {
		_, err := CustomProvider("acme", tc.spec)
		require.Error(t, err, tc.name)
		assert.Contains(t, err.Error(), tc.want, tc.name)
	}
}

// An issuer stands in for both endpoints: the operator gives one URL and hostit
// asks the service where its endpoints are, exactly as it does for an MCP
// server. Nothing is resolved here -- that needs the network -- so the spec is
// accepted and the endpoints stay empty until something resolves them.
func TestAnIssuerIsAcceptedInPlaceOfEndpoints(t *testing.T) {
	p, err := CustomProvider("acme", CustomSpec{Label: "Acme", Issuer: "https://acme.example.com"})
	require.NoError(t, err)
	assert.Empty(t, p.AuthURL, "not yet known")
	assert.True(t, p.NeedsDiscovery(), "so whoever uses it knows to resolve it first")

	fixed, err := CustomProvider("acme", CustomSpec{Label: "Acme", AuthURL: "https://a/x", TokenURL: "https://a/t"})
	require.NoError(t, err)
	assert.False(t, fixed.NeedsDiscovery())
}

// The name has to be usable in a URL and must not quietly shadow a built-in --
// an operator who names theirs "github" would silently change what every
// existing github connection means.
func TestACustomProviderCannotShadowABuiltinOrBeUnnamed(t *testing.T) {
	_, err := CustomProvider("github", CustomSpec{Label: "Mine", AuthURL: "https://a/x", TokenURL: "https://a/t"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "built-in")

	_, err = CustomProvider("Acme Corp", CustomSpec{Label: "Acme", AuthURL: "https://a/x", TokenURL: "https://a/t"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lowercase")
}

// A second GitHub App has to be declarable as a custom provider: its permissions
// are fixed on the app itself, so two permission profiles means two apps, and an
// operator can only express the second one here. That needs hybrid-token (a
// GitHub App issues an expiring token with a refresh one, or a permanent token
// with neither, depending on how it was registered) and the probe-url that the
// permanent variant is verified with.
func TestACustomProviderCanBeAGitHubApp(t *testing.T) {
	p, err := CustomProvider("github-readonly", CustomSpec{
		Label:       "GitHub (read-only)",
		AuthURL:     "https://github.com/login/oauth/authorize",
		TokenURL:    "https://github.com/login/oauth/access_token",
		HybridToken: true,
		ProbeURL:    "https://api.github.com/user",
	})
	require.NoError(t, err)
	assert.True(t, p.HybridToken, "which kind of token it got is decided per connection")
	assert.False(t, p.LongLivedToken)
	assert.Equal(t, "https://api.github.com/user", p.ProbeURL)
	// A GitHub App names no scopes, so the consent URL must carry none.
	assert.NotContains(t, p.AuthCodeURL("c", "https://r", "s"), "scope=")
}

// The two token models are mutually exclusive, and the hybrid one cannot verify
// its permanent variant without somewhere to probe -- both are refused at load,
// where the operator is looking.
func TestAHalfWrittenTokenModelIsRefused(t *testing.T) {
	base := func() CustomSpec {
		return CustomSpec{Label: "Acme", AuthURL: "https://a/x", TokenURL: "https://a/t"}
	}
	both := base()
	both.HybridToken, both.LongLivedToken, both.ProbeURL = true, true, "https://a/me"
	_, err := CustomProvider("acme", both)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "long-lived-token")

	noProbe := base()
	noProbe.HybridToken = true
	_, err = CustomProvider("acme", noProbe)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "probe-url")
}
