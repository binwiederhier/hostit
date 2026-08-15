// Package app orchestrates the lifecycle of hostit apps: the per-app Unix user, SSH
// keys, port allocation, home skeleton and container. Node-local system interaction
// is delegated to focused service packages (btrfs, systemd, container, unixuser, ssh,
// firewall), each injected as an interface so it can be faked in tests. Keeping these
// services separable is also the seam a future control/app-node split would use.
package app

import (
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"sync"
	"time"

	"heckel.io/hostit/btrfs"
	"heckel.io/hostit/config"
	"heckel.io/hostit/container"
	"heckel.io/hostit/firewall"
	"heckel.io/hostit/homefs"
	"heckel.io/hostit/run"
	"heckel.io/hostit/snapshot"
	"heckel.io/hostit/ssh"
	"heckel.io/hostit/store"
	"heckel.io/hostit/systemd"
	"heckel.io/hostit/unixuser"
	"heckel.io/hostit/workspace"
)

const (
	// userShellFile is the login shell for app users; it execs the SSH session
	// into the app container (see cmd/shell.go). Also used by exec.go's terminal.
	userShellFile = "/usr/bin/hostit-shell"
	// AppsGroup owns the sudoers grant that lets app users enter their own
	// container (and nothing else); see /etc/sudoers.d/hostit
	AppsGroup = "hostit-apps"
)

const (
	// settingAgentVersion records the hostit version the running agents were
	// started from, so an upgrade knows whose behaviour is stale
	settingAgentVersion = "agent_version"

	// AppNamePattern is what an app may be called: safe as a Unix username and as
	// a DNS label. Exported because the enter helper applies the same rule without
	// importing this package's machinery.
	AppNamePattern = `^[a-z]([a-z0-9-]{0,30}[a-z0-9])?$`

	// teardownWait bounds how long a create waits for a same-name background
	// teardown (delete-then-recreate); teardownPoll is its check interval, and
	// budgetDestroyWait/Poll pace the teardown's gentle qgroup destroy.
	teardownWait      = 30 * time.Second
	teardownPoll      = 200 * time.Millisecond
	budgetDestroyWait = 60 * time.Second
	budgetDestroyPoll = 5 * time.Second
)

var (
	// Version is the hostit build this daemon is, set once at startup. It is part
	// of each container's identity: the binary is bind-mounted into containers as
	// a file, so replacing it on the host leaves running containers on the old
	// inode until they are recreated.
	Version = "dev"

	// ErrAppExists is returned when the app name or a Unix user with that name already exists
	ErrAppExists = errors.New("app or user already exists")
	// ErrNoPortsAvailable is returned when the configured port range is exhausted
	ErrNoPortsAvailable = errors.New("no free ports in configured range")
	// ErrInvalid wraps all request validation errors (bad names, bad keys)
	ErrInvalid = errors.New("invalid request")
	// ErrLimitReached is returned when a user hit one of their resource limits
	ErrLimitReached = errors.New("limit reached")

	// appNameRegex limits names to things that are safe as Unix usernames and DNS labels
	appNameRegex = regexp.MustCompile(AppNamePattern)

	// reservedNames are blocked in addition to existing Unix users; mostly hostnames
	// with special meaning and common system accounts that may not exist yet
	reservedNames = []string{
		"hostit", "api", "www", "mail", "smtp", "imap", "ftp", "ssh", "git", "admin",
		"root", "daemon", "bin", "sys", "sync", "proxy", "backup", "nobody", "sshd",
		"postgres", "mysql", "redis", "ubuntu", "debian",
	}
)

// Services bundles the node-local system services the Manager depends on. Each is
// an interface so a test can substitute a fake for any single one; production builds
// the real, root-requiring implementations with NewSystemServices.
type Services struct {
	Btrfs     btrfs.Interface
	Systemd   systemd.Interface
	Container container.Interface
	User      unixuser.Interface
	SSH       ssh.Interface
	Firewall  firewall.Interface
	Runner    run.Runner
}

// NewSystemServices builds the real services the daemon runs with. btrfs, systemd
// and container shell out through the shared runner; unixuser, ssh and firewall
// touch the host directly (useradd, authorized_keys, nft) and must run as root.
func NewSystemServices(runner run.Runner) *Services {
	return &Services{
		Btrfs:     btrfs.New(runner),
		Systemd:   systemd.New(runner),
		Container: container.New(runner),
		User:      unixuser.New(userShellFile, AppsGroup),
		SSH:       ssh.New(),
		Firewall:  firewall.New(),
		Runner:    runner,
	}
}

// CreateOptions carries everything CreateApp needs beyond the name: who owns the
// app, which keys may log in, and the container's memory cap
type CreateOptions struct {
	OwnerID     string   // Empty for apps created with the global admin token
	RequestKeys []string // App-specific keys from the request
	ProfileKeys []string // The owner's profile keys (apply to all their apps)
	MemoryMB    int      // Container memory limit; 0 means unlimited
	DiskMB      int      // Disk quota; 0 means unlimited. On btrfs a hard qgroup cap
}

// Manager creates and deletes apps and everything that belongs to them, and
// runs their containers as root with per-app uid mappings
type Manager struct {
	// node is where this manager's ORCHESTRATION sends machine work (provision,
	// deprovision): itself by default (single process), or a remote node agent
	// when control and node run as separate services (SetNodeAgent).
	node NodeAgent

	config    *config.Config
	store     *store.Store
	runner    run.Runner
	btrfs     btrfs.Interface
	systemd   systemd.Interface
	container container.Interface
	user      unixuser.Interface
	ssh       ssh.Interface
	firewall  firewall.Interface
	homefs    *homefs.Service
	snapshots *snapshot.Service
	workspace *workspace.Service
	// rawAppsDir, when set, is a non-recursive bind of AppsDir the daemon's file
	// I/O goes through. A running container's idmapped rootfs mount covers the
	// subvolume path in the host namespace, and root writing through that mapped
	// view fails (EOVERFLOW: root is not in the mapping); the raw bind excludes
	// those child mounts, so the daemon always sees the disk as it is. Empty
	// until serve establishes the bind (tests, dry runs): AppsDir is used as-is.
	rawAppsDir string

	// memoryMB and diskMB cache per-app limits, so redeploys and quota checks
	// keep them; the authoritative values come from the owner's limits
	memoryMB map[string]int
	diskMB   map[string]int
	// background tracks the manager's fire-and-forget goroutines -- delete
	// teardowns and the post-create start -- so tests (and a graceful shutdown,
	// if it ever wants to) can wait for them instead of racing their I/O.
	// tearingDown holds the app names with a teardown in flight, so a same-name
	// create can wait for the dying user instead of failing on the collision.
	background  sync.WaitGroup
	tearingDown map[string]bool
	// nextPort rotates allocation through the range (see allocatePort)
	nextPort int
	// reservedPorts holds ports handed out by allocatePort but not yet registered
	// in the store, so concurrent creates never share one (see allocatePort)
	reservedPorts map[int]bool

	// stateCache holds the last measured state of every app, so listing apps
	// answers from memory instead of waiting on podman
	stateCache      map[string]State
	stateFresh      time.Time
	stateRefreshing bool

	// appLocks serializes mutating lifecycle work per app (deploy, snapshot,
	// rollback, delete), so operations on one app's subvolume never interleave --
	// e.g. a rollback swapping the subvolume while a deploy writes into it.
	appLocks map[string]*sync.Mutex

	mu         sync.Mutex // Protects memoryMB, diskMB, reservedPorts
	stateMu    sync.Mutex // Protects stateCache, stateFresh, stateRefreshing
	execMu     sync.Mutex // Serializes /run commands; they are builds, and the box has one core
	appLocksMu sync.Mutex // Protects appLocks
}

// NewManager creates a Manager from its config, store and the node-local services
// (real ones from NewSystemServices in production, fakes in tests).
func NewManager(conf *config.Config, s *store.Store, svc *Services) *Manager {
	m := &Manager{
		config:        conf,
		store:         s,
		runner:        svc.Runner,
		btrfs:         svc.Btrfs,
		systemd:       svc.Systemd,
		container:     svc.Container,
		user:          svc.User,
		ssh:           svc.SSH,
		firewall:      svc.Firewall,
		homefs:        homefs.New(ErrInvalid),
		memoryMB:      make(map[string]int),
		diskMB:        make(map[string]int),
		reservedPorts: make(map[int]bool),
		tearingDown:   make(map[string]bool),
		stateCache:    make(map[string]State),
		appLocks:      make(map[string]*sync.Mutex),
	}
	m.node = m // single process: orchestration and machine work are the same
	// The snapshot Service reuses the Manager's node-local services and store, and
	// calls back into it through snapshotHost for the app-lifecycle operations and
	// id-keyed lookups a snapshot or rollback needs.
	m.snapshots = snapshot.New(m.btrfs, m.systemd, m.container, s, snapshotHost{m})
	// The workspace Service owns the shared workspace image lifecycle (build,
	// per-app pinned tags, prune) and the base and app subvolumes the containers
	// run; it needs no callbacks into the Manager.
	m.workspace = workspace.New(m.container, s, m.btrfs, m.runner, conf.DataDir, conf.AppsDir)
	return m
}

// lockApp acquires the per-app lifecycle lock and returns its unlock func, so
// deploy/snapshot/rollback/delete on one app run one at a time and never race on
// its subvolume. It is NOT reentrant: a method already holding the lock must
// call the unlocked helpers (up, takeSnapshot), not the public locking ones.
func (m *Manager) lockApp(name string) func() {
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

// CreateApp registers a new app: it allocates a port, creates the Unix user with
// SSH access and writes the home skeleton. Its authorized_keys are the union
// of the request keys and the owner's profile keys; an app with neither is fine,
// since apps are driven through the API and SSH is opt-in.
func (m *Manager) CreateApp(name string, opts *CreateOptions) (*store.App, error) {
	return m.create(name, opts, "")
}

// Fork duplicates an existing app into a new one: the new app's subvolume is
// seeded from a writable btrfs snapshot of the source's (its files, config, data
// AND installed packages) rather than the demo skeleton. snapshotID picks a
// specific snapshot to seed from; empty means the source's current subvolume.
// Snapshots are whole-app subvolumes too, so either seed carries everything
// (old home-shaped snapshots were purged by the unification migration). The
// fork gets its own port, Unix user, subdomain and container. Requires btrfs
// (the snapshot primitive it relies on).
func (m *Manager) Fork(source, newName, snapshotID string, opts *CreateOptions) (*store.App, error) {
	if _, err := m.store.App(source); err != nil {
		return nil, err
	}
	// Seed from a specific snapshot if asked, else from the source's current
	// subvolume.
	seedPath := m.appSubvolume(source)
	if snapshotID != "" {
		snap, err := m.store.Snapshot(snapshotID)
		if err != nil {
			return nil, err
		}
		if snap.AppName != source {
			return nil, store.ErrSnapshotNotFound
		}
		seedPath = m.snapshotPath(source, snapshotID)
	}
	// Lock the source so its subvolume/snapshot is not rolled back or deleted
	// mid-copy; the new app's own deploy runs under its own lock in the background.
	defer m.lockApp(source)()
	return m.create(newName, opts, seedPath)
}

// create registers a new app. With seedPath == "" it writes the demo app's
// skeleton; with a seedPath (a subvolume: an app's subvolume or a whole-app
// snapshot) it forks -- seeding the app subvolume from a writable snapshot of
// that path (files AND installed packages in one CoW copy) and skipping the
// skeleton. Either way it allocates a port, creates the Unix user with SSH
// access, registers the app and starts it in the background. The
// authorized_keys are the union of the request keys and the owner's profile
// keys; an app with neither is fine, since apps are driven through the API and
// SSH is opt-in.
func (m *Manager) create(name string, opts *CreateOptions, seedPath string) (*store.App, error) {
	if opts == nil {
		opts = &CreateOptions{}
	}
	if err := m.validateName(name); err != nil {
		return nil, err
	}
	if err := validateKeys(opts.RequestKeys); err != nil {
		return nil, err
	}
	port, err := m.allocatePort()
	if err != nil {
		return nil, err
	}
	// The reservation has done its job once create returns: on success AddApp has
	// registered the port (UsedPorts covers it from then on), on failure it is
	// genuinely free again.
	defer m.releasePort(port)

	appKeys := opts.RequestKeys
	forking := seedPath != ""

	// Mint the app's stable id up front: the app subvolume (and its snapshots) are
	// keyed on the id, not the name, so a later rename never moves them. The id is
	// on the App struct that gets inserted below.
	app := &store.App{ID: store.NewAppID(), Name: name, Port: port, Host: store.HostLocal, OwnerID: opts.OwnerID, ImageTag: workspace.ImageTag(), UID: m.uidFor(port)}

	// Build the app on this machine (subvolume, user, keys, skeleton) -- the
	// node-local half; everything after this is registry bookkeeping.
	m.recordDiskLimit(name, opts.DiskMB)
	spec := &ProvisionSpec{
		ID:       app.ID,
		Name:     name,
		Port:     port,
		SSHKeys:  append(append([]string{}, appKeys...), opts.ProfileKeys...),
		SeedPath: seedPath,
		URL:      m.URL(&store.App{Name: name, Port: port}),
		DiskMB:   m.DiskLimit(name),
	}
	if err := m.node.Provision(spec); err != nil {
		return nil, err
	}

	// Register the app; roll back the user if this fails. The app was built above
	// (id minted so the subvolume could be created id-named); it is pinned to the
	// workspace image it is built with, so a later Containerfile change (e.g. adding
	// a runtime) only affects new apps, never this one.
	if err := m.store.AddApp(app); err != nil {
		m.provisionRollback(spec)
		return nil, err
	}
	if err := m.store.SetAppKeys(name, appKeys); err != nil {
		m.provisionRollback(spec)
		_ = m.store.RemoveApp(name)
		return nil, err
	}
	m.SetMemoryLimit(name, opts.MemoryMB)
	// Apply the disk budget now (create and fork alike), so a new app is capped
	// from the start rather than only after the next daemon restart: record the
	// limit, create the app's qgroup, join the subvolume, cap it. Failure only
	// warns -- the app works uncapped and the next startup's ensure retries.
	// Provision created and capped the budget; SetDiskLimit re-asserts it
	// ensure-style (idempotent) now that the registry row exists.
	m.node.SetDiskLimit(name, m.DiskLimit(name))
	m.ReconcilePortRules()

	m.startInBackground(name, forking)
	return app, nil
}

// SetNodeAgent points this manager's orchestration at a (remote) node agent:
// what control calls once a hostit-node dials in. Default is the manager
// itself (single process).
func (m *Manager) SetNodeAgent(node NodeAgent) {
	m.node = node
}

// WaitBackground blocks until the manager's fire-and-forget goroutines (post-
// create starts, delete teardowns) have finished: what a graceful shutdown or
// a test harness waits on before pulling the store away from under them.
func (m *Manager) WaitBackground() {
	m.background.Wait()
}

// DeleteApp removes the app from the registry and answers at once; the host
// teardown -- container stop, subvolume and snapshot deletes (with the qgroup
// sync ladder behind them), userdel -- takes seconds on a loaded box and
// continues in the background. ReconcileOrphans converges any teardown the
// daemon dies in the middle of, so the background path needs no new safety
// net; the app's port (and with it its uid block) stays reserved until the
// teardown finishes, so a new app cannot collide with the dying user.
func (m *Manager) DeleteApp(name string) error {
	unlock := m.lockApp(name) // serialize against a concurrent deploy/snapshot/rollback
	a, err := m.store.App(name)
	if err != nil {
		unlock()
		return err
	}
	// Everything the teardown needs is captured NOW: once the rows are gone,
	// name-keyed lookups (paths, ids, snapshots) resolve nothing.
	uid, uidErr := m.user.LookupUID(name)
	spec := &DeprovisionSpec{
		Name:      name,
		Port:      a.Port,
		UID:       uid,
		UIDKnown:  uidErr == nil,
		Unit:      m.unitName(name),
		Container: m.containerName(name),
		Subvol:    m.appSubvolumeByID(a.ID),
		SnapsRoot: m.snapshotsRoot(name),
	}
	if snaps, err := m.store.Snapshots(name); err == nil {
		for _, snap := range snaps {
			spec.SnapPaths = append(spec.SnapPaths, m.snapshotPath(name, snap.ID))
		}
	}
	if err := m.store.RemoveApp(name); err != nil {
		unlock()
		return err
	}
	m.mu.Lock()
	m.reservedPorts[a.Port] = true
	m.tearingDown[name] = true
	m.mu.Unlock()
	m.ReconcilePortRules()
	m.background.Add(1)
	go func() {
		defer m.background.Done()
		defer unlock()
		defer m.releasePort(a.Port)
		defer func() {
			m.mu.Lock()
			delete(m.tearingDown, name)
			m.mu.Unlock()
		}()
		m.node.Deprovision(spec)
	}()
	return nil
}

// ReconcilePortRules rebuilds the per-app loopback firewall rules from the
// registry; failures are logged, not fatal (nft may be absent in dev setups)
func (m *Manager) ReconcilePortRules() {
	apps, err := m.store.Apps()
	if err != nil {
		slog.Warn("Cannot list apps for port rules", "error", err)
		return
	}
	rules := make([]firewall.Rule, 0, len(apps))
	for _, a := range apps {
		uid, err := m.user.LookupUID(a.Name)
		if err != nil {
			slog.Warn("Cannot look up uid for port rule", "app", a.Name, "error", err)
			continue
		}
		rules = append(rules, firewall.Rule{Port: a.Port, UID: uid})
	}
	if err := m.firewall.Apply(rules); err != nil {
		slog.Warn("Cannot apply port rules", "error", err)
	}
}

// SetKeys replaces the app-specific SSH keys; the app's authorized_keys become
// those plus the owner's profile keys
func (m *Manager) SetKeys(name string, appKeys, profileKeys []string) error {
	app, err := m.store.App(name)
	if err != nil {
		return err
	}
	if err := validateKeys(appKeys); err != nil {
		return err
	}
	if err := m.store.SetAppKeys(app.Name, appKeys); err != nil {
		return err
	}
	return m.writeKeys(app.Name, appKeys, profileKeys)
}

// SyncKeys rewrites an app's authorized_keys from its stored app keys plus the
// given profile keys; used when a user adds or removes a profile key
func (m *Manager) SyncKeys(name string, profileKeys []string) error {
	appKeys, err := m.store.AppKeys(name)
	if err != nil {
		return err
	}
	return m.writeKeys(name, appKeys, profileKeys)
}

func (m *Manager) writeKeys(name string, appKeys, profileKeys []string) error {
	keys := append(append([]string{}, appKeys...), profileKeys...)
	return m.writeKeysIn(m.appFiles(name), name, keys)
}

// writeKeysIn writes an app's authorized_keys through its chained files root:
// the files dir sits inside the tenant-owned subvolume, so a symlink planted at
// home (or home/app) must not redirect root's key write out of it.
func (m *Manager) writeKeysIn(files homefs.Dir, name string, keys []string) error {
	root, err := m.homefs.OpenRoot(files)
	if err != nil {
		return err
	}
	defer root.Close()
	return m.ssh.WriteAuthorizedKeys(root, name, keys)
}

// App returns a registered app by name
func (m *Manager) App(name string) (*store.App, error) {
	return m.store.App(name)
}

// Apps returns all registered apps
func (m *Manager) Apps() ([]*store.App, error) {
	return m.store.Apps()
}

// URL returns the public URL of an app
func (m *Manager) URL(app *store.App) string {
	scheme := "https"
	if m.config.TLS == config.TLSOff {
		scheme = "http"
	}
	return fmt.Sprintf("%s://%s.%s", scheme, app.Name, m.config.BaseDomain)
}

// Store exposes the underlying registry, mainly for the server and tests
func (m *Manager) Store() *store.Store {
	return m.store
}

func (m *Manager) validateName(name string) error {
	if !appNameRegex.MatchString(name) {
		return fmt.Errorf("%w: invalid app name %q, must match %s", ErrInvalid, name, appNameRegex.String())
	}
	if slices.Contains(reservedNames, name) {
		return fmt.Errorf("%w: app name %q is reserved", ErrInvalid, name)
	}
	if _, err := m.store.App(name); err == nil {
		return ErrAppExists
	} else if !errors.Is(err, store.ErrAppNotFound) {
		return err
	}
	if m.user.Exists(name) {
		// The unix user of a just-deleted same-name app dies in the background;
		// waiting the teardown out turns an instant delete-then-recreate from a
		// spurious "already exists" into a create that simply takes a moment.
		if !m.waitForTeardown(name) {
			return ErrAppExists
		}
	}
	return nil
}

// waitForTeardown waits (bounded) for an in-flight background teardown of this
// name to release the unix user; false when none is running or it ran out of
// patience -- the name really is taken then.
func (m *Manager) waitForTeardown(name string) bool {
	deadline := time.Now().Add(teardownWait)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		inFlight := m.tearingDown[name]
		m.mu.Unlock()
		if !inFlight {
			return !m.user.Exists(name)
		}
		time.Sleep(teardownPoll)
	}
	return false
}

// allocatePort returns the lowest free port in the configured range
func (m *Manager) allocatePort() (int, error) {
	used, err := m.store.UsedPorts()
	if err != nil {
		return 0, err
	}
	// Reserve in memory until the app is registered (or the create fails): the
	// port only shows up in UsedPorts at AddApp time, seconds after allocation,
	// and two concurrent creates reading the store in that window were both handed
	// the same port -- the second useradd then failed on the taken uid.
	m.mu.Lock()
	defer m.mu.Unlock()
	// Rotate through the range instead of always taking the lowest free port: a
	// just-deleted app's port maps to a uid whose budget qgroup may still hold
	// the dying subvolume's uncommitted bytes (the teardown destroys it gently,
	// without a filesystem sync), and immediate reuse started brand-new apps
	// over their disk cap -- EDQUOT on the container's first mkdir. Scanning
	// upward from the last grant leaves freed uids fallow until the wrap-around;
	// the qgroup sweep collects their leftovers long before that.
	span := m.config.PortMax - m.config.PortMin + 1
	if m.nextPort < m.config.PortMin || m.nextPort > m.config.PortMax {
		m.nextPort = m.config.PortMin
	}
	for i := 0; i < span; i++ {
		port := m.config.PortMin + (m.nextPort-m.config.PortMin+i)%span
		if !slices.Contains(used, port) && !m.reservedPorts[port] {
			m.reservedPorts[port] = true
			m.nextPort = port + 1
			return port, nil
		}
	}
	return 0, ErrNoPortsAvailable
}

// releasePort ends a port's in-memory reservation: after AddApp registered it
// (UsedPorts covers it from then on), or when the create failed. Leaving it
// reserved would leak the port until the next daemon restart.
func (m *Manager) releasePort(port int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.reservedPorts, port)
}

// BackfillUIDs records the uid block base for rows created before the uid
// column existed (uid 0); rows that already have one are left alone. Run once
// at startup, like the other backfills.
func (m *Manager) BackfillUIDs() {
	apps, err := m.store.Apps()
	if err != nil {
		return
	}
	for _, a := range apps {
		if a.UID != 0 {
			continue
		}
		if err := m.store.SetAppUID(a.Name, m.uidFor(a.Port)); err != nil {
			slog.Warn("Cannot backfill app uid", "app", a.Name, "error", err)
		}
	}
}

// uidFor is an app's base uid: a contiguous workspace.UIDBlockSize-wide block, one per
// app, spaced by port so blocks never overlap. Container uid 0 maps here.
func (m *Manager) uidFor(port int) int {
	return workspace.UIDBlockStart + (port-m.config.PortMin)*workspace.UIDBlockSize
}

// lookupIDs returns the app's contiguous id block: its uid/gid (which become
// container root) and the block size that runs up from there.
func (m *Manager) lookupIDs(username string) (workspace.IDs, error) {
	uid, gid, err := m.user.LookupIDs(username)
	if err != nil {
		return workspace.IDs{}, err
	}
	return workspace.IDs{UID: uid, GID: gid, Count: workspace.UIDBlockSize}, nil
}
