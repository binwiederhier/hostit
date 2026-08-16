package node

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"heckel.io/hostit/app"
	"heckel.io/hostit/store"
)

// ControlLink is the node's reverse channel to control: it implements
// app.ControlSink by POSTing callbacks over the same duplex connection the
// RPC rides on. The client is swapped on every (re)dial; between connections
// callbacks are dropped with a warning -- every payload is either re-measured
// on a cadence (usage) or re-derived from authoritative node state on the
// next connect (snapshots via the rejoin's re-sync, power via the mirror).
type ControlLink struct {
	mu     sync.Mutex // Protects client
	client *http.Client
}

var _ app.ControlSink = (*ControlLink)(nil)

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
	resp, err := client.Post("http://control"+callbackPathPrefix+kind, "application/json", bytes.NewReader(body))
	if err != nil {
		slog.Warn("Control callback failed", "kind", kind, "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Warn("Control callback refused", "kind", kind, "status", resp.Status)
	}
}

// callbackPathPrefix roots the callback routes on control's duplex side.
const callbackPathPrefix = "/callback/"

// CallbackStore is the slice of the registry the callback handlers write.
type CallbackStore interface {
	SetAppPoweredOff(name string, poweredOff bool) error
	UpdateAppUsage(name string, usedMB int) error
	ReplaceAppSnapshots(appName string, snaps []*store.Snapshot) error
}

// CallbackHandler is control's receiving side, served over the duplex session
// of ONE node's connection: it applies the node-originated control-plane data
// to the registry.
func CallbackHandler(nodeID string, st CallbackStore) http.Handler {
	mux := http.NewServeMux()
	handle := func(kind string, fn func(body []byte) error) {
		mux.HandleFunc("POST "+callbackPathPrefix+kind, func(w http.ResponseWriter, r *http.Request) {
			var buf bytes.Buffer
			if _, err := buf.ReadFrom(r.Body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
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
