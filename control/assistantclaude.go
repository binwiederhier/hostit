package control

import (
	"context"
	"errors"

	"heckel.io/hostit/assistant"
	"heckel.io/hostit/node/api"
)

// claudeBackend adapts the node's assistant verbs to assistant.ClaudeRunner: it
// asks the app's node to run one sandboxed `claude -p` turn and maps the node's
// streamed events to the assistant's own events, so the web UI streams a Claude
// Max turn exactly as it does an API turn. The container runs on the node the
// app lives on; control holds only the subscription token, which it passes down
// per turn (never stored on a node).
type claudeBackend struct {
	node  NodeAgent
	token string
}

var _ assistant.ClaudeRunner = (*claudeBackend)(nil)

// RunTurn runs one turn and returns its token usage. A tool that errors is a
// normal event (the model adapts); only a failed run (claude crashed, the
// sandbox could not start) returns an error.
func (c *claudeBackend) RunTurn(ctx context.Context, appName, prompt, systemPrompt string, images []assistant.Attachment, publish func(assistant.Event)) (assistant.Usage, error) {
	var usage assistant.Usage
	var runErr error
	lastTool := "" // tool_result events do not name their tool; pair by order

	spec := &api.AssistantTurnSpec{
		Name:         appName,
		Prompt:       prompt,
		SystemPrompt: systemPrompt,
		Images:       toAPIImages(images),
		OAuthToken:   c.token,
	}
	err := c.node.RunAssistantTurn(ctx, spec, func(ev *api.AssistantEvent) {
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
				// Surface the turn's tokens to the UI (the node reports them once, at
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

// Answer runs a one-shot, tool-less answer on the subscription -- what the
// app-facing /api/container/assistant endpoint needs from the Claude Max
// backend. It runs on the app's node: `claude -p --output-format json` with no
// tools, returning just the answer and usage.
func (c *claudeBackend) Answer(ctx context.Context, appName, model, system, prompt string) (string, assistant.Usage, error) {
	text, usage, err := c.node.AnswerAssistant(ctx, &api.AssistantAnswerSpec{
		Name:       appName,
		Model:      model,
		System:     system,
		Prompt:     prompt,
		OAuthToken: c.token,
	})
	if err != nil {
		return "", assistant.Usage{}, err
	}
	var u assistant.Usage
	if usage != nil {
		u = assistant.Usage{
			InputTokens:      int(usage.InputTokens),
			OutputTokens:     int(usage.OutputTokens),
			CacheWriteTokens: int(usage.CacheWriteTokens),
			CacheReadTokens:  int(usage.CacheReadTokens),
		}
	}
	return text, u, nil
}

// toAPIImages converts the assistant's uploaded attachments to the wire image
// blocks the node feeds claude; only the media type and base64 bytes cross.
func toAPIImages(images []assistant.Attachment) []api.AssistantImage {
	if len(images) == 0 {
		return nil
	}
	out := make([]api.AssistantImage, 0, len(images))
	for _, a := range images {
		out = append(out, api.AssistantImage{MediaType: a.MediaType, Data: a.Data})
	}
	return out
}
