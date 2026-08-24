package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/assistant"
	"heckel.io/hostit/store"
)

// An app asking the model a question over its own socket. This is what lets an
// app be an AI app without holding an API key -- which is the whole point: a key
// in an app's environment is a key nothing can rotate and nobody can meter.
func TestAnAppAsksTheAssistantAQuestion(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID, UID: 1234}))
	s.usernameForUID = func(uid int) (string, error) { return "dash", nil }
	fake := stubAssistant(t, s, "Arrr, ahoy!")

	rr := socketRequestBody(t, s, "POST", "/api/container/assistant", `{"prompt":"Say hello","system":"You are a pirate."}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var got apiAskResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	assert.Equal(t, "Arrr, ahoy!", got.Text)
	assert.NotEmpty(t, got.Model, "the app is told which model answered")
	assert.Positive(t, got.Usage.InputTokens+got.Usage.OutputTokens, "and what it cost, so it can stay within a budget")

	require.Len(t, fake.calls, 1)
	assert.Empty(t, fake.calls[0].Tools, "no tools reach an app's own request")
	require.Len(t, fake.calls[0].System, 1)
	assert.Contains(t, string(fake.calls[0].System[0]), "pirate")
}

// A conversation, so an app can be a chat.
func TestAnAppSendsAWholeConversation(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID, UID: 1234}))
	s.usernameForUID = func(uid int) (string, error) { return "dash", nil }
	fake := stubAssistant(t, s, "aye")

	body := `{"messages":[{"role":"user","content":"one"},{"role":"assistant","content":"two"},{"role":"user","content":"three"}]}`
	rr := socketRequestBody(t, s, "POST", "/api/container/assistant", body)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Len(t, fake.calls, 1)
	assert.Len(t, fake.calls[0].Messages, 3)
}

// The two forms are alternatives. Accepting both at once would silently drop
// one, and an app would be debugging a prompt that never went anywhere.
func TestPromptAndMessagesTogetherAreRefused(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID, UID: 1234}))
	s.usernameForUID = func(uid int) (string, error) { return "dash", nil }
	stubAssistant(t, s, "ok")

	rr := socketRequestBody(t, s, "POST", "/api/container/assistant",
		`{"prompt":"hi","messages":[{"role":"user","content":"hi"}]}`)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "not both")
}

func TestABadAskIsAFourHundredNotAFiveHundred(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID, UID: 1234}))
	s.usernameForUID = func(uid int) (string, error) { return "dash", nil }
	stubAssistant(t, s, "ok")

	for _, body := range []string{
		`{}`,
		`{"messages":[{"role":"system","content":"x"}]}`,
		`{"prompt":"hi","model":"gpt-9"}`,
	} {
		rr := socketRequestBody(t, s, "POST", "/api/container/assistant", body)
		assert.Equal(t, http.StatusBadRequest, rr.Code, body)
	}
}

// The endpoint is the APP's, resolved by the socket's peer credentials -- so it
// is not reachable from the public web at all, the same as every other
// container endpoint.
func TestTheAskEndpointIsNotOnThePublicAPI(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	token, _, err := s.users.CreateToken(u.ID, "laptop")
	require.NoError(t, err)

	for _, path := range []string{"/api/container/assistant", "/v1/assistant"} {
		rr := request(t, s.API(), "POST", path, `{"prompt":"hi"}`, token)
		assert.Equal(t, http.StatusNotFound, rr.Code, path)
	}
}

// fakeAnthropic is a stand-in Messages API. Using the REAL client against it
// exercises the whole path -- the JSON hostit sends, the JSON it parses, the
// usage it reads -- where a stubbed interface would only prove the handler
// calls something.
type fakeAnthropic struct {
	*httptest.Server
	reply string
	calls []recordedAsk
}

type recordedAsk struct {
	Model     string            `json:"model"`
	MaxTokens int               `json:"max_tokens"`
	System    []json.RawMessage `json:"system"`
	Messages  []struct {
		Role    string            `json:"role"`
		Content []json.RawMessage `json:"content"`
	} `json:"messages"`
	Tools []json.RawMessage `json:"tools"`
}

func stubAssistant(t *testing.T, s *Server, reply string) *fakeAnthropic {
	t.Helper()
	f := &fakeAnthropic{reply: reply}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got recordedAsk
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &got))
		f.calls = append(f.calls, got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","role":"assistant","stop_reason":"end_turn",
			"content":[{"type":"text","text":`+quote(f.reply)+`}],
			"usage":{"input_tokens":11,"output_tokens":5}}`)
	}))
	t.Cleanup(f.Server.Close)
	s.assistant = assistant.NewManager(assistant.NewClientAt("sk-test", f.URL), nil,
		assistant.NewMemoryStore(), assistant.Credentials{AnthropicAPIKey: "sk-test"})
	return f
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// An app discovers which models it may pick, filtered to what this instance has
// configured, with the default it gets when it names none.
func TestAnAppDiscoversTheAssistantModels(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID, UID: 1234}))
	s.usernameForUID = func(uid int) (string, error) { return "dash", nil }
	stubAssistant(t, s, "hi") // configures the metered (anthropic) backend

	rr := socketRequestBody(t, s, "GET", "/api/container/assistant/models", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var got apiAssistantModels
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	require.NotEmpty(t, got.Models, "an app must be able to see its choices")
	assert.NotEmpty(t, got.Default, "and which one it gets by default")

	var sawAnthropic bool
	for _, m := range got.Models {
		if m.ID == "anthropic-sonnet-5" {
			sawAnthropic = true
			assert.Equal(t, "anthropic", m.Backend)
		}
		assert.Empty(t, m.Backend == "" && m.ID != "", "every model names its backend")
	}
	assert.True(t, sawAnthropic, "the configured metered backend's models are offered")
}

// stubClaudeRunner is a control-side fake assistant.ClaudeRunner, so the handler
// can drive the subscription (askClaude) path without a real sandbox.
type stubClaudeRunner struct {
	text string
	err  error
}

func (r *stubClaudeRunner) RunTurn(context.Context, string, string, string, []assistant.Attachment, func(assistant.Event)) (assistant.Usage, error) {
	return assistant.Usage{}, r.err
}

func (r *stubClaudeRunner) Answer(context.Context, string, string, string, string) (string, assistant.Usage, error) {
	return r.text, assistant.Usage{InputTokens: 3, OutputTokens: 2}, r.err
}

func claudeApp(t *testing.T, s *Server, runner assistant.ClaudeRunner) {
	t.Helper()
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID, UID: 1234}))
	s.usernameForUID = func(uid int) (string, error) { return "dash", nil }
	m := assistant.NewManager(assistant.NewClient("k"), nil, assistant.NewMemoryStore(), assistant.Credentials{ClaudeCodeOAuthToken: "t"})
	m.SetClaudeRunner(runner)
	s.assistant = m
}

// The discovery endpoint's Default is the head of the catalog, and only
// configured backends surface (here: metered API only, so no claude-* leaks).
func TestModelsEndpointDefaultAndFiltering(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID, UID: 1234}))
	s.usernameForUID = func(uid int) (string, error) { return "dash", nil }
	stubAssistant(t, s, "hi") // anthropic only

	rr := socketRequestBody(t, s, "GET", "/api/container/assistant/models", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var got apiAssistantModels
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	assert.Equal(t, "anthropic-opus-5", got.Default, "default is the head of the catalog")
	for _, m := range got.Models {
		assert.NotEqual(t, "claude", m.Backend, "an unconfigured backend must not surface")
	}
}

// No backend configured: the endpoint is a 200 with an EMPTY list and default,
// not an error -- so an app can cleanly detect "nothing available".
func TestModelsEndpointEmptyWhenUnconfigured(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	u := newActiveTestUser(t, s, "owner@example.com")
	require.NoError(t, s.apps.Store().AddApp(&store.App{ID: "a1", Name: "dash", Port: 10000, Host: store.HostLocal, OwnerID: u.ID, UID: 1234}))
	s.usernameForUID = func(uid int) (string, error) { return "dash", nil }
	// s.assistant left nil

	rr := socketRequestBody(t, s, "GET", "/api/container/assistant/models", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var got apiAssistantModels
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	assert.Empty(t, got.Models)
	assert.Empty(t, got.Default)
	assert.NotNil(t, got.Models, "an empty list, never null")
}

// The subscription path, end to end through the handler: a claude-* model answers
// via the runner, and a runner failure is a 502 carrying NO backend internals.
func TestSubscriptionHandlerPathAndError(t *testing.T) {
	t.Parallel()
	t.Run("answers via the subscription", func(t *testing.T) {
		s := newTestServer(t)
		claudeApp(t, s, &stubClaudeRunner{text: "Arrr"})
		rr := socketRequestBody(t, s, "POST", "/api/container/assistant", `{"prompt":"ahoy","model":"claude-opus-5"}`)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var got apiAskResponse
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
		assert.Equal(t, "Arrr", got.Text)
		assert.Equal(t, "claude-opus-5", got.Model)
	})
	t.Run("a backend failure is a 502 with no internals", func(t *testing.T) {
		s := newTestServer(t)
		claudeApp(t, s, &stubClaudeRunner{err: errors.New("stderr: /var/lib/hostit/secret leaked")})
		rr := socketRequestBody(t, s, "POST", "/api/container/assistant", `{"prompt":"ahoy","model":"claude-opus-5"}`)
		assert.Equal(t, http.StatusBadGateway, rr.Code)
		assert.NotContains(t, rr.Body.String(), "/var/lib/hostit", "the sandbox's own error must not reach the tenant")
	})
}

// assistantOwnerKey never returns empty -- an empty key is reserveRun's UNLIMITED
// admin exemption, and a legacy app with no owner must not inherit it.
func TestAssistantOwnerKeyNeverEmpty(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "u1", assistantOwnerKey(&store.App{ID: "a1", OwnerID: "u1"}), "the owner when it has one")
	key := assistantOwnerKey(&store.App{ID: "a1", OwnerID: ""})
	assert.NotEmpty(t, key, "an ownerless app still gets a bounded key")
	assert.Contains(t, key, "a1")
}
