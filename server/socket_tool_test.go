package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSocketSelfTool exercises the mediation boundary the sandboxed Claude Max
// backend reaches its tools through: a tool call over the peercred-scoped socket
// runs against the calling app and nothing else. It writes a file and reads it
// back, proving the change actually lands in the app's home, and confirms a tool
// error comes back as a result (is_error) rather than an HTTP error.
func TestSocketSelfTool(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	rr := request(t, s.API(), "POST", "/api/apps", `{"name":"blog"}`, testToken)
	require.Equal(t, http.StatusCreated, rr.Code)
	s.usernameForUID = func(uid int) (string, error) { return "blog", nil }

	// write_file: the change flows socket -> daemon -> Manager -> app home.
	w := socketToolRequest(t, s, "write_file", `{"path":"public/index.html","content":"<h1>hi</h1>"}`)
	require.Equal(t, http.StatusOK, w.Code)
	assert.False(t, toolResp(t, w).IsError)

	// read_file sees exactly what was written -- so the mediated write really landed.
	w = socketToolRequest(t, s, "read_file", `{"path":"public/index.html"}`)
	require.Equal(t, http.StatusOK, w.Code)
	res := toolResp(t, w)
	assert.False(t, res.IsError)
	assert.Contains(t, res.Output, "<h1>hi</h1>")

	// list_files shows the file the model just wrote.
	w = socketToolRequest(t, s, "list_files", `{"path":"public"}`)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, toolResp(t, w).Output, "index.html")

	// A missing file is a tool error (is_error), not an HTTP failure: the model
	// reads it and adapts, exactly as with a failed shell command.
	w = socketToolRequest(t, s, "read_file", `{"path":"does-not-exist"}`)
	require.Equal(t, http.StatusOK, w.Code)
	assert.True(t, toolResp(t, w).IsError)
}

func socketToolRequest(t *testing.T, s *Server, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/self/tool/"+name, strings.NewReader(body))
	req = req.WithContext(withPeerUID(req.Context(), 1234))
	w := httptest.NewRecorder()
	s.socketHandler().ServeHTTP(w, req)
	return w
}

func toolResp(t *testing.T, w *httptest.ResponseRecorder) apiToolResponse {
	t.Helper()
	var resp apiToolResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp
}
