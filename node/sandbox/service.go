// Package sandbox is the node-side engine that runs one Claude Max assistant
// turn as `claude -p` inside a locked-down podman container. It runs on the node
// an app lives on, behind the NodeAgent RunAssistantTurn/AnswerAssistant verbs;
// control keeps the transcript, the SSE fan-out, the rate limiting and the
// accounting (see package assistant).
//
// The container is locked down exactly like an app container: uid-mapped to the
// app's own uid, all privilege escalation off, memory and pid caps, its own
// network. It holds no host mount but the hostit agent binary and the node's
// app-socket dir, so its ONLY tools are the hostit MCP tools reached through
// that socket -- and the node's app socket resolves the caller to the app by
// peer uid, the same scoping an app's own in-container CLI gets. The
// subscription token arrives per turn (never stored on the node) and is passed
// as an env var; a prompt-injection from the app's content cannot read it,
// because every built-in tool is denied and the MCP-only tools cannot touch the
// container's own environment.
package sandbox

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"heckel.io/hostit/node/api"
	"heckel.io/hostit/workspace"
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

	// Event and stream-json type strings, kept local so the parser needs no
	// import from the control-side assistant package.
	evtInit       = "init"
	evtText       = "text"
	evtThinking   = "thinking"
	evtToolUse    = "tool_use"
	evtToolResult = "tool_result"
	evtResult     = "result"
	evtError      = "error"
)

// assistantContainerfile builds the sandbox image: node (Claude Code is a node
// CLI) plus the pinned claude, and nothing else. The hostit binary is
// bind-mounted at run time (as with app containers), never baked in, and the
// subscription is a mounted env/credential, so this image carries no secret.
const (
	assistantContainerfile = `FROM docker.io/library/node:22-slim
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      ca-certificates git \
    && rm -rf /var/lib/apt/lists/* \
    && npm install -g @anthropic-ai/claude-code@` + claudeVersion + ` \
    && npm cache clean --force \
    && mkdir -p ` + assistantContainerHome + ` && chmod 0777 ` + assistantContainerHome + `
CMD ["/bin/bash"]
`
)

// Engine runs one assistant turn per RunTurn/Answer call, on the operator's
// Claude Max subscription. It holds no per-app state and no secret: the target
// app is named per turn (mapped to that app's uid so the node's app socket
// scopes every tool call to it), and the subscription token arrives in the spec.
type Engine struct {
	hostitBin  string // the agent binary on the host, bind-mounted into the sandbox
	socketFile string // the node's app socket (host path); its dir is mounted so the sandbox reaches control
	dataDir    string // where per-app raw session logs are written
	// identity resolves an app to the uid its container maps to and the app id
	// its container/log are named for. The default reads the local passwd file,
	// which on the node knows every app it hosts.
	identity func(appName string) (uid, gid int, appID string, err error)
}

// Identity is the resolver signature; see Engine.identity.
type Identity func(appName string) (uid, gid int, appID string, err error)

// SetIdentity replaces how the engine resolves an app's uid and id (tests).
func (e *Engine) SetIdentity(fn Identity) { e.identity = fn }

// NewEngine builds an engine that reaches control through socketFile (the
// node's app socket) and logs raw sessions under dataDir.
func NewEngine(socketFile, dataDir string) *Engine {
	return &Engine{
		hostitBin:  workspace.HostBinFile,
		socketFile: socketFile,
		dataDir:    dataDir,
		identity:   appIdentity,
	}
}

// RunTurn launches the sandbox for one turn: it feeds prompt (and any uploaded
// images) on stdin, appends systemPrompt to Claude Code's own system prompt (so
// the agent knows it is working on a hostit app), and calls onEvent for each
// streamed event. It blocks until claude exits (or ctx is cancelled, which
// kills the container).
func (e *Engine) RunTurn(ctx context.Context, spec *api.AssistantTurnSpec, onEvent func(*api.AssistantEvent)) error {
	uid, gid, appID, err := e.identity(spec.Name)
	if err != nil {
		return err
	}
	image, err := e.EnsureImage()
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
	stdin, streamJSON := claudeStdin(spec.Prompt, spec.Images)
	args := append(e.baseArgs(name, uid, gid, spec.OAuthToken), "-i", image)
	args = append(args, e.claudeArgs(spec.SystemPrompt, streamJSON)...)

	cmd := exec.CommandContext(ctx, podman, args...)
	cmd.Stdin = strings.NewReader(stdin)
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
	sessionLog := e.openSessionLog(appID)
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
		onEvent(&api.AssistantEvent{Type: evtError, ErrorMsg: msg})
	}
	return nil
}

// Answer runs a ONE-SHOT, tool-less answer on the subscription: `claude -p
// --output-format json` with NO tools -- no MCP server is wired and every
// built-in is denied -- so it cannot touch the app; it just answers. It returns
// the answer text and usage, which is exactly what the app-facing
// /api/container/assistant endpoint needs from the Claude Max backend. Unlike
// RunTurn it keeps no transcript and streams nothing.
func (e *Engine) Answer(ctx context.Context, spec *api.AssistantAnswerSpec) (string, *api.AssistantUsage, error) {
	uid, gid, appID, err := e.identity(spec.Name)
	if err != nil {
		return "", nil, err
	}
	image, err := e.EnsureImage()
	if err != nil {
		return "", nil, fmt.Errorf("cannot prepare the sandbox image: %w", err)
	}
	name := containerName(appID) // randHex-suffixed, so it never collides with a live turn
	defer func() { _ = exec.Command(podman, "rm", "--force", name).Run() }()

	args := append(e.baseArgs(name, uid, gid, spec.OAuthToken), "-i", image)
	args = append(args, answerArgs(spec.Model, spec.System)...)
	cmd := exec.CommandContext(ctx, podman, args...)
	cmd.Stdin = strings.NewReader(spec.Prompt)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// Log the sandbox's stderr operator-side, but never hand it to the tenant:
		// it can carry host paths, container internals or CLI diagnostics that are
		// none of the app's business.
		slog.Warn("Claude answer sandbox failed", "app", spec.Name, "error", err, "stderr", strings.TrimSpace(stderr.String()))
		return "", nil, ErrAnswerBackend
	}
	text, usage, err := parseAnswer(out)
	if err != nil {
		slog.Warn("Claude answer could not be read", "app", spec.Name, "error", err)
		return "", nil, ErrAnswerBackend
	}
	return text, usage, nil
}

// ErrAnswerBackend is what a failed subscription answer returns to the tenant --
// a generic message, so the sandbox's own diagnostics stay operator-side.
var ErrAnswerBackend = errors.New("the assistant backend could not answer right now")

// Shell drops into an interactive shell in the sandbox, for trying the claude
// invocation by hand on the host (debugging without redeploys). token is the
// subscription token the debug session runs on.
func (e *Engine) Shell(appName, token string) error {
	uid, gid, appID, err := e.identity(appName)
	if err != nil {
		return err
	}
	image, err := e.EnsureImage()
	if err != nil {
		return err
	}
	name := containerName(appID)
	socket := filepath.Join(workspace.ContainerRunDir, "hostit.sock")
	args := append(e.baseArgs(name, uid, gid, token), "-it", image, "/bin/bash") //nolint // interactive shell, no claude args
	fmt.Fprintf(os.Stderr, "==> shell in sandbox %s (app=%s uid=%d). Try: claude --version; hostit mcp --socket %s\n", name, appName, uid, socket)
	cmd := exec.Command(podman, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// baseArgs are the "podman run" flags common to every launch: locked down exactly
// like an app container (uid-mapped, no-new-privileges, mem/pids limits, its own
// network), holding no host mount but the hostit binary and the node's app-socket
// dir, and carrying the subscription plus the flags that keep claude
// non-interactive and away from its auto-updater.
func (e *Engine) baseArgs(name string, uid, gid int, token string) []string {
	socketDir := filepath.Dir(e.socketFile)
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
		// container, and it arrives per turn (never stored on the node). Passed as
		// an env var for now; the MCP-only agent has no tool to read /proc, but a
		// 0400 file is the hardening TODO.
		"--env", "CLAUDE_CODE_OAUTH_TOKEN=" + token,
		"--env", "DISABLE_TELEMETRY=1",
		"--env", "DISABLE_ERROR_REPORTING=1",
		"--env", "DISABLE_AUTOUPDATER=1",
		// The agent binary and the app socket: the same two mounts every app
		// container gets, so the sandbox reaches its MCP tools the way an app's own
		// CLI does -- the node's app socket resolves the caller to the app by peer
		// uid (the uid map above), and relays to control.
		"--volume", e.hostitBin + ":" + workspace.ContainerBinFile + ":ro",
		"--volume", socketDir + ":" + workspace.ContainerRunDir + ":ro",
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
func (e *Engine) claudeArgs(systemPrompt string, streamJSON bool) []string {
	socket := filepath.Join(workspace.ContainerRunDir, "hostit.sock")
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"hostit":{"command":%q,"args":["mcp","--socket",%q]}}}`, workspace.ContainerBinFile, socket)
	args := []string{
		"claude", "-p",
		"--output-format", "stream-json",
		"--verbose",
	}
	// An image turn feeds stdin as stream-json (see claudeStdin); text turns
	// keep the plain prompt, so the flag rides only when needed.
	if streamJSON {
		args = append(args, "--input-format", "stream-json")
	}
	args = append(args,
		"--strict-mcp-config",
		"--mcp-config", mcpConfig,
		"--permission-mode", "dontAsk",
		"--allowedTools", mcpToolGlob,
		"--disallowedTools", disallowedBuiltins,
	)
	if strings.TrimSpace(systemPrompt) != "" {
		args = append(args, "--append-system-prompt", systemPrompt)
	}
	return args
}

// answerArgs builds `claude -p --output-format json` with NO tools: no MCP server
// is wired (so none of hostit's tools exist) and every built-in is denied, so a
// one-shot answer cannot read, write or run anything. model is passed to
// --model so the caller's claude-* choice is honoured; system rides as an
// appended system prompt.
func answerArgs(model, system string) []string {
	args := []string{
		"claude", "-p",
		"--output-format", "json",
		"--permission-mode", "dontAsk",
		"--disallowedTools", disallowedBuiltins,
	}
	if strings.TrimSpace(model) != "" {
		args = append(args, "--model", model)
	}
	if strings.TrimSpace(system) != "" {
		args = append(args, "--append-system-prompt", system)
	}
	return args
}

// claudeStdin renders what the sandbox feeds claude on stdin. Without images
// it is the plain prompt, exactly as before. With images it is ONE stream-json
// user message -- image blocks first, then the prompt text -- because -p's
// plain text mode has no way to carry image bytes; the switch is per turn so
// the ordinary path stays the simple one.
func claudeStdin(prompt string, images []api.AssistantImage) (stdin string, streamJSON bool) {
	if len(images) == 0 {
		return prompt, false
	}
	content := make([]map[string]any, 0, len(images)+1)
	for _, img := range images {
		content = append(content, map[string]any{
			"type":   "image",
			"source": map[string]any{"type": "base64", "media_type": img.MediaType, "data": img.Data},
		})
	}
	content = append(content, map[string]any{"type": "text", "text": prompt})
	line, err := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": content},
	})
	if err != nil {
		return prompt, false // cannot happen for these types; fail open to text
	}
	return string(line) + "\n", true
}

// AssistantLogDir is where per-app raw claude session logs are written, under the
// node data dir. Root-owned (0700); a tenant cannot read another app's -- or its
// own -- session log.
func (e *Engine) AssistantLogDir() string {
	return filepath.Join(e.dataDir, "assistant")
}

// openSessionLog creates (truncating) the raw session log for one app's current
// turn, keyed on the app id (not its name, which a rename would change). Best
// effort: returns nil on failure, and the turn proceeds without it.
func (e *Engine) openSessionLog(appID string) *os.File {
	if err := os.MkdirAll(e.AssistantLogDir(), 0o700); err != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(e.AssistantLogDir(), filepath.Base(appID)+".jsonl"),
		os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	return f
}

// EnsureImage builds the sandbox image if it is not already present, tagging it
// by a hash of the Containerfile so an edit rebuilds it.
func (e *Engine) EnsureImage() (string, error) {
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
// id. The uid map scopes the node's app socket to this one app; the id keys
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
	return uid, gid, workspace.IDFromHomeDir(u.HomeDir), nil
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
