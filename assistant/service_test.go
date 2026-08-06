package assistant

import (
	"context"
	"encoding/json"
	"fmt"
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
	files  map[string]string
	writes []string
	execFn func(command string) ExecResult
}

func newFakeOps() *fakeOps { return &fakeOps{files: map[string]string{}} }

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
			if ev.Type == "done" || ev.Type == "error" {
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
	ch, cancel := m.Subscribe(app)
	defer cancel()
	require.NoError(t, m.Send(app, text))
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
	m := NewManager(fc, ops, NewMemoryStore(), "test-model")

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

func TestToolErrorIsReportedButLoopContinues(t *testing.T) {
	t.Parallel()
	ops := newFakeOps() // ReadFile of a missing file returns an error
	fc := &fakeCompleter{replies: []response{
		{StopReason: "tool_use", Content: []ContentBlock{toolUse("tu_1", "read_file", `{"path":"missing.txt"}`)}},
		{StopReason: "end_turn", Content: []ContentBlock{{Type: "text", Text: "That file does not exist yet."}}},
	}}
	m := NewManager(fc, ops, NewMemoryStore(), "test-model")

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
	m := NewManager(fc, newFakeOps(), NewMemoryStore(), "test-model")

	events := runTurn(t, m, "blog", "go")
	assert.Equal(t, "error", events[len(events)-1].Type)
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
	m := NewManager(fc, ops, NewMemoryStore(), "test-model")

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
	runTurn(t, NewManager(fc, ops, store, "m"), "blog", "make a page")

	// A different manager on the same store recovers the conversation as display
	// items -- what a reload or another device sees.
	items, err := NewManager(&fakeCompleter{}, ops, store, "m").Transcript("blog")
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
	m := NewManager(&fakeCompleter{}, ops, store, "m")
	require.NoError(t, m.Reset("blog"))
	items, err = m.Transcript("blog")
	require.NoError(t, err)
	assert.Empty(t, items)
}

func TestRunReportsAPIError(t *testing.T) {
	t.Parallel()
	fc := &fakeCompleter{} // no replies -> complete returns an error
	m := NewManager(fc, newFakeOps(), NewMemoryStore(), "test-model")

	events := runTurn(t, m, "blog", "hi")
	require.NotEmpty(t, events)
	assert.Equal(t, "error", events[len(events)-1].Type)
}

func TestBroadcastsToEverySubscriber(t *testing.T) {
	t.Parallel()
	fc := &fakeCompleter{replies: []response{
		{StopReason: "end_turn", Content: []ContentBlock{{Type: "text", Text: "broadcast"}}},
	}}
	m := NewManager(fc, newFakeOps(), NewMemoryStore(), "test-model")

	// Two devices watching the same app both see the same stream.
	ch1, c1 := m.Subscribe("blog")
	defer c1()
	ch2, c2 := m.Subscribe("blog")
	defer c2()
	require.NoError(t, m.Send("blog", "hi"))

	got1 := eventTypes(drainUntilDone(t, ch1))
	got2 := eventTypes(drainUntilDone(t, ch2))
	assert.Equal(t, got1, got2)
	assert.Equal(t, []string{"user", "text", "done"}, got1)
}

func TestSecondSenderIsRejectedWhileBusy(t *testing.T) {
	t.Parallel()
	// A model call that blocks until released keeps the turn "running".
	g := &gateCompleter{entered: make(chan struct{}, 1), release: make(chan struct{})}
	m := NewManager(g, newFakeOps(), NewMemoryStore(), "test-model")

	require.NoError(t, m.Send("blog", "one"))
	<-g.entered // the run is now mid-call, so the app is busy
	assert.True(t, m.Running("blog"))
	assert.ErrorIs(t, m.Send("blog", "two"), ErrBusy, "a second sender must not clobber the first")

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
	m := NewManager(fc, newFakeOps(), store, "test-model")

	require.NoError(t, m.Send("blog", "hello"))
	require.Eventually(t, func() bool { return !m.Running("blog") }, 3*time.Second, 10*time.Millisecond)

	items, err := m.Transcript("blog")
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, "user", items[0].Kind)
	assert.Equal(t, "hello", items[0].Text)
	assert.Equal(t, "text", items[1].Kind)
	assert.Equal(t, "done anyway", items[1].Text)
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
