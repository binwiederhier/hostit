package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"time"

	"heckel.io/hostit/config"
)

// ClaudeRunner runs one assistant turn on the Claude Max backend: it launches the
// sandboxed `claude -p`, publishes each streamed Event as it happens, and returns
// the turn's token usage. systemPrompt is appended to Claude Code's own system
// prompt so the agent knows it is working on a hostit app. The server implements
// this over app.AssistantSandbox; the assistant package stays free of podman.
type ClaudeRunner interface {
	RunTurn(ctx context.Context, app, prompt, systemPrompt string, publish func(Event)) (Usage, error)
}

// SetClaudeRunner switches this Manager to the Claude Max backend. It keeps all
// the Manager's other machinery -- sessions, subscribers, per-user rate limits,
// the persisted transcript -- and only replaces how a turn is executed.
func (m *Manager) SetClaudeRunner(r ClaudeRunner) {
	m.claude = r
}

// runClaudeTurn is one turn on the Claude Max backend. The turn is claude's own
// agent loop in the sandbox; this streams its events to every subscriber and
// persists what it did. history already carries (and the caller already saved and
// showed) the user's message; prior turns are replayed into the prompt for
// continuity, since the sandbox container is ephemeral. Attachments are ignored.
//
// It returns nil when it handled the turn (success or a clean cancel, having
// published "done"), and a non-nil error when the SUBSCRIPTION was unavailable --
// the signal for the caller to fall back to the API backend. On that error path it
// deliberately saves nothing, so the fallback turn starts from a clean transcript.
func (m *Manager) runClaudeTurn(ctx context.Context, s *session, app string, history []Message, userText string) error {
	prior := history[:len(history)-1] // everything before the user message just added
	s.publish(Event{Type: evtModel, Text: config.ExternalClaudeMode})
	acc := &claudeAccumulator{}
	usage, err := m.claude.RunTurn(ctx, app, buildClaudePrompt(prior, userText), systemPrompt(app), func(ev Event) {
		s.publish(ev)
		acc.add(ev)
	})

	// Record usage best-effort; a failure to account must never interrupt the turn.
	if usage != (Usage{}) {
		if uerr := m.store.RecordUsage(app, usage); uerr != nil {
			slog.Warn("Cannot record assistant usage", "app", app, "error", uerr)
		}
	}

	if err != nil {
		if ctx.Err() != nil {
			acc.flush() // cancelled (Stop / timeout): keep what was done, end cleanly
			tagModel(acc.messages, config.ExternalClaudeMode)
			m.save(app, append(history, acc.messages...))
			s.publish(Event{Type: evtDone})
			return nil
		}
		return err // subscription unavailable: caller falls back to the API backend
	}
	// Persist what the agent did, reconstructed from the same ordered stream the
	// subscribers saw, so a reload shows this turn (badged as External Claude).
	acc.flush()
	tagModel(acc.messages, config.ExternalClaudeMode)
	m.save(app, append(history, acc.messages...))
	s.publish(Event{Type: evtDone})
	return nil
}

// tagModel records, on each assistant message, which model produced it and when,
// so the chat can show the reply's info popover.
func tagModel(msgs []Message, model string) {
	now := time.Now().Unix()
	for i := range msgs {
		if msgs[i].Role == roleAssistant {
			msgs[i].Model = model
			msgs[i].Time = now
		}
	}
}

// buildClaudePrompt gives the stateless sandbox its context: the recent
// transcript rendered as text, then the new user message. The first message has
// no prior, so it is sent as-is.
func buildClaudePrompt(prior []Message, userText string) string {
	items := toItems(recentHistory(prior, maxContextTurns))
	if len(items) == 0 {
		return userText
	}
	return "Here is the conversation so far, for context:\n\n" +
		RenderTranscript(items) +
		"\n\n---\n\nThe user now says:\n\n" + userText
}

// claudeAccumulator rebuilds the stored transcript from the ordered event stream:
// an assistant message (text + tool_use blocks) followed by a user message holding
// the matching tool_results, mirroring the Anthropic shape. tool_results pair to
// tool_uses FIFO, so a batch of parallel calls in one message pairs correctly. The
// ids are UNIQUE ACROSS TURNS (random, not a per-turn counter) -- a repeated
// call_1/call_2 would make a later API turn 400 with "multiple tool_result blocks
// with id X". Thinking is streamed but, as in the API loop, not stored.
type claudeAccumulator struct {
	messages       []Message
	pendingAsst    []ContentBlock // the assistant message being built (text + tool_use)
	pendingResults []ContentBlock // the user message of tool_results being built
	toolIDs        []string       // FIFO of tool_use ids still awaiting a result
}

func (a *claudeAccumulator) add(ev Event) {
	switch ev.Type {
	case evtText:
		a.flushResults() // text after some results closes that user message
		a.pendingAsst = append(a.pendingAsst, ContentBlock{Type: blockText, Text: ev.Text})
	case evtToolUse:
		a.flushResults()
		id := newToolID()
		a.toolIDs = append(a.toolIDs, id)
		a.pendingAsst = append(a.pendingAsst, ContentBlock{Type: blockToolUse, ID: id, Name: ev.Tool, Input: rawJSON(ev.Input)})
	case evtToolResult:
		a.flushAsst() // results go in a user message after the assistant that called them
		id := ""
		if len(a.toolIDs) > 0 {
			id, a.toolIDs = a.toolIDs[0], a.toolIDs[1:]
		}
		a.pendingResults = append(a.pendingResults, ContentBlock{Type: blockToolResult, ToolUseID: id, Content: ev.Output, IsError: ev.IsError})
	}
}

func (a *claudeAccumulator) flushAsst() {
	if len(a.pendingAsst) > 0 {
		a.messages = append(a.messages, Message{Role: roleAssistant, Content: a.pendingAsst})
		a.pendingAsst = nil
	}
}

func (a *claudeAccumulator) flushResults() {
	if len(a.pendingResults) > 0 {
		a.messages = append(a.messages, Message{Role: roleUser, Content: a.pendingResults})
		a.pendingResults = nil
	}
}

// flush appends any assistant message and tool_results still being built.
func (a *claudeAccumulator) flush() {
	a.flushAsst()
	a.flushResults()
}

// dedupeToolIDs returns a copy of history with every tool_use / tool_result id
// re-minted uniquely and re-paired in document order (FIFO: results answer calls
// in the order the calls were made -- how both backends emit them). It repairs
// transcripts written before ids were unique across turns, so a later API turn is
// not rejected for duplicate tool_result ids. It copies; storage changes only when
// the turn saves the returned history.
func dedupeToolIDs(history []Message) []Message {
	out := make([]Message, len(history))
	var queue []string
	for i, msg := range history {
		blocks := make([]ContentBlock, len(msg.Content))
		copy(blocks, msg.Content)
		for j := range blocks {
			switch blocks[j].Type {
			case blockToolUse:
				id := newToolID()
				blocks[j].ID = id
				queue = append(queue, id)
			case blockToolResult:
				if len(queue) > 0 {
					blocks[j].ToolUseID = queue[0]
					queue = queue[1:]
				}
			}
		}
		out[i] = Message{Role: msg.Role, Content: blocks, Model: msg.Model, Time: msg.Time}
	}
	return out
}

// newToolID mints a transcript-wide-unique id for a reconstructed tool call, so
// ids never repeat across turns (see claudeAccumulator).
func newToolID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic(err) // only fails if the system entropy source is broken
	}
	return "call_" + hex.EncodeToString(b)
}

// rawJSON returns a tool input as a JSON object, defaulting to {} when empty so
// the stored block is always valid.
func rawJSON(s string) json.RawMessage {
	if s == "" {
		return json.RawMessage("{}")
	}
	return json.RawMessage(s)
}
