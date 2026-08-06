// Package assistant is hostit's in-browser coding agent: a loop around the
// Anthropic Messages API whose tools are the app's own REST operations. It lets
// an owner build and iterate on an app from a phone -- the model thinks, calls
// tools to read/write files and run commands in the app's container, sees the
// results, and repeats until the change is done.
package assistant

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

const (
	// maxIterations bounds one Run: how many times the model may call tools and
	// look at the results before we stop. A backstop against a loop that never
	// decides it is finished, not a normal limit.
	maxIterations = 40
	// maxTokens caps a single reply (thinking plus output).
	maxTokens = 16000
	// assistantEffort tunes how hard the model works per turn
	assistantEffort = "high"
)

var (
	// errMaxIterations means the loop hit its step limit without the model
	// deciding it was done
	errMaxIterations = errors.New("assistant reached its step limit without finishing")
)

// Manager runs assistant sessions. One conversation is kept per app, persisted
// through the Store so it survives reloads and restarts.
type Manager struct {
	client completer
	ops    AppOps
	store  Store
	model  string
}

// NewManager wires the loop to a model client, the app operations it drives, and
// the store that persists each app's conversation
func NewManager(client completer, ops AppOps, store Store, model string) *Manager {
	return &Manager{
		client: client,
		ops:    ops,
		store:  store,
		model:  model,
	}
}

// Reset forgets an app's conversation, so the next message starts fresh
func (m *Manager) Reset(app string) error {
	return m.store.Delete(app)
}

// Transcript returns the app's stored conversation as display items, for a page
// that is loading it fresh (a reload, or another device joining)
func (m *Manager) Transcript(app string) ([]Item, error) {
	history, err := m.store.Load(app)
	if err != nil {
		return nil, err
	}
	return toItems(history), nil
}

// Run sends one user message for an app and drives the loop to completion,
// emitting an Event for everything that happens along the way. It appends to the
// app's transcript so a conversation builds up across calls. emit is called from
// this goroutine, in order; it must not block for long.
func (m *Manager) Run(ctx context.Context, app, userText string, emit func(Event)) error {
	history := append(m.load(app), Message{
		Role:    "user",
		Content: []ContentBlock{{Type: "text", Text: userText}},
	})

	for iter := 0; iter < maxIterations; iter++ {
		resp, err := m.client.complete(ctx, request{
			Model:        m.model,
			MaxTokens:    maxTokens,
			System:       systemPrompt(app),
			Messages:     history,
			Tools:        toolDefs(),
			Thinking:     &thinkingConfig{Type: "adaptive"},
			OutputConfig: &outputConfig{Effort: assistantEffort},
		})
		if err != nil {
			emit(Event{Type: "error", Error: err.Error()})
			return err
		}

		// Stream the reply to the client, then keep it in the transcript. Thinking
		// blocks are shown but not stored: an adaptive thinking block carries
		// internal fields we do not round-trip, and the model does not need its
		// earlier thinking echoed back to keep going.
		toolUses := m.emitReply(resp.Content, emit)
		history = append(history, Message{Role: "assistant", Content: withoutThinking(resp.Content)})
		if len(toolUses) == 0 {
			// No tool calls: the model has said its piece and is done.
			m.save(app, history)
			emit(Event{Type: "done"})
			return nil
		}

		// Run each requested tool and hand the results back as the next user turn.
		results := make([]ContentBlock, 0, len(toolUses))
		for _, tu := range toolUses {
			out, isErr := m.dispatch(app, tu.Name, tu.Input)
			emit(Event{Type: "tool_result", Tool: tu.Name, Output: out, IsError: isErr})
			results = append(results, ContentBlock{
				Type:      "tool_result",
				ToolUseID: tu.ID,
				Content:   out,
				IsError:   isErr,
			})
		}
		history = append(history, Message{Role: "user", Content: results})
	}

	m.save(app, history)
	emit(Event{Type: "error", Error: errMaxIterations.Error()})
	return errMaxIterations
}

// withoutThinking drops thinking blocks so they are not echoed back to the API.
// The visible reply (text, tool calls) is what the next turn needs.
func withoutThinking(blocks []ContentBlock) []ContentBlock {
	out := make([]ContentBlock, 0, len(blocks))
	for _, b := range blocks {
		if b.Type == "thinking" || b.Type == "redacted_thinking" {
			continue
		}
		out = append(out, b)
	}
	return out
}

// emitReply streams the model's text and thinking to the client and returns the
// tool calls it wants run
func (m *Manager) emitReply(content []ContentBlock, emit func(Event)) []ContentBlock {
	var toolUses []ContentBlock
	for _, b := range content {
		switch b.Type {
		case "text":
			emit(Event{Type: "text", Text: b.Text})
		case "thinking":
			emit(Event{Type: "thinking", Text: b.Thinking})
		case "tool_use":
			emit(Event{Type: "tool_use", Tool: b.Name, Input: string(b.Input)})
			toolUses = append(toolUses, b)
		}
	}
	return toolUses
}

func (m *Manager) load(app string) []Message {
	history, err := m.store.Load(app)
	if err != nil {
		slog.Warn("Cannot load assistant transcript; starting fresh", "app", app, "error", err)
		return nil
	}
	return history
}

func (m *Manager) save(app string, history []Message) {
	if err := m.store.Save(app, history); err != nil {
		slog.Warn("Cannot persist assistant transcript", "app", app, "error", err)
	}
}

// systemPrompt sets the model's stance: it is working on one hostit app, it makes
// changes with the tools, and the app serves itself. Kept short on purpose --
// the model learns the specifics by reading the app's own files.
func systemPrompt(app string) string {
	return fmt.Sprintf(`You are hostit's built-in coding assistant, working on a single app named %q.

hostit is a mini-app platform. Each app has a home directory you act on with your tools:
- public/     files served on the web (a "static" app serves exactly this)
- hostit.yml  how the app runs: "mode: static" to serve public/, or a "run:" command that must listen on $PORT
- src/, bin/, docs/  source, binaries and docs, by convention

The app is live at its subdomain. A static app serves public/ immediately, so editing files there is enough. But whenever you change hostit.yml (for example switching mode, or setting a run: command) or a run: app's code, you MUST call deploy to make it live -- writing files alone does not apply a configuration change. The container has common runtimes (python3, go, sqlite3).

Work like a careful engineer: read before you write (list_files, read_file), make the smallest change that works, run_command to build or verify, deploy when the config changed, and read_logs to debug a running app. Explain briefly what you are doing. When the user's request is done, stop and say so. Do not ask permission for each step; just do the work and report what you changed.`, app)
}
