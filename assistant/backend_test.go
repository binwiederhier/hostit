package assistant

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The catalog is what the credentials can actually serve, never a list someone
// wrote down: configure one backend and you get its models, configure both and
// you get both groups, configure neither and there is nothing to pick.
func TestCatalogFollowsTheConfiguredCredentials(t *testing.T) {
	t.Parallel()
	assert.Empty(t, Catalog(Credentials{}), "nothing configured offers nothing")

	api := Catalog(Credentials{AnthropicAPIKey: "k"})
	require.NotEmpty(t, api)
	for _, o := range api {
		assert.Equal(t, BackendAnthropic, o.Backend)
	}
	assert.Equal(t, "anthropic-haiku-4-5", api[0].ID, "the metered API leads with the cheap model")

	sub := Catalog(Credentials{ClaudeCodeOAuthToken: "t"})
	require.NotEmpty(t, sub)
	assert.Equal(t, "claude-opus-5", sub[0].ID, "a subscription turn costs the same, so lead with the strongest")

	both := Catalog(Credentials{AnthropicAPIKey: "k", ClaudeCodeOAuthToken: "t"})
	assert.Len(t, both, len(api)+len(sub))
	assert.Equal(t, BackendClaude, both[0].Backend, "the subscription group comes first")
}

// The same model reachable two ways is two different choices, because they bill
// differently -- that is the whole reason ids carry the backend.
func TestTheSameModelIsTwoOptionsWhenBothBackendsExist(t *testing.T) {
	t.Parallel()
	both := Catalog(Credentials{AnthropicAPIKey: "k", ClaudeCodeOAuthToken: "t"})

	claude, ok := Lookup(Credentials{AnthropicAPIKey: "k", ClaudeCodeOAuthToken: "t"}, "claude-opus-5")
	require.True(t, ok)
	anthropic, ok := Lookup(Credentials{AnthropicAPIKey: "k", ClaudeCodeOAuthToken: "t"}, "anthropic-opus-5")
	require.True(t, ok)

	assert.NotEqual(t, claude.ID, anthropic.ID)
	assert.Equal(t, claude.Model, anthropic.Model, "the same model upstream")
	assert.NotEqual(t, claude.Backend, anthropic.Backend, "but a different bill")
	assert.Equal(t, "Opus 5", claude.Label)
	_ = both
}

// A selection naming a backend this instance does not have must not resolve --
// an app that remembers a choice from before a credential was removed falls
// back rather than failing its next turn.
func TestLookupRefusesAnOptionThisInstanceCannotRun(t *testing.T) {
	t.Parallel()
	_, ok := Lookup(Credentials{AnthropicAPIKey: "k"}, "claude-opus-5")
	assert.False(t, ok, "the subscription is not configured here")
	_, ok = Lookup(Credentials{AnthropicAPIKey: "k"}, "anthropic-opus-5")
	assert.True(t, ok)
}

// The default is fixed, not configured: the subscription's Opus when it exists
// (already paid for), else the API's Sonnet.
func TestDefaultPrefersTheSubscription(t *testing.T) {
	t.Parallel()
	d, ok := Default(Credentials{AnthropicAPIKey: "k", ClaudeCodeOAuthToken: "t"})
	require.True(t, ok)
	assert.Equal(t, "claude-opus-5", d.ID)

	d, ok = Default(Credentials{AnthropicAPIKey: "k"})
	require.True(t, ok)
	assert.Equal(t, "anthropic-sonnet-5", d.ID)

	_, ok = Default(Credentials{})
	assert.False(t, ok, "nothing configured has no default")
}
