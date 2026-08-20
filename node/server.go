package node

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

	"heckel.io/hostit/cluster"
	"heckel.io/hostit/nodeconf"
	"heckel.io/hostit/nodelink"
	"heckel.io/hostit/preflight"
	"heckel.io/hostit/run"
	"heckel.io/hostit/store"
	"heckel.io/hostit/unixuser"
	"heckel.io/hostit/workspace"
)

// The node's configuration lives in its own leaf package (nodeconf) so that
// reading it does not drag in this package's machine stack; these aliases keep
// it spelled node.Config everywhere the node itself uses it.
type Config = nodeconf.Config

const (
	// DefaultConfigFile is where a node's config lives on a package install.
	DefaultConfigFile = nodeconf.DefaultConfigFile
	legacyConfigFile  = nodeconf.LegacyConfigFile
)

var (
	NewConfig  = nodeconf.NewConfig
	LoadConfig = nodeconf.LoadConfig
)

const (
	// redialDelay paces reconnects to control
	redialDelay = 2 * time.Second
	// dialTimeout bounds one connection attempt to control. Without it a stop
	// during an unreachable-control window sits in the OS connect timeout
	// (minutes), longer than systemd's stop window -- the daemon gets SIGKILLed.
	dialTimeout = 10 * time.Second
)

// resolveConfigFile picks the node's own file when it exists, else the
// pre-split shared one when THAT exists, else the intended path -- so an
// upgrade never strands a running node and a missing-file error names the
// location the operator is meant to create.
func resolveConfigFile(path string) string {
	if _, err := os.Stat(path); err == nil {
		return path
	}
	if _, err := os.Stat(legacyConfigFile); err == nil {
		slog.Warn("Reading the pre-split shared config; move it to the node's own file", "file", legacyConfigFile, "expected", path)
		return legacyConfigFile
	}
	return path
}

// sigCh receives the termination signal; the dial loop closes the live control
// connection on it so ServeAgent unblocks instead of ignoring SIGTERM until
// systemd's stop timeout.
var sigCh = make(chan os.Signal, 1)

// Serve runs the node daemon from its config file until a termination signal.
// It is the machine half only: a Machine doing what control tells it to, with
// no orchestration of its own.
func Serve(configPath, version string) error {
	conf, err := LoadConfig(resolveConfigFile(configPath))
	if err != nil {
		return err
	}
	if err := conf.Validate(); err != nil {
		return err
	}
	if err := preflight.Check(conf.AppsDir); err != nil {
		return err
	}
	// The container binary is bind-mounted into every app; a missing source
	// would have podman conjure an empty directory in its place and every app
	// fail at PID 1, far from the cause. Refuse to start instead.
	if _, err := os.Stat(workspace.HostBinFile); err != nil {
		return fmt.Errorf("%s is missing (ships with the hostit-node package): %w", workspace.HostBinFile, err)
	}
	// The node's own SQLite: a MIRROR of the app/snapshot rows this node
	// hosts, pushed by control (never control's registry, even colocated).
	s, err := store.NewStore(filepath.Join(conf.DataDir, "node.db"))
	if err != nil {
		return err
	}
	defer s.Close()
	Version = version
	machine := NewMachine(conf, s, NewSystemServices(run.New(), conf.NodeID, conf.AppsBindAddress, conf.AppsAllowedAddresses))

	// The node-local startup, same order as the fused daemon's. Limits are
	// not applied here: control re-asserts every app's memory and disk limit
	// in its rejoin handshake right after the first dial-in.
	machine.EnableDiskBudgets()
	if err := machine.MountRawAppsView(filepath.Join(filepath.Dir(conf.SocketFile), "apps-raw")); err != nil {
		return err
	}
	done := make(chan struct{})
	defer close(done)
	// Migrate app users still on the old login-shell path. Best effort and
	// loud: the old path stays shipped this release, so a failed sweep strands
	// nobody -- it just postpones dropping the old file.
	go func() {
		changed, err := unixuser.New(userShellFile, AppsGroup).SweepShellPaths(legacyUserShellFile)
		if err != nil {
			slog.Warn("Login-shell migration incomplete; the old path keeps working", "error", err)
		}
		if len(changed) > 0 {
			slog.Info("Migrated app users to the new login shell", "users", changed, "shell", userShellFile)
		}
	}()
	go func() {
		if err := machine.EnsureWorkspaceBase(); err != nil {
			slog.Warn("Cannot prepare workspace base rootfs; the first app deploy will retry", "error", err)
		}
		machine.PruneOldWorkspaceImages()
	}()
	// Anything that acts on "the set of apps this node hosts" waits for the
	// first registry mirror: against an unsynced (possibly empty) mirror,
	// ReconcileOrphans would tear down every app on the machine.
	go func() {
		select {
		case <-machine.Synced():
		case <-done:
			return
		}
		if removed := machine.Reconcile(nil); len(removed) > 0 {
			slog.Info("Cleaned up leftovers of apps that no longer exist", "apps", removed)
		}
		restarted, err := machine.RestartStaleAgents(version)
		if err != nil {
			slog.Warn("Cannot restart apps after upgrade", "error", err)
		} else if len(restarted) > 0 {
			slog.Info("Restarted apps to pick up the new version", "apps", restarted, "version", version)
		}
	}()
	go machine.DiskUsageLoop(done)
	machine.SeedStates()
	go machine.StateLoop(done)
	go machine.QgroupSweepLoop(6*time.Hour, done)

	// Dial control forever; on death, redial with backoff. Control runs its
	// rejoin handshake on every register.
	//
	// Same host means the member socket: no certificate, no CA, nothing minted
	// in advance. Another machine means mTLS.
	sameHost := cluster.IsSocketAddr(conf.ControlURL)
	var tlsConf *tls.Config
	if !sameHost {
		tlsConf, err = nodelink.DialCreds(conf)
		if err != nil {
			return err
		}
	}
	link := nodelink.NewControlLink()
	machine.SetControlSink(link)
	// The app socket: served HERE on every host, relayed to control over the
	// link. Started before the dial loop so the file exists the moment a
	// container starts; requests before the first connection answer 502, which
	// the in-container CLI can retry, unlike a missing socket file.
	appSocket, err := ServeAppSocket(conf.SocketFile, s, link)
	if err != nil {
		return err
	}
	defer appSocket.Close()
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
	slog.Info("Node ready; dialing control", "addr", conf.ControlURL, "id", conf.NodeID)
	for {
		select {
		case <-stopping:
			return nil
		default:
		}
		conn, err := dialControl(conf.ControlURL, sameHost, tlsConf)
		if err != nil {
			time.Sleep(redialDelay)
			continue
		}
		connMu.Lock()
		current = conn
		connMu.Unlock()
		slog.Info("Connected to control", "addr", conf.ControlURL)
		machine.ResetSyncSeq() // control's sequence restarts with its process
		if err := nodelink.ServeAgent(conn, conf.NodeID, machine, link.SetClient); err != nil {
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

// dialControl opens one connection to control: the member socket when they
// share a host, mTLS otherwise. Same-host needs no credentials at all -- the
// socket is only openable by control's own user or root, and the kernel tells
// control which it is.
func dialControl(addr string, sameHost bool, tlsConf *tls.Config) (net.Conn, error) {
	if sameHost {
		return cluster.DialSocket(cluster.SocketPath(addr))
	}
	return tls.DialWithDialer(&net.Dialer{Timeout: dialTimeout}, "tcp", addr, tlsConf)
}
