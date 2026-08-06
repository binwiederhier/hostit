package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

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
	m := NewManager(fc, ops, "test-model")

	var events []Event
	err := m.Run(context.Background(), "blog", "make a hello page", func(e Event) { events = append(events, e) })
	require.NoError(t, err)

	// The tool actually ran against the app
	assert.Equal(t, "<h1>hi</h1>", ops.files["public/index.html"])
	assert.Equal(t, []string{"public/index.html"}, ops.writes)

	// The client saw the loop unfold: thinking, text, the tool call, its result, done
	types := eventTypes(events)
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
	m := NewManager(fc, ops, "test-model")

	var events []Event
	err := m.Run(context.Background(), "blog", "read missing.txt", func(e Event) { events = append(events, e) })
	require.NoError(t, err, "a failing tool is the model's problem to handle, not a run failure")

	var result *Event
	for i := range events {
		if events[i].Type == "tool_result" {
			result = &events[i]
		}
	}
	require.NotNil(t, result)
	assert.True(t, result.IsError, "the tool result must be marked an error")
	assert.True(t, toolResultIsError(fc.calls[1].Messages, "tu_1"), "the error must reach the model as is_error")
}

func TestRunStopsAtStepLimit(t *testing.T) {
	t.Parallel()
	// A model that never stops asking for tools must not loop forever
	fc := &endlessToolCompleter{}
	m := NewManager(fc, newFakeOps(), "test-model")

	var last Event
	err := m.Run(context.Background(), "blog", "go", func(e Event) { last = e })
	require.ErrorIs(t, err, errMaxIterations)
	assert.Equal(t, "error", last.Type)
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
	m := NewManager(fc, ops, "test-model")

	require.NoError(t, m.Run(context.Background(), "blog", "hello", func(Event) {}))
	require.NoError(t, m.Run(context.Background(), "blog", "again", func(Event) {}))

	// The second call must carry the whole prior exchange: user, assistant, user
	msgs := fc.calls[1].Messages
	require.GreaterOrEqual(t, len(msgs), 3)
	assert.Equal(t, "hello", msgs[0].Content[0].Text)
	assert.Equal(t, "assistant", msgs[1].Role)
	assert.Equal(t, "again", msgs[len(msgs)-1].Content[0].Text)

	// Reset forgets it
	m.Reset("blog")
	require.NoError(t, m.Run(context.Background(), "blog", "fresh", func(Event) {}))
	assert.Len(t, fc.calls[2].Messages, 1, "after reset only the new message remains")
}

func TestRunReportsAPIError(t *testing.T) {
	t.Parallel()
	fc := &fakeCompleter{} // no replies -> complete returns an error
	m := NewManager(fc, newFakeOps(), "test-model")

	var events []Event
	err := m.Run(context.Background(), "blog", "hi", func(e Event) { events = append(events, e) })
	require.Error(t, err)
	require.NotEmpty(t, events)
	assert.Equal(t, "error", events[len(events)-1].Type)
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
