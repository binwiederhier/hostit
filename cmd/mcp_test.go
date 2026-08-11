package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeToolCaller records calls and returns canned results, standing in for the
// daemon socket so the MCP protocol can be tested without a running daemon.
type fakeToolCaller struct {
	calls   []toolCall
	output  string
	isError bool
	err     error
}

type toolCall struct {
	name string
	args string
}

func (f *fakeToolCaller) Tool(name string, args []byte) (string, bool, error) {
	f.calls = append(f.calls, toolCall{name: name, args: string(args)})
	return f.output, f.isError, f.err
}

// run feeds newline-delimited JSON-RPC requests through the server and returns
// the response lines it wrote.
func run(t *testing.T, ctl toolCaller, requests ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	srv := &mcpServer{ctl: ctl, out: &out, version: "test"}
	require.NoError(t, srv.serve(strings.NewReader(strings.Join(requests, "\n")+"\n")))
	var responses []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m), "each response line is valid JSON: %q", line)
		responses = append(responses, m)
	}
	return responses
}

func TestMCPInitializeEchoesProtocolVersion(t *testing.T) {
	t.Parallel()
	resp := run(t, &fakeToolCaller{}, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	require.Len(t, resp, 1)
	result := resp[0]["result"].(map[string]any)
	assert.Equal(t, "2025-06-18", result["protocolVersion"], "the client's protocol version is echoed back")
	assert.Contains(t, result, "capabilities")
	assert.Equal(t, "hostit", result["serverInfo"].(map[string]any)["name"])
}

func TestMCPNotificationGetsNoResponse(t *testing.T) {
	t.Parallel()
	// A message without an id is a notification; it must be acknowledged by silence,
	// not a response (a response to a notification is a protocol error).
	resp := run(t, &fakeToolCaller{}, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	assert.Empty(t, resp)
}

func TestMCPToolsListExposesAppToolsWithoutRefreshPreview(t *testing.T) {
	t.Parallel()
	resp := run(t, &fakeToolCaller{}, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	require.Len(t, resp, 1)
	tools := resp[0]["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.(map[string]any)["name"].(string)] = true
	}
	// The app operations are advertised...
	for _, want := range []string{"list_files", "read_file", "write_file", "run_command", "deploy"} {
		assert.True(t, names[want], "tool %q is exposed over MCP", want)
	}
	// ...but refresh_preview is a UI-only signal with no meaning to a headless backend.
	assert.False(t, names["refresh_preview"], "refresh_preview is not exposed")
	// Every tool must carry an inputSchema (MCP's field name, not input_schema).
	for _, tl := range tools {
		assert.Contains(t, tl.(map[string]any), "inputSchema")
	}
}

func TestMCPToolCallForwardsToDaemonAndWrapsResult(t *testing.T) {
	t.Parallel()
	ctl := &fakeToolCaller{output: "hostit.yml\n  public/", isError: false}
	resp := run(t, ctl, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_files","arguments":{"path":"public"}}}`)
	require.Len(t, resp, 1)

	// The call is forwarded to the daemon with the tool name and its arguments.
	require.Len(t, ctl.calls, 1)
	assert.Equal(t, "list_files", ctl.calls[0].name)
	assert.JSONEq(t, `{"path":"public"}`, ctl.calls[0].args)

	// The output comes back as MCP text content, not an error.
	result := resp[0]["result"].(map[string]any)
	assert.Equal(t, false, result["isError"])
	content := result["content"].([]any)
	assert.Equal(t, "text", content[0].(map[string]any)["type"])
	assert.Equal(t, "hostit.yml\n  public/", content[0].(map[string]any)["text"])
}

func TestMCPToolErrorIsResultNotProtocolError(t *testing.T) {
	t.Parallel()
	// A tool that reports an error (missing file, failed command) comes back as a
	// normal result with isError set -- the model reads it and adapts. Only a
	// transport failure would be a JSON-RPC error.
	ctl := &fakeToolCaller{output: "no such file", isError: true}
	resp := run(t, ctl, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"nope"}}}`)
	require.Len(t, resp, 1)
	assert.NotContains(t, resp[0], "error", "a tool error is not a JSON-RPC error")
	result := resp[0]["result"].(map[string]any)
	assert.Equal(t, true, result["isError"])
}

func TestMCPUnknownMethodReturnsError(t *testing.T) {
	t.Parallel()
	resp := run(t, &fakeToolCaller{}, `{"jsonrpc":"2.0","id":5,"method":"nonsense/method"}`)
	require.Len(t, resp, 1)
	assert.Equal(t, float64(-32601), resp[0]["error"].(map[string]any)["code"])
}

func TestMCPHandlesLargeWriteFileArgument(t *testing.T) {
	t.Parallel()
	// write_file carries file content, which can exceed bufio's default line size;
	// the reader must not split the message.
	big := strings.Repeat("x", 200*1024)
	args, err := json.Marshal(map[string]string{"path": "big.txt", "content": big})
	require.NoError(t, err)
	req, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 6, "method": "tools/call", "params": map[string]any{"name": "write_file", "arguments": json.RawMessage(args)}})
	require.NoError(t, err)

	ctl := &fakeToolCaller{output: "wrote big.txt"}
	resp := run(t, ctl, string(req))
	require.Len(t, resp, 1)
	require.Len(t, ctl.calls, 1)
	var got map[string]string
	require.NoError(t, json.Unmarshal([]byte(ctl.calls[0].args), &got))
	assert.Len(t, got["content"], 200*1024, "the full large argument reached the daemon intact")
}
