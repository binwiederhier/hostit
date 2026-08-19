package control

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/assistant"
	"heckel.io/hostit/store"
)

// The dropdown is what the credentials can serve, not a list an operator
// maintains: there is no catalog setting and no per-user allowlist any more, so
// these come straight from the assistant's registry.
func TestAssistantOptionsFollowTheConfiguredCredentials(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	s.config.AnthropicAPIKey, s.config.ClaudeCodeOAuthToken = "", ""
	assert.Empty(t, s.assistantOptions(), "nothing configured offers nothing")

	s.config.AnthropicAPIKey = "k"
	api := s.assistantOptions()
	require.NotEmpty(t, api)
	for _, o := range api {
		assert.Equal(t, assistant.BackendAnthropic, o.Backend)
	}

	s.config.ClaudeCodeOAuthToken = "t"
	both := s.assistantOptions()
	assert.Greater(t, len(both), len(api))
	assert.Equal(t, assistant.BackendClaude, both[0].Backend, "the subscription group leads")
}

// A turn runs what the app last chose; a request overrides it; and a choice
// this instance can no longer run falls back to the default instead of failing.
func TestResolveModePrefersTheRequestThenTheAppsChoice(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.config.AnthropicAPIKey, s.config.ClaudeCodeOAuthToken = "k", "t"
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "blog", Port: 10000, Host: store.HostLocal, OwnerID: u.ID}))

	assert.Equal(t, "anthropic-haiku-4-5", s.resolveMode("anthropic-haiku-4-5", "blog"), "an explicit request wins")

	require.NoError(t, s.apps.Store().SetAppAssistantMode("blog", "anthropic-opus-5"))
	assert.Equal(t, "anthropic-opus-5", s.resolveMode("", "blog"), "else the app's remembered choice")

	// The subscription goes away: the remembered choice on it no longer resolves.
	require.NoError(t, s.apps.Store().SetAppAssistantMode("blog", "claude-opus-5"))
	s.config.ClaudeCodeOAuthToken = ""
	assert.Equal(t, "anthropic-opus-5", s.resolveMode("", "blog"), "falls back to the default rather than failing the turn")
}

// With the subscription configured it supplies the default, since it is already
// paid for.
func TestResolveModeDefaultsToTheSubscription(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	s.config.AnthropicAPIKey, s.config.ClaudeCodeOAuthToken = "k", "t"
	assert.Equal(t, "claude-fable-5", s.resolveMode("", "no-such-app"), "the head of the catalog")

	s.config.ClaudeCodeOAuthToken = ""
	assert.Equal(t, "anthropic-opus-5", s.resolveMode("", "no-such-app"), "without it, the API's first model")
}
