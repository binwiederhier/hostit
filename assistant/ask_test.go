package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingStore is MemoryStore plus a note of what usage was recorded, which
// is the half that matters here: an app's spend has to land somewhere the
// operator can see it.
type recordingStore struct {
	*MemoryStore
	usage []recordedUsage
}

type recordedUsage struct {
	app   string
	usage Usage
}

func (s *recordingStore) RecordUsage(app string, u Usage) error {
	s.usage = append(s.usage, recordedUsage{app: app, usage: u})
	return nil
}

func askManager(t *testing.T, replies ...response) (*Manager, *fakeCompleter, *recordingStore) {
	t.Helper()
	fc := &fakeCompleter{replies: replies}
	st := &recordingStore{MemoryStore: NewMemoryStore()}
	m := NewManager(fc, newFakeOps(), st, Credentials{AnthropicAPIKey: "sk-test"})
	return m, fc, st
}

// An app asking the model a question. This answers and nothing else -- no tools: the
// app gets text back, and never gets the API key, which is the whole point --
// an app that held the key could spend the operator's money without limit and
// without being seen.
func TestAnAppAsksAQuestionAndGetsTheAnswer(t *testing.T) {
	t.Parallel()
	m, fc, st := askManager(t, response{Content: []ContentBlock{{Type: blockText, Text: "Arrr, ahoy!"}},
		Usage: usage{InputTokens: 12, OutputTokens: 4}})

	got, err := m.Ask(context.Background(), "blog", AskRequest{
		System:   "You are a pirate.",
		Messages: []AskMessage{{Role: "user", Content: "Say hello"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "Arrr, ahoy!", got.Text)
	assert.NotEmpty(t, got.Model, "the app is told which model answered")

	require.Len(t, fc.calls, 1)
	assert.Empty(t, fc.calls[0].Tools, "NO tools: an app that could call write_file on itself through this is a self-modifying loop with nobody in the room")
	require.Len(t, fc.calls[0].System, 1)
	assert.Contains(t, fc.calls[0].System[0].Text, "pirate")

	// Metered onto the app, so it shows up in the operator's cost view rather
	// than being spent invisibly.
	require.Len(t, st.usage, 1)
	assert.Equal(t, "blog", st.usage[0].app)
	assert.Equal(t, 12, st.usage[0].usage.InputTokens)
}

// A conversation, so an app can be a chat: the whole history goes up, because
// the model is stateless and hostit keeps nothing per app here.
func TestAConversationIsPassedThrough(t *testing.T) {
	t.Parallel()
	m, fc, _ := askManager(t, response{Content: []ContentBlock{{Type: blockText, Text: "aye"}}})

	_, err := m.Ask(context.Background(), "blog", AskRequest{Messages: []AskMessage{
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "two"},
		{Role: "user", Content: "three"},
	}})
	require.NoError(t, err)
	require.Len(t, fc.calls[0].Messages, 3)
	assert.Equal(t, "assistant", fc.calls[0].Messages[1].Role)
}

// Every request spends the operator's money, so the ceiling is hostit's, not
// the app's. A request asking for more is clamped rather than refused: the app
// asked for something reasonable-sounding and gets an answer, just a bounded one.
func TestMaxTokensIsClampedToTheCeiling(t *testing.T) {
	t.Parallel()
	m, fc, _ := askManager(t, response{Content: []ContentBlock{{Type: blockText, Text: "ok"}}},
		response{Content: []ContentBlock{{Type: blockText, Text: "ok"}}})

	_, err := m.Ask(context.Background(), "blog", AskRequest{
		Messages: []AskMessage{{Role: "user", Content: "hi"}}, MaxTokens: 999999,
	})
	require.NoError(t, err)
	assert.Equal(t, maxAskTokens, fc.calls[0].MaxTokens)

	_, err = m.Ask(context.Background(), "blog", AskRequest{
		Messages: []AskMessage{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, defaultAskTokens, fc.calls[1].MaxTokens, "and a request that says nothing gets a modest default")
}

func TestAnEmptyOrOversizedRequestIsRefused(t *testing.T) {
	t.Parallel()
	m, _, _ := askManager(t)

	_, err := m.Ask(context.Background(), "blog", AskRequest{})
	assert.ErrorIs(t, err, ErrAskInvalid, "nothing to ask")

	_, err = m.Ask(context.Background(), "blog", AskRequest{
		Messages: []AskMessage{{Role: "user", Content: strings.Repeat("x", maxAskChars+1)}},
	})
	assert.ErrorIs(t, err, ErrAskTooLarge)

	many := make([]AskMessage, maxAskMessages+1)
	for i := range many {
		many[i] = AskMessage{Role: "user", Content: "x"}
	}
	_, err = m.Ask(context.Background(), "blog", AskRequest{Messages: many})
	assert.ErrorIs(t, err, ErrAskTooLarge)

	_, err = m.Ask(context.Background(), "blog", AskRequest{Messages: []AskMessage{{Role: "system", Content: "x"}}})
	assert.ErrorIs(t, err, ErrAskInvalid, "a role the API does not take")
}

// An app in a loop must not be able to spend without bound. The same reservation
// the interactive assistant uses applies here, keyed on the app's OWNER, because
// it is their operator's budget either way.
func TestAnAppInALoopIsRateLimited(t *testing.T) {
	t.Parallel()
	replies := make([]response, 10)
	for i := range replies {
		replies[i] = response{Content: []ContentBlock{{Type: blockText, Text: "ok"}}}
	}
	m, _, _ := askManager(t, replies...)
	m.maxRunsPerHour = 2

	req := AskRequest{Messages: []AskMessage{{Role: "user", Content: "hi"}}}
	for i := 0; i < 2; i++ {
		_, err := m.AskFor(context.Background(), "u_1", "blog", req)
		require.NoError(t, err, "call %d", i)
	}
	_, err := m.AskFor(context.Background(), "u_1", "blog", req)
	assert.ErrorIs(t, err, ErrRateLimited, "the third in the window is refused")
}

// The model has to KNOW this exists, or it keeps telling people to obtain an
// API key for something the platform already does.
func TestThePromptTellsTheModelTheAppCanAskAModel(t *testing.T) {
	t.Parallel()
	p := systemPrompt("blog", false, nil, "")
	// Whitespace-insensitive: the prompt is hard-wrapped, so a phrase that
	// reads as one thing can straddle a line break.
	flat := strings.Join(strings.Fields(p), " ")
	assert.Contains(t, flat, "/api/container/assistant")
	assert.Contains(t, flat, "with no API key of its own")
	assert.Contains(t, flat, `"messages"`)
	assert.Contains(t, strings.ToLower(flat), "no tools and no file access")
}

// The prompt is what the model builds from, so it must teach the spelling we
// want written into apps. It taught /v1 -- which works and always will, but is
// the legacy root -- so every app the assistant produced was built against it.
// Reported after an app came out using /v1 endpoints throughout.
func TestThePromptTeachesTheCurrentContainerAPI(t *testing.T) {
	t.Parallel()
	conns := []Connection{
		{Slug: "work-cal", Provider: "google-calendar", ProviderLabel: "Google Calendar"},
		{Slug: "issues", Provider: "mcp", ProviderLabel: "MCP server", MCP: true},
	}
	p := systemPrompt("blog", false, conns, "")

	assert.NotContains(t, p, "/v1/",
		"the prompt is where an app's URLs come from; /v1 keeps working but is not what to teach")
	for _, want := range []string{
		"/api/container/connections/work-cal/token",
		"/api/container/connections",
		"/api/container/mcp/issues/tools",
		"/api/container/mcp/issues/call",
		"/api/container/assistant",
	} {
		assert.Contains(t, p, want)
	}
}

// bothBackends builds a Manager with the metered API AND a fake Claude Max
// backend configured, so a request can pick either.
func bothBackends(t *testing.T, apiReply response) (*Manager, *fakeCompleter, *fakeClaudeRunner) {
	t.Helper()
	fc := &fakeCompleter{replies: []response{apiReply}}
	m := NewManager(fc, newFakeOps(), &recordingStore{MemoryStore: NewMemoryStore()},
		Credentials{AnthropicAPIKey: "sk-test", ClaudeCodeOAuthToken: "oauth-test"})
	runner := &fakeClaudeRunner{answerText: "from the subscription"}
	m.SetClaudeRunner(runner)
	return m, fc, runner
}

// A claude-* model routes to the subscription (Answer), an anthropic-* model to
// the metered API -- the whole point of exposing both.
func TestAskRoutesByModelBackend(t *testing.T) {
	t.Parallel()
	m, fc, runner := bothBackends(t, response{Content: []ContentBlock{{Type: blockText, Text: "from the API"}}})

	got, err := m.Ask(context.Background(), "blog", AskRequest{
		Model: "claude-opus-5", Messages: []AskMessage{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "from the subscription", got.Text)
	assert.Equal(t, "claude-opus-5", got.Model)
	assert.Equal(t, 1, runner.answerCalls, "claude-* goes to the subscription")
	assert.Empty(t, fc.calls, "and NOT to the metered API")
	assert.Equal(t, "claude-opus-5", runner.answerModel, "the backend model string is passed through")

	got, err = m.Ask(context.Background(), "blog", AskRequest{
		Model: "anthropic-sonnet-5", Messages: []AskMessage{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "from the API", got.Text)
	assert.Equal(t, "anthropic-sonnet-5", got.Model)
	require.Len(t, fc.calls, 1, "anthropic-* goes to the metered API")
	assert.Equal(t, "claude-sonnet-5", fc.calls[0].Model, "with the upstream model string")
	assert.Equal(t, 1, runner.answerCalls, "and the subscription was not called again")
}

// No model named: the default is the head of the catalog, same as the chat UI.
// The subscription registers first, so with both configured the default is claude.
func TestAskDefaultsToTheHeadOfTheCatalog(t *testing.T) {
	t.Parallel()
	m, fc, runner := bothBackends(t, response{Content: []ContentBlock{{Type: blockText, Text: "api"}}})
	got, err := m.Ask(context.Background(), "blog", AskRequest{Messages: []AskMessage{{Role: "user", Content: "hi"}}})
	require.NoError(t, err)
	assert.Equal(t, 1, runner.answerCalls, "default picks the subscription (head of the catalog)")
	assert.Empty(t, fc.calls)
	assert.Equal(t, "from the subscription", got.Text)
}

// An unconfigured or unknown model is refused with the list of what IS offered.
func TestAskRefusesAnUnavailableModel(t *testing.T) {
	t.Parallel()
	m, _, _ := askManager(t) // anthropic only
	_, err := m.Ask(context.Background(), "blog", AskRequest{
		Model: "claude-opus-5", Messages: []AskMessage{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAskInvalid)
	assert.Contains(t, err.Error(), "anthropic-", "the error names what this instance does offer")
}

// The catalog an app discovers reflects what is configured.
func TestAskModelsReflectsConfiguredBackends(t *testing.T) {
	t.Parallel()
	m, _, _ := bothBackends(t, response{})
	ids := make(map[string]bool)
	for _, o := range m.AskModels() {
		ids[o.ID] = true
	}
	assert.True(t, ids["anthropic-sonnet-5"], "the API models are offered")
	assert.True(t, ids["claude-opus-5"], "the subscription models are offered")
}

// Claude selected but no runner wired (configured in creds, SetClaudeRunner never
// called): unavailable, and NO silent fallback to the API -- unlike an
// interactive turn, an app's ask must not be quietly rerouted to a paid backend
// it did not choose.
func TestAskClaudeWithoutARunnerIsUnavailable(t *testing.T) {
	t.Parallel()
	fc := &fakeCompleter{replies: []response{{Content: []ContentBlock{{Type: blockText, Text: "api"}}}}}
	m := NewManager(fc, newFakeOps(), NewMemoryStore(), Credentials{ClaudeCodeOAuthToken: "t"})
	_, err := m.Ask(context.Background(), "blog", AskRequest{Messages: []AskMessage{{Role: "user", Content: "hi"}}})
	require.ErrorIs(t, err, ErrAskUnavailable)
	assert.Empty(t, fc.calls, "no silent fallback to the metered API")
}

// A subscription error propagates; it does NOT fall back to the API either.
func TestAskClaudeRunnerErrorPropagates(t *testing.T) {
	t.Parallel()
	m, fc, runner := bothBackends(t, response{Content: []ContentBlock{{Type: blockText, Text: "api"}}})
	runner.err = errors.New("sandbox boom")
	_, err := m.Ask(context.Background(), "blog", AskRequest{
		Model: "claude-opus-5", Messages: []AskMessage{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)
	assert.Empty(t, fc.calls, "a claude error is not retried on the API")
}

// The app's system prompt and the flattened conversation reach the subscription.
func TestAskClaudePassesSystemAndFlattenedPrompt(t *testing.T) {
	t.Parallel()
	m, _, runner := bothBackends(t, response{})
	_, err := m.Ask(context.Background(), "blog", AskRequest{
		Model: "claude-opus-5", System: "be a pirate",
		Messages: []AskMessage{{Role: "user", Content: "ahoy"}, {Role: "assistant", Content: "arr"}, {Role: "user", Content: "more"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "be a pirate", runner.answerSystem, "the APP's system prompt, not the build assistant's")
	assert.Contains(t, runner.answerPrompt, "User: ahoy")
	assert.Contains(t, runner.answerPrompt, "Assistant: arr")
	assert.Contains(t, runner.answerPrompt, "User: more")
}

// A claude answer is metered onto the app, same as an API answer.
func TestAskClaudeMetersUsageOntoTheApp(t *testing.T) {
	t.Parallel()
	st := &recordingStore{MemoryStore: NewMemoryStore()}
	m := NewManager(&fakeCompleter{}, newFakeOps(), st, Credentials{ClaudeCodeOAuthToken: "t"})
	m.SetClaudeRunner(&fakeClaudeRunner{answerText: "hi", usage: Usage{InputTokens: 7, OutputTokens: 3}})
	_, err := m.Ask(context.Background(), "blog", AskRequest{
		Model: "claude-fable-5", Messages: []AskMessage{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	require.Len(t, st.usage, 1)
	assert.Equal(t, "blog", st.usage[0].app)
	assert.Equal(t, 7, st.usage[0].usage.InputTokens)
}

// toClaudePrompt: a single message is its bare text; a conversation is labelled.
func TestToClaudePrompt(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "just this", toClaudePrompt(AskRequest{Messages: []AskMessage{{Role: "user", Content: "just this"}}}))
	multi := toClaudePrompt(AskRequest{Messages: []AskMessage{
		{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}, {Role: "user", Content: "bye"},
	}})
	assert.Contains(t, multi, "User: hi")
	assert.Contains(t, multi, "Assistant: hello")
	assert.Contains(t, multi, "User: bye")
	assert.NotContains(t, multi, "\n\n\n", "trimmed, no trailing blank block")
}

// No backend configured at all: unavailable (501), for both an empty and a named
// model -- not a 400 that reads like the app got the request wrong.
func TestAskWithNoBackendConfiguredIsUnavailable(t *testing.T) {
	t.Parallel()
	m := NewManager(&fakeCompleter{}, newFakeOps(), NewMemoryStore(), Credentials{})
	for _, model := range []string{"", "anthropic-sonnet-5"} {
		_, err := m.Ask(context.Background(), "blog", AskRequest{Model: model, Messages: []AskMessage{{Role: "user", Content: "hi"}}})
		require.ErrorIs(t, err, ErrAskUnavailable, "model=%q", model)
	}
}

func TestAvailableModelIDsWhenNothingConfigured(t *testing.T) {
	t.Parallel()
	assert.Contains(t, availableModelIDs(Credentials{}), "none")
}
