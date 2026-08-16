package control

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"heckel.io/hostit/config"
	"heckel.io/hostit/node"
	"heckel.io/hostit/nodeapi"
	"heckel.io/hostit/ssh"
	"heckel.io/hostit/store"
	"heckel.io/hostit/workspace"
)

const (
	// teardownWait bounds how long a create waits for a same-name background
	// teardown (delete-then-recreate); teardownPoll is its check interval.
	teardownWait = 30 * time.Second
	teardownPoll = 200 * time.Millisecond
)

var (
	// ErrNoPortsAvailable is returned when the configured port range is exhausted.
	// ErrInvalid wraps all request validation errors (bad names, bad keys);
	// ErrAppExists and ErrLimitReached live with the wire contract in nodeapi
	// and are re-exported in nodeagent.go.
	ErrNoPortsAvailable = errors.New("no free ports in configured range")

	// reservedNames are blocked in addition to existing Unix users; mostly hostnames
	// with special meaning and common system accounts that may not exist yet
	reservedNames = []string{
		"hostit", "api", "www", "mail", "smtp", "imap", "ftp", "ssh", "git", "admin",
		"root", "daemon", "bin", "sys", "sync", "proxy", "backup", "nobody", "sshd",
		"postgres", "mysql", "redis", "ubuntu", "debian",
	}
)

// CreateOptions carries everything CreateApp needs beyond the name: who owns the
// app, which keys may log in, and the container's memory cap
type CreateOptions struct {
	OwnerID     string   // Empty for apps created with the global admin token
	RequestKeys []string // App-specific keys from the request
	ProfileKeys []string // The owner's profile keys (apply to all their apps)
	MemoryMB    int      // Container memory limit; 0 means unlimited
	DiskMB      int      // Disk quota; 0 means unlimited. On btrfs a hard qgroup cap
	Host        string   // Target node; empty means placement picks one (a fork pins its source's)
}

// Manager creates and deletes apps and everything that belongs to them, and
// runs their containers as root with per-app uid mappings. It is (still) both
// halves of the platform in one type: the embedded node.Machine is the
// node-local half; the fields below are the control plane's (orchestration,
// placement, port allocation from the registry).
type Manager struct {
	*node.Machine

	// node is where this manager's ORCHESTRATION sends machine work (provision,
	// deprovision): itself by default (single process), or a remote node agent
	// when control and node run as separate services (SetNodeAgent).
	node NodeAgent
	// registry is the connected-nodes registry in multi-node control mode
	// (SetNodeRegistry); nil in a single process.
	registry *NodeRegistry

	// nextPort rotates allocation through the range (see allocatePort)
	nextPort int
	// reservedPorts holds ports handed out by allocatePort but not yet registered
	// in the store, so concurrent creates never share one (see allocatePort)
	reservedPorts map[int]bool

	// ctlStates is the control plane's OWN view of app states -- what
	// CachedStates serves. Fed by the per-node poll loops (IngestStates) in
	// split mode and by RefreshStates through the node agent otherwise. The
	// machine half keeps its own measurement cache; the two are different
	// things that happened to share a map before the split.
	ctlStates      map[string]State
	ctlStatesFresh time.Time
	ctlRefreshing  bool
	ctlStatesMu    sync.Mutex // Protects ctlStates, ctlStatesFresh, ctlRefreshing

	// store and config are the control plane's OWN references (the same
	// objects as the machine's in any real process). Declared here so the
	// control methods stop reaching into the machine's internals: the outer
	// fields shadow the embedded ones for every *Manager method.
	store  *store.Store
	config *config.Config

	pmu sync.Mutex // Protects nextPort, reservedPorts (the control side's own lock)
	// mirrorSeq orders mirror pushes so a node can drop a stale one; see
	// SyncState.Seq. Per control process, which is why a node resets its view
	// of it on every new connection.
	mirrorSeq atomic.Int64
}

// NewManager creates a Manager from its config, store and the node-local services
// (real ones from node.NewSystemServices in production, fakes in tests).
func NewManager(conf *config.Config, s *store.Store, svc *node.Services) *Manager {
	m := &Manager{
		Machine:       node.NewMachine(machineConfig(conf), s, svc),
		reservedPorts: make(map[int]bool),
		ctlStates:     make(map[string]State),
		store:         s,
		config:        conf,
	}
	m.node = m // single process: orchestration and machine work are the same
	// A machine-side lifecycle change invalidates the control cache's entry,
	// so the UI never confidently serves the pre-transition state for a TTL.
	m.Machine.OnStateChanged(func(name string) {
		m.ctlStatesMu.Lock()
		delete(m.ctlStates, name)
		m.ctlStatesMu.Unlock()
	})
	return m
}

// machineConfig is the node config the FUSED daemon's machine half runs on:
// control does the machine work itself, so the node is always the local one
// and the paths are control's. A split deployment's node reads its own file
// instead -- the two configs are separate types precisely so control's
// settings cannot leak into a node.
func machineConfig(conf *config.Config) *node.Config {
	c := node.NewConfig()
	c.DataDir, c.AppsDir, c.SocketFile = conf.DataDir, conf.AppsDir, conf.SocketFile
	return c
}

// CreateApp registers a new app: it allocates a port, creates the Unix user with
// SSH access and writes the home skeleton. Its authorized_keys are the union
// of the request keys and the owner's profile keys; an app with neither is fine,
// since apps are driven through the API and SSH is opt-in.
func (m *Manager) CreateApp(name string, opts *CreateOptions) (*store.App, error) {
	return m.create(name, opts, nil)
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
	src, err := m.store.App(source)
	if err != nil {
		return nil, err
	}
	// Seed from a specific snapshot if asked, else from the source's current
	// subvolume. The seed travels as IDs -- the node resolves them against its
	// own pool.
	if snapshotID != "" {
		snap, err := m.store.Snapshot(snapshotID)
		if err != nil {
			return nil, err
		}
		if snap.AppName != source {
			return nil, store.ErrSnapshotNotFound
		}
	}
	// A fork always lands on the source's node: the seed subvolume is a
	// node-local btrfs path there.
	if opts == nil {
		opts = &CreateOptions{}
	}
	opts.Host = hostOrLocal(src.Host)
	// Lock the source so its subvolume/snapshot is not rolled back or deleted
	// mid-copy; the new app's own deploy runs under its own lock in the background.
	defer m.LockApp(source)()
	return m.create(newName, opts, &seedRef{AppID: src.ID, SnapshotID: snapshotID})
}

// seedRef names a fork's seed for the provision spec: ids, never paths.
type seedRef struct {
	AppID      string
	SnapshotID string
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
func (m *Manager) create(name string, opts *CreateOptions, seed *seedRef) (*store.App, error) {
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
	forking := seed != nil

	// Mint the app's stable id up front: the app subvolume (and its snapshots) are
	// keyed on the id, not the name, so a later rename never moves them. The id is
	// on the App struct that gets inserted below.
	host := opts.Host
	if host == "" {
		host = m.placeNode()
	}
	app := &store.App{ID: store.NewAppID(), Name: name, Port: port, Host: host, OwnerID: opts.OwnerID, ImageTag: workspace.ImageTag(), UID: workspace.UIDFor(port)}

	// Register the app FIRST and push the mirror before any machine state
	// exists: the node's orphan reconcile treats unknown ids as leftovers, so
	// a subvolume appearing before the mirror knows its id gets torn down by
	// a concurrent reconcile (seen live on the stage two-node setup). The app
	// is pinned to the workspace image it is built with, so a later
	// Containerfile change (e.g. adding a runtime) only affects new apps.
	m.RecordDiskLimit(name, opts.DiskMB)
	if err := m.store.AddApp(app); err != nil {
		return nil, err
	}
	if err := m.store.SetAppKeys(name, appKeys); err != nil {
		_ = m.store.RemoveApp(name)
		return nil, err
	}
	m.PushMirror()

	// Build the app on this machine (subvolume, user, keys, skeleton) -- the
	// node-local half. On failure the row goes away again (and the mirror
	// with it); the node's provision cleans up its own partial state.
	spec := &ProvisionSpec{
		Host:    host,
		ID:      app.ID,
		Name:    name,
		Port:    port,
		SSHKeys: append(append([]string{}, appKeys...), opts.ProfileKeys...),
		URL:     m.URL(&store.App{Name: name, Port: port}),
		DiskMB:  m.DiskLimit(name),
	}
	if seed != nil {
		spec.SeedAppID, spec.SeedSnapshotID = seed.AppID, seed.SnapshotID
	}
	if err := m.node.Provision(spec); err != nil {
		_ = m.store.RemoveApp(name)
		m.PushMirror()
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
	// Port rules are applied by the node inside Provision (that is where the
	// app's unix user and the right firewall table live).

	m.startInBackground(name, forking)
	return app, nil
}

// SetNodeAgent points this manager's orchestration at a (remote) node agent:
// what control calls once a hostit-node dials in. Default is the manager
// itself (single process).
func (m *Manager) SetNodeAgent(node NodeAgent) {
	m.node = node
}

// DeleteApp removes the app from the registry and answers at once; the host
// teardown -- container stop, subvolume and snapshot deletes (with the qgroup
// sync ladder behind them), userdel -- takes seconds on a loaded box and
// continues in the background. ReconcileOrphans converges any teardown the
// daemon dies in the middle of, so the background path needs no new safety
// net; the app's port (and with it its uid block) stays reserved until the
// teardown finishes, so a new app cannot collide with the dying user.
func (m *Manager) DeleteApp(name string) error {
	unlock := m.LockApp(name) // serialize against a concurrent deploy/snapshot/rollback
	a, err := m.store.App(name)
	if err != nil {
		unlock()
		return err
	}
	// Everything the teardown needs is captured NOW: once the rows are gone,
	// name-keyed lookups (paths, ids, snapshots) resolve nothing. The uid
	// comes from the row (recorded at create, backfilled for older apps), not
	// a machine lookup -- the unix user lives on the app's node, not here.
	spec := &DeprovisionSpec{
		Host:      hostOrLocal(a.Host),
		ID:        a.ID,
		Name:      name,
		Port:      a.Port,
		UID:       a.UID,
		UIDKnown:  a.UID != 0,
		Unit:      workspace.UnitName(a.ID),
		Container: workspace.ContainerName(a.ID),
	}
	if err := m.store.RemoveApp(name); err != nil {
		unlock()
		return err
	}
	m.PushMirror()
	m.pmu.Lock()
	m.reservedPorts[a.Port] = true
	m.pmu.Unlock()
	m.SetTearingDown(name, true)
	// The port's drop rule is removed by the node inside Deprovision (its
	// firewall table, its user lookups); the port stays reserved until then.
	m.TrackedGo(func() {
		defer unlock()
		defer m.releasePort(a.Port)
		defer m.SetTearingDown(name, false)
		m.node.Deprovision(spec)
	})
	return nil
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

// validateKeys is the control side's early check of user-supplied SSH keys,
// wrapping the ssh package's parse in ErrInvalid so the server reports a bad
// request; the node re-validates whatever it is told to write.
func validateKeys(keys []string) error {
	if err := ssh.ValidateKeys(keys); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	return nil
}

func (m *Manager) validateName(name string) error {
	if !nodeapi.ValidName(name) {
		return fmt.Errorf("%w: invalid app name %q, must match %s", ErrInvalid, name, nodeapi.AppNamePattern)
	}
	if slices.Contains(reservedNames, name) {
		return fmt.Errorf("%w: app name %q is reserved", ErrInvalid, name)
	}
	if _, err := m.store.App(name); err == nil {
		return ErrAppExists
	} else if !errors.Is(err, store.ErrAppNotFound) {
		return err
	}
	if m.UserExists(name) {
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
		if !m.IsTearingDown(name) {
			return !m.UserExists(name)
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
	m.pmu.Lock()
	defer m.pmu.Unlock()
	// Rotate through the range instead of always taking the lowest free port: a
	// just-deleted app's port maps to a uid whose budget qgroup may still hold
	// the dying subvolume's uncommitted bytes (the teardown destroys it gently,
	// without a filesystem sync), and immediate reuse started brand-new apps
	// over their disk cap -- EDQUOT on the container's first mkdir. Scanning
	// upward from the last grant leaves freed uids fallow until the wrap-around;
	// the qgroup sweep collects their leftovers long before that.
	span := workspace.PortMax - workspace.PortMin + 1
	if m.nextPort < workspace.PortMin || m.nextPort > workspace.PortMax {
		m.nextPort = workspace.PortMin
	}
	for i := 0; i < span; i++ {
		port := workspace.PortMin + (m.nextPort-workspace.PortMin+i)%span
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
	m.pmu.Lock()
	defer m.pmu.Unlock()
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
		if err := m.store.SetAppUID(a.Name, workspace.UIDFor(a.Port)); err != nil {
			slog.Warn("Cannot backfill app uid", "app", a.Name, "error", err)
		}
	}
}
