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
	"sync"
	"time"
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
	// maxContextTurns bounds how many recent user-delimited turns are sent to the
	// model. The full conversation is still persisted and shown; this only caps
	// what each request carries, so cost and context size do not grow without end.
	maxContextTurns = 12

	// defaultMaxRunsPerUser caps how many turns one user may run at once across all
	// their apps, and defaultMaxRunsPerHour caps how many they may start per hour.
	// Every turn spends the operator's API budget, so these stop a runaway loop.
	defaultMaxRunsPerUser = 3
	defaultMaxRunsPerHour = 60
	// runRateWindow is the window defaultMaxRunsPerHour is measured over
	runRateWindow = time.Hour
)

var (
	// errMaxIterations means the loop hit its step limit without the model
	// deciding it was done
	errMaxIterations = errors.New("assistant reached its step limit without finishing")
	// ErrTooManyRuns means the user already has the most turns running they may
	ErrTooManyRuns = errors.New("too many assistant turns running; finish one first")
	// ErrRateLimited means the user has started too many turns recently
	ErrRateLimited = errors.New("assistant rate limit reached; please try again later")
)

// Manager runs assistant sessions. Each app's conversation is persisted through
// the Store, and its live run is owned by the server (a background goroutine),
// not by the request that started it -- so a run keeps going when the sender
// leaves, and every subscriber (browser, phone) sees the same stream.
type Manager struct {
	client   completer
	ops      AppOps
	store    Store
	model    string
	sessions map[string]*session
	mu       sync.Mutex // Protects sessions

	// Per-user run limits (across all of a user's apps), since every turn spends
	// the operator's API budget. maxRunsPerUser/maxRunsPerHour are fields, not
	// consts, so tests can dial them down.
	maxRunsPerUser int
	maxRunsPerHour int
	userRuns       map[string]*userRuns
	limitMu        sync.Mutex // Protects userRuns
}

// userRuns tracks one user's assistant usage for rate limiting
type userRuns struct {
	active int         // turns running right now
	starts []time.Time // start times within the rate window
}

// NewManager wires the loop to a model client, the app operations it drives, and
// the store that persists each app's conversation
func NewManager(client completer, ops AppOps, store Store, model string) *Manager {
	return &Manager{
		client:         client,
		ops:            ops,
		store:          store,
		model:          model,
		sessions:       make(map[string]*session),
		maxRunsPerUser: defaultMaxRunsPerUser,
		maxRunsPerHour: defaultMaxRunsPerHour,
		userRuns:       make(map[string]*userRuns),
	}
}

// reserveRun claims a run slot for a user, enforcing the concurrency and rate
// limits. The global-admin token has no user id and is not limited. On success
// the caller must releaseRun when the turn ends.
func (m *Manager) reserveRun(userID string) error {
	if userID == "" {
		return nil
	}
	m.limitMu.Lock()
	defer m.limitMu.Unlock()
	u := m.userRuns[userID]
	if u == nil {
		u = &userRuns{}
		m.userRuns[userID] = u
	}
	cutoff := time.Now().Add(-runRateWindow)
	kept := u.starts[:0]
	for _, t := range u.starts {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	u.starts = kept
	if u.active >= m.maxRunsPerUser {
		return ErrTooManyRuns
	}
	if len(u.starts) >= m.maxRunsPerHour {
		return ErrRateLimited
	}
	u.active++
	u.starts = append(u.starts, time.Now())
	return nil
}

func (m *Manager) releaseRun(userID string) {
	if userID == "" {
		return
	}
	m.limitMu.Lock()
	if u := m.userRuns[userID]; u != nil && u.active > 0 {
		u.active--
	}
	m.limitMu.Unlock()
}

// sessionFor returns the app's live session, creating it on first use
func (m *Manager) sessionFor(app string) *session {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[app]
	if s == nil {
		s = newSession()
		m.sessions[app] = s
	}
	return s
}

// Subscribe registers a watcher for an app's live events and returns a channel to
// read them from plus a function to stop watching. Every subscriber sees the same
// stream, so multiple devices stay in sync. It refuses (ErrTooManySubscribers)
// past a per-app cap, so a client cannot exhaust the server by opening streams.
func (m *Manager) Subscribe(app string) (<-chan Event, func(), error) {
	s := m.sessionFor(app)
	id, ch, ok := s.subscribe()
	if !ok {
		return nil, nil, ErrTooManySubscribers
	}
	return ch, func() { s.unsubscribe(id) }, nil
}

// Running reports whether a turn is in progress for an app, so a page loading
// fresh can show the working state and disable sending.
func (m *Manager) Running(app string) bool {
	return m.sessionFor(app).isRunning()
}

// Send starts a turn for an app in the background and returns at once. userID is
// the caller (empty for the global admin token); it bounds the caller's usage. It
// refuses ErrBusy if a turn is already running for this app, or a rate error if
// the user is over their limits. Events flow to every subscriber, not this caller.
func (m *Manager) Send(app, userID, userText string) error {
	if err := m.reserveRun(userID); err != nil {
		return err
	}
	s := m.sessionFor(app)
	if !s.begin() {
		m.releaseRun(userID)
		return ErrBusy
	}
	go m.runLoop(s, app, userID, userText)
	return nil
}

// Stop cancels the app's in-progress turn (if any) and reports whether there was
// one to stop. The run's goroutine unwinds on its own -- it publishes a done so
// every watcher clears the working state -- and the transcript keeps the steps it
// had already saved.
func (m *Manager) Stop(app string) bool {
	return m.sessionFor(app).stop()
}

// Reset forgets an app's conversation, so the next message starts fresh. It claims
// the run slot for the duration, so a Send cannot race the delete and leave the
// transcript half-written; it refuses (ErrBusy) while a turn is running.
func (m *Manager) Reset(app string) error {
	s := m.sessionFor(app)
	if !s.begin() {
		return ErrBusy
	}
	defer s.end()
	return m.store.Delete(app)
}

// DropSession forgets an app's live session and deletes its transcript, called
// when an app is deleted so nothing is left leaking in memory or the registry.
func (m *Manager) DropSession(app string) {
	m.mu.Lock()
	delete(m.sessions, app)
	m.mu.Unlock()
	_ = m.store.Delete(app)
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

// runLoop is one turn, run in a server-owned goroutine (not a request). It
// publishes every step to the app's session so all subscribers see it, and saves
// the transcript after each step so a reload mid-run recovers the progress. It is
// bound to a background context, so the sender leaving does not cancel it.
func (m *Manager) runLoop(s *session, app, userID, userText string) {
	defer s.end()
	defer m.releaseRun(userID)
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	s.setCancel(cancel) // so Stop can cancel this run

	history := append(m.load(app), Message{
		Role:    "user",
		Content: []ContentBlock{{Type: "text", Text: userText}},
	})
	m.save(app, history)
	s.publish(Event{Type: "user", Text: userText})

	for iter := 0; iter < maxIterations; iter++ {
		resp, err := m.client.complete(ctx, request{
			Model:        m.model,
			MaxTokens:    maxTokens,
			System:       systemPrompt(app),
			Messages:     recentHistory(history, maxContextTurns),
			Tools:        toolDefs(),
			Thinking:     &thinkingConfig{Type: "adaptive"},
			OutputConfig: &outputConfig{Effort: assistantEffort},
		})
		if err != nil {
			// A cancelled context is the owner pressing Stop (or the run timing out),
			// not a failure: end the turn cleanly so watchers just stop working.
			if ctx.Err() != nil {
				s.publish(Event{Type: "done"})
				return
			}
			s.publish(Event{Type: "error", Error: err.Error()})
			return
		}

		// Keep the reply in the transcript. Thinking blocks are shown but not
		// stored: an adaptive thinking block carries internal fields we do not
		// round-trip, and the model does not need its earlier thinking echoed back.
		toolUses := m.publishReply(s, resp.Content)
		history = append(history, Message{Role: "assistant", Content: withoutThinking(resp.Content)})
		m.save(app, history)
		if len(toolUses) == 0 {
			// No tool calls: the model has said its piece and is done.
			s.publish(Event{Type: "done"})
			return
		}

		// Run each requested tool and hand the results back as the next user turn.
		results := make([]ContentBlock, 0, len(toolUses))
		for _, tu := range toolUses {
			out, isErr := m.dispatch(app, tu.Name, tu.Input)
			s.publish(Event{Type: "tool_result", Tool: tu.Name, Output: out, IsError: isErr})
			results = append(results, ContentBlock{
				Type:      "tool_result",
				ToolUseID: tu.ID,
				Content:   out,
				IsError:   isErr,
			})
		}
		history = append(history, Message{Role: "user", Content: results})
		m.save(app, history)
	}

	s.publish(Event{Type: "error", Error: errMaxIterations.Error()})
}

// recentHistory windows the conversation sent to the model to the last maxTurns
// human turns (plus their assistant/tool messages), so cost and context size do
// not grow without bound over a long-lived app conversation. It always cuts on a
// human "user" message, keeping tool_use/tool_result pairs intact and the message
// order valid. The full history is still persisted and shown; only the request is
// trimmed.
func recentHistory(history []Message, maxTurns int) []Message {
	var starts []int
	for i, msg := range history {
		if msg.Role == "user" && hasTextBlock(msg) {
			starts = append(starts, i)
		}
	}
	if len(starts) <= maxTurns {
		return history
	}
	return history[starts[len(starts)-maxTurns]:]
}

// hasTextBlock reports whether a message carries human text (a turn start), as
// opposed to a user message that only carries tool results
func hasTextBlock(msg Message) bool {
	for _, b := range msg.Content {
		if b.Type == "text" {
			return true
		}
	}
	return false
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

// publishReply streams the model's text and thinking to every subscriber and
// returns the tool calls it wants run
func (m *Manager) publishReply(s *session, content []ContentBlock) []ContentBlock {
	var toolUses []ContentBlock
	for _, b := range content {
		switch b.Type {
		case "text":
			s.publish(Event{Type: "text", Text: b.Text})
		case "thinking":
			s.publish(Event{Type: "thinking", Text: b.Thinking})
		case "tool_use":
			s.publish(Event{Type: "tool_use", Tool: b.Name, Input: string(b.Input)})
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
