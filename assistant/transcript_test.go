package assistant

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRenderTranscript(t *testing.T) {
	items := []Item{
		{Kind: "user", Text: "add a dark mode toggle"},
		{Kind: "text", Text: "Sure, adding one now."},
		{Kind: "tool", Tool: "write_file", Input: `{"path":"index.html"}`, Output: "wrote index.html"},
		{Kind: "tool", Tool: "deploy", Input: "{}", Output: "deployed"},
		{Kind: "tool", Tool: "read_logs", Output: "boom", IsError: true},
		{Kind: "text", Text: "Done, the toggle is in the header."},
	}
	out := RenderTranscript(items)

	// Both sides of the conversation and the tools the assistant ran are present,
	// so an agent reading this has the full history of what was tried.
	assert.Contains(t, out, "add a dark mode toggle")
	assert.Contains(t, out, "Done, the toggle is in the header.")
	assert.Contains(t, out, "write_file")
	assert.Contains(t, out, "wrote index.html")
	assert.Contains(t, out, "deployed")
	// A failed tool is labelled as an error, not silently shown as normal output.
	assert.Contains(t, out, "Error: boom")
	// An empty input object is noise, not context, so it is left out.
	assert.NotContains(t, out, "Input: {}")
}

func TestRenderTranscriptEmpty(t *testing.T) {
	assert.Equal(t, "", RenderTranscript(nil))
}
