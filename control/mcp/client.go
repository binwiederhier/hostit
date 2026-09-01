package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// protocolVersion is the MCP revision hostit speaks. Sent on initialize and,
// per the spec, echoed as a header on every later request so a server serving
// several revisions knows which one it is talking to.
const protocolVersion = "2025-06-18"

// maxResponseBytes caps what a tool call may return. An MCP server is a third
// party the app owner chose, not one hostit vets, and the answer flows into an
// assistant transcript -- an unbounded read is somebody else's memory budget.
const maxResponseBytes = 4 << 20

var (
	// ErrUnauthorized is the server saying the token is missing, wrong, or
	// expired. Distinct from a general failure because it is the one error with
	// an obvious remedy: refresh, or tell the owner to reconnect.
	ErrUnauthorized = errors.New("the MCP server rejected the credential")
	// ErrProtocol is a server that answered, but not with MCP.
	ErrProtocol = errors.New("the server did not answer as an MCP server")
)

// Tool is one tool a server offers.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// ToolResult is the outcome of a call. IsError distinguishes a tool that ran
// and failed from a call that never happened -- see CallTool.
type ToolResult struct {
	Text    string `json:"text"`
	IsError bool   `json:"is_error,omitempty"`
}

// Client talks to one MCP server over Streamable HTTP. Not safe to share
// across servers; one per connection, made per request.
type Client struct {
	baseURL string
	token   string
	http    *http.Client

	session string // handed out by initialize, echoed on later calls
	ready   bool
	// Protects session and ready.
	mu sync.Mutex
}

// NewClient returns a client for an MCP endpoint. token may be empty for a
// server that wants no authorization.
// NewClient builds an MCP client over the given HTTP client. The client MUST be
// the SSRF-guarded outbound client for any user-supplied server URL: the tool
// data plane (ListTools/CallTool) re-resolves DNS on every call, so a public
// name that rebinds to an internal address is only stopped by the guarded
// dialer -- add-time URL validation cannot. A nil client falls back to a plain
// one (tests against loopback).
func NewClient(client *http.Client, baseURL, token string) *Client {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{
		baseURL: baseURL,
		token:   token,
		http:    client,
	}
}

// ListTools returns what the server offers.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	if err := c.initialize(ctx); err != nil {
		return nil, err
	}
	var out struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := c.call(ctx, "tools/list", map[string]any{}, &out); err != nil {
		return nil, err
	}
	tools := make([]Tool, 0, len(out.Tools))
	for _, t := range out.Tools {
		tools = append(tools, Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return tools, nil
}

// CallTool runs one tool.
//
// A tool that fails returns a result with IsError set and NO error: the call
// succeeded and the answer is bad news. Only a call that did not happen -- an
// unreachable server, a rejected token, a malformed answer -- is an error. The
// caller wants to retry one and show the other.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (ToolResult, error) {
	if err := c.initialize(ctx); err != nil {
		return ToolResult{}, err
	}
	if args == nil {
		args = map[string]any{}
	}
	var out struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, &out); err != nil {
		return ToolResult{}, err
	}
	// Content is a list of parts of assorted types (text, image, resource).
	// Only text is carried through: the consumer is an assistant transcript or
	// an app's JSON, and neither has anywhere to put an inline image yet.
	var b strings.Builder
	for _, part := range out.Content {
		if part.Type == "text" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(part.Text)
		}
	}
	return ToolResult{Text: b.String(), IsError: out.IsError}, nil
}

// initialize performs the MCP handshake once per client. Servers are entitled
// to refuse everything else until it has happened, and to issue a session id
// during it that later calls must carry.
func (c *Client) initialize(ctx context.Context) error {
	c.mu.Lock()
	done := c.ready
	c.mu.Unlock()
	if done {
		return nil
	}
	var out struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	params := map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "hostit", "version": "1"},
	}
	if err := c.call(ctx, "initialize", params, &out); err != nil {
		return err
	}
	c.mu.Lock()
	c.ready = true
	c.mu.Unlock()
	// Best-effort: the spec asks for it, and no server refuses work over its
	// absence, so a failure here must not sink an otherwise-working session.
	_ = c.notify(ctx, "notifications/initialized")
	return nil
}

// call sends one JSON-RPC request and decodes result into out.
func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return err
	}
	resp, err := c.post(ctx, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("%w (HTTP %d)", ErrUnauthorized, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s answered HTTP %d: %s", method, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
		c.mu.Lock()
		c.session = id
		c.mu.Unlock()
	}
	payload, err := readPayload(resp)
	if err != nil {
		return err
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	if envelope.Error != nil {
		return fmt.Errorf("%s failed: %s (code %d)", method, envelope.Error.Message, envelope.Error.Code)
	}
	if out == nil || len(envelope.Result) == 0 {
		return nil
	}
	return json.Unmarshal(envelope.Result, out)
}

// notify sends a JSON-RPC notification, which by definition has no id and gets
// no answer.
func (c *Client) notify(ctx context.Context, method string) error {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	if err != nil {
		return err
	}
	resp, err := c.post(ctx, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return nil
}

func (c *Client) post(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	c.mu.Lock()
	session := c.session
	c.mu.Unlock()
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	return c.http.Do(req)
}

// readPayload pulls the JSON-RPC message out of the answer, which Streamable
// HTTP allows to arrive either as a plain JSON body or as an SSE stream. The
// stream carries comments and other events too, so it is read for the first
// data: line that parses rather than assumed to start with one.
func readPayload(resp *http.Response) ([]byte, error) {
	limited := io.LimitReader(resp.Body, maxResponseBytes)
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return io.ReadAll(limited)
	}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 0, 64*1024), maxResponseBytes)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if json.Valid([]byte(data)) {
			return []byte(data), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("%w: the event stream carried no JSON-RPC message", ErrProtocol)
}
