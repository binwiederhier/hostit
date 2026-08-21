package nodelink

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strconv"
	"time"

	"heckel.io/hostit/appctl"
	"heckel.io/hostit/nodeapi"
	"heckel.io/hostit/store"
)

// The NodeAgent RPC: hostit-node serves its in-process NodeAgent (the Manager)
// behind RPCHandler; hostit-control talks to it through NewRemoteAgent, which
// implements nodeapi.NodeAgent over the duplex client. Small verbs are one JSON
// envelope per call; file bodies, tars and reads stream raw.

const (
	// errHeader/errCodeHeader carry a verb's failure across the raw file-stream
	// responses (the JSON verbs use the rpcResp envelope instead).
	errHeader     = "X-Hostit-Err"
	errCodeHeader = "X-Hostit-Err-Code"
)

// rpcReq is the argument envelope for the JSON verbs. One struct for all of
// them keeps the wire boring; every verb reads only its own fields.
type rpcReq struct {
	Name        string   `json:"name,omitempty"`
	Path        string   `json:"path,omitempty"`
	To          string   `json:"to,omitempty"`
	Command     string   `json:"command,omitempty"`
	Label       string   `json:"label,omitempty"`
	ID          string   `json:"id,omitempty"`
	Lines       int      `json:"lines,omitempty"`
	Max         int64    `json:"max,omitempty"`
	TimeoutSec  int      `json:"timeout_sec,omitempty"`
	MemoryMB    int      `json:"memory_mb,omitempty"`
	CPUMilli    int      `json:"cpu_milli,omitempty"`
	DiskMB      int      `json:"disk_mb,omitempty"`
	Auto        bool     `json:"auto,omitempty"`
	AppKeys     []string `json:"app_keys,omitempty"`
	ProfileKeys []string `json:"profile_keys,omitempty"`
	Names       []string `json:"names,omitempty"`
}

// rpcResp is the result envelope; Err/ErrCode carry failures (ErrCode maps
// back to the sentinel errors the control plane matches with errors.Is).
type rpcResp struct {
	Output    string                   `json:"output,omitempty"`
	OK        bool                     `json:"ok,omitempty"`
	Cmd       string                   `json:"cmd,omitempty"`
	Args      []string                 `json:"args,omitempty"`
	Paths     []string                 `json:"paths,omitempty"`
	Exec      *nodeapi.ExecResult      `json:"exec,omitempty"`
	Snapshot  *store.Snapshot          `json:"snapshot,omitempty"`
	Snapshots []*store.Snapshot        `json:"snapshots,omitempty"`
	States    map[string]nodeapi.State `json:"states,omitempty"`
	Heartbeat *nodeapi.Heartbeat       `json:"heartbeat,omitempty"`
	Listing   *nodeapi.Listing         `json:"listing,omitempty"`
	Stat      *nodeapi.FileInfo        `json:"stat,omitempty"`
	Err       string                   `json:"err,omitempty"`
	ErrCode   string                   `json:"err_code,omitempty"`
}

// The sentinels that must survive the wire: the control plane's error-to-HTTP
// mapping (writeAppError) matches these with errors.Is.
var (
	wireErrs = map[string]error{
		"powered_off":   appctl.ErrPoweredOff,
		"app_notfound":  store.ErrAppNotFound,
		"snap_notfound": store.ErrSnapshotNotFound,
		"app_exists":    nodeapi.ErrAppExists,
		"file_notfound": fs.ErrNotExist,
		"invalid":       nodeapi.ErrInvalid,
		"limit":         nodeapi.ErrLimitReached,
	}
)

func errCode(err error) string {
	for code, sentinel := range wireErrs {
		if errors.Is(err, sentinel) {
			return code
		}
	}
	return ""
}

func decodeErr(resp *rpcResp) error {
	if resp.Err == "" && resp.ErrCode == "" {
		return nil
	}
	if sentinel, ok := wireErrs[resp.ErrCode]; ok {
		if resp.Err != "" && resp.Err != sentinel.Error() {
			return fmt.Errorf("%w: %s", sentinel, resp.Err)
		}
		return sentinel
	}
	return errors.New(resp.Err)
}

// RPCHandler serves a NodeAgent over HTTP: JSON verbs at /v1/{verb}, raw
// streams for file content. This is what hostit-node mounts on its dialed
// connection; the transport (mTLS identity) is the caller's concern.
// The terminal's in-band framing, control -> node: a pty's input can be any
// byte, so keystrokes and resizes need an envelope to stay apart. Output flows
// back raw -- it is all pty bytes, nothing to distinguish.
const (
	terminalFrameData   = 'd'
	terminalFrameResize = 'r'
)

func RPCHandler(agent nodeapi.NodeAgent) http.Handler {
	mux := http.NewServeMux()
	verb := func(name string, fn func(*rpcReq) *rpcResp) {
		mux.HandleFunc("POST /v1/"+name, func(w http.ResponseWriter, r *http.Request) {
			var req rpcReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeRPC(w, &rpcResp{Err: "bad request: " + err.Error()})
				return
			}
			writeRPC(w, fn(&req))
		})
	}
	fail := func(err error) *rpcResp { return &rpcResp{Err: errString(err), ErrCode: errCode(err)} }
	okOut := func(out string, err error) *rpcResp {
		if err != nil {
			return fail(err)
		}
		return &rpcResp{Output: out}
	}
	okErr := func(err error) *rpcResp {
		if err != nil {
			return fail(err)
		}
		return &rpcResp{OK: true}
	}

	mux.HandleFunc("POST /v1/sync", func(w http.ResponseWriter, r *http.Request) {
		var state nodeapi.SyncState
		if err := json.NewDecoder(r.Body).Decode(&state); err != nil {
			writeRPC(w, &rpcResp{Err: "bad request: " + err.Error()})
			return
		}
		writeRPC(w, okErr(agent.Sync(&state)))
	})
	mux.HandleFunc("POST /v1/reconcile", func(w http.ResponseWriter, r *http.Request) {
		// The desired document rides the body; an empty body means "converge
		// against your mirror" (an older control).
		var desired nodeapi.DesiredState
		if err := json.NewDecoder(r.Body).Decode(&desired); err != nil {
			agent.Reconcile(nil)
			writeRPC(w, &rpcResp{OK: true})
			return
		}
		agent.Reconcile(&desired)
		writeRPC(w, &rpcResp{OK: true})
	})
	mux.HandleFunc("POST /v1/provision", func(w http.ResponseWriter, r *http.Request) {
		var spec nodeapi.ProvisionSpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			writeRPC(w, &rpcResp{Err: "bad request: " + err.Error()})
			return
		}
		err := agent.Provision(&spec)
		writeRPC(w, &rpcResp{OK: err == nil, Err: errString(err), ErrCode: errCode(err)})
	})
	mux.HandleFunc("POST /v1/deprovision", func(w http.ResponseWriter, r *http.Request) {
		var spec nodeapi.DeprovisionSpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			writeRPC(w, &rpcResp{Err: "bad request: " + err.Error()})
			return
		}
		agent.Deprovision(&spec)
		writeRPC(w, &rpcResp{OK: true})
	})
	verb("ensure", func(q *rpcReq) *rpcResp { return okOut(agent.Ensure(q.Name)) })
	verb("up", func(q *rpcReq) *rpcResp { return okOut(agent.Up(q.Name)) })
	verb("poweron", func(q *rpcReq) *rpcResp { return okOut(agent.PowerOn(q.Name)) })
	verb("status", func(q *rpcReq) *rpcResp { return okOut(agent.Status(q.Name)) })
	verb("down", func(q *rpcReq) *rpcResp { return okErr(agent.Down(q.Name)) })
	verb("restart", func(q *rpcReq) *rpcResp { return okErr(agent.Restart(q.Name)) })
	verb("startapp", func(q *rpcReq) *rpcResp { return okErr(agent.StartApp(q.Name)) })
	verb("stopapp", func(q *rpcReq) *rpcResp { return okErr(agent.StopApp(q.Name)) })
	verb("restartapp", func(q *rpcReq) *rpcResp { return okErr(agent.RestartApp(q.Name)) })
	verb("logs", func(q *rpcReq) *rpcResp { return okOut(agent.Logs(q.Name, q.Lines)) })
	verb("exec", func(q *rpcReq) *rpcResp {
		res, err := agent.Exec(q.Name, q.Command, time.Duration(q.TimeoutSec)*time.Second)
		if err != nil {
			return fail(err)
		}
		return &rpcResp{Exec: res}
	})
	// The terminal is not a verb: it hijacks its stream and bridges the pty,
	// because a shell is a byte stream with a caller waiting at both ends.
	mux.HandleFunc("GET /v1/terminal/{name}", func(w http.ResponseWriter, r *http.Request) {
		serveTerminal(w, r, agent)
	})
	verb("listfiles", func(q *rpcReq) *rpcResp {
		listing, err := agent.ListFiles(q.Name, q.Path)
		if err != nil {
			return fail(err)
		}
		return &rpcResp{Listing: listing}
	})
	verb("deletefile", func(q *rpcReq) *rpcResp { return okErr(agent.DeleteFile(q.Name, q.Path)) })
	verb("movefile", func(q *rpcReq) *rpcResp { return okErr(agent.MoveFile(q.Name, q.Path, q.To)) })
	verb("makedir", func(q *rpcReq) *rpcResp { return okErr(agent.MakeDir(q.Name, q.Path)) })
	verb("statfile", func(q *rpcReq) *rpcResp {
		stat, err := agent.StatFile(q.Name, q.Path)
		if err != nil {
			return fail(err)
		}
		return &rpcResp{Stat: stat}
	})
	verb("fileexists", func(q *rpcReq) *rpcResp { return &rpcResp{OK: agent.FileExists(q.Name, q.Path)} })
	verb("setkeys", func(q *rpcReq) *rpcResp { return okErr(agent.SetKeys(q.Name, q.AppKeys, q.ProfileKeys)) })
	// Rename rides Name (old), To (new) and ID; To is the same field movefile uses.
	verb("rename", func(q *rpcReq) *rpcResp { return okErr(agent.Rename(q.Name, q.To, q.ID)) })
	verb("setmemorylimit", func(q *rpcReq) *rpcResp { agent.SetMemoryLimit(q.Name, q.MemoryMB); return &rpcResp{OK: true} })
	verb("setdisklimit", func(q *rpcReq) *rpcResp { agent.SetDiskLimit(q.Name, q.DiskMB); return &rpcResp{OK: true} })
	verb("setcpulimit", func(q *rpcReq) *rpcResp { agent.SetCPULimit(q.Name, q.CPUMilli); return &rpcResp{OK: true} })
	verb("snapshot", func(q *rpcReq) *rpcResp {
		snap, err := agent.TakeSnapshot(q.Name, q.Label, q.Auto)
		if err != nil {
			return fail(err)
		}
		return &rpcResp{Snapshot: snap}
	})
	verb("snapshots", func(*rpcReq) *rpcResp {
		snaps, err := agent.Snapshots()
		if err != nil {
			return fail(err)
		}
		return &rpcResp{Snapshots: snaps}
	})
	verb("deletesnapshot", func(q *rpcReq) *rpcResp { return okErr(agent.DeleteSnapshot(q.Name, q.ID)) })
	verb("rollback", func(q *rpcReq) *rpcResp { return okErr(agent.Rollback(q.Name, q.ID)) })
	verb("states", func(q *rpcReq) *rpcResp { return &rpcResp{States: agent.States(q.Names)} })
	verb("heartbeat", func(q *rpcReq) *rpcResp { return &rpcResp{Heartbeat: agent.Heartbeat()} })

	// File content streams raw: the body IS the file, args ride the query.
	mux.HandleFunc("PUT /v1/file", func(w http.ResponseWriter, r *http.Request) {
		mode, _ := strconv.ParseUint(r.URL.Query().Get("mode"), 8, 32)
		err := agent.WriteFileFrom(r.URL.Query().Get("name"), r.URL.Query().Get("path"), r.Body, os.FileMode(mode))
		writeRPC(w, &rpcResp{OK: err == nil, Err: errString(err), ErrCode: errCode(err)})
	})
	mux.HandleFunc("GET /v1/file", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var b []byte
		var err error
		if max, _ := strconv.ParseInt(q.Get("max"), 10, 64); max > 0 {
			b, err = agent.ReadFileMax(q.Get("name"), q.Get("path"), max)
		} else {
			b, err = agent.ReadFile(q.Get("name"), q.Get("path"))
		}
		if err != nil {
			w.Header().Set(errHeader, errString(err))
			w.Header().Set(errCodeHeader, errCode(err))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(b)
	})
	mux.HandleFunc("POST /v1/tar", func(w http.ResponseWriter, r *http.Request) {
		paths, err := agent.ExtractTar(r.URL.Query().Get("name"), r.Body)
		if err != nil {
			writeRPC(w, &rpcResp{Err: errString(err), ErrCode: errCode(err)})
			return
		}
		writeRPC(w, &rpcResp{Paths: paths})
	})
	return mux
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func writeRPC(w http.ResponseWriter, resp *rpcResp) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// serveTerminal is the node's side of a browser terminal: it starts the login
// shell on a local pty (agent.Terminal -- the only machine where the app's
// user exists), hijacks the duplex stream, and bridges the two until either
// side hangs up. Input arrives framed (data vs resize); output leaves raw.
func serveTerminal(w http.ResponseWriter, r *http.Request, agent nodeapi.NodeAgent) {
	session, err := agent.Terminal(r.PathValue("name"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = session.Close()
		http.Error(w, "cannot hijack this connection", http.StatusInternalServerError)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		_ = session.Close()
		return
	}
	defer conn.Close()
	defer session.Close()
	_ = buf.Flush()
	if _, err := conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\n\r\n")); err != nil {
		return
	}

	// pty -> stream, raw.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(conn, session)
	}()

	// stream -> pty, framed.
	reader := bufio.NewReader(conn)
	for {
		header := make([]byte, 3)
		if _, err := io.ReadFull(reader, header); err != nil {
			break
		}
		payload := make([]byte, int(header[1])<<8|int(header[2]))
		if _, err := io.ReadFull(reader, payload); err != nil {
			break
		}
		switch header[0] {
		case terminalFrameData:
			if _, err := session.Write(payload); err != nil {
				return
			}
		case terminalFrameResize:
			if len(payload) == 4 {
				_ = session.Resize(uint16(payload[0])<<8|uint16(payload[1]), uint16(payload[2])<<8|uint16(payload[3]))
			}
		}
	}
	<-done
}
