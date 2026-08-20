package nodelink

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"heckel.io/hostit/cluster"
	"heckel.io/hostit/nodeapi"
	"heckel.io/hostit/store"
)

// callbackPathPrefix roots the callback routes on control's duplex side.
const callbackPathPrefix = "/callback/"

// ControlLink is the node's reverse channel to control: it implements
// nodeapi.ControlSink by POSTing callbacks over the same duplex connection the
// RPC rides on. The client is swapped on every (re)dial; between connections
// callbacks are dropped with a warning. Usage recovers on its own (re-measured
// on a cadence) and a power transition on the next lifecycle change. Snapshot
// records taken during an outage do NOT yet recover -- the rejoin sync flows
// control->node and overwrites them; a node->control snapshot re-report on
// connect is still owed (see plans/260815-hostit-nodeagent.md).
type ControlLink struct {
	client *http.Client
	mu     sync.Mutex // Protects client
}

var _ nodeapi.ControlSink = (*ControlLink)(nil)

// NewControlLink creates the (initially disconnected) link.
func NewControlLink() *ControlLink {
	return &ControlLink{}
}

// SetClient swaps in the live session's client on every (re)dial.
func (l *ControlLink) SetClient(client *http.Client) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.client = client
}

// Client returns the live session's client, or nil between connections. The
// app-socket relay uses it directly: unlike the fire-and-forget callbacks, a
// relayed request has a caller waiting for the answer.
func (l *ControlLink) Client() *http.Client {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.client
}

// The callback payloads; control's node listener side decodes them.
type powerCallback struct {
	Name       string `json:"name"`
	PoweredOff bool   `json:"powered_off"`
}

type usageCallback struct {
	Name   string `json:"name"`
	UsedMB int    `json:"used_mb"`
}

type snapshotsCallback struct {
	Name      string            `json:"name"`
	Snapshots []*store.Snapshot `json:"snapshots"`
}

func (l *ControlLink) PowerChanged(name string, poweredOff bool) {
	l.post("power", &powerCallback{Name: name, PoweredOff: poweredOff})
}

func (l *ControlLink) UsageChanged(name string, usedMB int) {
	l.post("usage", &usageCallback{Name: name, UsedMB: usedMB})
}

func (l *ControlLink) SnapshotsChanged(name string, snaps []*store.Snapshot) {
	l.post("snapshots", &snapshotsCallback{Name: name, Snapshots: snaps})
}

// post fires one callback; best effort (see the type comment for why loss is
// acceptable).
func (l *ControlLink) post(kind string, payload any) {
	l.mu.Lock()
	client := l.client
	l.mu.Unlock()
	if client == nil {
		slog.Warn("Dropping control callback; not connected", "kind", kind)
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	resp, err := client.Post("http://"+cluster.ControlID+callbackPathPrefix+kind, "application/json", bytes.NewReader(body))
	if err != nil {
		slog.Warn("Control callback failed", "kind", kind, "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Warn("Control callback refused", "kind", kind, "status", resp.Status)
	}
}

// CallbackStore is the slice of the registry the callback handlers write.
// AppHost resolves an app's hosting node, the scoping check every write goes
// through.
type CallbackStore interface {
	AppHost(name string) (string, error)
	SetAppPoweredOff(name string, poweredOff bool) error
	UpdateAppUsage(name string, usedMB int) error
	ReplaceAppSnapshots(appName string, snaps []*store.Snapshot) error
}

// CallbackHandler is control's receiving side, served over the duplex session
// of ONE node's connection: it applies the node-originated control-plane data
// to the registry. Every write is scoped to the app's hosting node -- the
// node id is the connection's authenticated identity, and a node may only
// report data for apps it hosts. Without this, a compromised node could flip
// another tenant's app powered_off, poison its usage, or wipe its snapshot
// records by naming it in a callback.
func CallbackHandler(nodeID string, st CallbackStore) http.Handler {
	mux := http.NewServeMux()
	// handle decodes the body, checks the named app belongs to this node, then
	// applies the write. Every callback payload carries its target in "name".
	handle := func(kind string, fn func(body []byte) error) {
		mux.HandleFunc("POST "+callbackPathPrefix+kind, func(w http.ResponseWriter, r *http.Request) {
			var buf bytes.Buffer
			if _, err := buf.ReadFrom(r.Body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			var target struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(buf.Bytes(), &target); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			host, err := st.AppHost(target.Name)
			if host == "" {
				host = store.HostLocal // a legacy unset host means the colocated node
			}
			if err != nil || host != nodeID {
				slog.Warn("Rejecting node callback for an app it does not host", "node", nodeID, "app", target.Name, "kind", kind)
				http.Error(w, "app not hosted by this node", http.StatusForbidden)
				return
			}
			if err := fn(buf.Bytes()); err != nil {
				slog.Warn("Node callback failed", "node", nodeID, "kind", kind, "error", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
		})
	}
	handle("power", func(body []byte) error {
		var cb powerCallback
		if err := json.Unmarshal(body, &cb); err != nil {
			return err
		}
		return st.SetAppPoweredOff(cb.Name, cb.PoweredOff)
	})
	handle("usage", func(body []byte) error {
		var cb usageCallback
		if err := json.Unmarshal(body, &cb); err != nil {
			return err
		}
		return st.UpdateAppUsage(cb.Name, cb.UsedMB)
	})
	handle("snapshots", func(body []byte) error {
		var cb snapshotsCallback
		if err := json.Unmarshal(body, &cb); err != nil {
			return err
		}
		return st.ReplaceAppSnapshots(cb.Name, cb.Snapshots)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, fmt.Sprintf("unknown callback %s", r.URL.Path), http.StatusNotFound)
	})
	return mux
}
