// Package node is hostit's Machine half: the Machine type that owns THIS
// host's app containers, unix users, btrfs subvolumes, firewall rules and
// state measurement (implementing api.NodeAgent), the serve loop that
// dials control and answers its RPC, and the mTLS/yamux transport underneath.
// The control plane lives in package app/server; this package cannot import
// it -- a node only does what control tells it to.
package node

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"heckel.io/hostit/app"
	"heckel.io/hostit/homefs"
	"heckel.io/hostit/node/api"
	"heckel.io/hostit/node/screenshot"
	"heckel.io/hostit/snapshot"
	"heckel.io/hostit/store"
	"heckel.io/hostit/system/btrfs"
	"heckel.io/hostit/system/nftables"
	"heckel.io/hostit/system/podman"
	"heckel.io/hostit/system/run"
	"heckel.io/hostit/system/ssh"
	"heckel.io/hostit/system/systemd"
	"heckel.io/hostit/system/unixuser"
	"heckel.io/hostit/workspace"
)

var (
	// Version is the hostit build this daemon is, set once at startup. It is part
	// of each container's identity: the binary is bind-mounted into containers as
	// a file, so replacing it on the host leaves running containers on the old
	// inode until they are recreated.
	Version = "dev"
)

const (
	// userShellFile is the login shell for app users; it execs the SSH session
	// into the app container (see cmd/shell.go). Also used by exec.go's terminal.
	// It lives off $PATH with the rest of the container-facing pieces.
	userShellFile = "/usr/lib/hostit/bin/hostit-shell"
	// AppsGroup owns the sudoers grant that lets app users enter their own
	// container (and nothing else); see /etc/sudoers.d/hostit
	AppsGroup = "hostit-apps"
)

const (
	// teardownWait bounds how long a provision waits for a same-name teardown
	// still running here (delete-then-recreate); teardownPoll is its interval.
	teardownWait = 30 * time.Second
	teardownPoll = 200 * time.Millisecond

	// settingAgentVersion records the hostit version the running agents were
	// started from, so an upgrade knows whose behaviour is stale
	settingAgentVersion = "agent_version"

	// budgetDestroyWait/Poll pace the teardown's gentle qgroup destroy.
	budgetDestroyWait = 60 * time.Second
	budgetDestroyPoll = 5 * time.Second
)

const (
	// configFile is the app's own configuration, written by whoever builds it
	configFile = app.ConfigFile
	// appLogFile is where the agent records an app's output, below the app's home
	appLogFile = app.LogFile
	// appStateFile is where the agent records the run: process state; maxStateRead
	// caps that tiny file when the daemon reads it
	appStateFile = app.StateFile
	maxStateRead = 64
	// maxLogRead caps how much of that log a request reads; the agent rotates it
	// at 10 MB, and a reader only ever wants the tail
	maxLogRead = 16 * 1024 * 1024
	// maxConfigSize caps hostit.yml when it is read on a request path
	maxConfigSize = 64 * 1024
)

// Machine is the machine half of the platform: the services and state that act
// on THIS host's apps (subvolumes, unix users, containers, port rules, files,
// state measurement). It implements the api.NodeAgent verbs and does only
// what control tells it to. In a split deployment Serve runs a bare Machine;
// in the fused daemon the control.Manager embeds one, so the control half's
// orchestration calls the same code through promotion.
type Machine struct {
	config *Config
	// store is this half's view of the registry: the full registry in a single
	// process (and on control), the pushed mirror on a split node. The control
	// half reads it through promotion until the split gives it its own.
	store     *store.Store
	runner    run.Runner
	btrfs     btrfs.Interface
	systemd   systemd.Interface
	container podman.Interface
	user      unixuser.Interface
	ssh       ssh.Interface
	firewall  nftables.Interface
	homefs    *homefs.Service
	snapshots *snapshot.Service
	workspace *workspace.Service
	// shots renders dashboard previews: it runs the chrome container and the
	// per-shot egress firewall on this machine, behind the Screenshot verb.
	shots *screenshot.Engine
	// rawAppsDir, when set, is a non-recursive bind of AppsDir the daemon's file
	// I/O goes through. A running container's idmapped rootfs mount covers the
	// subvolume path in the host namespace, and root writing through that mapped
	// view fails (EOVERFLOW: root is not in the mapping); the raw bind excludes
	// those child mounts, so the daemon always sees the disk as it is. Empty
	// until serve establishes the bind (tests, dry runs): AppsDir is used as-is.
	rawAppsDir string

	// sink is the node's reverse channel to control for control-plane data the
	// node originates (usage, poweroffs, snapshot records); nil in a single
	// process (SetControlSink).
	sink api.ControlSink
	// synced closes when the first registry mirror arrives (Sync); gates the
	// node's destructive startup work.
	synced     chan struct{}
	syncedOnce sync.Once
	// syncSeq is the last mirror sequence applied (see api.SyncState.Seq);
	// reset per control connection by ResetSyncSeq.
	syncSeq int64
	// onStateChanged, when set, tells the OTHER half that an app's state just
	// moved (deploy, power, restart), so the control plane can invalidate its
	// cached entry instead of serving the old state for a whole TTL. Wired by
	// NewManager; a one-way, in-process hook.
	onStateChanged func(name string)

	// memoryMB and diskMB cache per-app limits, so redeploys and quota checks
	// keep them; the authoritative values come from the owner's limits
	memoryMB map[string]int
	diskMB   map[string]int
	cpuMilli map[string]int
	// background tracks the manager's fire-and-forget goroutines -- delete
	// teardowns and the post-create start -- so tests (and a graceful shutdown,
	// if it ever wants to) can wait for them instead of racing their I/O.
	// tearingDown holds the app names with a teardown in flight, so a same-name
	// create can wait for the dying user instead of failing on the collision.
	background  sync.WaitGroup
	tearingDown map[string]bool

	// stateCache holds the last measured state of every app, so listing apps
	// answers from memory instead of waiting on podman
	stateCache      map[string]api.State
	stateFresh      time.Time
	stateRefreshing bool

	// orphansLastPass/orphansThisPass hold the resources seen orphaned by the
	// previous and current sweep; only something orphaned twice running is
	// removed (see confirmOrphan).
	orphansLastPass map[string]bool
	orphansThisPass map[string]bool

	// appLocks serializes mutating lifecycle work per app (deploy, snapshot,
	// rollback, delete), so operations on one app's subvolume never interleave --
	// e.g. a rollback swapping the subvolume while a deploy writes into it.
	appLocks map[string]*sync.Mutex

	// mu also protects the Manager's reservedPorts for now: one lock spans the
	// halves until the control split gives the control side its own.
	mu         sync.Mutex // Protects memoryMB, diskMB, tearingDown, reservedPorts
	stateMu    sync.Mutex // Protects stateCache, stateFresh, stateRefreshing
	execMu     sync.Mutex // Serializes /run commands; they are builds, and the box has one core
	appLocksMu sync.Mutex // Protects appLocks
	syncMu     sync.Mutex // Protects syncSeq
	orphanMu   sync.Mutex // Protects orphansLastPass, orphansThisPass

	// exportSem bounds concurrent workspace exports: each takes a read-only btrfs
	// snapshot that pins the workspace's old blocks until the stream ends, so an
	// unbounded count is a shared-node disk-exhaustion vector.
	exportSem chan struct{}
}

// NewMachine creates the Machine half from its config, the store view (the
// full registry in a single process, the pushed mirror on a split node) and
// the node-local services (real ones from NewSystemServices in production,
// fakes in tests).
func NewMachine(conf *Config, s *store.Store, svc *Services) *Machine {
	m := &Machine{
		config:          conf,
		store:           s,
		runner:          svc.Runner,
		btrfs:           svc.Btrfs,
		systemd:         svc.Systemd,
		container:       svc.Container,
		user:            svc.User,
		ssh:             svc.SSH,
		firewall:        svc.Firewall,
		homefs:          homefs.New(api.ErrInvalid),
		memoryMB:        make(map[string]int),
		cpuMilli:        make(map[string]int),
		synced:          make(chan struct{}),
		diskMB:          make(map[string]int),
		tearingDown:     make(map[string]bool),
		stateCache:      make(map[string]api.State),
		appLocks:        make(map[string]*sync.Mutex),
		orphansLastPass: make(map[string]bool),
		orphansThisPass: make(map[string]bool),
		exportSem:       make(chan struct{}, maxConcurrentExports),
	}
	// The snapshot Service reuses the Machine's node-local services and store, and
	// calls back into it through snapshotHost for the app-lifecycle operations and
	// id-keyed lookups a snapshot or rollback needs.
	m.snapshots = snapshot.New(m.btrfs, m.systemd, m.container, s, snapshotHost{m})
	// The workspace Service owns the shared workspace image lifecycle (build,
	// per-app pinned tags, prune) and the base and app subvolumes the containers
	// run; it needs no callbacks into the Machine.
	m.workspace = workspace.New(m.container, s, m.btrfs, m.runner, conf.DataDir, conf.AppsDir)
	// The screenshot Engine renders dashboard previews on this machine: it runs
	// the sandboxed chrome container and the per-shot egress firewall, and keeps
	// its per-shot scratch under the data dir. It pulls the chrome image lazily
	// on the first shot, so a node that is never asked for a preview never does.
	m.shots = screenshot.NewEngine(m.runner, filepath.Join(conf.DataDir, "previews"))
	return m
}

// OnStateChanged wires the other half's cache-invalidation hook (see the
// field's comment); the control plane sets it at construction.
func (m *Machine) OnStateChanged(fn func(name string)) {
	m.onStateChanged = fn
}

// Services bundles the node-local system services the Manager depends on. Each is
// an interface so a test can substitute a fake for any single one; production builds
// the real, root-requiring implementations with NewSystemServices.
type Services struct {
	Btrfs     btrfs.Interface
	Systemd   systemd.Interface
	Container podman.Interface
	User      unixuser.Interface
	SSH       ssh.Interface
	Firewall  nftables.Interface
	Runner    run.Runner
}

// NewSystemServices builds the real services the daemon runs with. btrfs, systemd
// and container shell out through the shared runner; unixuser, ssh and firewall
// touch the host directly (useradd, authorized_keys, nft) and must run as root.
func NewSystemServices(runner run.Runner, nodeID, bindAddr string, allowFrom []string) *Services {
	return &Services{
		Btrfs:     btrfs.New(runner),
		Systemd:   systemd.New(runner),
		Container: podman.New(runner),
		User:      unixuser.New(userShellFile, AppsGroup),
		SSH:       ssh.New(),
		Firewall:  nftables.New(FirewallTable(nodeID), bindAddr, allowFrom),
		Runner:    runner,
	}
}

// FirewallTable names the node's nftables table: the historical "hostit" for
// the local node, "hostit_<id>" otherwise (two colocated nodes must not share
// a table -- each reconcile replaces it wholesale).
func FirewallTable(nodeID string) string {
	if nodeID == "" || nodeID == store.HostLocal {
		return "hostit"
	}
	return "hostit_" + strings.ReplaceAll(nodeID, "-", "_")
}

// LockApp acquires the per-app lifecycle lock and returns its unlock func, so
// deploy/snapshot/rollback/delete on one app run one at a time and never race on
// its subvolume. It is NOT reentrant: a method already holding the lock must
// call the unlocked helpers (up, takeSnapshot), not the public locking ones.
func (m *Machine) LockApp(name string) func() {
	m.appLocksMu.Lock()
	mu := m.appLocks[name]
	if mu == nil {
		mu = &sync.Mutex{}
		m.appLocks[name] = mu
	}
	m.appLocksMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// WaitBackground blocks until the manager's fire-and-forget goroutines (post-
// create starts, delete teardowns) have finished: what a graceful shutdown or
// a test harness waits on before pulling the store away from under them.
func (m *Machine) WaitBackground() {
	m.background.Wait()
}

// TrackedGo runs fn in a goroutine tracked by the background group, so
// WaitBackground covers it; how the control half's fire-and-forget work
// (post-create starts, delete teardowns) stays waitable without reaching
// into the Machine's WaitGroup.
func (m *Machine) TrackedGo(fn func()) {
	m.background.Add(1)
	go func() {
		defer m.background.Done()
		fn()
	}()
}

// SetTearingDown marks (or clears) an app name as having a teardown in
// flight, under the Machine's lock; IsTearingDown is the matching read. The
// control half's delete/create paths coordinate through these.
func (m *Machine) SetTearingDown(name string, tearing bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if tearing {
		m.tearingDown[name] = true
	} else {
		delete(m.tearingDown, name)
	}
}

func (m *Machine) IsTearingDown(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tearingDown[name]
}

// UserExists reports whether the app's unix login exists on this Machine;
// name validation and the teardown wait consult it.
func (m *Machine) UserExists(name string) bool {
	return m.user.Exists(name)
}

// Workspace exposes the Machine's workspace service (image and base subvolume
// lifecycle); Runner its command runner. Read-only wiring accessors.
func (m *Machine) Workspace() *workspace.Service { return m.workspace }
func (m *Machine) Runner() run.Runner            { return m.runner }

// MeasuredState peeks this Machine's own measurement cache; the control
// plane's cache is CachedStates, a different thing.
func (m *Machine) MeasuredState(name string) (api.State, bool) {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	s, ok := m.stateCache[name]
	return s, ok
}

// ReconcilePortRules rebuilds the per-app loopback firewall rules from the
// registry; failures are logged, not fatal (nft may be absent in dev setups)
func (m *Machine) ReconcilePortRules() {
	apps, err := m.store.Apps()
	if err != nil {
		slog.Warn("Cannot list apps for port rules", "error", err)
		return
	}
	rules := make([]nftables.Rule, 0, len(apps))
	for _, a := range apps {
		uid, err := m.user.LookupUID(a.Name)
		if err != nil {
			slog.Warn("Cannot look up uid for port rule", "app", a.Name, "error", err)
			continue
		}
		rules = append(rules, nftables.Rule{Port: a.Port, UID: uid})
	}
	if err := m.firewall.Apply(rules); err != nil {
		slog.Warn("Cannot apply port rules", "error", err)
	}
}

// SetKeys replaces the app-specific SSH keys; the app's authorized_keys become
// those plus the owner's profile keys
func (m *Machine) SetKeys(name string, appKeys, profileKeys []string) error {
	app, err := m.store.App(name)
	if err != nil {
		return err
	}
	if err := validateKeys(appKeys); err != nil {
		return err
	}
	return m.writeKeys(app.Name, appKeys, profileKeys)
}

func (m *Machine) writeKeys(name string, appKeys, profileKeys []string) error {
	keys := append(append([]string{}, appKeys...), profileKeys...)
	return m.writeKeysIn(m.AppFiles(name), name, keys)
}

// validateKeys ensures every entry is a parseable authorized_keys line, wrapping
// the ssh package's check in api.ErrInvalid so the server reports it as a bad request.
func validateKeys(keys []string) error {
	if err := ssh.ValidateKeys(keys); err != nil {
		return fmt.Errorf("%w: %s", api.ErrInvalid, err.Error())
	}
	return nil
}

// writeKeysIn writes an app's authorized_keys through its chained files root:
// the files dir sits inside the tenant-owned subvolume, so a symlink planted at
// home (or home/app) must not redirect root's key write out of it.
func (m *Machine) writeKeysIn(files homefs.Dir, name string, keys []string) error {
	root, err := m.homefs.OpenRoot(files)
	if err != nil {
		return err
	}
	defer root.Close()
	return m.ssh.WriteAuthorizedKeys(root, name, keys)
}

// uidFor is an app's base uid: a contiguous workspace.UIDBlockSize-wide block, one per
// app, spaced by port so blocks never overlap. Container uid 0 maps here.
func (m *Machine) uidFor(port int) int {
	return workspace.UIDFor(port)
}

// lookupIDs returns the app's contiguous id block: its uid/gid (which become
// container root) and the block size that runs up from there.
func (m *Machine) LookupIDs(username string) (workspace.IDs, error) {
	uid, gid, err := m.user.LookupIDs(username)
	if err != nil {
		return workspace.IDs{}, err
	}
	return workspace.IDs{UID: uid, GID: gid, Count: workspace.UIDBlockSize}, nil
}
