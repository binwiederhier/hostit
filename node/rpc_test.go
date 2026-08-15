package node

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/app"
	"heckel.io/hostit/appctl"
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
func startRPC(t *testing.T, agent app.NodeAgent) app.NodeAgent {
	t.Helper()
	nodeConn, controlConn := net.Pipe()
	// Node side: serves the agent.
	_, err := Duplex(nodeConn, true, RPCHandler(agent))
	require.NoError(t, err)
	// Control side: a client that implements app.NodeAgent.
	client, err := Duplex(controlConn, false, nil)
	require.NoError(t, err)
	return NewRemoteAgent(client)
}

// fakeAgentFull adds the file verbs with real signatures (the embedded
// interface trick above cannot express io.Reader cleanly).
type fakeAgentFull struct {
	app.NodeAgent
	calls   []string
	written map[string][]byte
}

func (f *fakeAgentFull) Ensure(name string) (string, error) {
	f.calls = append(f.calls, "ensure:"+name)
	return "up", nil
}
func (f *fakeAgentFull) Down(name string) error { return appctl.ErrPoweredOff }
func (f *fakeAgentFull) Logs(name string, lines int) (string, error) {
	return "line1\nline2", nil
}
func (f *fakeAgentFull) Exec(name, command string, timeout time.Duration) (*app.ExecResult, error) {
	return &app.ExecResult{Output: "ran " + command, ExitCode: 3, TimedOut: true}, nil
}
func (f *fakeAgentFull) TakeSnapshot(name, label string, auto bool) (*store.Snapshot, error) {
	return &store.Snapshot{ID: "snap-1", AppName: name, Label: label, Auto: auto}, nil
}
func (f *fakeAgentFull) States(names []string) map[string]app.State {
	return map[string]app.State{names[0]: {Running: true, AppState: "running"}}
}
func (f *fakeAgentFull) Heartbeat() *app.Heartbeat {
	return &app.Heartbeat{Version: "test", BtrfsCapable: true}
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
	b, ok := f.written[name+"/"+relPath]
	if !ok {
		return nil, errors.New("no such file")
	}
	return b, nil
}
