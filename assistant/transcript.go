package assistant

import (
	"fmt"
	"strings"
)

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
	Model   string `json:"model,omitempty"` // on an assistant reply: which model produced it
	Time    int64  `json:"time,omitempty"`  // on an assistant reply: when (unix seconds)
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
					if strings.HasPrefix(b.Text, attachmentNotePrefix) {
						continue // the attachment note is for the model, not the display
					}
					items = append(items, Item{Kind: "user", Text: b.Text})
				} else {
					items = append(items, Item{Kind: "text", Text: b.Text, Model: msg.Model, Time: msg.Time})
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

// RenderTranscript turns a session's display items into a plain-markdown log an
// external agent can read as context: who said what, and every tool the built-in
// assistant ran with its result. It is the text form of what the chat UI shows,
// so an agent the owner switches to continues the work instead of starting cold.
func RenderTranscript(items []Item) string {
	var b strings.Builder
	for _, it := range items {
		switch it.Kind {
		case "user":
			fmt.Fprintf(&b, "## User\n\n%s\n\n", strings.TrimSpace(it.Text))
		case "text":
			fmt.Fprintf(&b, "## Assistant\n\n%s\n\n", strings.TrimSpace(it.Text))
		case "tool":
			fmt.Fprintf(&b, "### Tool: %s\n", it.Tool)
			// An empty input object is noise, not context, so leave it out.
			if in := strings.TrimSpace(it.Input); in != "" && in != "{}" {
				fmt.Fprintf(&b, "Input: %s\n", in)
			}
			out := strings.TrimSpace(it.Output)
			if out == "" {
				out = "(no output)"
			}
			label := "Output"
			if it.IsError {
				label = "Error"
			}
			fmt.Fprintf(&b, "%s: %s\n\n", label, out)
		}
	}
	return strings.TrimSpace(b.String())
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
