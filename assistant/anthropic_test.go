package assistant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientSendsAuthAndParsesReply(t *testing.T) {
	t.Parallel()
	var gotBody request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "test-key", r.Header.Get("x-api-key"))
		assert.Equal(t, anthropicVersion, r.Header.Get("anthropic-version"))
		assert.Equal(t, "application/json", r.Header.Get("content-type"))
		b, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(b, &gotBody))
		_, _ = w.Write([]byte(`{"id":"msg_1","role":"assistant","stop_reason":"end_turn",
			"content":[{"type":"text","text":"hello"}],"usage":{"input_tokens":10,"output_tokens":3}}`))
	}))
	defer srv.Close()

	c := NewClient("test-key")
	c.url = srv.URL
	resp, err := c.complete(context.Background(), request{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	})
	require.NoError(t, err)
	assert.Equal(t, "end_turn", resp.StopReason)
	require.Len(t, resp.Content, 1)
	assert.Equal(t, "hello", resp.Content[0].Text)
	assert.Equal(t, "test-model", gotBody.Model)
}

func TestClientReportsAPIError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad model"}}`))
	}))
	defer srv.Close()

	c := NewClient("test-key")
	c.url = srv.URL
	_, err := c.complete(context.Background(), request{Model: "nope"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad model")
}
