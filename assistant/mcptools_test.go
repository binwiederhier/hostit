package assistant

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A granted MCP server's tools have to be REAL tools, not a paragraph in the
// prompt telling the model to curl something. The model is good at calling
// tools and bad at inventing HTTP requests against a server it cannot see.
func TestGrantedMCPToolsBecomeToolsTheModelCanCall(t *testing.T) {
	t.Parallel()
	defs := toolDefs([]MCPTool{
		{Connection: "issues", Name: "list_issues", Description: "List issues", InputSchema: json.RawMessage(`{"type":"object"}`)},
	})

	var found *Tool
	for i := range defs {
		if defs[i].Name == "mcp__conn__issues__list_issues" {
			found = &defs[i]
		}
	}
	require.NotNil(t, found, "the tool list must carry the server's tools alongside the built-in ones")
	assert.Contains(t, found.Description, "List issues")
	assert.Contains(t, found.Description, "issues", "the model should be able to tell two connected servers apart")
	assert.JSONEq(t, `{"type":"object"}`, string(found.InputSchema), "the server's own schema, not a guess at one")
}

// An app with no MCP connection gets exactly the tools it always had.
func TestWithoutAnMCPConnectionTheToolListIsUnchanged(t *testing.T) {
	t.Parallel()
	assert.Equal(t, len(toolDefs(nil)), len(ToolDefs()))
}

// A server tool name is whatever its author chose; the API only accepts a
// narrow character set, and a name it rejects fails the whole request rather
// than just that tool.
func TestAnAwkwardToolNameIsMadeSafeAndStillDispatches(t *testing.T) {
	t.Parallel()
	tool := MCPTool{Connection: "my-server", Name: "search/files.v2", Description: "Search"}
	name := mcpToolName(tool)

	assert.Regexp(t, `^[a-zA-Z0-9_-]{1,64}$`, name)
	conn, remote, ok := parseMCPToolName(name, []MCPTool{tool})
	require.True(t, ok, "and it must still resolve back to the tool it came from")
	assert.Equal(t, "my-server", conn)
	assert.Equal(t, "search/files.v2", remote, "the server is called by ITS name, not the sanitised one")
}

// The dispatch path: a model calling an MCP tool reaches the connection, and a
// tool that fails is reported to the model rather than raised.
func TestDispatchingAnMCPToolCallsTheConnection(t *testing.T) {
	t.Parallel()
	ops := newFakeOps()
	ops.mcpTools = []MCPTool{{Connection: "issues", Name: "list_issues"}}
	ops.mcpResult = "issue 1, issue 2"

	out, isErr := DispatchTool(ops, "blog", "mcp__conn__issues__list_issues", json.RawMessage(`{"team":"core"}`))
	assert.False(t, isErr)
	assert.Equal(t, "issue 1, issue 2", out)
	require.Len(t, ops.mcpCalls, 1)
	assert.Equal(t, "issues", ops.mcpCalls[0].connection)
	assert.Equal(t, "list_issues", ops.mcpCalls[0].tool)
	assert.Equal(t, "core", ops.mcpCalls[0].args["team"])

	ops.mcpErr = "the mailbox is locked"
	out, isErr = DispatchTool(ops, "blog", "mcp__conn__issues__list_issues", json.RawMessage(`{}`))
	assert.True(t, isErr, "so the model reads it and adapts, as it would a failed command")
	assert.Contains(t, out, "locked")
}

// A tool for a connection this app was never granted must not be callable, even
// if the model invents the name.
func TestAnUngrantedMCPToolNameIsRefused(t *testing.T) {
	t.Parallel()
	ops := newFakeOps()
	ops.mcpTools = []MCPTool{{Connection: "issues", Name: "list_issues"}}

	out, isErr := DispatchTool(ops, "blog", "mcp__conn__secrets__read_all", json.RawMessage(`{}`))
	assert.True(t, isErr)
	assert.Empty(t, ops.mcpCalls, "and nothing was sent anywhere")
	assert.Contains(t, out, "not")
}

// The prompt must not send the model after a credential for an MCP server:
// that endpoint refuses, on purpose, and the tools are right there.
func TestThePromptDoesNotOfferATokenForAnMCPConnection(t *testing.T) {
	t.Parallel()
	p := systemPrompt("blog", false, []Connection{
		{Slug: "issues", Provider: "mcp", ProviderLabel: "MCP server", MCP: true},
	})
	assert.NotContains(t, p, "/v1/connections/issues/token",
		"there is no credential to fetch; hostit makes the calls")
	assert.Contains(t, p, "mcp__conn__issues__", "the model is pointed at the tools instead")

	// A mix must still explain the credential half for the connection that has one.
	mixed := systemPrompt("blog", false, []Connection{
		{Slug: "issues", Provider: "mcp", ProviderLabel: "MCP server", MCP: true},
		{Slug: "work-cal", Provider: "google-calendar", ProviderLabel: "Google Calendar"},
	})
	assert.Contains(t, mixed, "/v1/connections/work-cal/token")
	assert.NotContains(t, mixed, "/v1/connections/issues/token")
}
