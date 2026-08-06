package assistant

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDispatchTools(t *testing.T) {
	t.Parallel()
	ops := newFakeOps()
	ops.files["hostit.yml"] = "mode: static\n"
	ops.execFn = func(command string) ExecResult {
		return ExecResult{Output: "built\n", ExitCode: 0}
	}
	m := NewManager(&fakeCompleter{}, ops, NewMemoryStore(), "test-model")

	t.Run("write_file writes and confirms", func(t *testing.T) {
		out, isErr := m.dispatch("blog", "write_file", json.RawMessage(`{"path":"public/index.html","content":"hi"}`))
		assert.False(t, isErr)
		assert.Contains(t, out, "wrote 2 bytes")
		assert.Equal(t, "hi", ops.files["public/index.html"])
	})

	t.Run("read_file returns content", func(t *testing.T) {
		out, isErr := m.dispatch("blog", "read_file", json.RawMessage(`{"path":"hostit.yml"}`))
		assert.False(t, isErr)
		assert.Equal(t, "mode: static\n", out)
	})

	t.Run("read_file of a missing file is a tool error", func(t *testing.T) {
		out, isErr := m.dispatch("blog", "read_file", json.RawMessage(`{"path":"nope"}`))
		assert.True(t, isErr)
		assert.Contains(t, out, "no such file")
	})

	t.Run("run_command reports exit code in the output", func(t *testing.T) {
		out, isErr := m.dispatch("blog", "run_command", json.RawMessage(`{"command":"make"}`))
		assert.False(t, isErr)
		assert.Contains(t, out, "built")
		assert.Contains(t, out, "[exit code 0]")
	})

	t.Run("a non-zero exit is a tool error", func(t *testing.T) {
		ops.execFn = func(string) ExecResult { return ExecResult{Output: "boom", ExitCode: 1} }
		out, isErr := m.dispatch("blog", "run_command", json.RawMessage(`{"command":"false"}`))
		assert.True(t, isErr)
		assert.Contains(t, out, "[exit code 1]")
	})

	t.Run("missing required args are refused", func(t *testing.T) {
		_, isErr := m.dispatch("blog", "read_file", json.RawMessage(`{}`))
		assert.True(t, isErr)
		_, isErr = m.dispatch("blog", "run_command", json.RawMessage(`{"command":"  "}`))
		assert.True(t, isErr)
	})

	t.Run("refresh_preview confirms without touching the app", func(t *testing.T) {
		// It is a UI signal, not an app operation: the owner's browser reloads the
		// live preview when it sees the call on the stream. So it never errors and
		// does not depend on ops.
		out, isErr := m.dispatch("blog", "refresh_preview", json.RawMessage(`{}`))
		assert.False(t, isErr)
		assert.NotEmpty(t, out)
	})

	t.Run("unknown tool", func(t *testing.T) {
		out, isErr := m.dispatch("blog", "delete_everything", json.RawMessage(`{}`))
		assert.True(t, isErr)
		assert.Contains(t, out, "unknown tool")
	})
}

func TestToolDefsIncludeRefreshPreview(t *testing.T) {
	t.Parallel()
	var names []string
	for _, d := range toolDefs() {
		names = append(names, d.Name)
	}
	assert.Contains(t, names, "refresh_preview")
}
