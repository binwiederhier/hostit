package sandbox

import (
	"encoding/json"
	"fmt"
	"strings"

	"heckel.io/hostit/node/api"
)

// parseAssistantStreamLine turns one claude stream-json line into normalized
// events. It returns ALL meaningful events for the line (nil for a line it does not
// care about), so a message that batches several blocks -- e.g. parallel tool
// calls, or several tool_results together -- surfaces every one. Dropping the extra
// blocks would leave a tool spinning forever on a result that never arrived.
func parseAssistantStreamLine(line []byte) []*api.AssistantEvent {
	var raw struct {
		Type    string          `json:"type"`
		Subtype string          `json:"subtype"`
		Model   string          `json:"model"`
		Tools   []string        `json:"tools"`
		Message json.RawMessage `json:"message"`
		Result  string          `json:"result"`
		IsError bool            `json:"is_error"`
		Usage   json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil
	}
	switch raw.Type {
	case "system":
		if raw.Subtype == "init" {
			return []*api.AssistantEvent{{Type: evtInit, Model: raw.Model, Tools: raw.Tools}}
		}
	case "assistant", "user":
		return blockEvents(raw.Message)
	case "result":
		return []*api.AssistantEvent{{Type: evtResult, Result: raw.Result, IsError: raw.IsError, Usage: parseUsage(raw.Usage)}}
	}
	return nil
}

// blockEvents returns one event per meaningful content block in a message.
func blockEvents(raw json.RawMessage) []*api.AssistantEvent {
	var msg struct {
		Content []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			Thinking  string          `json:"thinking"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			Content   json.RawMessage `json:"content"`
			IsError   bool            `json:"is_error"`
			ToolUseID string          `json:"tool_use_id"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil
	}
	var out []*api.AssistantEvent
	for _, b := range msg.Content {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				out = append(out, &api.AssistantEvent{Type: evtText, Text: b.Text})
			}
		case "thinking":
			if strings.TrimSpace(b.Thinking) != "" {
				out = append(out, &api.AssistantEvent{Type: evtThinking, Text: b.Thinking})
			}
		case "tool_use":
			out = append(out, &api.AssistantEvent{Type: evtToolUse, Tool: stripToolPrefix(b.Name), Input: string(b.Input)})
		case "tool_result":
			out = append(out, &api.AssistantEvent{Type: evtToolResult, Output: toolResultText(b.Content), IsError: b.IsError})
		}
	}
	return out
}

// toolResultText pulls the text out of a tool_result content, which the API
// carries either as a bare string or as an array of text blocks.
func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		parts := make([]string, 0, len(blocks))
		for _, b := range blocks {
			parts = append(parts, b.Text)
		}
		return strings.Join(parts, " ")
	}
	return string(raw)
}

func parseUsage(raw json.RawMessage) *api.AssistantUsage {
	if len(raw) == 0 {
		return nil
	}
	var u struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	}
	if json.Unmarshal(raw, &u) != nil {
		return nil
	}
	return &api.AssistantUsage{
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
	}
}

// stripToolPrefix turns "mcp__hostit__write_file" into "write_file" for display.
func stripToolPrefix(name string) string {
	return strings.TrimPrefix(name, mcpToolPrefix)
}

// parseAnswer decodes `claude -p --output-format json`, a single result object
// the same shape as the stream's terminal "result" event, into the answer text
// and its usage. Its errors carry detail for the caller to LOG; the caller
// returns ErrAnswerBackend to the tenant rather than the detail.
func parseAnswer(out []byte) (string, *api.AssistantUsage, error) {
	var res struct {
		Result  string          `json:"result"`
		IsError bool            `json:"is_error"`
		Usage   json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return "", nil, fmt.Errorf("claude returned something that is not a result: %w", err)
	}
	if res.IsError {
		return "", nil, fmt.Errorf("claude ended with an error: %s", res.Result)
	}
	return res.Result, parseUsage(res.Usage), nil
}
