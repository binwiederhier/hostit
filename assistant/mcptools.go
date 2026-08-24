package assistant

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Granted MCP servers become tools the model calls directly, rather than a
// paragraph in the prompt telling it to curl something. The model is good at
// calling tools and bad at inventing HTTP requests against a server it cannot
// see, and a tool call is the one shape it can be given a real schema for.
//
// The call goes through hostit, not the app: an MCP token opens the WHOLE
// server, so handing it to the app -- or to the model -- would make the grant
// decorative. See control/mcp.go.

const (
	// connectedToolPrefix namespaces a connected server's tools so two servers
	// offering "search" are still two distinct tools. It is deliberately NOT
	// mcpToolPrefix (sandbox.go): that one is how Claude Code namespaces
	// hostit's OWN tools, and the two must not be confusable.
	connectedToolPrefix = "mcp__conn__"
	connectedToolSep    = "__"
	// maxToolNameLen is the API's limit. A name over it fails the whole request,
	// not just that tool, so it is truncated rather than sent.
	maxToolNameLen = 64
)

// MCPTool is one tool on a granted MCP server.
type MCPTool struct {
	// Connection is the slug the owner gave the server.
	Connection  string
	Name        string
	Description string
	InputSchema json.RawMessage
}

// mcpToolDefs turns granted server tools into tool definitions.
func mcpToolDefs(tools []MCPTool) []Tool {
	out := make([]Tool, 0, len(tools))
	for _, t := range tools {
		description := t.Description
		if description == "" {
			description = t.Name
		}
		// The connection is named in the description as well as the tool name,
		// because two servers can offer the same tool and the model has to be
		// able to say which one it means.
		out = append(out, Tool{
			Name:        mcpToolName(t),
			Description: fmt.Sprintf("%s (from the connected MCP server %q)", description, t.Connection),
			InputSchema: mcpInputSchema(t),
		})
	}
	return out
}

// mcpInputSchema is the server's own schema, or an empty object when it sent
// none -- the API rejects a tool with no schema at all.
func mcpInputSchema(t MCPTool) json.RawMessage {
	if len(t.InputSchema) == 0 || !json.Valid(t.InputSchema) {
		return schema(`{"type":"object"}`)
	}
	return t.InputSchema
}

// mcpToolName is the model-facing name. A server's tool name is whatever its
// author chose; the API accepts a narrow character set, so anything else is
// replaced rather than sent and rejected.
func mcpToolName(t MCPTool) string {
	name := connectedToolPrefix + sanitiseToolName(t.Connection) + connectedToolSep + sanitiseToolName(t.Name)
	if len(name) > maxToolNameLen {
		name = name[:maxToolNameLen]
	}
	return name
}

func sanitiseToolName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// parseMCPToolName resolves a model-facing name back to the connection and the
// tool's REAL name, which is what the server must be called by.
//
// It resolves against the GRANTED list rather than by splitting the string,
// which is the security-relevant half: a model that invents a plausible name
// finds nothing, so it can only reach servers this app actually holds.
func parseMCPToolName(name string, tools []MCPTool) (connection, remote string, ok bool) {
	if !strings.HasPrefix(name, connectedToolPrefix) {
		return "", "", false
	}
	for _, t := range tools {
		if mcpToolName(t) == name {
			return t.Connection, t.Name, true
		}
	}
	return "", "", false
}

// dispatchMCPTool runs one MCP tool call. Reports whether the name was an MCP
// tool at all, so the caller's switch can fall through to its own tools.
func dispatchMCPTool(ops AppOps, app, name string, input json.RawMessage) (result string, isError bool, handled bool) {
	if !strings.HasPrefix(name, connectedToolPrefix) {
		return "", false, false
	}
	granted := ops.MCPTools(app)
	connection, remote, ok := parseMCPToolName(name, granted)
	if !ok {
		return fmt.Sprintf("%s is not one of this app's connected MCP tools; the ones it has are: %s",
			name, strings.Join(mcpToolNames(granted), ", ")), true, true
	}
	var args map[string]any
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return "the arguments must be a JSON object: " + err.Error(), true, true
		}
	}
	out, toolErr, err := ops.CallMCPTool(app, connection, remote, args)
	if err != nil {
		return err.Error(), true, true
	}
	return out, toolErr, true
}

func mcpToolNames(tools []MCPTool) []string {
	if len(tools) == 0 {
		return []string{"none"}
	}
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, mcpToolName(t))
	}
	return out
}
