package assistant

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"heckel.io/hostit/config"
	"heckel.io/hostit/node"
)

const (
	// claudeVersion is the pinned Claude Code the sandbox image installs. It is
	// pinned (and auto-update disabled) because a new version could enable a
	// built-in tool by default and silently undo the MCP-only restriction that
	// keeps the subscription from being exfiltrated. See plans/260810-*.
	claudeVersion = "2.1.226"
	// assistantImagePrefix names the sandbox image; the tag is a hash of the
	// Containerfile, so editing it rebuilds (same scheme as the workspace image).
	assistantImagePrefix = "localhost/hostit-assistant"
	// assistantContainerHome is the sandbox's HOME: an ephemeral, image-provided
	// dir. No app home is ever mounted here -- that is the isolation. claude keeps
	// its scratch (~/.claude) here and it is thrown away with the container.
	assistantContainerHome = "/home/app"
	// assistantUIDCount is how many uids the sandbox maps, matching an app's block
	assistantUIDCount = 65536
	// assistantMemoryMB and assistantPids bound the sandbox like an app container
	assistantMemoryMB = 1024
	assistantPids     = 512
	// podman is the container CLI the sandbox drives, named once rather than repeated
	podman = "podman"

	// mcpToolGlob allows exactly the hostit MCP server's tools and nothing else.
	mcpToolGlob = "mcp__hostit__*"
	// mcpToolPrefix is how Claude Code namespaces the hostit MCP tools; stripped
	// for display so the UI shows "write_file", not "mcp__hostit__write_file".
	mcpToolPrefix = "mcp__hostit__"
	// disallowedBuiltins is the COMPLETE set of Claude Code's built-in tools for
	// the pinned claudeVersion, every one denied so the agent's only tools are the
	// hostit MCP tools. This is the load-bearing control (see the plan doc): a
	// blocklist works here ONLY because the version is pinned, and it MUST be
	// re-derived on every version bump -- a new built-in tool not on this list
	// would silently re-open a path to the credential. Verify with:
	//   claude -p --output-format stream-json ... </dev/null | jq '.tools'
	// against a run with NO --disallowedTools; the difference is what to add here.
	// `--permission-mode dontAsk` alone is NOT enough: some built-ins (ToolSearch)
	// are "safe" tools that run without a prompt, so they must be denied by name.
	disallowedBuiltins = "Task,Bash,CronCreate,CronDelete,CronList,DesignSync,Edit,EnterWorktree,ExitWorktree,NotebookEdit,Read,ReportFindings,ScheduleWakeup,SendMessage,Skill,TaskCreate,TaskGet,TaskList,TaskOutput,TaskStop,TaskUpdate,ToolSearch,WebFetch,WebSearch,Workflow,Write,Glob,Grep,MultiEdit,TodoWrite"
)

// assistantContainerfile builds the sandbox image: node (Claude Code is a node
// CLI) plus the pinned claude, and nothing else. The hostit binary is
// bind-mounted at run time (as with app containers), never baked in, and the
// subscription is a mounted env/credential, so this image carries no secret.
const assistantContainerfile = `FROM docker.io/library/node:22-slim
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      ca-certificates git \
    && rm -rf /var/lib/apt/lists/* \
    && npm install -g @anthropic-ai/claude-code@` + claudeVersion + ` \
    && npm cache clean --force \
    && mkdir -p ` + assistantContainerHome + ` && chmod 0777 ` + assistantContainerHome + `
CMD ["/bin/bash"]
`

// Sandbox runs one assistant turn as `claude -p` inside a locked-down
// podman container on the operator's Claude Max subscription. It holds no per-app
// state: the target app is named per turn, mapped to that app's uid so the
// daemon's peercred socket scopes every tool call to it. Both the CLI PoC and the
// server's claude-cli backend drive it.
type Sandbox struct {
	conf      *config.Config
	hostitBin string
}

// NewSandbox builds a sandbox launcher from the daemon config; it needs
// the subscription token (config claude-code-oauth-token) to run.
//
// The MCP bridge mounted into the sandbox is the AGENT binary (the same one
// bind-mounted into every app container), NOT this daemon's own executable:
// since the cmd split the daemon is hostit-control, which has no "mcp"
// command -- mounting os.Executable() worked only while one fused binary was
// both the daemon and the CLI.
func NewSandbox(conf *config.Config) (*Sandbox, error) {
	return &Sandbox{conf: conf, hostitBin: node.HostitBinFile}, nil
}

// StreamUsage is one turn's token usage as claude reports it.
type StreamUsage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheWriteTokens int64
	CacheReadTokens  int64
}

// StreamEvent is one normalized event from claude's stream, already
// stripped of the MCP tool-name prefix. Its consumers are the CLI (which prints
// it) and the server (which maps it to the assistant's own SSE events and stores
// the transcript).
type StreamEvent struct {
	Type     string // init | text | thinking | tool_use | tool_result | result | error
	Text     string
	Tool     string
	Input    string
	Output   string
	IsError  bool
	Model    string
	Tools    []string
	Usage    *StreamUsage
	Result   string
	ErrorMsg string
}

// RunTurn launches the sandbox for one turn: it feeds prompt on stdin, appends
// systemPrompt to Claude Code's own system prompt (so the agent knows it is
// working on a hostit app), and calls onEvent for each streamed event. It blocks
// until claude exits (or ctx is cancelled, which kills the container).
func (s *Sandbox) RunTurn(ctx context.Context, appName, prompt, systemPrompt string, onEvent func(StreamEvent)) error {
	uid, gid, appID, err := appIdentity(appName)
	if err != nil {
		return err
	}
	image, err := s.EnsureImage()
	if err != nil {
		return fmt.Errorf("cannot prepare the sandbox image: %w", err)
	}
	name := containerName(appID)
	// Guarantee the container (and claude inside) is gone when the turn ends. A
	// normal exit --rm's it, but if the turn is cancelled (the owner pressed Stop, or
	// it timed out) the podman client is killed and can orphan the container -- which
	// would keep a long task running and burning the subscription. Remove it
	// explicitly, out of band from the (now cancelled) turn context.
	defer func() { _ = exec.Command(podman, "rm", "--force", name).Run() }()
	args := append(s.baseArgs(name, uid, gid), "-i", image)
	args = append(args, s.claudeArgs(systemPrompt)...)

	cmd := exec.CommandContext(ctx, podman, args...)
	cmd.Stdin = strings.NewReader(prompt)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Tee the raw claude session (full stream-json) to an operator-readable log, so a
	// complex turn can be watched live (tail -f) or inspected after: the web chat
	// only shows distilled events. Best-effort -- a log failure never fails the turn.
	sessionLog := s.openSessionLog(appID)
	if sessionLog != nil {
		defer sessionLog.Close()
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	sawResult := false
	for scanner.Scan() {
		line := scanner.Bytes()
		if sessionLog != nil {
			_, _ = sessionLog.Write(line)
			_, _ = sessionLog.Write([]byte{'\n'})
		}
		for _, ev := range parseAssistantStreamLine(line) {
			if ev.Type == evtResult {
				sawResult = true
			}
			onEvent(ev)
		}
	}
	if err := cmd.Wait(); err != nil && !sawResult {
		// No result event and a non-zero exit: surface claude's stderr as an error
		// event so the turn ends cleanly with a message rather than silently.
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		onEvent(StreamEvent{Type: evtError, ErrorMsg: msg})
	}
	return nil
}

// Shell drops into an interactive shell in the sandbox, for trying the claude
// invocation by hand on the host (debugging without redeploys).
func (s *Sandbox) Shell(appName string) error {
	uid, gid, appID, err := appIdentity(appName)
	if err != nil {
		return err
	}
	image, err := s.EnsureImage()
	if err != nil {
		return err
	}
	name := containerName(appID)
	args := append(s.baseArgs(name, uid, gid), "-it", image, "/bin/bash")
	fmt.Fprintf(os.Stderr, "==> shell in sandbox %s (app=%s uid=%d). Try: claude --version; hostit mcp --socket %s\n", name, appName, uid, s.conf.SocketFile)
	cmd := exec.Command(podman, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// baseArgs are the "podman run" flags common to every launch: locked down exactly
// like an app container (uid-mapped, no-new-privileges, mem/pids limits, its own
// network), holding no host mount but the hostit binary and the daemon socket
// dir, and carrying the subscription plus the flags that keep claude
// non-interactive and away from its auto-updater.
func (s *Sandbox) baseArgs(name string, uid, gid int) []string {
	socketDir := filepath.Dir(s.conf.SocketFile)
	return []string{
		"run", "--rm", "--name", name,
		"--uidmap", fmt.Sprintf("0:%d:%d", uid, assistantUIDCount),
		"--gidmap", fmt.Sprintf("0:%d:%d", gid, assistantUIDCount),
		"--network", "slirp4netns",
		"--memory", strconv.Itoa(assistantMemoryMB) + "m",
		"--pids-limit", strconv.Itoa(assistantPids),
		"--security-opt", "no-new-privileges",
		"--security-opt", "apparmor=unconfined",
		"--env", "HOME=" + assistantContainerHome,
		"--workdir", assistantContainerHome,
		// The subscription: only ever mounted into THIS container, never an app
		// container. Passed as an env var for now; the MCP-only agent has no tool to
		// read /proc, but a 0400 file is the hardening TODO.
		"--env", "CLAUDE_CODE_OAUTH_TOKEN=" + s.conf.ClaudeCodeOAuthToken,
		"--env", "DISABLE_TELEMETRY=1",
		"--env", "DISABLE_ERROR_REPORTING=1",
		"--env", "DISABLE_AUTOUPDATER=1",
		"--volume", s.hostitBin + ":" + s.hostitBin + ":ro",
		"--volume", socketDir + ":" + socketDir + ":ro",
	}
}

// claudeArgs is the claude invocation. The load-bearing control: only the hostit
// MCP tools are allowed and every built-in tool is denied, so a prompt injection
// from the app's own content cannot make the agent read its credential or reach
// the web -- every action it can take is a mediated, app-scoped hostit operation.
//
// --strict-mcp-config (NOT --bare): --bare would skip --mcp-config entirely and
// leave the agent with no tools. The prompt is fed on stdin (see RunTurn), not as
// an argument, because --mcp-config is variadic and would swallow a trailing
// positional prompt.
func (s *Sandbox) claudeArgs(systemPrompt string) []string {
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"hostit":{"command":%q,"args":["mcp","--socket",%q]}}}`, s.hostitBin, s.conf.SocketFile)
	args := []string{
		"claude", "-p",
		"--output-format", "stream-json",
		"--verbose",
		"--strict-mcp-config",
		"--mcp-config", mcpConfig,
		"--permission-mode", "dontAsk",
		"--allowedTools", mcpToolGlob,
		"--disallowedTools", disallowedBuiltins,
	}
	if strings.TrimSpace(systemPrompt) != "" {
		args = append(args, "--append-system-prompt", systemPrompt)
	}
	return args
}

// AssistantLogDir is where per-app raw claude session logs are written, under the
// data dir. Root-owned (0700); a tenant cannot read another app's -- or its own --
// session log.
func (s *Sandbox) AssistantLogDir() string {
	return filepath.Join(s.conf.DataDir, "assistant")
}

// openSessionLog creates (truncating) the raw session log for one app's current
// turn, keyed on the app id (not its name, which a rename would change). Best
// effort: returns nil on failure, and the turn proceeds without it.
func (s *Sandbox) openSessionLog(appID string) *os.File {
	if err := os.MkdirAll(s.AssistantLogDir(), 0o700); err != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(s.AssistantLogDir(), filepath.Base(appID)+".jsonl"),
		os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	return f
}

// EnsureImage builds the sandbox image if it is not already present, tagging it
// by a hash of the Containerfile so an edit rebuilds it.
func (s *Sandbox) EnsureImage() (string, error) {
	sum := sha256.Sum256([]byte(assistantContainerfile))
	tag := assistantImagePrefix + ":" + hex.EncodeToString(sum[:6])
	if err := exec.Command(podman, "image", "exists", tag).Run(); err == nil {
		return tag, nil
	}
	dir, err := os.MkdirTemp("", "hostit-assistant-build-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "Containerfile"), []byte(assistantContainerfile), 0o644); err != nil {
		return "", err
	}
	fmt.Fprintf(os.Stderr, "==> building sandbox image %s (first run; installs claude %s)...\n", tag, claudeVersion)
	build := exec.Command(podman, "build", "-t", tag, "-f", filepath.Join(dir, "Containerfile"), dir)
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		return "", err
	}
	return tag, nil
}

// appIdentity resolves an app's Unix user to its uid/gid block base and its stable
// id. The uid map scopes the daemon's peercred socket to this one app; the id keys
// the (ephemeral) container name and the session log, so a rename never leaves a
// mismatched or orphaned name -- the app's home is apps/<id>/home/app (inside the
// id-keyed app subvolume), so the id comes out of the home path.
func appIdentity(appName string) (uid, gid int, appID string, err error) {
	u, err := user.Lookup(appName)
	if err != nil {
		return 0, 0, "", fmt.Errorf("cannot resolve app user %q: %w (is the app deployed on this host?)", appName, err)
	}
	uid, _ = strconv.Atoi(u.Uid)
	gid, _ = strconv.Atoi(u.Gid)
	return uid, gid, node.IDFromHomeDir(u.HomeDir), nil
}

// containerName is the ephemeral sandbox container name for one turn, keyed on the
// app id (not its name -- a rename would orphan it) plus a random suffix so
// concurrent turns for the same app never collide.
func containerName(appID string) string {
	return "hostit-assistant-" + appID + "-" + randHex(4)
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// parseAssistantStreamLine turns one claude stream-json line into normalized
// events. It returns ALL meaningful events for the line (nil for a line it does not
// care about), so a message that batches several blocks -- e.g. parallel tool
// calls, or several tool_results together -- surfaces every one. Dropping the extra
// blocks would leave a tool spinning forever on a result that never arrived.
func parseAssistantStreamLine(line []byte) []StreamEvent {
	var raw struct {
		Type    string          `json:"type"`
		Subtype string          `json:"subtype"`
		Model   string          `json:"model"`
		Tools   []string        `json:"tools"`
		Message json.RawMessage `json:"message"`
		Result  string          `json:"result"`
		IsError bool            `json:"is_error"`
		Usage   json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil
	}
	switch raw.Type {
	case "system":
		if raw.Subtype == "init" {
			return []StreamEvent{{Type: evtInit, Model: raw.Model, Tools: raw.Tools}}
		}
	case "assistant", "user":
		return blockEvents(raw.Message)
	case "result":
		return []StreamEvent{{Type: evtResult, Result: raw.Result, IsError: raw.IsError, Usage: parseUsage(raw.Usage)}}
	}
	return nil
}

// blockEvents returns one event per meaningful content block in a message.
func blockEvents(raw json.RawMessage) []StreamEvent {
	var msg struct {
		Content []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			Thinking  string          `json:"thinking"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			Content   json.RawMessage `json:"content"`
			IsError   bool            `json:"is_error"`
			ToolUseID string          `json:"tool_use_id"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil
	}
	var out []StreamEvent
	for _, b := range msg.Content {
		switch b.Type {
		case blockText:
			if strings.TrimSpace(b.Text) != "" {
				out = append(out, StreamEvent{Type: evtText, Text: b.Text})
			}
		case blockThinking:
			if strings.TrimSpace(b.Thinking) != "" {
				out = append(out, StreamEvent{Type: evtThinking, Text: b.Thinking})
			}
		case blockToolUse:
			out = append(out, StreamEvent{Type: evtToolUse, Tool: stripToolPrefix(b.Name), Input: string(b.Input)})
		case blockToolResult:
			out = append(out, StreamEvent{Type: evtToolResult, Output: toolResultText(b.Content), IsError: b.IsError})
		}
	}
	return out
}

// toolResultText pulls the text out of a tool_result content, which the API
// carries either as a bare string or as an array of text blocks.
func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		parts := make([]string, 0, len(blocks))
		for _, b := range blocks {
			parts = append(parts, b.Text)
		}
		return strings.Join(parts, " ")
	}
	return string(raw)
}

func parseUsage(raw json.RawMessage) *StreamUsage {
	if len(raw) == 0 {
		return nil
	}
	var u struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	}
	if json.Unmarshal(raw, &u) != nil {
		return nil
	}
	return &StreamUsage{
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
	}
}

// stripToolPrefix turns "mcp__hostit__write_file" into "write_file" for display.
func stripToolPrefix(name string) string {
	return strings.TrimPrefix(name, mcpToolPrefix)
}
