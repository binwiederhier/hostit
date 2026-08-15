package node

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"heckel.io/hostit/app"
	"heckel.io/hostit/store"
)

// remoteAgent implements app.NodeAgent over the duplex client: what
// hostit-control holds for every remote node. The URL host is cosmetic --
// routing is the underlying session.
type remoteAgent struct {
	c *http.Client
}

var _ app.NodeAgent = (*remoteAgent)(nil)

// NewRemoteAgent wraps a duplex client into a NodeAgent.
func NewRemoteAgent(c *http.Client) app.NodeAgent {
	return &remoteAgent{c: c}
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

func (a *remoteAgent) Exec(name, command string, timeout time.Duration) (*app.ExecResult, error) {
	resp, err := a.call("exec", &rpcReq{Name: name, Command: command, TimeoutSec: int(timeout / time.Second)})
	if err != nil {
		return nil, err
	}
	return resp.Exec, nil
}

func (a *remoteAgent) TerminalCommand(name string) (string, []string, error) {
	resp, err := a.call("terminal", &rpcReq{Name: name})
	if err != nil {
		return "", nil, err
	}
	return resp.Cmd, resp.Args, nil
}

func (a *remoteAgent) ListFiles(name, dir string) (*app.Listing, error) {
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

func (a *remoteAgent) StatFile(name, relPath string) (*app.FileInfo, error) {
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
		if sentinel, ok := wireErrs[httpResp.Header.Get("X-Hostit-Err-Code")]; ok {
			return nil, sentinel
		}
		return nil, errors.New(httpResp.Header.Get("X-Hostit-Err"))
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

func (a *remoteAgent) SyncKeys(name string, profileKeys []string) error {
	return a.do("synckeys", &rpcReq{Name: name, ProfileKeys: profileKeys})
}

func (a *remoteAgent) SetMemoryLimit(name string, memoryMB int) {
	_ = a.do("setmemorylimit", &rpcReq{Name: name, MemoryMB: memoryMB})
}

func (a *remoteAgent) SetDiskLimit(name string, diskMB int) {
	_ = a.do("setdisklimit", &rpcReq{Name: name, DiskMB: diskMB})
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

func (a *remoteAgent) States(names []string) map[string]app.State {
	resp, err := a.call("states", &rpcReq{Names: names})
	if err != nil {
		return map[string]app.State{}
	}
	return resp.States
}

func (a *remoteAgent) Heartbeat() *app.Heartbeat {
	resp, err := a.call("heartbeat", &rpcReq{})
	if err != nil {
		return nil
	}
	return resp.Heartbeat
}

// Provision/Deprovision cross the wire as their full spec envelopes.
func (a *remoteAgent) Provision(spec *app.ProvisionSpec) error {
	return a.postJSON("provision", spec)
}

func (a *remoteAgent) Deprovision(spec *app.DeprovisionSpec) {
	_ = a.postJSON("deprovision", spec)
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
