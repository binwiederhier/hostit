package control

import (
	"context"
	"errors"

	"heckel.io/hostit/assistant"
)

// claudeBackend adapts assistant.Sandbox to assistant.ClaudeRunner: it drives
// one sandboxed `claude -p` turn and maps its stream to the assistant's own
// events, so the web UI streams a Claude Max turn exactly as it does an API turn.
type claudeBackend struct {
	sandbox *assistant.Sandbox
}

var _ assistant.ClaudeRunner = (*claudeBackend)(nil)

// RunTurn runs one turn and returns its token usage. A tool that errors is a
// normal event (the model adapts); only a failed run (claude crashed, the
// sandbox could not start) returns an error.
func (c *claudeBackend) RunTurn(ctx context.Context, appName, prompt, systemPrompt string, images []assistant.Attachment, publish func(assistant.Event)) (assistant.Usage, error) {
	var usage assistant.Usage
	var runErr error
	lastTool := "" // tool_result events do not name their tool; pair by order

	err := c.sandbox.RunTurn(ctx, appName, prompt, systemPrompt, images, func(ev assistant.StreamEvent) {
		switch ev.Type {
		case "text":
			publish(assistant.Event{Type: "text", Text: ev.Text})
		case "thinking":
			publish(assistant.Event{Type: "thinking", Text: ev.Text})
		case "tool_use":
			lastTool = ev.Tool
			publish(assistant.Event{Type: "tool_use", Tool: ev.Tool, Input: ev.Input})
		case "tool_result":
			publish(assistant.Event{Type: "tool_result", Tool: lastTool, Output: ev.Output, IsError: ev.IsError})
		case "result":
			if ev.Usage != nil {
				usage = assistant.Usage{
					InputTokens:      int(ev.Usage.InputTokens),
					OutputTokens:     int(ev.Usage.OutputTokens),
					CacheWriteTokens: int(ev.Usage.CacheWriteTokens),
					CacheReadTokens:  int(ev.Usage.CacheReadTokens),
				}
				// Surface the turn's tokens to the UI (the sandbox reports them once, at
				// the end) so the chat can show a per-turn token count.
				u := usage
				publish(assistant.Event{Type: "usage", Usage: &u})
			}
			if ev.IsError {
				runErr = errors.New("assistant turn ended with an error")
			}
		case "error":
			runErr = errors.New(ev.ErrorMsg)
		}
	})
	if err != nil && runErr == nil {
		runErr = err
	}
	return usage, runErr
}
