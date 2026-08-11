package server

import (
	"context"
	"errors"

	"heckel.io/hostit/app"
	"heckel.io/hostit/assistant"
)

// claudeBackend adapts app.AssistantSandbox to assistant.ClaudeRunner: it drives
// one sandboxed `claude -p` turn and maps its stream to the assistant's own
// events, so the web UI streams a Claude Max turn exactly as it does an API turn.
type claudeBackend struct {
	sandbox *app.AssistantSandbox
}

var _ assistant.ClaudeRunner = (*claudeBackend)(nil)

// RunTurn runs one turn and returns its token usage. A tool that errors is a
// normal event (the model adapts); only a failed run (claude crashed, the
// sandbox could not start) returns an error.
func (c *claudeBackend) RunTurn(ctx context.Context, appName, prompt, systemPrompt string, publish func(assistant.Event)) (assistant.Usage, error) {
	var usage assistant.Usage
	var runErr error
	lastTool := "" // tool_result events do not name their tool; pair by order

	err := c.sandbox.RunTurn(ctx, appName, prompt, systemPrompt, func(ev app.AssistantStreamEvent) {
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
