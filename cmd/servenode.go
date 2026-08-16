package cmd

import (
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"heckel.io/hostit/app"
	"heckel.io/hostit/config"
	"heckel.io/hostit/node"
	"heckel.io/hostit/server"
	"heckel.io/hostit/store"
)

const (
	// nodeStatePoll is how often control pulls a node's batch states into its
	// cache in split mode -- the wire version of the node's own settle cadence.
	nodeStatePoll = 2 * time.Second
)

// listenForNode accepts hostit-node dial-ins on the mTLS node listener and
// wires each registration into the control plane: connected agents live in
// the node registry (orchestration routes each app's verbs to its hosting
// node), a per-node poll loop feeds the state cache, and the rejoin handshake
// pushes the node's registry mirror and re-asserts desired state.
func listenForNode(conf *config.Config, manager *app.Manager, srv *server.Server, done <-chan struct{}) error {
	tlsConf, ca, err := node.EnsureIPCCreds(conf.DataDir)
	if err != nil {
		return err
	}
	// The colocated node exists implicitly and is always authorized.
	if err := manager.Store().EnsureNode(store.HostLocal, "127.0.0.1"); err != nil {
		return err
	}
	registry := app.NewNodeRegistry()
	manager.SetNodeRegistry(registry)
	srv.SetNode(app.NewRoutingAgent(manager.Store(), registry))
	// Each node's registration supersedes its previous connection's poll loop:
	// without this, a stale loop against a dead session raced the live one.
	var mu sync.Mutex
	supersede := make(map[string]chan struct{})
	mux := http.NewServeMux()
	// Enrollment: a new node exchanges its one-time join token for an mTLS
	// certificate; the only route here that runs without a client cert.
	mux.Handle(node.JoinPath, node.JoinHandler(ca, manager.Store()))
	mux.Handle("/", node.ConnectHandler(func(nodeID string) bool {
		n, err := manager.Store().Node(nodeID)
		return err == nil && !n.JoinedAt.IsZero()
	}, func(nodeID string) http.Handler {
		// The node's reverse channel: usage, poweroffs and snapshot records it
		// originates land in the registry through these.
		return node.CallbackHandler(nodeID, manager.Store())
	}, func(nodeID string, remote app.NodeAgent) {
		slog.Info("Node connected", "node", nodeID)
		_ = manager.Store().SetNodeSeen(nodeID, time.Now())
		mu.Lock()
		if prev := supersede[nodeID]; prev != nil {
			close(prev)
		}
		superseded := make(chan struct{})
		supersede[nodeID] = superseded
		mu.Unlock()
		registry.Register(nodeID, remote)
		go pollNodeStates(manager, registry, nodeID, remote, done, superseded)
		go rejoin(manager, nodeID, remote)
	}, func(nodeID string, remote app.NodeAgent) {
		slog.Info("Node disconnected", "node", nodeID)
		registry.Unregister(nodeID, remote)
	}))
	httpSrv := &http.Server{
		Addr:      conf.ListenNode,
		TLSConfig: tlsConf,
		Handler:   mux,
	}
	go func() {
		slog.Info("Listening for node dial-ins", "addr", conf.ListenNode)
		if err := httpSrv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("Node listener failed", "error", err)
		}
	}()
	return nil
}

// nodeApps lists the apps hosted by one node.
func nodeApps(manager *app.Manager, nodeID string) []*store.App {
	apps, err := manager.Store().Apps()
	if err != nil {
		return nil
	}
	mine := make([]*store.App, 0, len(apps))
	for _, a := range apps {
		host := a.Host
		if host == "" {
			host = store.HostLocal
		}
		if host == nodeID {
			mine = append(mine, a)
		}
	}
	return mine
}

// pollNodeStates feeds one node's measurements into control's state cache, so
// dashboards and placement read memory, never the wire, per request. Each
// node is asked only about its own apps; the cache merges per name. The poll
// also doubles as the liveness heartbeat.
func pollNodeStates(manager *app.Manager, registry *app.NodeRegistry, nodeID string, remote app.NodeAgent, done, superseded <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case <-superseded:
			return
		case <-time.After(nodeStatePoll):
		}
		// `hostit-control node remove` runs in a separate process and only
		// deletes the registry row; the running daemon checks it here so a
		// removed node stops being routed verbs within a poll interval, rather
		// than only when its TCP session happens to drop (connect-time authz
		// alone would keep a live session going).
		if _, err := manager.Store().Node(nodeID); errors.Is(err, store.ErrNodeNotFound) {
			slog.Info("Node was removed; dropping its session", "node", nodeID)
			registry.Unregister(nodeID, remote)
			return
		}
		names := make([]string, 0)
		for _, a := range nodeApps(manager, nodeID) {
			names = append(names, a.Name)
		}
		if len(names) == 0 {
			continue
		}
		if states := remote.States(names); states != nil {
			// Scope to the apps we asked about: a node may only report state for
			// the apps it hosts, so a lying node's extra keys never reach the
			// cache (and thus other nodes' apps).
			manager.IngestStates(scopeStates(states, names))
			_ = manager.Store().SetNodeSeen(nodeID, time.Now())
		}
	}
}

// scopeStates keeps only the states for the apps in allowed: control asked a
// node about the apps it hosts, so anything else it returns is dropped rather
// than trusted into the cache.
func scopeStates(states map[string]app.State, allowed []string) map[string]app.State {
	keep := make(map[string]app.State, len(allowed))
	for _, name := range allowed {
		if s, ok := states[name]; ok {
			keep[name] = s
		}
	}
	return keep
}

// rejoin is the reconcile handshake, run on every node (re)connect: push the
// node's registry mirror FIRST (the node gates its destructive startup work
// on it, and every row-reading verb needs it), re-assert each of its apps'
// limits, then Ensure every app that should be running, so an app whose
// container died during the outage comes back without waiting for a user.
func rejoin(manager *app.Manager, nodeID string, remote app.NodeAgent) {
	manager.PushMirrorTo(nodeID, remote)
	// Converge the node to the just-pushed mirror: tear down apps deleted while
	// it was disconnected (the routed Deprovision was dropped) and re-assert its
	// port rules. Must follow the mirror push.
	remote.Reconcile()
	ensured := 0
	for _, a := range nodeApps(manager, nodeID) {
		remote.SetMemoryLimit(a.Name, manager.MemoryLimit(a.Name))
		remote.SetDiskLimit(a.Name, manager.DiskLimit(a.Name))
		if a.PoweredOff {
			continue
		}
		if _, err := remote.Ensure(a.Name); err != nil {
			slog.Warn("Rejoin: cannot ensure app", "node", nodeID, "app", a.Name, "error", err)
			continue
		}
		ensured++
	}
	slog.Info("Rejoin sweep complete", "node", nodeID, "apps", ensured)
}
