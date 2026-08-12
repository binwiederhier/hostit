package assistant

import (
	"encoding/json"
)

const (
	// Anthropic message roles: the author of a turn (Message.Role) and the role
	// checks on stored messages. A user turn carries human text or our tool results;
	// an assistant turn is the model's reply.
	roleUser      = "user"
	roleAssistant = "assistant"
)

const (
	// Anthropic content-block types: the tag on ContentBlock.Type. One turn's content
	// is a list of these, and claude's stream carries the same tags on its blocks.
	blockText             = "text"
	blockThinking         = "thinking"
	blockRedactedThinking = "redacted_thinking"
	blockToolUse          = "tool_use"
	blockToolResult       = "tool_result"
	blockImage            = "image"
)

const (
	// Assistant event vocabulary: the Type on events streamed to watchers (Event.Type)
	// and the normalized claude-stream events (StreamEvent.Type) the server maps onto
	// them. These strings are a browser contract -- the web UI keys off them -- so
	// their values must stay byte-identical.
	evtModel      = "model"
	evtUser       = "user"
	evtText       = "text"
	evtThinking   = "thinking"
	evtToolUse    = "tool_use"
	evtToolResult = "tool_result"
	evtUsage      = "usage"
	evtDone       = "done"
	evtError      = "error"
	evtNotice     = "notice"
	evtPaused     = "paused"
	evtInit       = "init"
	evtResult     = "result"
)

// Message is one turn in the conversation sent to the model. Content is a list of
// blocks because a single turn can carry several things at once: the model's
// thinking, its text, and its tool calls; our tool results go back the same way.
type Message struct {
	Role    string         `json:"role"` // "user" or "assistant"
	Content []ContentBlock `json:"content"`
	// Model is the mode/model that produced an assistant message (External Claude or
	// a model id). It is our own metadata for the UI, stripped before a message is
	// sent to the API (which rejects unknown fields); empty on user messages.
	Model string `json:"model,omitempty"`
	// Time is when an assistant message was produced (unix seconds), shown in the
	// reply's info popover. Our own metadata, likewise stripped before the API.
	Time int64 `json:"time,omitempty"`
}

// ContentBlock is one piece of a message. The Anthropic API uses a tagged union
// keyed on Type; we keep every field on one struct with omitempty. Thinking
// blocks are shown to the user but stripped before a turn is fed back (see
// withoutThinking), so their internal fields do not need to round-trip.
type ContentBlock struct {
	Type string `json:"type"`

	// type: text
	Text string `json:"text,omitempty"`

	// type: thinking / redacted_thinking
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"`

	// type: tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// type: tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   any    `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`

	// type: image
	Source *ImageSource `json:"source,omitempty"`

	// A prompt-cache breakpoint on this block (set on the tail of the conversation
	// so the prior turns are reused rather than re-read each message).
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

// ImageSource is an inline image the model can see (a chat attachment), sent as
// base64 in the shape the Anthropic API expects.
type ImageSource struct {
	Type      string `json:"type"`       // "base64"
	MediaType string `json:"media_type"` // e.g. "image/png"
	Data      string `json:"data"`       // base64-encoded bytes
}

// Attachment is a file the user uploaded with a message: already saved in the
// app's home at Path. For images, Data carries the base64 bytes so the model can
// see it; other files are only referenced by Path.
type Attachment struct {
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
	Data      string `json:"data,omitempty"`
}

// Tool is a function the model may call. InputSchema is a JSON Schema object
// describing the arguments; the model fills it in and we dispatch on Name.
type Tool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	CacheControl *cacheControl   `json:"cache_control,omitempty"`
}

// thinkingConfig turns on extended thinking. Current models use "adaptive" (the
// model decides how much to think); it returns that reasoning as thinking blocks
// we stream to the UI and feed back on the next turn.
type thinkingConfig struct {
	Type string `json:"type"` // "adaptive"
}

// outputConfig tunes how hard the model works; effort trades latency and tokens
// for thoroughness
type outputConfig struct {
	Effort string `json:"effort,omitempty"` // "low" | "medium" | "high"
}

// cacheControl marks a block as a prompt-cache breakpoint: Anthropic reuses the
// prefix up to it across requests instead of re-reading it, cutting the cost of
// the large, stable system prompt + tools and the growing conversation.
type cacheControl struct {
	Type string `json:"type"`          // "ephemeral"
	TTL  string `json:"ttl,omitempty"` // "" for the 5-minute default, "1h" for the extended cache
}

// ephemeralCache is the standard (5-minute) cache breakpoint; ephemeral1hCache is
// the 1-hour breakpoint (no beta header needed) for the large, stable prefix that
// stays warm across a whole session. The 1-hour write costs ~2x vs ~1.25x, which
// pays off since the prefix is read every turn and rewritten only rarely.
var (
	ephemeralCache   = &cacheControl{Type: "ephemeral"}
	ephemeral1hCache = &cacheControl{Type: "ephemeral", TTL: "1h"}
)

// systemBlock is one block of the system prompt; sent as an array (not a bare
// string) so it can carry a cache_control breakpoint.
type systemBlock struct {
	Type         string        `json:"type"` // "text"
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

// request is the body of POST /v1/messages
type request struct {
	Model        string          `json:"model"`
	MaxTokens    int             `json:"max_tokens"`
	System       []systemBlock   `json:"system,omitempty"`
	Messages     []Message       `json:"messages"`
	Tools        []Tool          `json:"tools,omitempty"`
	Thinking     *thinkingConfig `json:"thinking,omitempty"`
	OutputConfig *outputConfig   `json:"output_config,omitempty"`
}

// response is the model's reply. StopReason is "tool_use" when it wants us to run
// tools and feed the results back, "end_turn" when it is done.
type response struct {
	ID         string         `json:"id"`
	Role       string         `json:"role"`
	Content    []ContentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      usage          `json:"usage"`
}

type usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// apiError is the body the API returns on a non-2xx response
type apiError struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Event is one thing that happened during a run, streamed to the client as it
// happens so a phone watching the build sees the loop unfold: the model's
// thinking, its text, each tool it calls, and each result.
type Event struct {
	Type    string `json:"type"` // model | thinking | text | tool_use | tool_result | usage | done | error
	Text    string `json:"text,omitempty"`
	Tool    string `json:"tool,omitempty"`
	Input   string `json:"input,omitempty"`
	Output  string `json:"output,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
	Error   string `json:"error,omitempty"`
	// Usage carries the running token totals for the turn on a "usage" event, so the
	// UI can show a live counter as work happens.
	Usage *Usage `json:"usage,omitempty"`
}

// ExecResult is what running a command in the app's container produced. It
// mirrors app.ExecResult but lives here so this package does not depend on the
// app package: the server adapts one to the other.
type ExecResult struct {
	Output    string
	ExitCode  int
	Truncated bool
	TimedOut  bool
}
