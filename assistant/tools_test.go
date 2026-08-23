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
	m := NewManager(&fakeCompleter{}, ops, NewMemoryStore(), Credentials{AnthropicAPIKey: "k"})

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

// An access token that reaches a tool result is stored in the transcript --
// which lives in the database, in the clear, for as long as the conversation
// does. The system prompt tells the model not to print one, but an instruction
// is not a control: the obvious way to check a connection works is to curl the
// token endpoint and look at the output.
//
// So the shape the endpoint answers with is redacted on the way in. It does not
// catch a token the model deliberately mangles, and is not meant to -- it
// catches the accident, which is the realistic case.
func TestRedactCredentials(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, in, want string }{
		{
			"the token endpoint's answer",
			`{"provider":"google-calendar","access_token":"ya29.a0AfB_SECRET","expires_at":"2026-01-01T00:00:00Z"}`,
			`{"provider":"google-calendar","access_token":"[redacted]","expires_at":"2026-01-01T00:00:00Z"}`,
		},
		{
			"pretty-printed, as curl | jq would give it",
			"{\n  \"access_token\": \"xoxb-1234-abcd\",\n  \"provider\": \"slack\"\n}",
			"{\n  \"access_token\": \"[redacted]\",\n  \"provider\": \"slack\"\n}",
		},
		{
			"a refresh token, if one ever surfaced",
			`{"refresh_token":"1//04abcdef","access_token":"tok"}`,
			`{"refresh_token":"[redacted]","access_token":"[redacted]"}`,
		},
		{
			"several in one blob",
			`a {"access_token":"one"} b {"access_token":"two"} c`,
			`a {"access_token":"[redacted]"} b {"access_token":"[redacted]"} c`,
		},
		{"ordinary output is untouched", "total 4\ndrwxr-xr-x public", "total 4\ndrwxr-xr-x public"},
		{"the word alone is not enough to trigger it", "read the access_token docs", "read the access_token docs"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, RedactCredentials(c.in))
		})
	}
}
