package assistant

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPromptCachingBreakpoints(t *testing.T) {
	t.Parallel()
	req := request{
		Model:    "test",
		System:   cachedSystem("you are a helpful assistant"),
		Tools:    cachedToolDefs(),
		Messages: cacheConversation([]Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}}),
	}

	// The system prompt is sent as a cache-marked block, not a bare string, so the
	// large stable instructions are read once and reused.
	require.Len(t, req.System, 1)
	require.NotNil(t, req.System[0].CacheControl)
	assert.Equal(t, "ephemeral", req.System[0].CacheControl.Type)

	// The tools block is cached (a breakpoint on the last tool).
	require.NotEmpty(t, req.Tools)
	assert.NotNil(t, req.Tools[len(req.Tools)-1].CacheControl)

	// The conversation tail is cached, so prior turns are a reusable prefix.
	last := req.Messages[len(req.Messages)-1]
	require.NotEmpty(t, last.Content)
	assert.NotNil(t, last.Content[len(last.Content)-1].CacheControl)

	// And it all reaches the wire format.
	b, err := json.Marshal(req)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"cache_control":{"type":"ephemeral"}`)
}

func TestCacheConversationDoesNotMutateStoredHistory(t *testing.T) {
	t.Parallel()
	history := []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}}
	out := cacheConversation(history)
	assert.NotNil(t, out[0].Content[0].CacheControl, "the sent copy is cached")
	assert.Nil(t, history[0].Content[0].CacheControl, "the stored history stays uncached")
}
