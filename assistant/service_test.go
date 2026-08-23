package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCompleter returns scripted replies in order and records the requests it saw
type fakeCompleter struct {
	replies []response
	calls   []request
}

func (f *fakeCompleter) complete(_ context.Context, req request) (*response, error) {
	f.calls = append(f.calls, req)
	if len(f.calls) > len(f.replies) {
		return nil, fmt.Errorf("no scripted reply for call %d", len(f.calls))
	}
	r := f.replies[len(f.calls)-1]
	return &r, nil
}

// fakeOps is an in-memory stand-in for the app's REST operations
type fakeOps struct {
	files    map[string]string
	writes   []string
	execFn   func(command string) ExecResult
	archived bool
	conns    []Connection
	// The MCP half: what this app was granted, what a call returns, and what
	// was actually asked for.
	mcpTools  []MCPTool
	mcpResult string
	mcpErr    string
	mcpCalls  []fakeMCPCall
}

type fakeMCPCall struct {
	connection string
	tool       string
	args       map[string]any
}

func (f *fakeOps) MCPTools(_ string) []MCPTool { return f.mcpTools }

func (f *fakeOps) CallMCPTool(_, connection, tool string, args map[string]any) (string, bool, error) {
	f.mcpCalls = append(f.mcpCalls, fakeMCPCall{connection: connection, tool: tool, args: args})
	if f.mcpErr != "" {
		return f.mcpErr, true, nil
	}
	return f.mcpResult, false, nil
}

func newFakeOps() *fakeOps { return &fakeOps{files: map[string]string{}} }

func (f *fakeOps) Archived(_ string) bool { return f.archived }

func (f *fakeOps) Connections(_ string) []Connection { return f.conns }

func (f *fakeOps) ListFiles(_, _ string) (string, error) { return "hostit.yml\npublic/", nil }
func (f *fakeOps) ReadFile(_, path string) (string, error) {
	v, ok := f.files[path]
	if !ok {
		return "", fmt.Errorf("no such file: %s", path)
	}
	return v, nil
}
func (f *fakeOps) WriteFile(_, path, content string) error {
	f.files[path] = content
	f.writes = append(f.writes, path)
	return nil
}
func (f *fakeOps) Exec(_, command string, _ int) (ExecResult, error) {
	if f.execFn != nil {
		return f.execFn(command), nil
	}
	return ExecResult{Output: "ok", ExitCode: 0}, nil
}
func (f *fakeOps) Logs(_ string, _ int) (string, error) { return "a log line", nil }
func (f *fakeOps) Deploy(_ string) (string, error)      { return "deployed", nil }
func (f *fakeOps) Snapshot(_, _ string) (string, error) { return "saved snapshot snap-1", nil }
func (f *fakeOps) ListSnapshots(_ string) (string, error) {
	return "snap-1  2026-08-07 12:00  manual", nil
}
func (f *fakeOps) Rollback(_, id string) (string, error) { return "rolled back to " + id, nil }

func toolUse(id, name, input string) ContentBlock {
	return ContentBlock{Type: "tool_use", ID: id, Name: name, Input: json.RawMessage(input)}
}

func eventTypes(events []Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Type
	}
	return out
}

// drainUntilDone reads events from a subscription until the turn ends
func drainUntilDone(t *testing.T, ch <-chan Event) []Event {
	t.Helper()
	var events []Event
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return events
			}
			events = append(events, ev)
			if ev.Type == "done" || ev.Type == "error" || ev.Type == "paused" {
				return events
			}
		case <-deadline:
			t.Fatal("timed out waiting for the turn to finish")
			return events
		}
	}
}

// runTurn subscribes, sends a message, and returns the events of the whole turn,
// waiting for the background run to fully finish.
func runTurn(t *testing.T, m *Manager, app, text string) []Event {
	t.Helper()
	ch, cancel, err := m.Subscribe(app)
	require.NoError(t, err)
	defer cancel()
	require.NoError(t, m.Send(app, "", text, ""))
	events := drainUntilDone(t, ch)
	require.Eventually(t, func() bool { return !m.Running(app) }, 2*time.Second, 5*time.Millisecond)
	return events
}

func TestRunExecutesToolThenFinishes(t *testing.T) {
	t.Parallel()
	ops := newFakeOps()
	fc := &fakeCompleter{replies: []response{
		{StopReason: "tool_use", Content: []ContentBlock{
			{Type: "thinking", Thinking: "I'll create the page", Signature: "sig-1"},
			{Type: "text", Text: "Creating the page"},
			toolUse("tu_1", "write_file", `{"path":"public/index.html","content":"<h1>hi</h1>"}`),
		}},
		{StopReason: "end_turn", Content: []ContentBlock{{Type: "text", Text: "Done."}}},
	}}
	m := NewManager(fc, ops, NewMemoryStore(), Credentials{AnthropicAPIKey: "k"})

	events := runTurn(t, m, "blog", "make a hello page")

	// The tool actually ran against the app
	assert.Equal(t, "<h1>hi</h1>", ops.files["public/index.html"])
	assert.Equal(t, []string{"public/index.html"}, ops.writes)

	// Subscribers saw the loop unfold: the echoed user message, thinking, text, the
	// tool call, its result, done.
	types := eventTypes(events)
	assert.Equal(t, "user", types[0])
	assert.Contains(t, types, "thinking")
	assert.Contains(t, types, "text")
	assert.Contains(t, types, "tool_use")
	assert.Contains(t, types, "tool_result")
	assert.Equal(t, "done", types[len(types)-1])

	// The follow-up turn fed the tool results back, but did NOT echo the thinking
	// block (adaptive thinking blocks are shown, not replayed).
	require.Len(t, fc.calls, 2)
	assert.False(t, hasThinkingSignature(fc.calls[1].Messages, "sig-1"), "thinking must not be echoed back to the API")
	assert.True(t, hasToolResult(fc.calls[1].Messages, "tu_1"), "the tool result must be fed back")
}

func TestRollbackRunsDirectly(t *testing.T) {
	t.Parallel()
	// Rollback is reversible (a safety snapshot is taken first), so it runs without
	// a confirmation step.
	fc := &fakeCompleter{replies: []response{
		{StopReason: "tool_use", Content: []ContentBlock{toolUse("tu_1", "rollback", `{"id":"snap-1"}`)}},
		{StopReason: "end_turn", Content: []ContentBlock{{Type: "text", Text: "Rolled back."}}},
	}}
	m := NewManager(fc, newFakeOps(), NewMemoryStore(), Credentials{AnthropicAPIKey: "k"})

	events := runTurn(t, m, "blog", "roll back to snap-1")

	assert.NotContains(t, eventTypes(events), "confirm", "no confirmation step")
	var result *Event
	for i := range events {
		if events[i].Type == "tool_result" {
			result = &events[i]
		}
	}
	require.NotNil(t, result)
	assert.False(t, result.IsError)
	assert.Contains(t, result.Output, "rolled back to snap-1")
	assert.Equal(t, "done", events[len(events)-1].Type)
}

func TestToolErrorIsReportedButLoopContinues(t *testing.T) {
	t.Parallel()
	ops := newFakeOps() // ReadFile of a missing file returns an error
	fc := &fakeCompleter{replies: []response{
		{StopReason: "tool_use", Content: []ContentBlock{toolUse("tu_1", "read_file", `{"path":"missing.txt"}`)}},
		{StopReason: "end_turn", Content: []ContentBlock{{Type: "text", Text: "That file does not exist yet."}}},
	}}
	m := NewManager(fc, ops, NewMemoryStore(), Credentials{AnthropicAPIKey: "k"})

	events := runTurn(t, m, "blog", "read missing.txt")

	var result *Event
	for i := range events {
		if events[i].Type == "tool_result" {
			result = &events[i]
		}
	}
	require.NotNil(t, result)
	assert.True(t, result.IsError, "the tool result must be marked an error")
	assert.Equal(t, "done", events[len(events)-1].Type, "a failing tool is the model's problem, not a run failure")
	assert.True(t, toolResultIsError(fc.calls[1].Messages, "tu_1"), "the error must reach the model as is_error")
}

func TestRunStopsAtStepLimit(t *testing.T) {
	t.Parallel()
	// A model that never stops asking for tools must not loop forever
	fc := &endlessToolCompleter{}
	m := NewManager(fc, newFakeOps(), NewMemoryStore(), Credentials{AnthropicAPIKey: "k"})

	// Hitting the step limit is surfaced as a friendly "paused" notice (say
	// "continue" to resume), not an error: nothing actually failed.
	events := runTurn(t, m, "blog", "go")
	last := events[len(events)-1]
	assert.Equal(t, "paused", last.Type)
	assert.Equal(t, maxIterationsNotice, last.Text)
	assert.Equal(t, maxIterations, fc.calls)
}

func TestTranscriptPersistsAcrossRuns(t *testing.T) {
	t.Parallel()
	ops := newFakeOps()
	fc := &fakeCompleter{replies: []response{
		{StopReason: "end_turn", Content: []ContentBlock{{Type: "text", Text: "first reply"}}},
		{StopReason: "end_turn", Content: []ContentBlock{{Type: "text", Text: "second reply"}}},
		{StopReason: "end_turn", Content: []ContentBlock{{Type: "text", Text: "third reply"}}},
	}}
	m := NewManager(fc, ops, NewMemoryStore(), Credentials{AnthropicAPIKey: "k"})

	runTurn(t, m, "blog", "hello")
	runTurn(t, m, "blog", "again")

	// The second call must carry the whole prior exchange: user, assistant, user
	msgs := fc.calls[1].Messages
	require.GreaterOrEqual(t, len(msgs), 3)
	assert.Equal(t, "hello", msgs[0].Content[0].Text)
	assert.Equal(t, "assistant", msgs[1].Role)
	assert.Equal(t, "again", msgs[len(msgs)-1].Content[0].Text)

	// Reset forgets it
	require.NoError(t, m.Reset("blog"))
	runTurn(t, m, "blog", "fresh")
	assert.Len(t, fc.calls[2].Messages, 1, "after reset only the new message remains")
}

func TestTranscriptIsPersistedAndRendered(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	ops := newFakeOps()
	fc := &fakeCompleter{replies: []response{
		{StopReason: "tool_use", Content: []ContentBlock{
			{Type: "text", Text: "Writing the page"},
			toolUse("tu_1", "write_file", `{"path":"public/index.html","content":"hi"}`),
		}},
		{StopReason: "end_turn", Content: []ContentBlock{{Type: "text", Text: "Done."}}},
	}}
	runTurn(t, NewManager(fc, ops, store, Credentials{AnthropicAPIKey: "k"}), "blog", "make a page")

	// A different manager on the same store recovers the conversation as display
	// items -- what a reload or another device sees.
	items, err := NewManager(&fakeCompleter{}, ops, store, Credentials{AnthropicAPIKey: "k"}).Transcript("blog")
	require.NoError(t, err)
	kinds := make([]string, len(items))
	var tool Item
	for i, it := range items {
		kinds[i] = it.Kind
		if it.Kind == "tool" {
			tool = it
		}
	}
	assert.Equal(t, []string{"user", "text", "tool", "text"}, kinds)
	assert.Equal(t, "make a page", items[0].Text)
	assert.Equal(t, "write_file", tool.Tool)
	assert.Contains(t, tool.Output, "wrote 2 bytes", "the tool result must be folded onto its call")

	// Reset clears the stored conversation
	m := NewManager(&fakeCompleter{}, ops, store, Credentials{AnthropicAPIKey: "k"})
	require.NoError(t, m.Reset("blog"))
	items, err = m.Transcript("blog")
	require.NoError(t, err)
	assert.Empty(t, items)
}

func TestRunReportsAPIError(t *testing.T) {
	t.Parallel()
	fc := &fakeCompleter{} // no replies -> complete returns an error
	m := NewManager(fc, newFakeOps(), NewMemoryStore(), Credentials{AnthropicAPIKey: "k"})

	events := runTurn(t, m, "blog", "hi")
	require.NotEmpty(t, events)
	assert.Equal(t, "error", events[len(events)-1].Type)
}

func TestBroadcastsToEverySubscriber(t *testing.T) {
	t.Parallel()
	fc := &fakeCompleter{replies: []response{
		{StopReason: "end_turn", Content: []ContentBlock{{Type: "text", Text: "broadcast"}}, Usage: usage{OutputTokens: 7}},
	}}
	m := NewManager(fc, newFakeOps(), NewMemoryStore(), Credentials{AnthropicAPIKey: "k"})

	// Two devices watching the same app both see the same stream.
	ch1, c1, err1 := m.Subscribe("blog")
	require.NoError(t, err1)
	defer c1()
	ch2, c2, err2 := m.Subscribe("blog")
	require.NoError(t, err2)
	defer c2()
	require.NoError(t, m.Send("blog", "", "hi", ""))

	got1 := eventTypes(drainUntilDone(t, ch1))
	got2 := eventTypes(drainUntilDone(t, ch2))
	assert.Equal(t, got1, got2)
	// "model" announces which model is answering; "usage" reports the turn's tokens
	// (as a live activity signal) before the reply text streams.
	assert.Equal(t, []string{"user", "model", "usage", "text", "done"}, got1)
}

func TestPublishesTokenUsage(t *testing.T) {
	t.Parallel()
	fc := &fakeCompleter{replies: []response{
		{StopReason: "end_turn", Content: []ContentBlock{{Type: "text", Text: "hi"}}, Usage: usage{InputTokens: 100, OutputTokens: 42}},
	}}
	m := NewManager(fc, newFakeOps(), NewMemoryStore(), Credentials{AnthropicAPIKey: "k"})
	ch, cancel, err := m.Subscribe("blog")
	require.NoError(t, err)
	defer cancel()
	require.NoError(t, m.Send("blog", "", "hi", ""))

	var got *Event
	for _, ev := range drainUntilDone(t, ch) {
		if ev.Type == "usage" {
			e := ev
			got = &e
			break
		}
	}
	require.NotNil(t, got, "a usage event must be published so the UI can show a token count")
	require.NotNil(t, got.Usage)
	assert.Equal(t, 42, got.Usage.OutputTokens)
	assert.Equal(t, 100, got.Usage.InputTokens)
}

// The web UI reads snake_case usage keys; a struct without json tags would serialize
// as InputTokens/OutputTokens and the counter would silently read undefined.
func TestUsageEventSerializesSnakeCase(t *testing.T) {
	t.Parallel()
	b, err := json.Marshal(Event{Type: "usage", Usage: &Usage{InputTokens: 100, OutputTokens: 42}})
	require.NoError(t, err)
	assert.Contains(t, string(b), `"output_tokens":42`)
	assert.Contains(t, string(b), `"input_tokens":100`)
}

func TestSecondSenderIsRejectedWhileBusy(t *testing.T) {
	t.Parallel()
	// A model call that blocks until released keeps the turn "running".
	g := &gateCompleter{entered: make(chan struct{}, 1), release: make(chan struct{})}
	m := NewManager(g, newFakeOps(), NewMemoryStore(), Credentials{AnthropicAPIKey: "k"})

	require.NoError(t, m.Send("blog", "", "one", ""))
	<-g.entered // the run is now mid-call, so the app is busy
	assert.True(t, m.Running("blog"))
	assert.ErrorIs(t, m.Send("blog", "", "two", ""), ErrBusy, "a second sender must not clobber the first")

	close(g.release)
	require.Eventually(t, func() bool { return !m.Running("blog") }, 2*time.Second, 5*time.Millisecond)
}

func TestRunContinuesWithoutASubscriber(t *testing.T) {
	t.Parallel()
	// The run is server-owned: it finishes and persists even with nobody watching
	// (i.e. the sender navigated away).
	store := NewMemoryStore()
	fc := &fakeCompleter{replies: []response{
		{StopReason: "end_turn", Content: []ContentBlock{{Type: "text", Text: "done anyway"}}},
	}}
	m := NewManager(fc, newFakeOps(), store, Credentials{AnthropicAPIKey: "k"})

	require.NoError(t, m.Send("blog", "", "hello", ""))
	require.Eventually(t, func() bool { return !m.Running("blog") }, 3*time.Second, 10*time.Millisecond)

	items, err := m.Transcript("blog")
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, "user", items[0].Kind)
	assert.Equal(t, "hello", items[0].Text)
	assert.Equal(t, "text", items[1].Kind)
	assert.Equal(t, "done anyway", items[1].Text)
}

func TestReserveRunCapsConcurrentTurnsPerUser(t *testing.T) {
	t.Parallel()
	g := &gateCompleter{entered: make(chan struct{}, 10), release: make(chan struct{})}
	m := NewManager(g, newFakeOps(), NewMemoryStore(), Credentials{AnthropicAPIKey: "k"}) // maxRunsPerUser = 3

	for i := 1; i <= 3; i++ {
		require.NoError(t, m.Send(fmt.Sprintf("app%d", i), "u1", "go", ""))
	}
	for i := 0; i < 3; i++ {
		<-g.entered // all three are now running
	}
	// A fourth concurrent turn for the same user is refused, across their apps.
	assert.ErrorIs(t, m.Send("app4", "u1", "go", ""), ErrTooManyRuns)
	// A different user is unaffected.
	require.NoError(t, m.Send("app5", "u2", "go", ""))
	// The global admin token (empty user) is never limited.
	require.NoError(t, m.Send("app6", "", "go", ""))

	close(g.release)
	require.Eventually(t, func() bool { return !m.Running("app1") }, 2*time.Second, 5*time.Millisecond)
}

func TestReserveRunCapsTurnsPerHour(t *testing.T) {
	t.Parallel()
	fc := &fakeCompleter{replies: []response{
		{StopReason: "end_turn", Content: []ContentBlock{{Type: "text", Text: "a"}}},
		{StopReason: "end_turn", Content: []ContentBlock{{Type: "text", Text: "b"}}},
	}}
	m := NewManager(fc, newFakeOps(), NewMemoryStore(), Credentials{AnthropicAPIKey: "k"})
	m.maxRunsPerHour = 2

	// Two turns finish, but their starts still count against the hourly window.
	for i := 0; i < 2; i++ {
		require.NoError(t, m.Send("blog", "u1", "go", ""))
		require.Eventually(t, func() bool { return !m.Running("blog") }, 2*time.Second, 5*time.Millisecond)
	}
	assert.ErrorIs(t, m.Send("blog", "u1", "go", ""), ErrRateLimited)
}

func TestRecentHistoryWindowsToLastTurns(t *testing.T) {
	t.Parallel()
	// Build 5 human turns, each: user text, assistant tool_use, user tool_result.
	var history []Message
	for i := 0; i < 5; i++ {
		history = append(history,
			Message{Role: "user", Content: []ContentBlock{{Type: "text", Text: fmt.Sprintf("turn %d", i)}}},
			Message{Role: "assistant", Content: []ContentBlock{toolUse("t", "list_files", `{}`)}},
			Message{Role: "user", Content: []ContentBlock{{Type: "tool_result", ToolUseID: "t", Content: "ok"}}},
		)
	}
	windowed := recentHistory(history, 2)

	// Starts on a human message (valid ordering) and keeps exactly the last 2 turns.
	require.Equal(t, "user", windowed[0].Role)
	assert.Equal(t, "turn 3", windowed[0].Content[0].Text)
	humanTurns := 0
	for _, msg := range windowed {
		if msg.Role == "user" && hasTextBlock(msg) {
			humanTurns++
		}
	}
	assert.Equal(t, 2, humanTurns)
	// A short history is returned whole.
	assert.Len(t, recentHistory(history, 99), len(history))
}

func TestSubscriberCap(t *testing.T) {
	t.Parallel()
	m := NewManager(&fakeCompleter{}, newFakeOps(), NewMemoryStore(), Credentials{AnthropicAPIKey: "k"})
	var cancels []func()
	for i := 0; i < maxSubsPerApp; i++ {
		_, cancel, err := m.Subscribe("blog")
		require.NoError(t, err)
		cancels = append(cancels, cancel)
	}
	_, _, err := m.Subscribe("blog")
	assert.ErrorIs(t, err, ErrTooManySubscribers, "past the cap, new watchers are refused")

	// Freeing one makes room again.
	cancels[0]()
	_, cancel, err := m.Subscribe("blog")
	require.NoError(t, err)
	cancel()
	for _, c := range cancels[1:] {
		c()
	}
}

func TestResetRefusedWhileRunning(t *testing.T) {
	t.Parallel()
	g := &gateCompleter{entered: make(chan struct{}, 1), release: make(chan struct{})}
	m := NewManager(g, newFakeOps(), NewMemoryStore(), Credentials{AnthropicAPIKey: "k"})

	require.NoError(t, m.Send("blog", "", "go", ""))
	<-g.entered
	assert.ErrorIs(t, m.Reset("blog"), ErrBusy, "Reset must not delete a transcript mid-run")

	close(g.release)
	require.Eventually(t, func() bool { return !m.Running("blog") }, 2*time.Second, 5*time.Millisecond)
	assert.NoError(t, m.Reset("blog"), "once idle, Reset works")
}

func TestStopCancelsRunningTurn(t *testing.T) {
	t.Parallel()
	g := &ctxGateCompleter{entered: make(chan struct{}, 1)}
	m := NewManager(g, newFakeOps(), NewMemoryStore(), Credentials{AnthropicAPIKey: "k"})

	ch, cancel, err := m.Subscribe("blog")
	require.NoError(t, err)
	defer cancel()
	require.NoError(t, m.Send("blog", "", "go", ""))
	<-g.entered // the run is now blocked inside the model call
	require.True(t, m.Running("blog"))

	require.True(t, m.Stop("blog"), "Stop reports it cancelled a running turn")

	events := drainUntilDone(t, ch)
	require.Eventually(t, func() bool { return !m.Running("blog") }, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, "done", events[len(events)-1].Type, "a stopped turn ends cleanly, not as an error")
	assert.False(t, m.Stop("blog"), "Stop on an idle app reports nothing to stop")
}

// ctxGateCompleter blocks inside the model call until its context is cancelled,
// so a test can catch a running turn and then Stop it
type ctxGateCompleter struct{ entered chan struct{} }

func (g *ctxGateCompleter) complete(ctx context.Context, _ request) (*response, error) {
	select {
	case g.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

// gateCompleter blocks inside the model call until released, so a test can catch
// the app while a turn is running
type gateCompleter struct {
	entered chan struct{}
	release chan struct{}
}

func (g *gateCompleter) complete(_ context.Context, _ request) (*response, error) {
	g.entered <- struct{}{}
	<-g.release
	return &response{StopReason: "end_turn", Content: []ContentBlock{{Type: "text", Text: "ok"}}}, nil
}

// endlessToolCompleter always asks for another tool, never ending the turn
type endlessToolCompleter struct{ calls int }

func (e *endlessToolCompleter) complete(_ context.Context, _ request) (*response, error) {
	e.calls++
	return &response{StopReason: "tool_use", Content: []ContentBlock{
		toolUse(fmt.Sprintf("tu_%d", e.calls), "list_files", `{}`),
	}}, nil
}

func hasThinkingSignature(msgs []Message, sig string) bool {
	for _, msg := range msgs {
		for _, b := range msg.Content {
			if b.Type == "thinking" && b.Signature == sig {
				return true
			}
		}
	}
	return false
}

func hasToolResult(msgs []Message, toolUseID string) bool {
	for _, msg := range msgs {
		for _, b := range msg.Content {
			if b.Type == "tool_result" && b.ToolUseID == toolUseID {
				return true
			}
		}
	}
	return false
}

func toolResultIsError(msgs []Message, toolUseID string) bool {
	for _, msg := range msgs {
		for _, b := range msg.Content {
			if b.Type == "tool_result" && b.ToolUseID == toolUseID && b.IsError {
				return true
			}
		}
	}
	return false
}

func TestStablePrefixUsesOneHourCache(t *testing.T) {
	// The stable system prompt + tools are cached with a 1-hour TTL so they stay
	// warm across a whole session; the growing conversation keeps the 5-minute one.
	sys := cachedSystem("hello")
	require.Len(t, sys, 1)
	require.NotNil(t, sys[0].CacheControl)
	assert.Equal(t, "1h", sys[0].CacheControl.TTL, "system prompt uses the 1-hour cache")

	tools := cachedToolDefs(nil)
	require.NotEmpty(t, tools)
	last := tools[len(tools)-1]
	require.NotNil(t, last.CacheControl)
	assert.Equal(t, "1h", last.CacheControl.TTL, "tool schema uses the 1-hour cache")

	msgs := cacheConversation([]Message{{Role: roleUser, Content: []ContentBlock{{Type: blockText, Text: "hi"}}}})
	require.NotEmpty(t, msgs)
	cc := msgs[0].Content[len(msgs[0].Content)-1].CacheControl
	require.NotNil(t, cc)
	assert.Equal(t, "", cc.TTL, "the conversation prefix keeps the default 5-minute cache")

	b, err := json.Marshal(ephemeralCache)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "ttl", "the 5-minute breakpoint omits ttl on the wire")
	b, err = json.Marshal(ephemeral1hCache)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"ttl":"1h"`)
}

// A reply is tagged with the OPTION that produced it, not the provider's model
// string: both backends can serve the same model, so "claude-sonnet-5" alone
// does not say which one answered (or who paid). The chat renders this tag, and
// it has to resolve against the catalog.
func TestReplyIsTaggedWithTheOptionThatRanIt(t *testing.T) {
	t.Parallel()
	fc := &fakeCompleter{replies: []response{
		{StopReason: "end_turn", Content: []ContentBlock{{Type: "text", Text: "ok"}}},
	}}
	m := NewManager(fc, newFakeOps(), NewMemoryStore(), Credentials{AnthropicAPIKey: "k"})

	ch, cancel, err := m.Subscribe("blog")
	require.NoError(t, err)
	defer cancel()
	require.NoError(t, m.Send("blog", "", "hi", "anthropic-sonnet-5"))
	events := drainUntilDone(t, ch)
	require.Eventually(t, func() bool { return !m.Running("blog") }, 2*time.Second, 5*time.Millisecond)

	// The provider is still asked for its own model string.
	require.NotEmpty(t, fc.calls)
	assert.Equal(t, "claude-sonnet-5", fc.calls[0].Model, "the API is asked for the model it knows")

	// What watchers and the transcript see is the option id.
	var modelEvent string
	for _, e := range events {
		if e.Type == evtModel {
			modelEvent = e.Text
		}
	}
	assert.Equal(t, "anthropic-sonnet-5", modelEvent, "the turn announces the option that ran")

	history := m.load("blog")
	require.NotEmpty(t, history)
	assert.Equal(t, "anthropic-sonnet-5", history[len(history)-1].Model, "the stored reply names the option")
}

// The assistant is told when the app it is working on is archived. Without it,
// the model plans a deploy, is refused, and has to guess why -- and it would
// keep suggesting "power it on", which archiving is precisely what refuses.
func TestSystemPromptSaysWhenTheAppIsArchived(t *testing.T) {
	t.Parallel()
	live := systemPrompt("blog", false, nil)
	assert.NotContains(t, strings.ToLower(live), "archived", "a normal app's prompt says nothing about archiving")

	shelved := systemPrompt("blog", true, nil)
	assert.Contains(t, strings.ToLower(shelved), "archived")
	// It must name the way out, since every running verb is refused until then.
	assert.Contains(t, strings.ToLower(shelved), "unarchive")
}

// The assistant cannot use what it is never told about. Before this, an app
// could hold a granted Google Calendar and the model would answer "there's no
// calendar integration configured", which was true only of its own knowledge.
func TestSystemPromptNamesTheAppsConnections(t *testing.T) {
	t.Parallel()
	quiet := systemPrompt("blog", false, nil)
	assert.NotContains(t, quiet, "connection", "an app with none gets no noise about them")

	withConns := systemPrompt("blog", false, []Connection{
		{Slug: "work-cal", Provider: "google-calendar", ProviderLabel: "Google Calendar"},
		{Slug: "openai", Provider: "generic", ProviderLabel: "API key or token"},
	})
	assert.Contains(t, withConns, "work-cal")
	assert.Contains(t, withConns, "Google Calendar")
	assert.Contains(t, withConns, "openai")
	// And how to actually reach one, or naming them achieves nothing
	assert.Contains(t, withConns, "/v1/connections/work-cal/token")
	assert.Contains(t, withConns, "/run/hostit/hostit.sock")
}

// The credential must be read by the APP at runtime, not fetched into the
// conversation: transcripts are stored, so a token printed once is a token
// stored forever.
func TestSystemPromptTellsTheModelNotToPrintTokens(t *testing.T) {
	t.Parallel()
	p := systemPrompt("blog", false, []Connection{{Slug: "work-cal", Provider: "google-calendar", ProviderLabel: "Google Calendar"}})
	lower := strings.ToLower(p)
	assert.Contains(t, lower, "never print")
	assert.Contains(t, lower, "expire")
}

// The redaction has to be WIRED IN, not merely available: a credential in a tool
// result is stored for as long as the conversation lives.
func TestATokenInToolOutputNeverReachesTheTranscript(t *testing.T) {
	t.Parallel()
	leak := `{"provider":"google-calendar","access_token":"ya29.LEAKED","expires_at":"2026-01-01T00:00:00Z"}`
	ops := newFakeOps()
	ops.execFn = func(string) ExecResult { return ExecResult{Output: leak} }
	fc := &fakeCompleter{replies: []response{
		{StopReason: "tool_use", Content: []ContentBlock{toolUse("tu_1", "run_command", `{"command":"curl --unix-socket /run/hostit/hostit.sock http://x/v1/connections/cal/token"}`)}},
		{StopReason: "end_turn", Content: []ContentBlock{{Type: "text", Text: "The connection works."}}},
	}}
	store := NewMemoryStore()
	m := NewManager(fc, ops, store, Credentials{AnthropicAPIKey: "k"})

	events := runTurn(t, m, "blog", "check the calendar connection")

	// Not in what the browser is shown
	for _, e := range events {
		assert.NotContains(t, e.Output, "ya29.LEAKED", "a credential must not be published to the UI")
	}
	// Nor in what is sent back to the model, nor in what is stored
	var whole strings.Builder
	add := func(msgs []Message) {
		for _, msg := range msgs {
			for _, b := range msg.Content {
				fmt.Fprintf(&whole, "%v %s ", b.Content, b.Text)
			}
		}
	}
	for _, call := range fc.calls {
		add(call.Messages)
	}
	saved, err := store.Load("blog")
	require.NoError(t, err)
	add(saved)
	assert.NotContains(t, whole.String(), "ya29.LEAKED", "a credential must not be kept in the transcript")
	assert.Contains(t, whole.String(), "[redacted]", "and the shape of it is visibly taken out")
}
