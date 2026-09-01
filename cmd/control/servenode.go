package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"heckel.io/hostit/cluster"
	"heckel.io/hostit/control"
	"heckel.io/hostit/control/config"
	nodeapi "heckel.io/hostit/node/api"
	nodelink "heckel.io/hostit/node/link"
	"heckel.io/hostit/proxy/api"
	proxylink "heckel.io/hostit/proxy/link"
	"heckel.io/hostit/store"
	"heckel.io/hostit/system/users"
)

const (
	// nodeStatePoll is how often control pulls a node's batch states into its
	// cache in split mode -- the wire version of the node's own settle cadence.
	nodeStatePoll = 2 * time.Second
	// nodeStatsInterval is how often that poll also asks the node about its
	// MACHINE (memory, disk, load). Slower than the state poll on purpose: app
	// state feeds placement and the dashboard's liveness, machine stats feed a
	// display. It matches the proxy's heartbeat pass, so both members' readings
	// age the same way.
	nodeStatsInterval = 30 * time.Second
)

// nodeRole is how control admits a node: the registry row is the membership
// switch on top of the certificate, each connection supersedes the previous
// one's poll loop, and every (re)connect runs the rejoin handshake.
func nodeRole(manager *control.Manager, registry *control.NodeRegistry, srv *control.Server, done <-chan struct{}, mu *sync.Mutex, supersede map[string]chan struct{}) *cluster.Role {
	return nodelink.Role(func(nodeID string) bool {
		// Membership IS the transport-proven identity: over mTLS a CA-signed
		// cert (OU=node), over the unix socket the kernel peer-cred gate. The
		// row self-registers on connect (below), so no pre-enrollment is needed.
		return true
	}, func(nodeID string) http.Handler {
		// The node's reverse channel: usage, poweroffs and snapshot records it
		// originates land in the registry through the callbacks, and the app
		// socket the node serves relays its /v1 requests through /apprelay --
		// both scoped to the apps this node hosts.
		mux := http.NewServeMux()
		mux.Handle("/callback/", nodelink.CallbackHandler(nodeID, manager.Store()))
		mux.Handle(nodeapi.AppRelayPrefix+"/", srv.AppRelayHandler(nodeID))
		return mux
	}, func(nodeID string, remote control.NodeAgent) {
		slog.Info("Node connected", "node", nodeID)
		if err := registerConnectedNode(manager.Store(), nodeID); err != nil {
			slog.Warn("Cannot register a connected node", "node", nodeID, "error", err)
		}
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
		// A node that just connected may have changed where its apps are
		// reachable, which changes the routing table.
		go srv.PushRoutes()
	}, func(nodeID string, remote control.NodeAgent) {
		slog.Info("Node disconnected", "node", nodeID)
		registry.Unregister(nodeID, remote)
	})
}

// listenForMembers accepts cluster dial-ins on the mTLS member listener. It is
// always running, even on a single-box install: the colocated proxy is a
// cluster member like any other, and this is the only way in.
//
// A node dials in and registers here; the colocated node does so too, over the
// member socket, as its own hostit-node process. Control keeps no machine of its
// own to be swapped -- it always routes verbs to a registered node.
//
// Each node registration wires into the control plane: connected agents live
// in the node registry (orchestration routes each app's verbs to its hosting
// node), a per-node poll loop feeds the state cache, and the rejoin handshake
// pushes the node's registry mirror and re-asserts desired state.
func listenForMembers(conf *config.Config, manager *control.Manager, srv *control.Server, done <-chan struct{}) error {
	// Each node's registration supersedes its previous connection's poll loop:
	// without this, a stale loop against a dead session raced the live one.
	var mu sync.Mutex
	supersede := make(map[string]chan struct{})
	mux := http.NewServeMux()
	// One listener admits every kind of cluster member; the certificate says
	// which. A node is authorized by its registry row on top of that cert: the
	// transport already proved the identity, the row is the membership switch
	// `hostit-control node add` flips on and `node remove` off.
	// A colocated node is NOT pre-seeded here: it self-registers when it dials in
	// (registerConnectedNode). Seeding "local" unconditionally showed a phantom
	// node on a [control, proxy] host that runs no node at all.
	roles := map[string]*cluster.Role{}
	roles[cluster.RoleNode] = nodeRole(manager, manager.NodeRegistry(), srv, done, &mu, supersede)
	// Proxies dial the same listener; their certificate says they are proxies,
	// and their registry row is the membership switch `proxy remove` flips off.
	// Every proxy that connects is handed the routing table immediately, so a
	// proxy that just started is never serving an empty table for longer than
	// its connect takes.
	roles[cluster.RoleProxy] = proxylink.Role(func(proxyID string) bool {
		// Same as the node: a CA-signed cert (OU=proxy) over mTLS, or the
		// peer-cred gate over the socket for the colocated proxy. The row
		// self-registers on connect below.
		return true
	}, srv, func(proxyID string, agent api.ProxyAgent) {
		slog.Info("Proxy connected", "proxy", proxyID)
		if err := manager.Store().EnsureProxy(proxyID); err != nil {
			slog.Warn("Cannot register a connected proxy", "proxy", proxyID, "error", err)
		}
		_ = manager.Store().SetProxySeen(proxyID, time.Now())
		srv.Proxies().Register(proxyID, agent)
		go srv.PushRoutes()
	}, func(proxyID string, agent api.ProxyAgent) {
		slog.Info("Proxy disconnected", "proxy", proxyID)
		srv.Proxies().Unregister(proxyID, agent)
	})
	mux.Handle("/", cluster.ConnectHandler(roles))
	// The same-host socket is always there: a node and a proxy sharing this
	// machine need no certificate, no CA and nothing minted in advance -- the
	// socket's existence is the only precondition, and the kernel says who is
	// calling. A single-box install needs no listen-cluster at all.
	// The colocated proxy runs as hostit-proxy, not control's user, so admit its
	// uid on the member socket; absent (remote or root proxy) registers nothing.
	cluster.TrustPeerUID(users.UID(users.Proxy))
	ln, err := cluster.ListenSocket(cluster.SocketPath(cluster.DefaultSocketFile), 0o666)
	if err != nil {
		return fmt.Errorf("cluster socket: %w", err)
	}
	sockSrv := cluster.SocketServer(mux)
	go func() {
		slog.Info("Listening for same-host members", "socket", cluster.SocketPath(cluster.DefaultSocketFile))
		if err := sockSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("Member socket failed", "error", err)
		}
	}()
	// mTLS on a real address is for members on OTHER machines, and only exists
	// when the operator names one.
	if conf.ListenCluster == "" {
		return nil
	}
	tlsConf, err := nodelink.ListenerCreds(conf.ClusterCertFile, conf.ClusterKeyFile, conf.ClusterCACertFile)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{
		Addr:      conf.ListenCluster,
		TLSConfig: tlsConf,
		Handler:   mux,
	}
	go func() {
		slog.Info("Listening for remote cluster dial-ins", "addr", conf.ListenCluster)
		if err := httpSrv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("Cluster listener failed", "error", err)
		}
	}()
	return nil
}

// registerConnectedNode records a node that just dialed in and stamps its
// liveness. The colocated node needs no pre-enrollment, so this is the ONLY
// place a node enters the registry from a connection -- a control host with
// nothing connected therefore lists no nodes (no phantom "local").
func registerConnectedNode(s *store.Store, nodeID string) error {
	if err := s.EnsureNode(nodeID, ""); err != nil {
		return err
	}
	return s.SetNodeSeen(nodeID, time.Now())
}

// pruneUnseenLocalNode removes a legacy phantom "local" row at startup. Earlier
// versions pre-seeded one on every control start, so a nodeless [control, proxy]
// host kept a never-seen "local" in `node list`. A real colocated node re-adds
// itself on connect (registerConnectedNode) with a fresh LastSeen, so pruning an
// UNSEEN "local" is safe and self-heals the old rows on upgrade.
func pruneUnseenLocalNode(s *store.Store) {
	n, err := s.Node(store.HostLocal)
	if err != nil || !n.LastSeen.IsZero() {
		return
	}
	if err := s.RemoveNode(store.HostLocal); err != nil {
		slog.Warn("Cannot prune the phantom local node", "error", err)
		return
	}
	slog.Info("Pruned a legacy phantom local node from the registry")
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
	var lastStats time.Time // zero: the first tick refreshes
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
		// Machine stats used to be written only by the connect handshake, so a
		// node that stayed connected reported whatever its load was when it
		// dialled in, forever. Refresh them on their own slower cadence.
		refreshStats := time.Since(lastStats) >= nodeStatsInterval
		if refreshStats {
			lastStats = time.Now()
		}
		pollNodeOnce(manager, nodeID, remote, refreshStats)
	}
}

// pollNodeOnce is one tick of pollNodeStates: measure the node's apps and
// stamp its liveness on any answer. With refreshStats it also re-reads what the
// node says about its own machine.
func pollNodeOnce(manager *control.Manager, nodeID string, remote control.NodeAgent, refreshStats bool) {
	names := make([]string, 0)
	for _, a := range nodeApps(manager, nodeID) {
		names = append(names, a.Name)
	}
	// RecordNodeStatus stamps liveness too, so this covers the empty-node case
	// below as well.
	if refreshStats {
		if hb := remote.Heartbeat(); hb != nil {
			if err := manager.RecordNodeStatus(nodeID, hb); err != nil {
				slog.Warn("Cannot record a node's machine stats", "node", nodeID, "error", err)
			}
		}
	}
	// A node hosting nothing must still be asked SOMETHING: this poll doubles
	// as the liveness heartbeat, and an empty node that is never polled reads
	// as LAST SEEN hours ago -- a freshly added node as dead before its first
	// app arrives. States([]) cannot be the probe (omitempty turns the empty
	// answer into nil, which means "could not measure"), so take the pulse.
	if len(names) == 0 {
		if remote.Heartbeat() != nil {
			_ = manager.Store().SetNodeSeen(nodeID, time.Now())
		}
		return
	}
	if states := remote.States(names); states != nil {
		// Scope to the apps we asked about: a node may only report state for
		// the apps it hosts, so a lying node's extra keys never reach the
		// cache (and thus other nodes' apps).
		manager.IngestStates(scopeStates(states, names))
		_ = manager.Store().SetNodeSeen(nodeID, time.Now())
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
