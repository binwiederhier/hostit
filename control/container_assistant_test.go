package control

import (
	"encoding/json"
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
	assert.Empty(t, fake.calls[0].Tools, "inference only: no tools reach an app's own request")
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
