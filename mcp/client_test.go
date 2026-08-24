package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeToolServer is an MCP server that answers the three calls hostit makes.
// It records what it was sent, because half of what matters here is not the
// answer but the request: the session header, the bearer token, the protocol
// version.
type fakeToolServer struct {
	*httptest.Server
	requireToken string
	sawToken     string
	sawSession   []string
	streamed     bool // answer as text/event-stream rather than plain JSON
	toolError    bool // answer tools/call with isError, not a transport error
}

func newFakeToolServer(t *testing.T, f *fakeToolServer) *fakeToolServer {
	t.Helper()
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.requireToken != "" && r.Header.Get("Authorization") != "Bearer "+f.requireToken {
			w.Header().Set("WWW-Authenticate", `Bearer`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.sawToken = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.sawSession = append(f.sawSession, r.Header.Get("Mcp-Session-Id"))

		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
			Params json.RawMessage `json:"params"`
		}
		require.NoError(t, json.Unmarshal(body, &req))

		var result string
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			result = `{"protocolVersion":"2025-06-18","serverInfo":{"name":"fake","version":"1.0"},"capabilities":{"tools":{}}}`
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/list":
			result = `{"tools":[
				{"name":"search","description":"Search the mailbox","inputSchema":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}},
				{"name":"send","description":"Send a message","inputSchema":{"type":"object"}}
			]}`
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			require.NoError(t, json.Unmarshal(req.Params, &p))
			if f.toolError {
				result = `{"isError":true,"content":[{"type":"text","text":"mailbox is locked"}]}`
				break
			}
			result = `{"content":[{"type":"text","text":"found ` + p.Arguments["q"].(string) + `"}]}`
		default:
			http.Error(w, "unexpected method "+req.Method, http.StatusBadRequest)
			return
		}
		// Compacted, because SSE frames one event per line: a pretty-printed
		// payload would break the framing, which no real server does.
		var payload bytes.Buffer
		require.NoError(t, json.Compact(&payload,
			[]byte(`{"jsonrpc":"2.0","id":`+string(req.ID)+`,"result":`+result+`}`)))
		if f.streamed {
			w.Header().Set("Content-Type", "text/event-stream")
			// Real servers send comments and other events around the one that
			// carries the answer; a reader that assumes the first line is the
			// payload works against a toy and fails against a real server.
			_, _ = io.WriteString(w, ": keep-alive\n\nevent: message\ndata: "+payload.String()+"\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload.Bytes())
	}))
	t.Cleanup(f.Server.Close)
	return f
}

func TestListToolsReturnsTheServersCatalog(t *testing.T) {
	f := newFakeToolServer(t, &fakeToolServer{})
	c := NewClient(f.URL, "")

	tools, err := c.ListTools(context.Background())
	require.NoError(t, err)
	require.Len(t, tools, 2)
	assert.Equal(t, "search", tools[0].Name)
	assert.Equal(t, "Search the mailbox", tools[0].Description)
	assert.Contains(t, string(tools[0].InputSchema), "required")
}

func TestTheSessionFromInitializeIsSentOnEveryLaterCall(t *testing.T) {
	f := newFakeToolServer(t, &fakeToolServer{})
	c := NewClient(f.URL, "")

	_, err := c.ListTools(context.Background())
	require.NoError(t, err)

	require.NotEmpty(t, f.sawSession)
	assert.Empty(t, f.sawSession[0], "initialize is what establishes the session")
	assert.Equal(t, "sess-1", f.sawSession[len(f.sawSession)-1],
		"a server that hands out a session id refuses later calls without it")
}

func TestTheBearerTokenIsSent(t *testing.T) {
	f := newFakeToolServer(t, &fakeToolServer{requireToken: "tok-abc"})
	c := NewClient(f.URL, "tok-abc")

	_, err := c.ListTools(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "tok-abc", f.sawToken)
}

func TestAServerThatWantsAuthIsReportedAsSuch(t *testing.T) {
	f := newFakeToolServer(t, &fakeToolServer{requireToken: "tok-abc"})
	c := NewClient(f.URL, "stale-token")

	_, err := c.ListTools(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnauthorized,
		"so the caller can tell 'reconnect this' apart from 'this is broken'")
}

func TestCallToolReturnsTheTextContent(t *testing.T) {
	f := newFakeToolServer(t, &fakeToolServer{})
	c := NewClient(f.URL, "")

	out, err := c.CallTool(context.Background(), "search", map[string]any{"q": "invoices"})
	require.NoError(t, err)
	assert.False(t, out.IsError)
	assert.Equal(t, "found invoices", out.Text)
}

// A tool that fails is not a transport failure: the call worked and the answer
// is bad news. Flattening the two would make "the mailbox is locked" look like
// "the server is down", and the app can do something about only one of them.
func TestAToolThatFailsIsAResultNotAnError(t *testing.T) {
	f := newFakeToolServer(t, &fakeToolServer{toolError: true})
	c := NewClient(f.URL, "")

	out, err := c.CallTool(context.Background(), "search", map[string]any{"q": "x"})
	require.NoError(t, err)
	assert.True(t, out.IsError)
	assert.Contains(t, out.Text, "locked")
}

// Streamable HTTP lets a server answer either way for the same request, so a
// client that handles only JSON works until the day it does not.
func TestAnSSEAnswerIsReadTheSameAsAJSONOne(t *testing.T) {
	f := newFakeToolServer(t, &fakeToolServer{streamed: true})
	c := NewClient(f.URL, "")

	tools, err := c.ListTools(context.Background())
	require.NoError(t, err)
	require.Len(t, tools, 2)
	assert.Equal(t, "send", tools[1].Name)
}
