package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClaudeRunner scripts a turn's events (as the sandbox would stream them) and
// records the prompt + system prompt it was handed. A non-nil err simulates the
// subscription being unavailable.
type fakeClaudeRunner struct {
	events       []Event
	usage        Usage
	err          error
	calls        int
	prompt       string
	systemPrompt string
}

func (f *fakeClaudeRunner) RunTurn(_ context.Context, _ string, prompt, systemPrompt string, publish func(Event)) (Usage, error) {
	f.calls++
	f.prompt, f.systemPrompt = prompt, systemPrompt
	for _, ev := range f.events {
		publish(ev)
	}
	return f.usage, f.err
}

func TestClaudeBackendRunsTurnAndStores(t *testing.T) {
	t.Parallel()
	runner := &fakeClaudeRunner{
		events: []Event{
			{Type: "text", Text: "On it."},
			{Type: "tool_use", Tool: "write_file", Input: `{"path":"public/index.html"}`},
			{Type: "tool_result", Tool: "write_file", Output: "wrote public/index.html"},
			{Type: "text", Text: "Done."},
		},
		usage: Usage{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 5},
	}
	store := NewMemoryStore()
	m := NewManager(&fakeCompleter{}, newFakeOps(), store, Credentials{AnthropicAPIKey: "k", ClaudeCodeOAuthToken: "t"})
	m.SetClaudeRunner(runner)

	events := runTurn(t, m, "blog", "make a hello page")

	// The turn routed through the Claude backend (not the API completer) and the
	// hostit system prompt was passed to it.
	assert.Contains(t, runner.systemPrompt, "hostit")
	assert.Contains(t, runner.prompt, "make a hello page")

	// Subscribers saw the same event shape as an API turn: user, text, tool call,
	// its result, done.
	types := eventTypes(events)
	assert.Equal(t, "user", types[0])
	assert.Contains(t, types, "text")
	assert.Contains(t, types, "tool_use")
	assert.Contains(t, types, "tool_result")
	assert.Equal(t, "done", types[len(types)-1])

	// The transcript was reconstructed from the stream and reloads: the tool result
	// is folded onto the call that produced it.
	items, err := m.Transcript("blog")
	require.NoError(t, err)
	var sawUser, sawTool bool
	for _, it := range items {
		if it.Kind == "user" && it.Text == "make a hello page" {
			sawUser = true
		}
		if it.Kind == "tool" && it.Tool == "write_file" {
			sawTool = true
			assert.Equal(t, "wrote public/index.html", it.Output)
			assert.False(t, it.IsError)
		}
	}
	assert.True(t, sawUser, "the user message is stored")
	assert.True(t, sawTool, "the tool call and its result are stored and paired")
}

func TestDedupeToolIDsPreservesModelTag(t *testing.T) {
	t.Parallel()
	// The repair pass rebuilds messages; it must keep the per-message model tag, or
	// a later API turn (which re-saves the repaired history) wipes the badge on
	// earlier External Claude replies.
	out := dedupeToolIDs([]Message{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "hello"}}, Model: "claude-opus-5"},
	})
	require.Len(t, out, 2)
	assert.Equal(t, "claude-opus-5", out[1].Model, "the repair keeps the model tag")
}

func TestThinkingOmittedForHaiku(t *testing.T) {
	t.Parallel()
	// Haiku rejects adaptive thinking + the extended output controls; Sonnet/Opus
	// take them. The dropdown offers all three, so the request must adapt per model.
	assert.Nil(t, thinkingFor("claude-haiku-4-5-20251001"), "Haiku gets no adaptive thinking")
	assert.Nil(t, outputConfigFor("claude-haiku-4-5-20251001"), "Haiku gets no effort config")
	assert.NotNil(t, thinkingFor("claude-sonnet-5"), "Sonnet keeps adaptive thinking")
	assert.NotNil(t, thinkingFor("claude-opus-5"), "Opus keeps adaptive thinking")
	assert.NotNil(t, outputConfigFor("claude-sonnet-5"))
}

func TestAPITurnRepairsCollidingToolIDsFromExternalTurns(t *testing.T) {
	t.Parallel()
	// Two prior External Claude turns each reconstructed the id "call_1" (the old
	// per-turn counter), so the stored transcript has a duplicate tool id. Switching
	// to an API model must NOT send that duplicate to the Messages API, which rejects
	// it ("multiple tool_result blocks with id call_1"). The repair re-mints ids.
	store := NewMemoryStore()
	require.NoError(t, store.Save("blog", []Message{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "one"}}},
		{Role: "assistant", Content: []ContentBlock{{Type: "tool_use", ID: "call_1", Name: "read_file", Input: json.RawMessage(`{}`)}}},
		{Role: "user", Content: []ContentBlock{{Type: "tool_result", ToolUseID: "call_1", Content: "a"}}},
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "two"}}},
		{Role: "assistant", Content: []ContentBlock{{Type: "tool_use", ID: "call_1", Name: "read_file", Input: json.RawMessage(`{}`)}}},
		{Role: "user", Content: []ContentBlock{{Type: "tool_result", ToolUseID: "call_1", Content: "b"}}},
	}))
	fc := &fakeCompleter{replies: []response{{StopReason: "end_turn", Content: []ContentBlock{{Type: "text", Text: "done"}}}}}
	m := NewManager(fc, newFakeOps(), store, Credentials{AnthropicAPIKey: "k", ClaudeCodeOAuthToken: "t"})

	runTurn(t, m, "blog", "three") // no claude runner set: an API turn

	require.NotEmpty(t, fc.calls)
	uses, results := map[string]int{}, map[string]int{}
	for _, msg := range fc.calls[0].Messages {
		for _, b := range msg.Content {
			if b.Type == "tool_use" {
				uses[b.ID]++
			}
			if b.Type == "tool_result" {
				results[b.ToolUseID]++
			}
		}
	}
	for id, c := range uses {
		assert.Equalf(t, 1, c, "tool_use id %q must be unique in the API request", id)
	}
	for id, c := range results {
		assert.Equalf(t, 1, c, "tool_result for %q must appear once", id)
	}
	assert.GreaterOrEqual(t, len(uses), 2, "both tool calls survived the repair")
}

func TestClaudeBackendPairsParallelToolResults(t *testing.T) {
	t.Parallel()
	// Claude may batch parallel calls: two tool_use blocks in one message, then two
	// tool_results together. Each result must pair (FIFO) to its call and fold onto
	// it -- an unmatched call is what shows as a tool spinning forever in the chat.
	runner := &fakeClaudeRunner{events: []Event{
		{Type: "tool_use", Tool: "read_file", Input: `{"path":"a"}`},
		{Type: "tool_use", Tool: "read_file", Input: `{"path":"b"}`},
		{Type: "tool_result", Output: "content-a"},
		{Type: "tool_result", Output: "content-b"},
		{Type: "text", Text: "done"},
	}}
	m := NewManager(&fakeCompleter{}, newFakeOps(), NewMemoryStore(), Credentials{AnthropicAPIKey: "k", ClaudeCodeOAuthToken: "t"})
	m.SetClaudeRunner(runner)
	runTurn(t, m, "blog", "read both files")

	items, err := m.Transcript("blog")
	require.NoError(t, err)
	var tools []Item
	for _, it := range items {
		if it.Kind == "tool" {
			tools = append(tools, it)
		}
	}
	require.Len(t, tools, 2, "both tool calls are stored")
	assert.Equal(t, "content-a", tools[0].Output, "first call folds its own result")
	assert.Equal(t, "content-b", tools[1].Output, "second call folds its own result (none left unmatched)")
}

func TestClaudeBackendFallsBackToAPIWhenSubscriptionFails(t *testing.T) {
	t.Parallel()
	// External Claude is selected but the subscription is unavailable; the turn must
	// not die -- it falls back to the metered API so the assistant keeps working.
	runner := &fakeClaudeRunner{err: errors.New("subscription expired")}
	fc := &fakeCompleter{replies: []response{
		{StopReason: "end_turn", Content: []ContentBlock{{Type: "text", Text: "Handled by the API instead."}}},
	}}
	store := NewMemoryStore()
	m := NewManager(fc, newFakeOps(), store, Credentials{AnthropicAPIKey: "k", ClaudeCodeOAuthToken: "t"})
	m.SetClaudeRunner(runner)

	events := runTurn(t, m, "blog", "do the thing") // empty mode defaults to External Claude here

	// The subscription was tried, a notice was shown, and the API produced the reply.
	assert.Equal(t, 1, runner.calls, "the external backend was attempted")
	types := eventTypes(events)
	assert.Contains(t, types, "notice", "the user is told about the fallback")
	require.Len(t, fc.calls, 1, "the API backend ran the turn after the fallback")
	assert.Equal(t, "done", types[len(types)-1])

	// The transcript has the user message exactly once (no duplication across the
	// external attempt and the API fallback) and the API's answer.
	items, err := m.Transcript("blog")
	require.NoError(t, err)
	userCount := 0
	var sawAPI bool
	for _, it := range items {
		if it.Kind == "user" {
			userCount++
		}
		if it.Kind == "text" && it.Text == "Handled by the API instead." {
			sawAPI = true
		}
	}
	assert.Equal(t, 1, userCount, "the user message is stored once")
	assert.True(t, sawAPI, "the API fallback answer is stored")
}

func TestClaudeBackendReplaysHistoryForContinuity(t *testing.T) {
	t.Parallel()
	runner := &fakeClaudeRunner{events: []Event{{Type: "text", Text: "ok"}}}
	m := NewManager(&fakeCompleter{}, newFakeOps(), NewMemoryStore(), Credentials{AnthropicAPIKey: "k", ClaudeCodeOAuthToken: "t"})
	m.SetClaudeRunner(runner)

	// First turn: no prior context, so the prompt is just the message.
	runTurn(t, m, "blog", "first message")
	assert.Equal(t, "first message", strings.TrimSpace(runner.prompt))

	// Second turn: the stored transcript is replayed into the prompt, since the
	// sandbox is stateless between turns.
	runTurn(t, m, "blog", "second message")
	assert.Contains(t, runner.prompt, "first message", "prior turn is replayed for continuity")
	assert.Contains(t, runner.prompt, "second message")
}
