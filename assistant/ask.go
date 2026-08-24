package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// An app asking the model a question, from inside its own container.
//
// It answers with text and NO tools -- on either backend. The interactive
// assistant drives an app (it reads and writes files, runs commands, deploys);
// none of that is offered here, because an app whose model could call write_file
// on itself is a self-modifying loop with nobody in the room. What an app gets
// back is text.
//
// The point is that the app never holds the API key or the subscription token.
// Going through hostit means every request is metered onto that app and
// rate-limited against its owner, exactly like an interactive turn. Which model
// answers is the app's choice from what this instance actually has configured
// (see Catalog): a claude-* id runs on the Claude Max subscription, an
// anthropic-* id on the metered API, and an empty choice takes the default.

const (
	// defaultAskTokens is a modest reply for a request that says nothing about
	// length; maxAskTokens is the ceiling, because the budget being spent is
	// the operator's rather than the app's. (Metered-API backend only; the
	// subscription backend has its own limits.)
	defaultAskTokens = 1024
	maxAskTokens     = 8192
	// maxAskMessages and maxAskChars bound one request. A chat app grows its
	// history forever unless something says otherwise, and the first thing an
	// unbounded history does is cost money.
	maxAskMessages = 60
	maxAskChars    = 200_000
)

var (
	// ErrAskInvalid means the request is not a question -- no messages, or a
	// role the API does not take.
	ErrAskInvalid = errors.New("invalid request")
	// ErrAskTooLarge means it is a question, but a bigger one than an app may ask.
	ErrAskTooLarge = errors.New("request too large")
	// ErrAskUnavailable means no backend is configured on this instance.
	ErrAskUnavailable = errors.New("the assistant is not available on this server")
)

// AskMessage is one turn of a conversation. Role is "user" or "assistant";
// system instructions go in AskRequest.System, where the API wants them.
type AskMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// AskRequest is one question.
type AskRequest struct {
	System    string       `json:"system,omitempty"`
	Messages  []AskMessage `json:"messages"`
	Model     string       `json:"model,omitempty"`
	MaxTokens int          `json:"max_tokens,omitempty"`
}

// AskResult is the answer.
type AskResult struct {
	Text  string `json:"text"`
	Model string `json:"model"`
	Usage Usage  `json:"usage"`
}

// AskModels is the catalog of models an app may pick, filtered to what this
// instance actually has configured -- so a container discovers its choices
// rather than guessing. Empty when no backend is available.
func (m *Manager) AskModels() []Option {
	return Catalog(m.creds)
}

// Ask answers one question for an app, unlimited. Prefer AskFor, which also
// applies the owner's rate limit; this exists for callers that have already
// reserved, and for tests.
func (m *Manager) Ask(ctx context.Context, app string, req AskRequest) (AskResult, error) {
	return m.ask(ctx, app, req)
}

// AskFor answers one question and counts it against the OWNER's rate limit, the
// same one interactive turns use -- it is the same budget either way, and an app
// looping on this endpoint is the cheapest way to spend it by accident.
func (m *Manager) AskFor(ctx context.Context, userID, app string, req AskRequest) (AskResult, error) {
	if err := m.reserveRun(userID); err != nil {
		return AskResult{}, err
	}
	defer m.releaseRun(userID)
	return m.ask(ctx, app, req)
}

func (m *Manager) ask(ctx context.Context, app string, req AskRequest) (AskResult, error) {
	option, err := m.askOption(req.Model)
	if err != nil {
		return AskResult{}, err
	}
	if err := validateAskRequest(req); err != nil {
		return AskResult{}, err
	}
	// Route on the chosen model's backend: a claude-* id runs on the
	// subscription sandbox, an anthropic-* id on the metered API.
	if option.Backend == BackendClaude {
		return m.askClaude(ctx, app, req, option)
	}
	return m.askAnthropic(ctx, app, req, option)
}

// askAnthropic answers on the metered API backend.
func (m *Manager) askAnthropic(ctx context.Context, app string, req AskRequest, option Option) (AskResult, error) {
	if m.client == nil {
		return AskResult{}, ErrAskUnavailable
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultAskTokens
	}
	if maxTokens > maxAskTokens {
		// Clamped rather than refused: the app asked for something that sounds
		// reasonable and should get an answer, just a bounded one.
		maxTokens = maxAskTokens
	}
	out := request{Model: option.Model, MaxTokens: maxTokens, Messages: toAPIMessages(req)}
	if s := strings.TrimSpace(req.System); s != "" {
		out.System = []systemBlock{{Type: blockText, Text: s}}
	}
	resp, err := m.client.complete(ctx, out)
	if err != nil {
		return AskResult{}, err
	}
	used := Usage{
		InputTokens:      resp.Usage.InputTokens,
		OutputTokens:     resp.Usage.OutputTokens,
		CacheWriteTokens: resp.Usage.CacheCreationInputTokens,
		CacheReadTokens:  resp.Usage.CacheReadInputTokens,
	}
	m.recordAskUsage(app, used)
	var text strings.Builder
	for _, block := range resp.Content {
		if block.Type == blockText {
			text.WriteString(block.Text)
		}
	}
	return AskResult{Text: text.String(), Model: option.ID, Usage: used}, nil
}

// askClaude answers on the Claude Max subscription backend, as a one-shot,
// tool-less answer (see Sandbox.Answer). The subscription takes a single
// prompt, not a messages array, so the conversation is flattened; max_tokens
// does not apply.
func (m *Manager) askClaude(ctx context.Context, app string, req AskRequest, option Option) (AskResult, error) {
	if m.claude == nil {
		return AskResult{}, ErrAskUnavailable
	}
	text, used, err := m.claude.Answer(ctx, app, option.Model, req.System, toClaudePrompt(req))
	if err != nil {
		return AskResult{}, err
	}
	m.recordAskUsage(app, used)
	return AskResult{Text: text, Model: option.ID, Usage: used}, nil
}

// recordAskUsage meters a call onto the app. Recorded even if it fails to store:
// the answer is already paid for, and losing the accounting is not a reason to
// lose the answer too.
func (m *Manager) recordAskUsage(app string, used Usage) {
	if m.store != nil {
		_ = m.store.RecordUsage(app, used)
	}
}

// askOption resolves the model an app asked for to a configured backend+model,
// defaulting to the head of the catalog (the same default the chat UI uses) when
// the app names nothing. With no backend configured it is unavailable (501); an
// unknown or unconfigured id on a configured instance is refused with the list
// of what this instance offers -- an ALLOWLIST, not a passthrough, since the id
// picks a paid backend.
func (m *Manager) askOption(want string) (Option, error) {
	if len(Catalog(m.creds)) == 0 {
		return Option{}, ErrAskUnavailable // nothing configured at all
	}
	if strings.TrimSpace(want) == "" {
		option, _ := Default(m.creds) // ok: the catalog is non-empty
		return option, nil
	}
	option, ok := Lookup(m.creds, want)
	if !ok {
		return Option{}, fmt.Errorf("%w: no model %q; this server offers %s",
			ErrAskInvalid, want, availableModelIDs(m.creds))
	}
	return option, nil
}

// AskDefaultModel is the model id an empty choice resolves to -- the head of the
// catalog, the same default a chat turn takes. Empty when no backend is
// configured. The discovery endpoint reports it so an app knows what it gets.
func (m *Manager) AskDefaultModel() string {
	if o, ok := Default(m.creds); ok {
		return o.ID
	}
	return ""
}

// availableModelIDs is the comma-separated ids an app may pick, for an error
// message that tells it what to send instead.
func availableModelIDs(creds Credentials) string {
	cat := Catalog(creds)
	if len(cat) == 0 {
		return "(none -- the assistant is not configured)"
	}
	ids := make([]string, 0, len(cat))
	for _, o := range cat {
		ids = append(ids, o.ID)
	}
	return strings.Join(ids, ", ")
}

// validateAskMessages bounds one request, backend-agnostically: it must be a
// conversation of user/assistant turns, not too many and not too large.
func validateAskRequest(req AskRequest) error {
	if len(req.Messages) == 0 {
		return fmt.Errorf("%w: no messages to send", ErrAskInvalid)
	}
	if len(req.Messages) > maxAskMessages {
		return fmt.Errorf("%w: %d messages, the most an app may send is %d",
			ErrAskTooLarge, len(req.Messages), maxAskMessages)
	}
	total := len(req.System)
	for i, msg := range req.Messages {
		if msg.Role != roleUser && msg.Role != roleAssistant {
			return fmt.Errorf(`%w: message %d has role %q; only "user" and "assistant" are messages, and system instructions go in "system"`,
				ErrAskInvalid, i, msg.Role)
		}
		if strings.TrimSpace(msg.Content) == "" {
			return fmt.Errorf("%w: message %d is empty", ErrAskInvalid, i)
		}
		total += len(msg.Content)
		if total > maxAskChars {
			return fmt.Errorf("%w: the conversation is over %d characters", ErrAskTooLarge, maxAskChars)
		}
	}
	return nil
}

// toAPIMessages converts a validated request to the API's message shape.
func toAPIMessages(req AskRequest) []Message {
	out := make([]Message, 0, len(req.Messages))
	for _, msg := range req.Messages {
		out = append(out, Message{Role: msg.Role, Content: []ContentBlock{{Type: blockText, Text: msg.Content}}})
	}
	return out
}

// toClaudePrompt flattens a validated conversation into one prompt for the
// subscription backend, which takes a single prompt rather than a messages
// array. A one-message request is just its text; a conversation is rendered with
// speaker labels so the model can follow the turns.
func toClaudePrompt(req AskRequest) string {
	if len(req.Messages) == 1 {
		return req.Messages[0].Content
	}
	var b strings.Builder
	for _, msg := range req.Messages {
		if msg.Role == roleAssistant {
			b.WriteString("Assistant: ")
		} else {
			b.WriteString("User: ")
		}
		b.WriteString(msg.Content)
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}
