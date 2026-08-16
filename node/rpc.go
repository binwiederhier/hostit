package node

import (
	"encoding/json"
	"errors"
	"fmt"
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
	States    map[string]nodeapi.State `json:"states,omitempty"`
	Heartbeat *nodeapi.Heartbeat       `json:"heartbeat,omitempty"`
	Listing   *nodeapi.Listing         `json:"listing,omitempty"`
	Stat      *nodeapi.FileInfo        `json:"stat,omitempty"`
	Err       string                   `json:"err,omitempty"`
	ErrCode   string                   `json:"err_code,omitempty"`
}

// The sentinels that must survive the wire: the control plane's error-to-HTTP
// mapping (writeAppError) matches these with errors.Is.
var wireErrs = map[string]error{
	"powered_off":   appctl.ErrPoweredOff,
	"app_notfound":  store.ErrAppNotFound,
	"snap_notfound": store.ErrSnapshotNotFound,
	"app_exists":    nodeapi.ErrAppExists,
	"file_notfound": fs.ErrNotExist,
	"invalid":       nodeapi.ErrInvalid,
	"limit":         nodeapi.ErrLimitReached,
}

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
	verb("reconcile", func(*rpcReq) *rpcResp { agent.Reconcile(); return &rpcResp{OK: true} })
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
	verb("terminal", func(q *rpcReq) *rpcResp {
		cmd, args, err := agent.TerminalCommand(q.Name)
		if err != nil {
			return fail(err)
		}
		return &rpcResp{Cmd: cmd, Args: args}
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
	verb("synckeys", func(q *rpcReq) *rpcResp { return okErr(agent.SyncKeys(q.Name, q.ProfileKeys)) })
	verb("setmemorylimit", func(q *rpcReq) *rpcResp { agent.SetMemoryLimit(q.Name, q.MemoryMB); return &rpcResp{OK: true} })
	verb("setdisklimit", func(q *rpcReq) *rpcResp { agent.SetDiskLimit(q.Name, q.DiskMB); return &rpcResp{OK: true} })
	verb("snapshot", func(q *rpcReq) *rpcResp {
		snap, err := agent.TakeSnapshot(q.Name, q.Label, q.Auto)
		if err != nil {
			return fail(err)
		}
		return &rpcResp{Snapshot: snap}
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
