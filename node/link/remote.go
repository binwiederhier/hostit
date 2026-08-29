package link

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"heckel.io/hostit/archive"
	"heckel.io/hostit/cluster"
	"heckel.io/hostit/node/api"
	"heckel.io/hostit/store"
)

// remoteAgent implements api.NodeAgent over the duplex client: what
// hostit-control holds for every remote node. The URL host is cosmetic --
// routing is the underlying session.
type remoteAgent struct {
	c *http.Client
	// dial opens a raw stream on the same session; nil in tests that only
	// exercise the JSON verbs. The terminal rides these: a pty is a byte
	// stream, and forcing it through request/response would mean polling.
	dial func() (net.Conn, error)
}

var _ api.NodeAgent = (*remoteAgent)(nil)

// NewRemoteAgent wraps a duplex client into a NodeAgent. dial opens raw
// streams on the same connection, for the terminal.
func NewRemoteAgent(c *http.Client, dial func() (net.Conn, error)) api.NodeAgent {
	return &remoteAgent{c: c, dial: dial}
}

// call posts one JSON verb and decodes the envelope (including sentinel errors).
func (a *remoteAgent) call(verb string, req *rpcReq) (*rpcResp, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpResp, err := a.c.Post("http://node/v1/"+verb, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("node rpc %s: %w", verb, err)
	}
	defer httpResp.Body.Close()
	var resp rpcResp
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("node rpc %s: %w", verb, err)
	}
	if err := decodeErr(&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (a *remoteAgent) out(verb, name string) (string, error) {
	resp, err := a.call(verb, &rpcReq{Name: name})
	if err != nil {
		return "", err
	}
	return resp.Output, nil
}

func (a *remoteAgent) do(verb string, req *rpcReq) error {
	_, err := a.call(verb, req)
	return err
}

func (a *remoteAgent) Ensure(name string) (string, error)  { return a.out("ensure", name) }
func (a *remoteAgent) Up(name string) (string, error)      { return a.out("up", name) }
func (a *remoteAgent) PowerOn(name string) (string, error) { return a.out("poweron", name) }
func (a *remoteAgent) Status(name string) (string, error)  { return a.out("status", name) }
func (a *remoteAgent) Down(name string) error              { return a.do("down", &rpcReq{Name: name}) }
func (a *remoteAgent) Restart(name string) error           { return a.do("restart", &rpcReq{Name: name}) }
func (a *remoteAgent) StartApp(name string) error          { return a.do("startapp", &rpcReq{Name: name}) }
func (a *remoteAgent) StopApp(name string) error           { return a.do("stopapp", &rpcReq{Name: name}) }
func (a *remoteAgent) RestartApp(name string) error        { return a.do("restartapp", &rpcReq{Name: name}) }

func (a *remoteAgent) Logs(name string, lines int) (string, error) {
	resp, err := a.call("logs", &rpcReq{Name: name, Lines: lines})
	if err != nil {
		return "", err
	}
	return resp.Output, nil
}

// SystemLogs fetches the remote node's own journal (not an app's log).
func (a *remoteAgent) SystemLogs(lines int) (string, error) {
	resp, err := a.call("system-logs", &rpcReq{Lines: lines})
	if err != nil {
		return "", err
	}
	return resp.Output, nil
}

func (a *remoteAgent) Exec(name, command string, timeout time.Duration) (*api.ExecResult, error) {
	resp, err := a.call("exec", &rpcReq{Name: name, Command: command, TimeoutSec: int(timeout / time.Second)})
	if err != nil {
		return nil, err
	}
	return resp.Exec, nil
}

func (a *remoteAgent) Terminal(name string) (api.TerminalSession, error) {
	if a.dial == nil {
		return nil, errors.New("terminal: no stream dialer on this connection")
	}
	stream, err := a.dial()
	if err != nil {
		return nil, err
	}
	// A hand-rolled upgrade on a fresh stream: the node's RPC handler hijacks
	// it and bridges the pty. http.Client cannot do this -- it never hands the
	// underlying connection back -- and both ends of this wire are ours.
	if _, err := fmt.Fprintf(stream, "GET /v1/terminal/%s HTTP/1.1\r\nHost: %s\r\nUpgrade: hostit-terminal\r\nConnection: Upgrade\r\n\r\n", url.PathEscape(name), cluster.ControlID); err != nil {
		_ = stream.Close()
		return nil, err
	}
	br := bufio.NewReader(stream)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = stream.Close()
		return nil, fmt.Errorf("terminal refused: %s", strings.TrimSpace(string(msg)))
	}
	return &remoteTerminal{stream: stream, r: br}, nil
}

// remoteTerminal frames keystrokes and resizes toward the node and reads the
// pty's output raw. The framing exists for exactly one reason: resize has to
// travel in-band on the same stream, and a pty's input bytes can be anything,
// so the two need an envelope to stay apart.
type remoteTerminal struct {
	stream net.Conn
	r      *bufio.Reader // may hold pty bytes that arrived with the 101
	mu     sync.Mutex    // Protects writes: frames must not interleave
}

func (t *remoteTerminal) Read(p []byte) (int, error) { return t.r.Read(p) }

func (t *remoteTerminal) Write(p []byte) (int, error) {
	if err := t.writeFrame(terminalFrameData, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (t *remoteTerminal) Resize(cols, rows uint16) error {
	payload := []byte{byte(cols >> 8), byte(cols), byte(rows >> 8), byte(rows)}
	return t.writeFrame(terminalFrameResize, payload)
}

func (t *remoteTerminal) Close() error { return t.stream.Close() }

func (t *remoteTerminal) writeFrame(kind byte, payload []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	header := []byte{kind, byte(len(payload) >> 8), byte(len(payload))}
	if _, err := t.stream.Write(header); err != nil {
		return err
	}
	_, err := t.stream.Write(payload)
	return err
}

func (a *remoteAgent) ListFiles(name, dir string) (*api.Listing, error) {
	resp, err := a.call("listfiles", &rpcReq{Name: name, Path: dir})
	if err != nil {
		return nil, err
	}
	return resp.Listing, nil
}

func (a *remoteAgent) DeleteFile(name, relPath string) error {
	return a.do("deletefile", &rpcReq{Name: name, Path: relPath})
}

func (a *remoteAgent) MoveFile(name, fromRel, toRel string) error {
	return a.do("movefile", &rpcReq{Name: name, Path: fromRel, To: toRel})
}

func (a *remoteAgent) MakeDir(name, relPath string) error {
	return a.do("makedir", &rpcReq{Name: name, Path: relPath})
}

func (a *remoteAgent) StatFile(name, relPath string) (*api.FileInfo, error) {
	resp, err := a.call("statfile", &rpcReq{Name: name, Path: relPath})
	if err != nil {
		return nil, err
	}
	return resp.Stat, nil
}

func (a *remoteAgent) FileExists(name, relPath string) bool {
	resp, err := a.call("fileexists", &rpcReq{Name: name, Path: relPath})
	return err == nil && resp.OK
}

func (a *remoteAgent) WriteFile(name, relPath string, content []byte, mode os.FileMode) error {
	return a.WriteFileFrom(name, relPath, bytes.NewReader(content), mode)
}

func (a *remoteAgent) WriteFileFrom(name, relPath string, r io.Reader, mode os.FileMode) error {
	u := "http://node/v1/file?" + url.Values{
		"name": {name}, "path": {relPath}, "mode": {fmt.Sprintf("%o", uint32(mode))},
	}.Encode()
	req, err := http.NewRequest("PUT", u, r)
	if err != nil {
		return err
	}
	httpResp, err := a.c.Do(req)
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()
	var resp rpcResp
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return err
	}
	return decodeErr(&resp)
}

func (a *remoteAgent) readFile(name, relPath string, max int64) ([]byte, error) {
	u := "http://node/v1/file?" + url.Values{
		"name": {name}, "path": {relPath}, "max": {fmt.Sprint(max)},
	}.Encode()
	httpResp, err := a.c.Get(u)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		if sentinel, ok := wireErrs[httpResp.Header.Get(errCodeHeader)]; ok {
			return nil, sentinel
		}
		return nil, errors.New(httpResp.Header.Get(errHeader))
	}
	return io.ReadAll(httpResp.Body)
}

func (a *remoteAgent) ReadFile(name, relPath string) ([]byte, error) {
	return a.readFile(name, relPath, 0)
}

func (a *remoteAgent) ReadFileMax(name, relPath string, max int64) ([]byte, error) {
	return a.readFile(name, relPath, max)
}

func (a *remoteAgent) ExtractTar(name string, r io.Reader) ([]string, error) {
	httpResp, err := a.c.Post("http://node/v1/tar?"+url.Values{"name": {name}}.Encode(), "application/x-tar", r)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()
	var resp rpcResp
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, err
	}
	if err := decodeErr(&resp); err != nil {
		return nil, err
	}
	return resp.Paths, nil
}

func (a *remoteAgent) SetKeys(name string, appKeys, profileKeys []string) error {
	return a.do("setkeys", &rpcReq{Name: name, AppKeys: appKeys, ProfileKeys: profileKeys})
}

func (a *remoteAgent) Rename(oldName, newName, id string) error {
	return a.do("rename", &rpcReq{Name: oldName, To: newName, ID: id})
}

func (a *remoteAgent) SetMemoryLimit(name string, memoryMB int) {
	_ = a.do("setmemorylimit", &rpcReq{Name: name, MemoryMB: memoryMB})
}

func (a *remoteAgent) SetCPULimit(name string, cpuMilli int) {
	_ = a.do("setcpulimit", &rpcReq{Name: name, CPUMilli: cpuMilli})
}

func (a *remoteAgent) SetDiskLimit(name string, diskMB int) {
	_ = a.do("setdisklimit", &rpcReq{Name: name, DiskMB: diskMB})
}

func (a *remoteAgent) Snapshots() ([]*store.Snapshot, error) {
	resp, err := a.call("snapshots", &rpcReq{})
	if err != nil {
		return nil, err
	}
	return resp.Snapshots, nil
}

func (a *remoteAgent) TakeSnapshot(name, label string, auto bool) (*store.Snapshot, error) {
	resp, err := a.call("snapshot", &rpcReq{Name: name, Label: label, Auto: auto})
	if err != nil {
		return nil, err
	}
	return resp.Snapshot, nil
}

func (a *remoteAgent) DeleteSnapshot(name, id string) error {
	return a.do("deletesnapshot", &rpcReq{Name: name, ID: id})
}

func (a *remoteAgent) Rollback(name, id string) error {
	return a.do("rollback", &rpcReq{Name: name, ID: id})
}

func (a *remoteAgent) States(names []string) map[string]api.State {
	resp, err := a.call("states", &rpcReq{Names: names})
	if err != nil {
		return nil // nil = "could not measure"; callers must not ingest it
	}
	return resp.States
}

func (a *remoteAgent) Heartbeat() *api.Heartbeat {
	resp, err := a.call("heartbeat", &rpcReq{})
	if err != nil {
		return nil
	}
	return resp.Heartbeat
}

// Provision/Deprovision cross the wire as their full spec envelopes.
func (a *remoteAgent) Provision(spec *api.ProvisionSpec) error {
	return a.postJSON("provision", spec)
}

func (a *remoteAgent) Deprovision(spec *api.DeprovisionSpec) {
	_ = a.postJSON("deprovision", spec)
}

// Sync pushes the registry mirror; a plain JSON verb.
func (a *remoteAgent) Sync(state *api.SyncState) error {
	return a.postJSON("sync", state)
}

// Reconcile hands the node its desired state; the removed ids are not
// needed by the caller (rejoin and the sweep loop), so they are dropped.
func (a *remoteAgent) Reconcile(desired *api.DesiredState) []string {
	if desired == nil {
		desired = &api.DesiredState{}
	}
	_ = a.postJSON("reconcile", desired)
	return nil
}

func (a *remoteAgent) postJSON(verb string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	httpResp, err := a.c.Post("http://node/v1/"+verb, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("node rpc %s: %w", verb, err)
	}
	defer httpResp.Body.Close()
	var resp rpcResp
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return fmt.Errorf("node rpc %s: %w", verb, err)
	}
	return decodeErr(&resp)
}

// ArchiveWorkspace streams the app's workspace archive off the node: the node
// snapshots and archives on its side, and the archive rides back as the response
// body, which the caller reads and closes.
func (a *remoteAgent) ArchiveWorkspace(name string, format archive.Format) (io.ReadCloser, error) {
	u := "http://node/v1/export?" + url.Values{"name": {name}, "format": {string(format)}}.Encode()
	httpResp, err := a.c.Get(u)
	if err != nil {
		return nil, err
	}
	if httpResp.StatusCode != http.StatusOK {
		defer httpResp.Body.Close()
		return nil, streamError(httpResp)
	}
	return httpResp.Body, nil
}

// ArchiveSnapshot streams an existing snapshot's archive off the node, the same
// way ArchiveWorkspace does but naming the snapshot instead of the live workspace.
func (a *remoteAgent) ArchiveSnapshot(name, snapshotID string, format archive.Format) (io.ReadCloser, error) {
	u := "http://node/v1/snapshot-export?" + url.Values{"name": {name}, "id": {snapshotID}, "format": {string(format)}}.Encode()
	httpResp, err := a.c.Get(u)
	if err != nil {
		return nil, err
	}
	if httpResp.StatusCode != http.StatusOK {
		defer httpResp.Body.Close()
		return nil, streamError(httpResp)
	}
	return httpResp.Body, nil
}

// streamError maps a non-200 raw-stream response to an error: a mapped sentinel
// when the node named one, its message when it set one, else the HTTP status --
// so a stream failure is never a blank, sentinel-less error.
func streamError(resp *http.Response) error {
	if sentinel, ok := wireErrs[resp.Header.Get(errCodeHeader)]; ok {
		return sentinel
	}
	if msg := resp.Header.Get(errHeader); msg != "" {
		return errors.New(msg)
	}
	return fmt.Errorf("node request failed: %s", resp.Status)
}
