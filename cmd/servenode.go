package cmd

import (
	"log/slog"
	"net/http"
	"time"

	"heckel.io/hostit/app"
	"heckel.io/hostit/config"
	"heckel.io/hostit/node"
	"heckel.io/hostit/server"
	"heckel.io/hostit/store"
)

const (
	// nodeStatePoll is how often control pulls the node's batch states into its
	// cache in split mode -- the wire version of the node's own settle cadence.
	nodeStatePoll = 2 * time.Second
)

// listenForNode accepts hostit-node dial-ins on the mTLS node listener and
// wires each registration into the control plane: the remote agent becomes
// the manager's and the server's NodeAgent, a poll loop feeds the state
// cache, and the rejoin sweep re-asserts desired state.
func listenForNode(conf *config.Config, manager *app.Manager, srv *server.Server, done <-chan struct{}) error {
	tlsConf, err := node.EnsureIPCCreds(conf.DataDir)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{
		Addr:      conf.ListenNode,
		TLSConfig: tlsConf,
		Handler: node.ConnectHandler(func(nodeID string, remote app.NodeAgent) {
			slog.Info("Node connected", "node", nodeID)
			manager.SetNodeAgent(remote)
			srv.SetNode(remote)
			go pollNodeStates(manager, remote, done)
			go rejoin(manager, remote)
		}),
	}
	go func() {
		slog.Info("Listening for node dial-ins", "addr", conf.ListenNode)
		if err := httpSrv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			slog.Error("Node listener failed", "error", err)
		}
	}()
	return nil
}

// pollNodeStates feeds the node's measurements into control's state cache, so
// dashboards and placement read memory, never the wire, per request.
func pollNodeStates(manager *app.Manager, remote app.NodeAgent, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case <-time.After(nodeStatePoll):
		}
		apps, err := manager.Store().Apps()
		if err != nil {
			continue
		}
		names := make([]string, 0, len(apps))
		for _, a := range apps {
			names = append(names, a.Name)
		}
		if states := remote.States(names); states != nil {
			manager.IngestStates(states)
		}
	}
}

// rejoin is the reconcile handshake, run on every node (re)connect: every app
// that should be running gets an Ensure, so an app whose container died
// during the outage comes back without waiting for a user action.
func rejoin(manager *app.Manager, remote app.NodeAgent) {
	apps, err := manager.Store().Apps()
	if err != nil {
		slog.Warn("Rejoin: cannot list apps", "error", err)
		return
	}
	ensured := 0
	for _, a := range apps {
		if a.PoweredOff {
			continue
		}
		if _, err := remote.Ensure(a.Name); err != nil {
			slog.Warn("Rejoin: cannot ensure app", "app", a.Name, "error", err)
			continue
		}
		ensured++
	}
	slog.Info("Rejoin sweep complete", "apps", ensured)
}

var _ = store.HostLocal // keep the import stable while the node table lands in 2b
