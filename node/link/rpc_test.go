package link

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/appctl"
	"heckel.io/hostit/cluster"
	"heckel.io/hostit/node/api"
	"heckel.io/hostit/store"
)

func TestRPCRoundTrips(t *testing.T) {
	t.Parallel()
	agent := &fakeAgentFull{written: map[string][]byte{}}
	remote := startRPC(t, agent)

	// A plain verb, both directions of data
	out, err := remote.Ensure("blog")
	require.NoError(t, err)
	assert.Equal(t, "up", out)
	assert.Contains(t, agent.calls, "ensure:blog")

	// Rename crosses the wire with both names and the stable id
	require.NoError(t, remote.Rename("blog", "shop", "id123"))
	assert.Contains(t, agent.calls, "rename:blog->shop:id123")

	// Sentinel errors survive the wire as errors.Is-able values
	err = remote.Down("blog")
	assert.ErrorIs(t, err, appctl.ErrPoweredOff)

	// Structured results
	res, err := remote.Exec("blog", "ls", 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "ran ls", res.Output)
	assert.Equal(t, 3, res.ExitCode)
	assert.True(t, res.TimedOut)

	snap, err := remote.TakeSnapshot("blog", "before", false)
	require.NoError(t, err)
	assert.Equal(t, "snap-1", snap.ID)

	states := remote.States([]string{"blog"})
	assert.True(t, states["blog"].Running)

	hb := remote.Heartbeat()
	require.NotNil(t, hb)
	assert.True(t, hb.BtrfsCapable)

	// Streamed file write + read
	require.NoError(t, remote.WriteFileFrom("blog", "public/index.html", strings.NewReader("<h1>hi</h1>"), 0o644))
	assert.Equal(t, "<h1>hi</h1>", string(agent.written["blog/public/index.html"]))
	b, err := remote.ReadFile("blog", "public/index.html")
	require.NoError(t, err)
	assert.Equal(t, "<h1>hi</h1>", string(b))

	// Logs (text over the wire)
	logs, err := remote.Logs("blog", 50)
	require.NoError(t, err)
	assert.Equal(t, "line1\nline2", logs)
}

// startRPC wires fake agent -> RPC server -> duplex over an in-memory pipe ->
// remote client, the exact shape hostit-node and hostit-control use.
func startRPC(t *testing.T, agent api.NodeAgent) api.NodeAgent {
	t.Helper()
	nodeConn, controlConn := net.Pipe()
	// Node side: serves the agent.
	_, _, err := cluster.Duplex(nodeConn, true, RPCHandler(agent))
	require.NoError(t, err)
	// Control side: a client that implements api.NodeAgent.
	client, _, err := cluster.Duplex(controlConn, false, nil)
	require.NoError(t, err)
	return NewRemoteAgent(client, nil)
}

// startRPCShortTimeout wires the same fake-agent -> RPC -> duplex shape as
// startRPC, but hands the remote a client whose per-RPC Timeout is short and a
// dialer for the streaming path -- so a test can prove a long assistant turn
// outlives that timeout rather than being cut by it.
func startRPCShortTimeout(t *testing.T, agent api.NodeAgent, timeout time.Duration) api.NodeAgent {
	t.Helper()
	nodeConn, controlConn := net.Pipe()
	_, _, err := cluster.Duplex(nodeConn, true, RPCHandler(agent))
	require.NoError(t, err)
	// Control side: reuse the duplex session but wrap it in a short-timeout
	// client, matching cluster.DuplexClient's transport (a stream per request).
	_, sess, err := cluster.Duplex(controlConn, false, nil)
	require.NoError(t, err)
	dial := func() (net.Conn, error) { return sess.OpenStream() }
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:       func(context.Context, string, string) (net.Conn, error) { return dial() },
			DisableKeepAlives: true,
		},
	}
	return NewRemoteAgent(client, dial)
}

// TestRPCAssistantTurnOutlivesTheRPCClientTimeout guards the fix for a turn
// that streamed longer than the per-RPC client Timeout being guillotined
// mid-stream (control cut the request, the node's r.Context() cancelled, and
// the sandbox `claude -p` aborted). The streaming path must be bounded by the
// caller's context, not that short backstop.
func TestRPCAssistantTurnOutlivesTheRPCClientTimeout(t *testing.T) {
	t.Parallel()
	agent := &fakeAgentFull{written: map[string][]byte{}, turnDelay: 400 * time.Millisecond}
	remote := startRPCShortTimeout(t, agent, 100*time.Millisecond)
	var types []string
	err := remote.RunAssistantTurn(context.Background(), &api.AssistantTurnSpec{Name: "blog", Prompt: "hi"}, func(ev *api.AssistantEvent) {
		types = append(types, ev.Type)
	})
	require.NoError(t, err, "a turn slower than the per-RPC timeout must not be cut")
	assert.Contains(t, types, "result", "the terminal result arrives after the client timeout would have fired")
}

// fakeAgentFull adds the file verbs with real signatures (the embedded
// interface trick above cannot express io.Reader cleanly).
type fakeAgentFull struct {
	api.NodeAgent
	calls     []string
	written   map[string][]byte
	readErr   error         // when set, ReadFile fails with this
	turnDelay time.Duration // when set, pause mid-turn to model a long streaming turn
	shotErr   error         // when set, Screenshot fails with this
}

func (f *fakeAgentFull) Screenshot(spec *api.ScreenshotSpec) ([]byte, error) {
	f.calls = append(f.calls, "screenshot:"+spec.Name+":"+spec.URL)
	if f.shotErr != nil {
		return nil, f.shotErr
	}
	return []byte("\x89PNG\r\n\x1a\nfake"), nil
}

func (f *fakeAgentFull) RunAssistantTurn(ctx context.Context, spec *api.AssistantTurnSpec, onEvent func(*api.AssistantEvent)) error {
	f.calls = append(f.calls, "assistant-turn:"+spec.Name)
	// A turn's shape: some text, a tool call and its result, then a terminal
	// result carrying usage -- exactly the sequence control maps to SSE events.
	onEvent(&api.AssistantEvent{Type: "text", Text: "hello " + spec.Prompt})
	// A real turn can run for many minutes between events; turnDelay models that
	// gap so a test can prove the RPC client does not cut a slow stream short.
	if f.turnDelay > 0 {
		select {
		case <-time.After(f.turnDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	onEvent(&api.AssistantEvent{Type: "tool_use", Tool: "write_file", Input: `{"path":"x"}`})
	onEvent(&api.AssistantEvent{Type: "tool_result", Output: "ok"})
	onEvent(&api.AssistantEvent{Type: "result", Result: "done", Usage: &api.AssistantUsage{InputTokens: 10, OutputTokens: 5}})
	return nil
}

func (f *fakeAgentFull) AnswerAssistant(_ context.Context, spec *api.AssistantAnswerSpec) (string, *api.AssistantUsage, error) {
	f.calls = append(f.calls, "assistant-answer:"+spec.Name)
	return "the answer to " + spec.Prompt, &api.AssistantUsage{InputTokens: 3, OutputTokens: 7}, nil
}

func (f *fakeAgentFull) Rename(oldName, newName, id string) error {
	f.calls = append(f.calls, "rename:"+oldName+"->"+newName+":"+id)
	return nil
}

func (f *fakeAgentFull) SetCPULimit(name string, cpuMilli int) {
	f.calls = append(f.calls, fmt.Sprintf("setcpulimit:%s:%d", name, cpuMilli))
}

func (f *fakeAgentFull) Ensure(name string) (string, error) {
	f.calls = append(f.calls, "ensure:"+name)
	return "up", nil
}
func (f *fakeAgentFull) Down(name string) error { return appctl.ErrPoweredOff }
func (f *fakeAgentFull) Logs(name string, lines int) (string, error) {
	return "line1\nline2", nil
}
func (f *fakeAgentFull) Exec(name, command string, timeout time.Duration) (*api.ExecResult, error) {
	return &api.ExecResult{Output: "ran " + command, ExitCode: 3, TimedOut: true}, nil
}
func (f *fakeAgentFull) TakeSnapshot(name, label string, auto bool) (*store.Snapshot, error) {
	return &store.Snapshot{ID: "snap-1", AppName: name, Label: label, Auto: auto}, nil
}
func (f *fakeAgentFull) States(names []string) map[string]api.State {
	return map[string]api.State{names[0]: {Running: true, AppState: "running"}}
}
func (f *fakeAgentFull) Heartbeat() *api.Heartbeat {
	return &api.Heartbeat{Version: "test", BtrfsCapable: true}
}
func (f *fakeAgentFull) WriteFileFrom(name, relPath string, r io.Reader, mode os.FileMode) error {
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		return err
	}
	f.written[name+"/"+relPath] = buf.Bytes()
	return nil
}
func (f *fakeAgentFull) ReadFile(name, relPath string) ([]byte, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	b, ok := f.written[name+"/"+relPath]
	if !ok {
		return nil, errors.New("no such file")
	}
	return b, nil
}

func TestRPCProvisionRoundTrips(t *testing.T) {
	t.Parallel()
	agent := &fakeAgentFull{written: map[string][]byte{}}
	remote := startRPC(t, agent)
	require.NoError(t, remote.Provision(&api.ProvisionSpec{ID: "aaa", Name: "blog", Port: 10000}))
	assert.Contains(t, agent.calls, "provision:blog:10000")
	remote.Deprovision(&api.DeprovisionSpec{Name: "blog", ID: "aaa"})
	assert.Contains(t, agent.calls, "deprovision:blog")
}

func TestRPCScreenshotRoundTripsBytes(t *testing.T) {
	t.Parallel()
	agent := &fakeAgentFull{written: map[string][]byte{}}
	remote := startRPC(t, agent)
	png, err := remote.Screenshot(&api.ScreenshotSpec{Name: "blog", URL: "https://blog.example.com", Isolate: true})
	require.NoError(t, err)
	assert.Equal(t, []byte("\x89PNG\r\n\x1a\nfake"), png, "the PNG bytes survive the wire intact")
	assert.Contains(t, agent.calls, "screenshot:blog:https://blog.example.com", "the spec reaches the node")
}

func TestRPCAssistantTurnStreamsEventsInOrder(t *testing.T) {
	t.Parallel()
	agent := &fakeAgentFull{written: map[string][]byte{}}
	remote := startRPC(t, agent)
	var types []string
	var lastUsage *api.AssistantUsage
	var firstText string
	err := remote.RunAssistantTurn(context.Background(), &api.AssistantTurnSpec{Name: "blog", Prompt: "hi"}, func(ev *api.AssistantEvent) {
		types = append(types, ev.Type)
		if ev.Type == "text" && firstText == "" {
			firstText = ev.Text
		}
		if ev.Usage != nil {
			lastUsage = ev.Usage
		}
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"text", "tool_use", "tool_result", "result"}, types, "every event crosses the wire, in order")
	assert.Equal(t, "hello hi", firstText, "event fields survive the wire")
	require.NotNil(t, lastUsage, "the terminal result carries usage back")
	assert.Equal(t, int64(10), lastUsage.InputTokens)
	assert.Contains(t, agent.calls, "assistant-turn:blog", "the spec reaches the node")
}

func TestRPCAssistantAnswerRoundTrips(t *testing.T) {
	t.Parallel()
	agent := &fakeAgentFull{written: map[string][]byte{}}
	remote := startRPC(t, agent)
	text, usage, err := remote.AnswerAssistant(context.Background(), &api.AssistantAnswerSpec{Name: "blog", Prompt: "q"})
	require.NoError(t, err)
	assert.Equal(t, "the answer to q", text)
	require.NotNil(t, usage)
	assert.Equal(t, int64(3), usage.InputTokens)
	assert.Equal(t, int64(7), usage.OutputTokens)
}

func TestRPCScreenshotPropagatesFailure(t *testing.T) {
	t.Parallel()
	agent := &fakeAgentFull{written: map[string][]byte{}, shotErr: errors.New("chrome exploded")}
	remote := startRPC(t, agent)
	_, err := remote.Screenshot(&api.ScreenshotSpec{Name: "blog", URL: "https://blog.example.com"})
	require.Error(t, err, "a node-side shot failure reaches control")
	assert.Contains(t, err.Error(), "chrome exploded")
}

func (f *fakeAgentFull) Provision(spec *api.ProvisionSpec) error {
	f.calls = append(f.calls, fmt.Sprintf("provision:%s:%d", spec.Name, spec.Port))
	return nil
}

func (f *fakeAgentFull) Deprovision(spec *api.DeprovisionSpec) {
	f.calls = append(f.calls, "deprovision:"+spec.Name)
}

// TestDialInRegistersARemoteAgent is the control-side accept path: a node
// dials, the connection upgrades to the duplex, and control gets a working
// NodeAgent keyed by the node's identity.
func TestDialInRegistersARemoteAgent(t *testing.T) {
	t.Parallel()
	agent := &fakeAgentFull{written: map[string][]byte{}}
	registered := make(chan string, 1)
	var got api.NodeAgent
	sockPath := filepath.Join(t.TempDir(), "cluster.sock")
	ln, err := cluster.ListenSocket(sockPath, 0o600)
	require.NoError(t, err)
	defer ln.Close()
	srv := cluster.SocketServer(cluster.ConnectHandler(map[string]*cluster.Role{
		cluster.RoleNode: Role(func(string) bool { return true }, nil, func(nodeID string, remote api.NodeAgent) {
			got = remote
			registered <- nodeID
		}, nil),
	}))
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	// The node side: plain TCP here (the mTLS identity is the transport's
	// concern, tested in transport_test.go); DialControl upgrades and serves.
	conn, err := cluster.DialSocket(sockPath)
	require.NoError(t, err)
	go func() {
		require.NoError(t, ServeAgent(conn, "node-b", agent, nil))
	}()

	select {
	case id := <-registered:
		assert.Equal(t, "node-b", id)
	case <-time.After(3 * time.Second):
		t.Fatal("control never registered the dialed node")
	}
	out, err := got.Ensure("blog")
	require.NoError(t, err)
	assert.Equal(t, "up", out)
}

// TestReadFileNotExistSurvivesTheWire: Readme() treats a missing README as
// empty via errors.Is(err, fs.ErrNotExist); the sentinel must survive RPC.
func TestReadFileNotExistSurvivesTheWire(t *testing.T) {
	t.Parallel()
	agent := &fakeAgentFull{written: map[string][]byte{}, readErr: fs.ErrNotExist}
	remote := startRPC(t, agent)
	_, err := remote.ReadFile("blog", "README.md")
	require.ErrorIs(t, err, fs.ErrNotExist)
}

// The CPU verb crosses the wire like the other limits: recorded on the
// machine side, applied at the next container recreation.
func TestSetCPULimitRoundTrips(t *testing.T) {
	t.Parallel()
	agent := &fakeAgentFull{written: map[string][]byte{}}
	remote := startRPC(t, agent)
	remote.SetCPULimit("blog", 1500)
	assert.Contains(t, agent.calls, "setcpulimit:blog:1500")
}
