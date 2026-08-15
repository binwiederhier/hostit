// Package noded is the hostit-node engine: the machine half of the platform
// as its own service. It runs the node-local startup and loops (budgets, raw
// apps view, workspace image, state/disk/snapshot/qgroup loops, reconciles)
// and dials control, serving the NodeAgent RPC over one mTLS connection.
// Colocated interim: it shares the host, store and config file with control;
// stateless remote nodes are Phase 2b.
package noded

import (
	"crypto/tls"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/urfave/cli/v2"
	"heckel.io/hostit/app"
	"heckel.io/hostit/config"
	"heckel.io/hostit/node"
	"heckel.io/hostit/run"
	"heckel.io/hostit/store"
)

const (
	// redialDelay paces reconnects to control
	redialDelay = 2 * time.Second
	// nodeID is the colocated node's identity (its cert CN)
	nodeID = "local"
)

// NewCLI is the hostit-node command line: one job, `serve`.
func NewCLI() *cli.App {
	return &cli.App{
		Name:  "hostit-node",
		Usage: "hostit's machine half: runs apps on this host and serves control's node RPC",
		Commands: []*cli.Command{{
			Name:   "serve",
			Usage:  "Run the node daemon (requires root)",
			Action: execServe,
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: config.DefaultServerConfigFile, Usage: "server config file (shared with hostit-control when colocated)"},
			},
		}},
	}
}

func execServe(c *cli.Context) error {
	conf, err := config.LoadConfig(c.String("config"))
	if err != nil {
		return err
	}
	if err := conf.Validate(); err != nil {
		return err
	}
	s, err := store.NewStore(filepath.Join(conf.DataDir, "hostit.db"))
	if err != nil {
		return err
	}
	defer s.Close()
	app.Version = c.App.Version
	manager := app.NewManager(conf, s, app.NewSystemServices(run.New()))

	// The node-local startup, same order as the fused daemon's.
	manager.EnableDiskBudgets()
	if err := manager.MountRawAppsView(filepath.Join(filepath.Dir(conf.SocketFile), "apps-raw")); err != nil {
		return err
	}
	go func() {
		if err := manager.EnsureWorkspaceBase(); err != nil {
			slog.Warn("Cannot prepare workspace base rootfs; the first app deploy will retry", "error", err)
		}
		restarted, err := manager.RestartStaleAgents(c.App.Version)
		if err != nil {
			slog.Warn("Cannot restart apps after upgrade", "error", err)
		} else if len(restarted) > 0 {
			slog.Info("Restarted apps to pick up the new version", "apps", restarted, "version", c.App.Version)
		}
		manager.PruneOldWorkspaceImages()
	}()
	if removed := manager.ReconcileOrphans(); len(removed) > 0 {
		slog.Info("Cleaned up leftovers of apps that no longer exist", "apps", removed)
	}
	done := make(chan struct{})
	defer close(done)
	go manager.DiskUsageLoop(done)
	manager.SeedStates()
	manager.BackfillUIDs()
	go manager.StateLoop(done)
	go manager.SnapshotLoop(time.Hour, done)
	go manager.QgroupSweepLoop(6*time.Hour, done)

	// Dial control forever: serve the RPC over the mTLS connection; on death,
	// redial with backoff. Control runs its rejoin handshake on every register.
	tlsConf, err := node.LoadNodeCreds(conf.DataDir, nodeID)
	if err != nil {
		return err
	}
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	slog.Info("Node ready; dialing control", "addr", conf.ListenNode, "id", nodeID)
	for {
		conn, err := tls.Dial("tcp", conf.ListenNode, tlsConf)
		if err != nil {
			time.Sleep(redialDelay)
			continue
		}
		slog.Info("Connected to control", "addr", conf.ListenNode)
		if err := node.ServeAgent(conn, nodeID, manager); err != nil {
			slog.Warn("Control connection failed", "error", err)
		}
		slog.Warn("Control connection lost; redialing")
		_ = conn.Close()
		if interrupted() {
			return nil
		}
		time.Sleep(redialDelay)
	}
}

// interrupted reports whether the process got a termination signal; the dial
// loop otherwise spins through shutdown.
func interrupted() bool {
	select {
	case <-sigCh:
		return true
	default:
		return false
	}
}

var sigCh = make(chan os.Signal, 1)
