package assistant

import "fmt"

// Item is one thing the chat shows: a user message, a line of the assistant's
// reply, or a tool call with its result. It is what a page loading an existing
// conversation renders, and it mirrors the Events streamed during a live run.
type Item struct {
	Kind    string `json:"kind"` // user | text | tool
	Text    string `json:"text,omitempty"`
	Tool    string `json:"tool,omitempty"`
	Input   string `json:"input,omitempty"`
	Output  string `json:"output,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
}

// toItems rebuilds the display transcript from the stored API messages: user text
// becomes a user bubble, the assistant's text and tool calls their own items, and
// each tool result is folded back onto the tool call it answers. Thinking is not
// stored, so it does not reappear on reload.
func toItems(history []Message) []Item {
	items := make([]Item, 0, len(history))
	toolIndex := make(map[string]int) // tool_use id -> index in items
	for _, msg := range history {
		for _, b := range msg.Content {
			switch b.Type {
			case "text":
				if msg.Role == "user" {
					items = append(items, Item{Kind: "user", Text: b.Text})
				} else {
					items = append(items, Item{Kind: "text", Text: b.Text})
				}
			case "tool_use":
				items = append(items, Item{Kind: "tool", Tool: b.Name, Input: string(b.Input)})
				toolIndex[b.ID] = len(items) - 1
			case "tool_result":
				if idx, ok := toolIndex[b.ToolUseID]; ok {
					items[idx].Output = contentString(b.Content)
					items[idx].IsError = b.IsError
				}
			}
		}
	}
	return items
}

// contentString renders a tool_result's content, which is a string once it has
// round-tripped through JSON
func contentString(content any) string {
	if content == nil {
		return ""
	}
	if s, ok := content.(string); ok {
		return s
	}
	return fmt.Sprint(content)
}
