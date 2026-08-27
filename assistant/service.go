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
	"strings"
	"sync"
	"time"

	"heckel.io/hostit/control/config"
	"heckel.io/hostit/platformdoc"
)

const (
	// maxIterations bounds one Run: how many times the model may call tools and
	// look at the results before we stop. A backstop against a loop that never
	// decides it is finished, not a normal limit.
	maxIterations = 40
	// maxIterationsNotice is shown to the owner when a turn hits maxIterations: a
	// friendly "paused, say continue" rather than an error, since nothing failed.
	maxIterationsNotice = "The assistant has been running for too long without human interaction. Say \"continue\" to resume the session."
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
	client completer
	ops    AppOps
	store  Store
	// creds is which backends this instance can run, for resolving a turn's mode.
	creds    Credentials
	claude   ClaudeRunner // non-nil switches turns to the Claude Max sandbox backend
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
func NewManager(client completer, ops AppOps, store Store, creds Credentials) *Manager {
	return &Manager{
		client:         client,
		ops:            ops,
		store:          store,
		creds:          creds,
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
func (m *Manager) Send(app, userID, userText, mode string, attachments ...Attachment) error {
	if err := m.reserveRun(userID); err != nil {
		return err
	}
	s := m.sessionFor(app)
	if !s.begin() {
		m.releaseRun(userID)
		return ErrBusy
	}
	go m.runLoop(s, app, userID, userText, mode, attachments)
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
func (m *Manager) runLoop(s *session, app, userID, userText, mode string, attachments []Attachment) {
	defer s.end()
	defer m.releaseRun(userID)
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	s.setCancel(cancel) // so Stop can cancel this run

	// A mode is an option id ("claude-opus-5", "anthropic-sonnet-5"): a backend
	// AND a model, because the same model runs both ways and bills differently.
	// The server resolves one before calling; an empty or unknown id here falls
	// back to whatever this instance can run.
	option, ok := Lookup(m.creds, mode)
	if !ok {
		if option, ok = Default(m.creds); !ok {
			s.publish(Event{Type: evtNotice, Text: "The assistant has no model configured."})
			return
		}
	}

	// Store and show the user's message once, up front, so a fallback from the
	// External Claude backend to the API does not duplicate it in the transcript.
	content, display := buildUserContent(userText, attachments)
	history := append(m.load(app), Message{Role: roleUser, Content: content})
	m.save(app, history)
	s.publish(Event{Type: evtUser, Text: display})

	// The subscription backend runs the agent in its sandbox. If the subscription
	// is unavailable, say so and fall back to the metered API, so a lapsed token
	// never leaves the assistant dead.
	// The runner is nil when the sandbox could not be built (no podman, a bad
	// image) even though the token is configured, so it is checked here rather
	// than assumed from the credential: the credential says the operator INTENDS
	// this backend, the runner says it can actually run.
	if option.Backend == BackendClaude && m.claude == nil {
		s.publish(Event{Type: evtNotice, Text: "Claude is configured but its sandbox is unavailable; using the API."})
		if fallback, ok := m.apiFallback(); ok {
			option = fallback
		} else {
			s.publish(Event{Type: evtNotice, Text: "No API key is configured either."})
			return
		}
	}
	if option.Backend == BackendClaude {
		if err := m.runClaudeTurn(ctx, s, app, history, userText, attachments, option); err == nil {
			return // handled the turn (published done) or was cancelled
		} else {
			fallback, hasFallback := m.apiFallback()
			if !hasFallback {
				s.publish(Event{Type: evtNotice, Text: fmt.Sprintf("Claude is unavailable (%s), and no API key is configured.", err.Error())})
				return
			}
			s.publish(Event{Type: evtNotice, Text: fmt.Sprintf("Claude is unavailable (%s). Falling back to %s.", err.Error(), fallback.Label)})
			option = fallback
		}
	}

	m.runAPILoop(ctx, s, app, option, history)
}

// apiFallback is where a turn lands when the subscription cannot run it: the
// API's Sonnet, because a forced fallback should not silently escalate cost,
// and otherwise whatever the API does offer. The second half matters -- looking
// up one slug and giving up would report "no API key is configured" on an
// instance that has one.
func (m *Manager) apiFallback() (Option, bool) {
	if o, ok := Lookup(m.creds, BackendAnthropic+"-sonnet-5"); ok {
		return o, true
	}
	for _, o := range Catalog(m.creds) {
		if o.Backend == BackendAnthropic {
			return o, true
		}
	}
	return Option{}, false
}

// thinkingFor returns the extended-thinking config for a model, or nil for models
// that do not support adaptive thinking. Sonnet/Opus support it; Haiku does not
// (the API rejects it), so a lighter model can still be offered in the dropdown.
func thinkingFor(model string) *thinkingConfig {
	if strings.Contains(strings.ToLower(model), "haiku") {
		return nil
	}
	return &thinkingConfig{Type: "adaptive"}
}

// outputConfigFor returns the effort config, likewise omitted for Haiku, which
// does not take the extended output controls.
func outputConfigFor(model string) *outputConfig {
	if strings.Contains(strings.ToLower(model), "haiku") {
		return nil
	}
	return &outputConfig{Effort: assistantEffort}
}

// runAPILoop is the metered-API turn: the model-call-and-tools loop, driven by
// the chosen option. history already carries the user's message and has been
// shown and saved by the caller.
//
// The option carries two model strings and they are not interchangeable: the
// API is asked for option.Model (what Anthropic calls it), while the reply is
// TAGGED with option.ID (which backend served it), because both backends offer
// the same models and only the id says who ran the turn.
func (m *Manager) runAPILoop(ctx context.Context, s *session, app string, option Option, history []Message) {
	model := option.Model
	// Repair any duplicate tool ids first: a transcript with External Claude turns
	// can carry tool_use ids that repeat across turns, which the Messages API
	// rejects. This re-mints them uniquely (and persists the repair when the turn
	// saves), so switching to an API model after using External Claude just works.
	history = dedupeToolIDs(history)
	// Close any tool_use whose result was never saved (an interruption between the
	// reply and its results, e.g. a restart), which the Messages API would reject.
	history = closeDanglingToolUses(history)
	// Tell watchers which option is answering this turn (so the chat can badge the
	// reply, and name the fallback truthfully when the subscription failed).
	s.publish(Event{Type: evtModel, Text: option.ID})
	var turn Usage // running token totals for this turn, published as it grows
	for iter := 0; iter < maxIterations; iter++ {
		resp, err := m.client.complete(ctx, request{
			Model:        model,
			MaxTokens:    maxTokens,
			System:       cachedSystem(systemPrompt(app, m.ops.Archived(app), m.ops.Connections(app), m.extraContext(app))),
			Messages:     apiRequestMessages(history),
			Tools:        cachedToolDefs(m.ops.MCPTools(app)),
			Thinking:     thinkingFor(model),
			OutputConfig: outputConfigFor(model),
		})
		if err != nil {
			// A cancelled context is the owner pressing Stop (or the run timing out),
			// not a failure: end the turn cleanly so watchers just stop working.
			if ctx.Err() != nil {
				s.publish(Event{Type: evtDone})
				return
			}
			s.publish(Event{Type: evtError, Error: err.Error()})
			return
		}
		// Record this step's token usage against the app (best effort; a failure to
		// account must never interrupt the run), and publish the running turn total so
		// the UI can show a live token counter.
		step := Usage{
			InputTokens:      resp.Usage.InputTokens,
			OutputTokens:     resp.Usage.OutputTokens,
			CacheWriteTokens: resp.Usage.CacheCreationInputTokens,
			CacheReadTokens:  resp.Usage.CacheReadInputTokens,
		}
		if err := m.store.RecordUsage(app, step); err != nil {
			slog.Warn("Cannot record assistant usage", "app", app, "error", err)
		}
		turn.InputTokens += step.InputTokens
		turn.OutputTokens += step.OutputTokens
		turn.CacheWriteTokens += step.CacheWriteTokens
		turn.CacheReadTokens += step.CacheReadTokens
		snapshot := turn
		s.publish(Event{Type: evtUsage, Usage: &snapshot})

		// Keep the reply in the transcript. Thinking blocks are shown but not
		// stored: an adaptive thinking block carries internal fields we do not
		// round-trip, and the model does not need its earlier thinking echoed back.
		toolUses := m.publishReply(s, resp.Content)
		history = append(history, Message{Role: roleAssistant, Content: withoutThinking(resp.Content), Model: option.ID, Time: time.Now().Unix()})
		m.save(app, history)
		if len(toolUses) == 0 {
			// No tool calls: the model has said its piece and is done.
			s.publish(Event{Type: evtDone})
			return
		}

		// Run each requested tool and hand the results back as the next user turn.
		results := make([]ContentBlock, 0, len(toolUses))
		for _, tu := range toolUses {
			out, isErr := m.dispatch(app, tu.Name, tu.Input)
			// Taken out before it is published OR stored: the transcript keeps
			// this for as long as the conversation lives, and a token printed
			// once is a token kept.
			out = RedactCredentials(out)
			s.publish(Event{Type: evtToolResult, Tool: tu.Name, Output: out, IsError: isErr})
			results = append(results, ContentBlock{
				Type:      blockToolResult,
				ToolUseID: tu.ID,
				Content:   out,
				IsError:   isErr,
			})
		}
		history = append(history, Message{Role: roleUser, Content: results})
		m.save(app, history)
	}

	s.publish(Event{Type: evtPaused, Text: maxIterationsNotice})
}

// apiRequestMessages builds the message list for one API request: the recent
// window with a cache breakpoint on the tail, and our own per-message Model
// metadata stripped (the Messages API rejects unknown fields). It copies, so
// stored history keeps its Model tags for the UI.
func apiRequestMessages(history []Message) []Message {
	msgs := recentHistory(history, maxContextTurns)
	clean := make([]Message, len(msgs))
	for i, msg := range msgs {
		msg.Model, msg.Time = "", 0 // our metadata, not part of the API message
		clean[i] = msg
	}
	return cacheConversation(clean)
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
		if msg.Role == roleUser && hasTextBlock(msg) {
			starts = append(starts, i)
		}
	}
	if len(starts) <= maxTurns {
		return history
	}
	return history[starts[len(starts)-maxTurns]:]
}

// cachedSystem returns the system prompt as a single cache-marked block, so the
// large, stable instructions are read once and reused across the conversation.
func cachedSystem(text string) []systemBlock {
	return []systemBlock{{Type: blockText, Text: text, CacheControl: ephemeral1hCache}}
}

// cachedToolDefs marks the tools block as cacheable (it never changes within a
// version), so the whole tool schema is reused instead of re-sent each turn.
func cachedToolDefs(mcpTools []MCPTool) []Tool {
	tools := toolDefs(mcpTools)
	if n := len(tools); n > 0 {
		tools[n-1].CacheControl = ephemeral1hCache
	}
	return tools
}

// cacheConversation puts a cache breakpoint on the last block of the last message
// so each turn reuses the prior turns as a cached prefix. It copies the tail it
// touches rather than mutating the stored history.
func cacheConversation(msgs []Message) []Message {
	if len(msgs) == 0 {
		return msgs
	}
	out := append([]Message(nil), msgs...)
	last := &out[len(out)-1]
	if len(last.Content) == 0 {
		return out
	}
	blocks := append([]ContentBlock(nil), last.Content...)
	blocks[len(blocks)-1].CacheControl = ephemeralCache
	last.Content = blocks
	return out
}

// hasTextBlock reports whether a message carries human text (a turn start), as
// opposed to a user message that only carries tool results
func hasTextBlock(msg Message) bool {
	for _, b := range msg.Content {
		if b.Type == blockText {
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
		if b.Type == blockThinking || b.Type == blockRedactedThinking {
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
		case blockText:
			s.publish(Event{Type: evtText, Text: b.Text})
		case blockThinking:
			s.publish(Event{Type: evtThinking, Text: b.Thinking})
		case blockToolUse:
			s.publish(Event{Type: evtToolUse, Tool: b.Name, Input: string(b.Input)})
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
// archivedNote leads the system prompt when the app is shelved. It goes FIRST
// because it changes what the rest of the prompt means: the instruction to
// deploy after a config change cannot be followed at all until the app is
// brought back.
func archivedNote(archived bool) string {
	if !archived {
		return ""
	}
	return `IMPORTANT: this app is ARCHIVED. It is powered off and refuses to run: deploy, power on, ` +
		`taking a snapshot and running a command are all rejected, and its subdomain serves nothing. ` +
		`You can still read and write its files. If the user asks for anything that needs the app running, ` +
		`say it has to be unarchived first (the Actions menu on the app page, or POST /api/apps/{app}/unarchive) ` +
		`rather than attempting it and reporting the refusal.

`
}

// connectionsNote tells the model what this app was granted and how to reach it.
// Empty when there is nothing granted, so an app without connections carries no
// text about them at all.
//
// The instruction is to have the APP read the credential at runtime rather than
// fetching it into the conversation: transcripts are stored, so a token printed
// once is a token stored for as long as the transcript lives. It is also simply
// how the app has to work -- an access token expires within the hour.
func connectionsNote(conns []Connection) string {
	if len(conns) == 0 {
		return ""
	}
	// Split by kind, because the two are used in completely different ways: a
	// credential is something the APP fetches at runtime, an MCP server is
	// something the MODEL calls right here. Telling the model to fetch a token
	// for an MCP server sends it at an endpoint that refuses on purpose.
	var credentials, servers []Connection
	for _, c := range conns {
		if c.MCP {
			servers = append(servers, c)
		} else {
			credentials = append(credentials, c)
		}
	}
	var b strings.Builder
	b.WriteString("\n\nThis app has been granted these connections, which it can use to act as its owner:\n")
	for _, c := range conns {
		fmt.Fprintf(&b, "- %s (%s)\n", c.Slug, c.ProviderLabel)
	}
	if len(credentials) > 0 {
		fmt.Fprintf(&b, `
The app reads a usable credential from the container API, by the name above:

  curl %s/api/container/connections/%s/token

That answers {"access_token": "...", "expires_at": "..."} (expires_at is absent when the
credential does not expire). Build this call INTO the app's code so it fetches a token per
run: an OAuth token expires within the hour, so anything you save to a file is dead by the
time it is used. NEVER print a token, echo it, or write it into a file -- read it at the
moment it is needed and use it. GET %s/api/container/connections lists what this app holds.
`, config.ContainerAPIURL, credentials[0].Slug, config.ContainerAPIURL)
	}
	if len(servers) > 0 {
		fmt.Fprintf(&b, `
The MCP servers above have NO credential to fetch -- hostit holds the token and makes the
calls. Their tools are already in your tool list, named %s<tool>; call them directly.
The app itself can do the same on the container API, without any OAuth of its own:

  curl %s/api/container/mcp/%s/tools
  curl %s/api/container/mcp/%s/call \
    -d '{"tool":"...","arguments":{}}'
`, connectedToolPrefix+servers[0].Slug+connectedToolSep, config.ContainerAPIURL, servers[0].Slug, config.ContainerAPIURL, servers[0].Slug)
	}
	b.WriteString(`
If the user asks for something one of these connections would provide, use it rather than
saying no integration is configured.`)
	return b.String()
}

// transportNote tells the assistant the two ways an app reaches hostit's
// container API, so every endpoint example below works over whichever transport
// the app's language makes easy.
func transportNote() string {
	return fmt.Sprintf(`

An app reaches hostit's container API at a plain loopback URL, %s -- an ordinary HTTP client, no token needed (e.g. GET %s/api/container/self). The examples below use it.`,
		config.ContainerAPIURL, config.ContainerAPIURL)
}

// extraContext is the operator's instance-wide note plus the app owner's own
// profile note, joined for appending to the system prompt (empty if neither set).
func (m *Manager) extraContext(app string) string {
	parts := make([]string, 0, 2)
	if p := m.ops.InstancePrompt(); p != "" {
		parts = append(parts, p)
	}
	if p := m.ops.OwnerPrompt(app); p != "" {
		parts = append(parts, p)
	}
	return strings.Join(parts, "\n\n")
}

// previewNote is the shared platform preview guidance (platformdoc), the same
// facts the external /info guide gives -- so the web assistant knows about the
// live preview, the ?hostit_preview cache-buster and the private-app caveat too.
func previewNote() string {
	return "\n\n" + platformdoc.PreviewNote()
}

// extraNote renders the extra context as a leading block, or nothing when empty.
func extraNote(extra string) string {
	if extra == "" {
		return ""
	}
	return extra + "\n\n"
}

func systemPrompt(app string, archived bool, conns []Connection, extra string) string {
	return extraNote(extra) + archivedNote(archived) + transportNote() + previewNote() + connectionsNote(conns) + fmt.Sprintf(`You are hostit's built-in coding assistant, working on a single app named %q.

hostit is a mini-app platform. Each app has a home directory you act on with your tools:
- public/     files served on the web (a "static" app serves exactly this)
- hostit.yml  how the app runs: "mode: static" to serve public/, or "mode: app" with a "run:" command that binds 0.0.0.0:$PORT ($PORT is always 80)
- data/       persistent data you keep between deploys: sqlite databases, state files
- src/, bin/, docs/  source, binaries, docs, by convention

The app is live at its subdomain. A static app serves public/ immediately, so editing files there is enough. But whenever you change hostit.yml (for example switching mode, or setting a run: command) or a run: app's code, you MUST call deploy to make it live -- writing files alone does not apply a configuration change. The container has common runtimes (python3, go, sqlite3).

The app can ask a MODEL a question itself, on the container API, with no API key
of its own -- this is how you build an app that thinks:

  curl %s/api/container/assistant \
    -d '{"prompt":"...","system":"...","max_tokens":500}'

That answers {"text":"...","model":"...","usage":{...}}. Send "messages":
[{"role":"user","content":"..."},{"role":"assistant","content":"..."}] instead of
"prompt" to carry a conversation, since the model keeps nothing between calls.
Add "model":"<id>" to choose a model, or omit it for the default. Read the ids
from GET /api/container/assistant/models rather than guessing them: it lists what
THIS server offers (claude-* run on the operator's Claude subscription, anthropic-*
on the metered API), and a server may have one, the other, or both.
It has NO tools and no file access, and it is metered to this app
and rate-limited, so make one call per thing you actually need rather than one
per loop iteration. When a user asks for something that needs a model at runtime
(summarise these logs, answer in a persona, classify this text), build it on this
rather than telling them to obtain an API key.

Work like a careful engineer: read before you write (list_files, read_file), make the smallest change that works, run_command to build or verify, deploy when the config changed, and read_logs to debug a running app. Explain briefly what you are doing. When the user's request is done, stop and say so. Do not ask permission for each step; just do the work and report what you changed.`, app, config.ContainerAPIURL)
}
