package appcli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/urfave/cli/v2"
	"heckel.io/hostit/appctl"
	"heckel.io/hostit/assistant"
	"heckel.io/hostit/controlconf"
)

const (
	// mcpProtocolVersion is the MCP version this server speaks when a client does
	// not name one. The transport is stdio with newline-delimited JSON-RPC 2.0.
	mcpProtocolVersion = "2024-11-05"
	// mcpServerName identifies this server to the client; the client namespaces
	// our tools as mcp__<name>__<tool>, which is what --allowedTools allowlists.
	mcpServerName = "hostit"
	// mcpMaxLine caps one JSON-RPC message. write_file carries file content, so it
	// matches the daemon's own per-call cap (maxSelfToolBody).
	mcpMaxLine = 16 * 1024 * 1024
)

// cmdMCP is the sandboxed Claude Max backend's bridge to hostit. It runs INSIDE
// the assistant sandbox container (as the app's uid) and speaks MCP over stdio to
// the `claude` process that spawned it. Each tool call is forwarded to the daemon
// over the peercred-authenticated socket, which scopes it to this one app -- so
// the model, confined to these MCP tools, can only ever touch the app the turn
// belongs to, never the host, another app, or its own credential.
var (
	cmdMCP = &cli.Command{
		Name:   "mcp",
		Usage:  "Serve hostit's app tools over MCP (used by the sandboxed assistant backend)",
		Hidden: true,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "socket", Usage: "daemon socket path; defaults to the standard location"},
		},
		Action: execMCP,
	}
)

func execMCP(c *cli.Context) error {
	socket := c.String("socket")
	if socket == "" {
		socket = controlconf.DefaultSocketFile
	}
	srv := &mcpServer{
		ctl:     appctl.NewController(socket),
		out:     os.Stdout,
		version: c.App.Version,
	}
	return srv.serve(os.Stdin)
}

// toolCaller runs one app-scoped tool call and returns its output plus whether
// the tool reported an error (a transport failure is the error). appctl.Controller
// implements it against the daemon socket; tests use a fake.
type toolCaller interface {
	Tool(name string, args []byte) (string, bool, error)
}

// mcpServer is one MCP stdio session: it reads newline-delimited JSON-RPC from
// the client on stdin and writes responses on stdout. Nothing but protocol JSON
// goes to stdout; diagnostics go to stderr so they never corrupt the stream.
type mcpServer struct {
	ctl     toolCaller
	out     io.Writer
	version string
}

// rpcRequest is an incoming JSON-RPC 2.0 message. A missing id means a
// notification, which gets no response.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// mcpTool is one tool as MCP lists it. It mirrors assistant.Tool but with MCP's
// field name (inputSchema, not input_schema).
type mcpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// mcpContent is one block of a tool result; we only ever return text.
type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (s *mcpServer) serve(in io.Reader) error {
	r := bufio.NewReaderSize(in, 64*1024)
	for {
		line, err := readLine(r)
		if err == io.EOF {
			return nil // the client (claude) closed the stream: the turn is over
		} else if err != nil {
			return err
		}
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			fmt.Fprintf(os.Stderr, "hostit mcp: bad message: %v\n", err)
			continue
		}
		s.handle(&req)
	}
}

// handle dispatches one request. A request with no id is a notification (e.g.
// notifications/initialized) and is acknowledged only by doing nothing.
func (s *mcpServer) handle(req *rpcRequest) {
	switch req.Method {
	case "initialize":
		s.reply(req.ID, s.initializeResult(req.Params))
	case "tools/list":
		s.reply(req.ID, map[string]any{"tools": mcpTools()})
	case "tools/call":
		s.handleToolCall(req)
	case "ping":
		s.reply(req.ID, map[string]any{})
	default:
		// Notifications (no id) are fine to ignore; a request we do not know gets a
		// method-not-found so the client is not left waiting.
		if len(req.ID) > 0 {
			s.replyError(req.ID, -32601, "method not found: "+req.Method)
		}
	}
}

// initializeResult echoes the client's protocol version (falling back to ours)
// and advertises the tools capability.
func (s *mcpServer) initializeResult(params json.RawMessage) map[string]any {
	version := mcpProtocolVersion
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(params, &p); err == nil && p.ProtocolVersion != "" {
		version = p.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": mcpServerName, "version": s.version},
	}
}

// handleToolCall forwards one tool call to the daemon over the socket. A tool
// that reports an error (a failed command, a missing file) comes back as a normal
// result with isError set, which is what the model reads and adapts to; only a
// transport failure is a JSON-RPC error.
func (s *mcpServer) handleToolCall(req *rpcRequest) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &call); err != nil {
		s.replyError(req.ID, -32602, "invalid params: "+err.Error())
		return
	}
	args := call.Arguments
	if len(args) == 0 {
		args = []byte("{}")
	}
	output, isErr, err := s.ctl.Tool(call.Name, args)
	if err != nil {
		// The daemon was unreachable or refused the call: surface it to the model as
		// a tool error rather than killing the session, so it can report cleanly.
		output, isErr = "hostit tool call failed: "+err.Error(), true
	}
	s.reply(req.ID, map[string]any{
		"content": []mcpContent{{Type: "text", Text: output}},
		"isError": isErr,
	})
}

// mcpTools lists the tools this server exposes: the app's own operations, minus
// refresh_preview (a UI-only signal with no meaning to a headless backend).
func mcpTools() []mcpTool {
	defs := assistant.ToolDefs()
	tools := make([]mcpTool, 0, len(defs))
	for _, d := range defs {
		if d.Name == "refresh_preview" {
			continue
		}
		tools = append(tools, mcpTool{Name: d.Name, Description: d.Description, InputSchema: d.InputSchema})
	}
	return tools
}

func (s *mcpServer) reply(id json.RawMessage, result any) {
	if len(id) == 0 {
		return // a notification takes no response
	}
	s.write(rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *mcpServer) replyError(id json.RawMessage, code int, message string) {
	if len(id) == 0 {
		return
	}
	s.write(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}})
}

// write emits one JSON-RPC message as a single line (newline-delimited framing).
func (s *mcpServer) write(resp rpcResponse) {
	b, err := json.Marshal(resp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hostit mcp: cannot marshal response: %v\n", err)
		return
	}
	b = append(b, '\n')
	if _, err := s.out.Write(b); err != nil {
		fmt.Fprintf(os.Stderr, "hostit mcp: cannot write response: %v\n", err)
	}
}

// readLine reads one newline-delimited message, growing past bufio's default so a
// large write_file argument is not split. It returns the line without the newline.
func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if err == bufio.ErrBufferFull {
			if len(buf) > mcpMaxLine {
				return nil, fmt.Errorf("message exceeds %d bytes", mcpMaxLine)
			}
			continue
		}
		if err != nil {
			if len(buf) > 0 && err == io.EOF {
				return trimNewline(buf), nil // a final line without a trailing newline
			}
			return nil, err
		}
		return trimNewline(buf), nil
	}
}

func trimNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
