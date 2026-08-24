package assistant

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/controlconf"
	"heckel.io/hostit/workspace"
)

// The sandbox launch flags are the load-bearing security control (see
// plans/260810-hostit-claude-max-backend.md): the agent runs on the operator's
// subscription, so a prompt injection from the app's own content must not be able
// to read the credential or reach the web. These tests pin the argv that enforces
// that -- MCP-only tools, every built-in denied, uid-mapped to the app, the token
// only in this container -- so a refactor (or a claude version bump that reopens a
// tool) breaks a test loudly instead of silently widening the attack surface.

func testSandbox() *Sandbox {
	return &Sandbox{
		conf: &controlconf.Config{
			ControlSocketFile:    "/run/hostit/hostit.sock",
			DataDir:              "/var/lib/hostit",
			ClaudeCodeOAuthToken: "sk-test-subscription-token",
		},
		hostitBin: "/usr/local/bin/hostit",
	}
}

// hasFlagValue reports whether args contains flag immediately followed by value.
func hasFlagValue(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func flagValue(args []string, flag string) (string, bool) {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1], true
		}
	}
	return "", false
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func TestSandboxClaudeArgsAreMCPOnly(t *testing.T) {
	args := testSandbox().claudeArgs("", false)

	// Non-interactive, streaming.
	if !hasFlagValue(args, "--output-format", "stream-json") {
		t.Errorf("expected --output-format stream-json, got %v", args)
	}
	if !contains(args, "-p") {
		t.Errorf("expected -p (print mode), got %v", args)
	}

	// The MCP config must be loaded, and strictly (not --bare, which skips it and
	// would leave the agent with NO tools).
	if !contains(args, "--strict-mcp-config") {
		t.Error("expected --strict-mcp-config so the hostit MCP server is the only tool source")
	}
	if contains(args, "--bare") {
		t.Error("--bare must never be used: it skips --mcp-config, disarming the whole model")
	}

	// The load-bearing pair: allow ONLY the hostit MCP tools, deny EVERY built-in.
	if !hasFlagValue(args, "--allowedTools", mcpToolGlob) {
		t.Errorf("expected --allowedTools %q, got %v", mcpToolGlob, args)
	}
	if !hasFlagValue(args, "--permission-mode", "dontAsk") {
		t.Errorf("expected --permission-mode dontAsk, got %v", args)
	}
	if !hasFlagValue(args, "--disallowedTools", disallowedBuiltins) {
		t.Errorf("expected --disallowedTools to be the full pinned built-in set, got %v", args)
	}

	// The MCP config names the hostit binary + the daemon socket (peercred-scoped).
	mcpConfig, ok := flagValue(args, "--mcp-config")
	if !ok {
		t.Fatalf("expected --mcp-config, got %v", args)
	}
	if !strings.Contains(mcpConfig, workspace.ContainerBinFile) {
		t.Errorf("mcp-config should invoke the binary at its in-sandbox path, got %q", mcpConfig)
	}
	if !strings.Contains(mcpConfig, "/run/hostit/hostit.sock") {
		t.Errorf("mcp-config should point at the daemon socket, got %q", mcpConfig)
	}

	// A system prompt is appended only when non-empty.
	if contains(args, "--append-system-prompt") {
		t.Error("empty system prompt must not add --append-system-prompt")
	}
	withPrompt := testSandbox().claudeArgs("You are working on a hostit app.", false)
	if v, ok := flagValue(withPrompt, "--append-system-prompt"); !ok || v != "You are working on a hostit app." {
		t.Errorf("expected the system prompt appended, got %v", withPrompt)
	}
}

// TestSandboxDisallowsEveryBuiltinTool is the canary for a claude version bump.
// disallowedBuiltins is a BLOCKLIST that only works because the version is pinned;
// a new built-in tool not on the list silently reopens a path to the credential.
// The exact-equality assertion forces whoever bumps claudeVersion to consciously
// re-derive the set (and update this constant), and the semantic check guarantees
// the most dangerous built-ins are always denied.
func TestSandboxDisallowsEveryBuiltinTool(t *testing.T) {
	const pinned = "Task,Bash,CronCreate,CronDelete,CronList,DesignSync,Edit,EnterWorktree,ExitWorktree,NotebookEdit,Read,ReportFindings,ScheduleWakeup,SendMessage,Skill,TaskCreate,TaskGet,TaskList,TaskOutput,TaskStop,TaskUpdate,ToolSearch,WebFetch,WebSearch,Workflow,Write,Glob,Grep,MultiEdit,TodoWrite"
	if disallowedBuiltins != pinned {
		t.Fatalf("disallowedBuiltins drifted from the pinned set for claude %s.\n"+
			"If you bumped claudeVersion, re-derive the built-in tool list (see the\n"+
			"comment on disallowedBuiltins) and update BOTH the constant and this test.\n"+
			"got:  %s\nwant: %s", claudeVersion, disallowedBuiltins, pinned)
	}

	denied := make(map[string]bool)
	for _, name := range strings.Split(disallowedBuiltins, ",") {
		denied[name] = true
	}
	// The tools that would each, alone, defeat the sandbox: shell/file/web access
	// and the "safe" tools that run without a permission prompt.
	for _, critical := range []string{
		"Bash", "Read", "Write", "Edit", "MultiEdit", "NotebookEdit",
		"Task", "Skill", "ToolSearch", "WebFetch", "WebSearch", "Glob", "Grep",
	} {
		if !denied[critical] {
			t.Errorf("built-in %q must be in --disallowedTools", critical)
		}
	}
}

func TestSandboxBaseArgsLockdown(t *testing.T) {
	const (
		uid = 200000
		gid = 200000
	)
	args := testSandbox().baseArgs("hostit-assistant-demo6-abcd", uid, gid)

	// uid/gid mapped to the app's block, so the daemon's peercred socket scopes
	// every tool call to exactly this app.
	if !hasFlagValue(args, "--uidmap", "0:200000:65536") {
		t.Errorf("expected --uidmap 0:200000:65536, got %v", args)
	}
	if !hasFlagValue(args, "--gidmap", "0:200000:65536") {
		t.Errorf("expected --gidmap 0:200000:65536, got %v", args)
	}
	if !hasFlagValue(args, "--name", "hostit-assistant-demo6-abcd") {
		t.Errorf("expected the container name passed through, got %v", args)
	}

	// Locked down like an app container.
	if !contains(args, "--rm") {
		t.Error("expected --rm so a normal exit removes the container")
	}
	if !hasFlagValue(args, "--security-opt", "no-new-privileges") {
		t.Errorf("expected --security-opt no-new-privileges, got %v", args)
	}
	if !hasFlagValue(args, "--network", "slirp4netns") {
		t.Errorf("expected an isolated slirp4netns network, got %v", args)
	}

	// The subscription rides ONLY in this container's env, and claude's
	// auto-updater is disabled (a new version could reopen a built-in tool).
	if !hasFlagValue(args, "--env", "CLAUDE_CODE_OAUTH_TOKEN=sk-test-subscription-token") {
		t.Errorf("expected the subscription token in the sandbox env, got %v", args)
	}
	if !hasFlagValue(args, "--env", "DISABLE_AUTOUPDATER=1") {
		t.Errorf("expected DISABLE_AUTOUPDATER=1, got %v", args)
	}

	// Only the hostit binary and the daemon socket dir are mounted, both read-only.
	// The host source is wherever the binary lives (hostit-app, off $PATH); in
	// the sandbox it appears at the container path, same as in an app container.
	if !hasFlagValue(args, "--volume", "/usr/local/bin/hostit:"+workspace.ContainerBinFile+":ro") {
		t.Errorf("expected the hostit binary mounted read-only at %s, got %v", workspace.ContainerBinFile, args)
	}
	if !hasFlagValue(args, "--volume", "/run/hostit:/run/hostit:ro") {
		t.Errorf("expected the socket dir mounted read-only, got %v", args)
	}
}

func TestSandboxContainerNameKeysOnAppID(t *testing.T) {
	// The container name must derive from the app id, not the app name (a rename
	// changes the name but not apps/<id>), or Stop's `podman rm --force <name>`
	// could target the wrong -- or an orphaned -- container.
	name := containerName("demo6")
	if !strings.HasPrefix(name, "hostit-assistant-demo6-") {
		t.Errorf("container name must be keyed on the app id, got %q", name)
	}
	if name == containerName("demo6") {
		t.Error("expected a random suffix so concurrent turns for one app never collide")
	}
}

func TestSandboxSessionLogKeysOnAppID(t *testing.T) {
	dir := t.TempDir()
	s := testSandbox()
	s.conf.DataDir = dir

	f := s.openSessionLog("demo6")
	if f == nil {
		t.Fatal("openSessionLog returned nil")
	}
	defer f.Close()

	want := filepath.Join(dir, "assistant", "demo6.jsonl")
	if f.Name() != want {
		t.Errorf("session log must be keyed on the app id: got %q, want %q", f.Name(), want)
	}

	// The log dir is operator-only (0700) and the log itself 0600: a tenant must
	// not be able to read another app's -- or its own -- raw session.
	di, err := os.Stat(filepath.Join(dir, "assistant"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("assistant log dir perms = %o, want 700", perm)
	}
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("session log perms = %o, want 600", perm)
	}
}

// The MCP bridge must be the AGENT binary (workspace.HostBinFile, the one bind-
// mounted into every app container), never this daemon's own executable: since
// the cmd split the daemon is hostit-control, which has no "mcp" command, and
// mounting it silently broke the sandbox's only tool surface.
func TestNewSandboxMountsTheAgentBinary(t *testing.T) {
	s, err := NewSandbox(&controlconf.Config{ClaudeCodeOAuthToken: "sk-test"})
	if err != nil {
		t.Fatal(err)
	}
	if s.hostitBin != workspace.HostBinFile {
		t.Fatalf("sandbox mounts %q as the MCP bridge, want the agent binary %q", s.hostitBin, workspace.HostBinFile)
	}
}

// Images cannot ride -p's plain-text stdin: with any image the sandbox
// switches to stream-json input and feeds ONE user message whose blocks carry
// the images and then the prompt text.
func TestClaudeStdinCarriesImages(t *testing.T) {
	t.Parallel()
	stdin, streamJSON := claudeStdin("describe this", nil)
	assert.False(t, streamJSON, "no images: plain -p stdin, unchanged behavior")
	assert.Equal(t, "describe this", stdin)

	stdin, streamJSON = claudeStdin("describe this", []Attachment{{Path: "uploads/a.png", MediaType: "image/png", Data: "aGVsbG8="}})
	require.True(t, streamJSON)
	require.True(t, strings.HasSuffix(stdin, "\n"), "stream-json input is line-delimited")
	var line struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type   string       `json:"type"`
				Text   string       `json:"text"`
				Source *ImageSource `json:"source"`
			} `json:"content"`
		} `json:"message"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdin), &line))
	assert.Equal(t, "user", line.Type)
	assert.Equal(t, "user", line.Message.Role)
	require.Len(t, line.Message.Content, 2)
	assert.Equal(t, "image", line.Message.Content[0].Type)
	require.NotNil(t, line.Message.Content[0].Source)
	assert.Equal(t, "base64", line.Message.Content[0].Source.Type)
	assert.Equal(t, "image/png", line.Message.Content[0].Source.MediaType)
	assert.Equal(t, "aGVsbG8=", line.Message.Content[0].Source.Data)
	assert.Equal(t, "text", line.Message.Content[1].Type)
	assert.Equal(t, "describe this", line.Message.Content[1].Text)
}

func TestClaudeArgsInputFormat(t *testing.T) {
	t.Parallel()
	assert.NotContains(t, testSandbox().claudeArgs("", false), "--input-format")
	args := testSandbox().claudeArgs("", true)
	i := slices.Index(args, "--input-format")
	require.GreaterOrEqual(t, i, 0, "stream-json input must be switched on for an image turn")
	require.Less(t, i+1, len(args))
	assert.Equal(t, "stream-json", args[i+1])
}

// parseAnswer decodes the subscription's one-shot `claude -p --output-format
// json` output. It is the whole Claude-Max answer decode, so all four shapes are
// pinned here (the live path is exercised on a real box, not in CI).
func TestParseAnswer(t *testing.T) {
	t.Parallel()
	t.Run("result and usage", func(t *testing.T) {
		text, u, err := parseAnswer([]byte(`{"type":"result","result":"ahoy","is_error":false,` +
			`"usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":2,"cache_read_input_tokens":3}}`))
		require.NoError(t, err)
		assert.Equal(t, "ahoy", text)
		assert.Equal(t, Usage{InputTokens: 10, OutputTokens: 5, CacheWriteTokens: 2, CacheReadTokens: 3}, u)
	})
	t.Run("is_error carries detail and no text", func(t *testing.T) {
		text, _, err := parseAnswer([]byte(`{"result":"quota exceeded","is_error":true}`))
		require.Error(t, err)
		assert.Empty(t, text)
		assert.Contains(t, err.Error(), "quota exceeded", "the detail is there for the caller to LOG")
	})
	t.Run("non-JSON is an error", func(t *testing.T) {
		_, _, err := parseAnswer([]byte("claude: command not found"))
		require.Error(t, err)
	})
	t.Run("missing usage is zero, not an error", func(t *testing.T) {
		text, u, err := parseAnswer([]byte(`{"result":"hi","is_error":false}`))
		require.NoError(t, err)
		assert.Equal(t, "hi", text)
		assert.Equal(t, Usage{}, u)
	})
}

// answerArgs must be TOOL-LESS: no MCP server is wired and every built-in is
// denied, so a one-shot answer cannot read, write, run or reach the network's
// tools. This is the canary for that guarantee (the token is mounted and the
// sandbox has egress, so the only barrier is "no tools").
func TestAnswerArgsAreToolless(t *testing.T) {
	t.Parallel()
	args := answerArgs("claude-opus-5", "be brief")
	assert.True(t, hasFlagValue(args, "--output-format", "json"), "one-shot json, not a stream")
	assert.True(t, hasFlagValue(args, "--disallowedTools", disallowedBuiltins), "the same blocklist the tool path uses")
	assert.NotContains(t, args, "--mcp-config", "no hostit MCP server, so none of its tools exist")
	assert.NotContains(t, args, "--allowedTools", "nothing is allow-listed either")
	assert.True(t, hasFlagValue(args, "--model", "claude-opus-5"))
	assert.True(t, hasFlagValue(args, "--append-system-prompt", "be brief"))

	bare := answerArgs("", "")
	assert.NotContains(t, bare, "--model", "no --model when none is chosen")
	assert.NotContains(t, bare, "--append-system-prompt", "no system flag when none is given")
	assert.True(t, hasFlagValue(bare, "--disallowedTools", disallowedBuiltins), "still tool-less with no model/system")
}
