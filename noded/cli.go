// Package noded is the hostit-node engine: the machine half of the platform
// as its own service. It runs the node-local startup and loops (budgets, raw
// apps view, workspace image, state/disk/snapshot/qgroup loops, reconciles)
// and dials control, serving the NodeAgent RPC over one mTLS connection.
// Colocated interim: it shares the host, store and config file with control;
// stateless remote nodes are Phase 2b.
package noded

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/urfave/cli/v2"
	"heckel.io/hostit/app"
	"heckel.io/hostit/cmd"
	"heckel.io/hostit/config"
	"heckel.io/hostit/node"
	"heckel.io/hostit/run"
	"heckel.io/hostit/store"
	"heckel.io/hostit/user"
)

const (
	// redialDelay paces reconnects to control
	redialDelay = 2 * time.Second
)

// NewCLI is the hostit-node command line: `serve` and `join`.
func NewCLI() *cli.App {
	return &cli.App{
		Name:  "hostit-node",
		Usage: "hostit's machine half: runs apps on this host and serves control's node RPC",
		Commands: []*cli.Command{
			{
				Name:   "serve",
				Usage:  "Run the node daemon (requires root)",
				Action: execServe,
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: config.DefaultServerConfigFile, Usage: "server config file (shared with hostit-control when colocated)"},
				},
			},
			{
				Name:   "join",
				Usage:  "Enroll this machine with control: exchange a one-time join token for this node's mTLS certificate",
				Action: execJoin,
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: config.DefaultServerConfigFile, Usage: "node config file"},
					&cli.StringFlag{Name: "control", Required: true, Usage: "control's node listener, host:port"},
					&cli.StringFlag{Name: "token", Required: true, Usage: "join token from `hostit-control node add`"},
				},
			},
		},
	}
}

// execJoin runs the enrollment exchange and tells the operator what the
// config must say for serve to dial in as this node.
func execJoin(c *cli.Context) error {
	conf, err := config.LoadConfig(c.String("config"))
	if err != nil {
		return err
	}
	token, control := c.String("token"), c.String("control")
	name, _, _, err := node.ParseJoinToken(token)
	if err != nil {
		return err
	}
	if err := node.Join(control, token, conf.DataDir); err != nil {
		return err
	}
	fmt.Printf("Joined as node %q; credentials stored under %s.\n", name, conf.DataDir)
	fmt.Printf("Make sure the config sets:\n\n  node-id: %s\n  listen-node: %s\n\nthen start hostit-node.\n", name, control)
	return nil
}

func execServe(c *cli.Context) error {
	conf, err := config.LoadConfig(c.String("config"))
	if err != nil {
		return err
	}
	if err := conf.Validate(); err != nil {
		return err
	}
	if err := cmd.Preflight(conf.AppsDir); err != nil {
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
	// The machine half of every stored limit (control only records them in
	// split mode): re-cap each app's budget qgroup to its owner's limit.
	if err := applyLimits(s, manager, conf); err != nil {
		return err
	}
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
	tlsConf, err := node.LoadNodeCreds(conf.DataDir, conf.NodeID)
	if err != nil {
		return err
	}
	// A termination signal closes the live connection: ServeAgent blocks on the
	// session and would otherwise ignore SIGTERM until systemd SIGKILLs us
	// after its stop timeout.
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	var connMu sync.Mutex
	var current net.Conn
	stopping := make(chan struct{})
	go func() {
		<-sigCh
		close(stopping)
		connMu.Lock()
		if current != nil {
			_ = current.Close()
		}
		connMu.Unlock()
	}()
	slog.Info("Node ready; dialing control", "addr", conf.ListenNode, "id", conf.NodeID)
	for {
		select {
		case <-stopping:
			return nil
		default:
		}
		conn, err := tls.Dial("tcp", conf.ListenNode, tlsConf)
		if err != nil {
			time.Sleep(redialDelay)
			continue
		}
		connMu.Lock()
		current = conn
		connMu.Unlock()
		slog.Info("Connected to control", "addr", conf.ListenNode)
		if err := node.ServeAgent(conn, conf.NodeID, manager); err != nil {
			slog.Warn("Control connection failed", "error", err)
		}
		_ = conn.Close()
		select {
		case <-stopping:
			return nil
		default:
		}
		slog.Warn("Control connection lost; redialing")
		time.Sleep(redialDelay)
	}
}

var sigCh = make(chan os.Signal, 1)

// applyLimits applies each app owner's stored memory/disk limits on this
// machine -- the split-mode counterpart of the fused daemon's startup step.
func applyLimits(s *store.Store, manager *app.Manager, conf *config.Config) error {
	users := user.NewManager(conf, s)
	apps, err := s.Apps()
	if err != nil {
		return err
	}
	defaults, err := users.Defaults()
	if err != nil {
		return err
	}
	for _, a := range apps {
		limits := defaults
		if a.OwnerID != "" {
			if owner, err := users.User(a.OwnerID); err == nil {
				if limits, err = users.Limits(owner); err != nil {
					return err
				}
			}
		}
		manager.SetMemoryLimit(a.Name, limits.MemoryMB)
		manager.SetDiskLimit(a.Name, limits.DiskMB)
	}
	return nil
}
