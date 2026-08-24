package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// An app asking the model a question, from inside its own container.
//
// This is INFERENCE and nothing else. The interactive assistant drives an app --
// it reads and writes files, runs commands, deploys -- and none of that is
// offered here: an app that could call write_file on itself through this is a
// self-modifying loop with nobody in the room. What an app gets is text.
//
// The point is that the app never holds the API key. An app that held it could
// spend the operator's money without limit and without being seen; going
// through hostit means every request is metered onto that app and rate-limited
// against its owner, exactly like an interactive turn.

const (
	// defaultAskTokens is a modest reply for a request that says nothing about
	// length; maxAskTokens is the ceiling, because the budget being spent is
	// the operator's rather than the app's.
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
	// ErrAskUnavailable means no metered backend is configured on this instance.
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
	if m.client == nil {
		return AskResult{}, ErrAskUnavailable
	}
	msgs, err := askMessages(req)
	if err != nil {
		return AskResult{}, err
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
	model, err := askModel(req.Model)
	if err != nil {
		return AskResult{}, err
	}
	out := request{Model: model, MaxTokens: maxTokens, Messages: msgs}
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
	// Recorded even if it fails to store: the answer is already paid for, and
	// losing the accounting is not a reason to lose the answer too.
	if m.store != nil {
		_ = m.store.RecordUsage(app, used)
	}
	var text strings.Builder
	for _, block := range resp.Content {
		if block.Type == blockText {
			text.WriteString(block.Text)
		}
	}
	return AskResult{Text: text.String(), Model: model, Usage: used}, nil
}

// askModel resolves the model an app asked for, defaulting to the cheapest one
// that is still good at this. An app looping over log lines every minute should
// not silently be doing it on the most expensive model available, and an app
// that wants a better one can say so by slug.
//
// An ALLOWLIST rather than a passthrough: the string reaches a paid API, and
// "whatever the app typed" is not something to forward on that basis.
func askModel(want string) (string, error) {
	models := anthropicBackend{}.Models()
	if strings.TrimSpace(want) == "" {
		return models[len(models)-1].Model, nil
	}
	for _, m := range models {
		if want == m.Slug || want == m.Model {
			return m.Model, nil
		}
	}
	names := make([]string, 0, len(models))
	for _, m := range models {
		names = append(names, m.Slug)
	}
	return "", fmt.Errorf("%w: no model %q; this server offers %s",
		ErrAskInvalid, want, strings.Join(names, ", "))
}

// askMessages validates and converts the conversation.
func askMessages(req AskRequest) ([]Message, error) {
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("%w: no messages to send", ErrAskInvalid)
	}
	if len(req.Messages) > maxAskMessages {
		return nil, fmt.Errorf("%w: %d messages, the most an app may send is %d",
			ErrAskTooLarge, len(req.Messages), maxAskMessages)
	}
	total := len(req.System)
	out := make([]Message, 0, len(req.Messages))
	for i, msg := range req.Messages {
		if msg.Role != "user" && msg.Role != "assistant" {
			return nil, fmt.Errorf(`%w: message %d has role %q; only "user" and "assistant" are messages, and system instructions go in "system"`,
				ErrAskInvalid, i, msg.Role)
		}
		if strings.TrimSpace(msg.Content) == "" {
			return nil, fmt.Errorf("%w: message %d is empty", ErrAskInvalid, i)
		}
		total += len(msg.Content)
		if total > maxAskChars {
			return nil, fmt.Errorf("%w: the conversation is over %d characters", ErrAskTooLarge, maxAskChars)
		}
		out = append(out, Message{Role: msg.Role, Content: []ContentBlock{{Type: blockText, Text: msg.Content}}})
	}
	return out, nil
}
