package main

import (
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"heckel.io/hostit/cluster"
	"heckel.io/hostit/config"
	"heckel.io/hostit/control"
	"heckel.io/hostit/nodelink"
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
func listenForNode(conf *config.Config, manager *control.Manager, srv *control.Server, done <-chan struct{}) error {
	tlsConf, err := nodelink.ListenerCreds(conf.ClusterCertFile, conf.ClusterKeyFile, conf.ClusterCACertFile, conf.DataDir)
	if err != nil {
		return err
	}
	// The colocated node exists implicitly and is always authorized.
	if err := manager.Store().EnsureNode(store.HostLocal, "127.0.0.1"); err != nil {
		return err
	}
	registry := control.NewNodeRegistry()
	manager.SetNodeRegistry(registry)
	srv.SetNode(control.NewRoutingAgent(manager.Store(), registry))
	// Each node's registration supersedes its previous connection's poll loop:
	// without this, a stale loop against a dead session raced the live one.
	var mu sync.Mutex
	supersede := make(map[string]chan struct{})
	mux := http.NewServeMux()
	// One listener admits every kind of cluster member; the certificate says
	// which. A node is authorized by its registry row on top of that cert: the
	// transport already proved the identity, the row is the membership switch
	// `hostit-control node add` flips on and `node remove` off.
	roles := map[string]*cluster.Role{}
	roles[cluster.RoleNode] = nodelink.Role(func(nodeID string) bool {
		_, err := manager.Store().Node(nodeID)
		return err == nil
	}, func(nodeID string) http.Handler {
		// The node's reverse channel: usage, poweroffs and snapshot records it
		// originates land in the registry through these.
		return nodelink.CallbackHandler(nodeID, manager.Store())
	}, func(nodeID string, remote control.NodeAgent) {
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
	}, func(nodeID string, remote control.NodeAgent) {
		slog.Info("Node disconnected", "node", nodeID)
		registry.Unregister(nodeID, remote)
	})
	mux.Handle("/", cluster.ConnectHandler(roles))
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
func nodeApps(manager *control.Manager, nodeID string) []*store.App {
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
func pollNodeStates(manager *control.Manager, registry *control.NodeRegistry, nodeID string, remote control.NodeAgent, done, superseded <-chan struct{}) {
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
func scopeStates(states map[string]control.State, allowed []string) map[string]control.State {
	keep := make(map[string]control.State, len(allowed))
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
func rejoin(manager *control.Manager, nodeID string, remote control.NodeAgent) {
	// Ask the node about itself first: where its apps are reachable (routing
	// needs it, and only the node knows), which build it runs, and -- by
	// answering at all -- that it is alive. Without this a node that hosts no
	// apps never updated last_seen, because that was a side effect of the
	// state poll.
	if hb := remote.Heartbeat(); hb != nil {
		if err := manager.RecordNodeStatus(nodeID, hb); err != nil {
			slog.Warn("Cannot record a node's status", "node", nodeID, "error", err)
		}
	}
	// Recover snapshot records the node wrote while the connection was down,
	// BEFORE the mirror push overwrites its rows with control's older list.
	manager.IngestNodeSnapshots(nodeID, remote)
	manager.PushMirrorTo(nodeID, remote)
	// Hand the node its whole desired configuration and let it converge: build
	// what is missing, correct keys and limits that drifted while it was away,
	// drop what control no longer lists. It is built here, from the registry,
	// so the node needs no memory of its own.
	desired, err := manager.DesiredState(nodeID)
	if err != nil {
		slog.Warn("Cannot build the desired state for a node", "node", nodeID, "error", err)
	} else {
		remote.Reconcile(desired)
	}
	ensured := 0
	for _, a := range nodeApps(manager, nodeID) {
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
