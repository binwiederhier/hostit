package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"time"
)

// ClaudeRunner runs one assistant turn on the Claude Max backend: it launches the
// sandboxed `claude -p`, publishes each streamed Event as it happens, and returns
// the turn's token usage. systemPrompt is appended to Claude Code's own system
// prompt so the agent knows it is working on a hostit app. images are the turn's
// uploaded images, which the prompt text cannot carry. The server implements
// this over app.AssistantSandbox; the assistant package stays free of container.
type ClaudeRunner interface {
	RunTurn(ctx context.Context, app, prompt, systemPrompt string, images []Attachment, publish func(Event)) (Usage, error)
	// Answer runs a ONE-SHOT, tool-less answer on the Claude Max backend and
	// returns just the answer text and usage -- what /api/container/assistant
	// wants from the subscription: no tools, no transcript, no streaming. model
	// is the backend's own model string (e.g. "claude-opus-5"); system is the
	// APP's system prompt (not the build assistant's); prompt is the rendered
	// conversation.
	Answer(ctx context.Context, app, model, system, prompt string) (string, Usage, error)
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
// continuity, since the sandbox container is ephemeral. Attachment paths ride in
// the prompt (so the agent can read them over its MCP tools) and images go to the
// runner as blocks, since text cannot carry them.
//
// It returns nil when it handled the turn (success or a clean cancel, having
// published "done"), and a non-nil error when the SUBSCRIPTION was unavailable --
// the signal for the caller to fall back to the API backend. On that error path it
// deliberately saves nothing, so the fallback turn starts from a clean transcript.
func (m *Manager) runClaudeTurn(ctx context.Context, s *session, app string, history []Message, userText string, attachments []Attachment, option Option) error {
	prior := history[:len(history)-1] // everything before the user message just added
	s.publish(Event{Type: evtModel, Text: option.ID})
	acc := &claudeAccumulator{}
	usage, err := m.claude.RunTurn(ctx, app, buildClaudePrompt(prior, userText, attachments), systemPrompt(app, m.ops.Archived(app), m.ops.Connections(app), m.extraContext(app)), imageAttachments(attachments), func(ev Event) {
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
			tagModel(acc.messages, option.ID)
			m.save(app, append(history, acc.messages...))
			s.publish(Event{Type: evtDone})
			return nil
		}
		return err // subscription unavailable: caller falls back to the API backend
	}
	// Persist what the agent did, reconstructed from the same ordered stream the
	// subscribers saw, so a reload shows this turn (badged as External Claude).
	acc.flush()
	tagModel(acc.messages, option.ID)
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
// transcript rendered as text, then the new user message, then where this
// turn's uploads were saved -- the agent can read them over its MCP tools
// (image BYTES travel separately; text cannot carry them). The first message
// has no prior, so it is sent as-is.
func buildClaudePrompt(prior []Message, userText string, attachments []Attachment) string {
	prompt := userText
	if items := toItems(recentHistory(prior, maxContextTurns)); len(items) > 0 {
		prompt = "Here is the conversation so far, for context:\n\n" +
			RenderTranscript(items) +
			"\n\n---\n\nThe user now says:\n\n" + userText
	}
	if len(attachments) > 0 {
		paths := make([]string, 0, len(attachments))
		for _, a := range attachments {
			paths = append(paths, a.Path)
		}
		prompt += "\n\n" + attachmentNotePrefix + strings.Join(paths, ", ")
	}
	return prompt
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
		// Redacted here too: this backend's tools run inside the sandbox rather
		// than through dispatch, so it does not pass the check in service.go.
		a.pendingResults = append(a.pendingResults, ContentBlock{Type: blockToolResult, ToolUseID: id, Content: RedactCredentials(ev.Output), IsError: ev.IsError})
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

// toolInterruptedNote is the synthetic tool_result content used to close a
// tool_use whose real result never arrived (see closeDanglingToolUses).
const toolInterruptedNote = "[interrupted: this tool call did not complete and produced no result]"

// closeDanglingToolUses returns a copy of history in which every tool_use is
// answered. A tool_use whose tool_result was never saved -- an interruption
// between the assistant reply and its results (Stop, timeout, a control restart)
// -- leaves the transcript ending in an unanswered call, which the Messages API
// rejects with "tool_use ids were found without tool_result blocks immediately
// after". For each such call it inserts a synthetic error tool_result into the
// user message that must follow (creating that message when none does), so a
// transcript corrupted by an interruption self-heals on the next turn instead of
// failing forever. It copies; storage changes only when the turn saves the result.
func closeDanglingToolUses(history []Message) []Message {
	out := make([]Message, 0, len(history))
	for i := 0; i < len(history); i++ {
		msg := history[i]
		out = append(out, msg)
		// Collect the tool_use ids this (assistant) message opened.
		var ids []string
		for _, b := range msg.Content {
			if b.Type == blockToolUse {
				ids = append(ids, b.ID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		// Which ids does the immediately following user message already answer?
		hasNext := i+1 < len(history) && history[i+1].Role == roleUser
		answered := map[string]bool{}
		if hasNext {
			for _, b := range history[i+1].Content {
				if b.Type == blockToolResult {
					answered[b.ToolUseID] = true
				}
			}
		}
		// Synthesize a result for every unanswered call, in call order.
		var synth []ContentBlock
		for _, id := range ids {
			if !answered[id] {
				synth = append(synth, ContentBlock{Type: blockToolResult, ToolUseID: id, Content: toolInterruptedNote, IsError: true})
			}
		}
		if len(synth) == 0 {
			continue
		}
		if hasNext {
			// tool_result blocks must lead the user turn, so the synthetic ones go
			// in front of whatever that message already holds.
			next := history[i+1]
			next.Content = append(synth, next.Content...)
			out = append(out, next)
			i++ // that user message is now emitted; do not process it again
		} else {
			// Nothing follows the dangling call: a fresh user message carries the
			// synthetic results so the pairing is complete.
			out = append(out, Message{Role: roleUser, Content: synth})
		}
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
