// Package app orchestrates the lifecycle of hostit apps: the per-app Unix user, SSH
// keys, port allocation, home scaffold and container. Node-local system interaction
// is delegated to focused service packages (btrfs, systemd, container) plus the
// SystemOps interface, all injected so they can be faked in tests. Keeping these
// services separable is also the seam a future control/app-node split would use.
package app

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"time"

	"heckel.io/hostit/btrfs"
	"heckel.io/hostit/config"
	"heckel.io/hostit/container"
	"heckel.io/hostit/run"
	"heckel.io/hostit/store"
	"heckel.io/hostit/systemd"
)

const (
	// settingAgentVersion records the hostit version the running agents were
	// started from, so an upgrade knows whose behaviour is stale
	settingAgentVersion = "agent_version"

	// AppNamePattern is what an app may be called: safe as a Unix username and as
	// a DNS label. Exported because the enter helper applies the same rule without
	// importing this package's machinery.
	AppNamePattern = `^[a-z]([a-z0-9-]{0,30}[a-z0-9])?$`
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

// SystemOps abstracts the root-privileged system operations the Manager needs;
// the real implementation (NewSystemOps) shells out to useradd, loginctl, nft
// and friends.
type SystemOps interface {
	UserExists(username string) bool
	LookupUID(username string) (int, error)
	LookupIDs(username string) (IDs, error)
	CreateUser(username, home string, uid int) error
	RemapUser(username, home string, uid int) error
	// SetUserHome points a user at a new home path without moving files (the home
	// was already moved); used by the one-off migration to id-keyed homes.
	SetUserHome(username, home string) error
	// RenameUser changes a user's login name in place (uid and home unchanged);
	// this is the only OS mutation a rename needs.
	RenameUser(oldName, newName string) error
	// SyncGroupName renames a user's primary group to match its login name when the
	// two have drifted (an app renamed before group-rename shipped kept its old group
	// name). Used by the one-off group-name migration.
	SyncGroupName(username string) error
	// KillUserProcesses force-kills every process owned by the user, so usermod is
	// not blocked by a leftover terminal/SSH session. Used only after the app's unit
	// is stopped, so this reaps session wrappers, not the running app.
	KillUserProcesses(username string) error
	DeleteUser(username string) error
	WriteAuthorizedKeys(username, home string, keys []string) error
	WriteScaffold(username, home string, files map[string]string) error
	ChownToUserIn(root *os.Root, username, rel string) error
	ApplyPortRules(rules []PortRule) error
}

// PortRule restricts loopback connects to an app port to root and the owning UID
type PortRule struct {
	Port int
	UID  int
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
	config    *config.Config
	store     *store.Store
	ops       SystemOps
	runner    run.Runner
	btrfs     *btrfs.Service
	systemd   *systemd.Service
	container *container.Service

	// memoryMB and diskMB cache per-app limits, so redeploys and quota checks
	// keep them; the authoritative values come from the owner's limits
	memoryMB map[string]int
	diskMB   map[string]int

	// stateCache holds the last measured state of every app, so listing apps
	// answers from memory instead of waiting on podman
	stateCache      map[string]State
	stateFresh      time.Time
	stateRefreshing bool

	// btrfs detection is cached: whether AppsDir is btrfs decides if snapshots and
	// hard qgroup quotas are available. On a plain ext4 host it stays false.
	btrfsOnce sync.Once
	btrfsOK   bool

	// appLocks serializes mutating lifecycle work per app (deploy, snapshot,
	// rollback, delete), so operations on one app's home never interleave -- e.g. a
	// rollback deleting the home while a deploy writes into it.
	appLocks map[string]*sync.Mutex

	mu         sync.Mutex // Protects memoryMB, diskMB
	stateMu    sync.Mutex // Protects stateCache, stateFresh, stateRefreshing
	buildMu    sync.Mutex // Serializes image builds; two at once OOM a small host
	execMu     sync.Mutex // Serializes /run commands; they are builds, and the box has one core
	appLocksMu sync.Mutex // Protects appLocks
}

// NewManager creates a Manager
func NewManager(conf *config.Config, s *store.Store, ops SystemOps, runner run.Runner) *Manager {
	return &Manager{
		config:     conf,
		store:      s,
		ops:        ops,
		runner:     runner,
		btrfs:      btrfs.New(runner),
		systemd:    systemd.New(runner),
		container:  container.New(runner),
		memoryMB:   make(map[string]int),
		diskMB:     make(map[string]int),
		stateCache: make(map[string]State),
		appLocks:   make(map[string]*sync.Mutex),
	}
}

// lockApp acquires the per-app lifecycle lock and returns its unlock func, so
// deploy/snapshot/rollback/delete on one app run one at a time and never race on
// its home subvolume. It is NOT reentrant: a method already holding the lock must
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
// SSH access and scaffolds the home directory. Its authorized_keys are the union
// of the request keys and the owner's profile keys; an app with neither is fine,
// since apps are driven through the API and SSH is opt-in.
func (m *Manager) CreateApp(name string, opts *CreateOptions) (*store.App, error) {
	return m.create(name, opts, "")
}

// Fork duplicates an existing app into a new one: the new app's home is seeded
// from a writable btrfs snapshot of the source (its files, config and data) rather
// than the demo scaffold. snapshotID picks a specific snapshot to seed from; empty
// means the source's current home. The fork gets its own port, Unix user, subdomain
// and container. Requires btrfs (the snapshot primitive it relies on).
func (m *Manager) Fork(source, newName, snapshotID string, opts *CreateOptions) (*store.App, error) {
	if !m.btrfsEnabled() {
		return nil, ErrSnapshotsUnavailable
	}
	if _, err := m.store.App(source); err != nil {
		return nil, err
	}
	// Seed from a specific snapshot if asked, else from the source's current home.
	seedPath := m.appHome(source)
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
	// Lock the source so its home/snapshot is not rolled back or deleted mid-copy;
	// the new app's own deploy runs under its own lock in the background.
	defer m.lockApp(source)()
	return m.create(newName, opts, seedPath)
}

// create registers a new app. With seedPath == "" it scaffolds the demo app; with
// a seedPath (a subvolume: an app's home or a snapshot) it forks -- seeding the home
// from a writable snapshot of that path and skipping the scaffold. Either way it
// allocates a port, creates the Unix user with SSH access, registers the app and
// starts it in the background. The authorized_keys are the union of the request keys
// and the owner's profile keys; an app with neither is fine, since apps are driven
// through the API and SSH is opt-in.
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

	appKeys := opts.RequestKeys
	sshKeys := append(append([]string{}, appKeys...), opts.ProfileKeys...)
	forking := seedPath != ""

	// Mint the app's stable id up front: the home directory (and its snapshots) are
	// keyed on the id, not the name, so a later rename never moves them. The id is
	// on the App struct that gets inserted below.
	app := &store.App{ID: store.NewAppID(), Name: name, Port: port, Host: store.HostLocal, OwnerID: opts.OwnerID, ImageTag: workspaceImageTag()}

	// Create the user, install keys and populate the home directory. The uid is a
	// contiguous block derived from the (unique) port, so the container maps as a
	// single offset and podman idmap-mounts the image instead of copying it.
	home := m.appHomeByID(app.ID)
	// Seed the home. A fork is a writable snapshot of the seed subvolume (an instant
	// CoW copy of its files); a fresh app is an empty subvolume on btrfs that the
	// scaffold then fills. Either directory is adopted by useradd, whose own mkdir
	// is a no-op.
	if forking {
		if err := m.btrfs.Snapshot(seedPath, home, false); err != nil {
			return nil, fmt.Errorf("cannot seed %s: %w", name, err)
		}
	} else if m.btrfsEnabled() {
		if err := m.btrfs.CreateSubvolume(home); err != nil {
			return nil, fmt.Errorf("cannot create home subvolume for %s: %w", name, err)
		}
	}
	// Record whether this home is a btrfs subvolume: a plain-directory home (btrfs
	// off) silently lacks snapshots/quotas/rollback/fork, so make the choice visible.
	slog.Info("Creating app", "app", name, "port", port, "subvolume", forking || m.btrfsEnabled(), "forked", forking)
	if err := m.ops.CreateUser(name, home, m.uidFor(port)); err != nil {
		if m.btrfsEnabled() {
			_ = m.btrfs.DeleteSubvolume(home)
		}
		return nil, fmt.Errorf("cannot create user %s: %w", name, err)
	}
	cleanup := func() {
		_ = m.ops.DeleteUser(name)
		// Remove the id-keyed home we just created. The app is not in the store on
		// the early failures, so this deletes the concrete path rather than resolving
		// it by name; a brand-new app has no snapshots to clean up.
		if m.btrfsEnabled() {
			_ = m.btrfs.DeleteSubvolume(home)
		}
	}
	if err := m.ops.WriteAuthorizedKeys(name, home, sshKeys); err != nil {
		cleanup()
		return nil, fmt.Errorf("cannot write authorized keys for %s: %w", name, err)
	}
	// A fork keeps the source's files; only a fresh app gets the demo scaffold.
	if !forking {
		if err := m.ops.WriteScaffold(name, home, scaffoldFiles(name, m.URL(&store.App{Name: name, Port: port}), WorkspaceRuntimes)); err != nil {
			cleanup()
			return nil, fmt.Errorf("cannot write scaffold for %s: %w", name, err)
		}
	}

	// Register the app; roll back the user if this fails. The app was built above
	// (id minted so the home could be created id-named); it is pinned to the
	// workspace image it is built with, so a later Containerfile change (e.g. adding
	// a runtime) only affects new apps, never this one.
	if err := m.store.AddApp(app); err != nil {
		cleanup()
		return nil, err
	}
	if err := m.store.SetAppKeys(name, appKeys); err != nil {
		cleanup()
		_ = m.store.RemoveApp(name)
		return nil, err
	}
	m.SetMemoryLimit(name, opts.MemoryMB)
	// Apply the disk quota now (create and fork alike), so a new app is capped from
	// the start rather than only after the next daemon restart. On btrfs this sets
	// the hard qgroup limit on the home subvolume.
	m.SetDiskLimit(name, opts.DiskMB)
	// The forked home is full of files owned by the source's uid; make them the new
	// app's.
	if forking {
		uid := m.uidFor(port)
		if _, err := m.runner.Run("chown", "-R", fmt.Sprintf("%d:%d", uid, uid), home); err != nil {
			slog.Warn("Cannot chown forked home", "app", name, "error", err)
		}
	}
	m.ReconcilePortRules()

	// Start the app in the background, so the URL serves something without the API
	// call waiting for a container (and, on the app user's first app, an image
	// build) to come up
	go func() {
		// How long this took is the question asked whenever an app "would not
		// start": the API returns at once, and the wait is podman's queue behind
		// whatever else the host is doing
		started := time.Now()
		if _, err := m.Up(name); err != nil {
			slog.Warn("Cannot start app; it exists but serves nothing yet",
				"app", name, "took", time.Since(started).Round(time.Second), "error", err)
			return
		}
		slog.Info("App started", "app", name, "forked", forking, "took", time.Since(started).Round(time.Second))
	}()
	return app, nil
}

// DeleteApp stops the app's user session, deletes the Unix user including the home
// directory, and removes the app from the registry
func (m *Manager) DeleteApp(name string) error {
	defer m.lockApp(name)() // serialize against a concurrent deploy/snapshot/rollback
	if _, err := m.store.App(name); err != nil {
		return err
	}
	// Stop the app first: a running container keeps processes alive, and
	// userdel refuses to remove a user that still has any
	if err := m.systemd.DisableNow(m.unitName(name)); err != nil {
		slog.Warn("Cannot disable the app's unit; reconciling at next start", "app", name, "error", err)
	}
	// The unit lingers in "failed" otherwise, and a Restart=always unit that
	// systemd still knows about keeps retrying a container that is gone
	_ = m.systemd.ResetFailed(m.unitName(name))
	_ = m.container.RemoveForce(m.containerName(name))
	// On btrfs the home and snapshots are subvolumes that userdel's rm -rf cannot
	// remove, so delete them first.
	if m.btrfsEnabled() {
		m.deleteAppSubvolumes(name)
	}
	if err := m.ops.DeleteUser(name); err != nil {
		return fmt.Errorf("cannot delete user %s: %w", name, err)
	}
	// userdel --remove will not delete a home directory it does not own -- on btrfs
	// the home was a subvolume removed above, and any recreated stub is root-owned --
	// so remove whatever is left, or an empty home dir is orphaned under AppsDir.
	if err := os.RemoveAll(m.appHome(name)); err != nil {
		slog.Warn("Could not remove leftover home directory after deleting app", "app", name, "path", m.appHome(name), "error", err)
	}
	if err := m.store.RemoveApp(name); err != nil {
		return err
	}
	m.ReconcilePortRules()
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
	rules := make([]PortRule, 0, len(apps))
	for _, a := range apps {
		uid, err := m.ops.LookupUID(a.Name)
		if err != nil {
			slog.Warn("Cannot look up uid for port rule", "app", a.Name, "error", err)
			continue
		}
		rules = append(rules, PortRule{Port: a.Port, UID: uid})
	}
	if err := m.ops.ApplyPortRules(rules); err != nil {
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
	return m.ops.WriteAuthorizedKeys(name, filepath.Join(m.config.AppsDir, name), keys)
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
	if m.ops.UserExists(name) {
		return ErrAppExists
	}
	return nil
}

// allocatePort returns the lowest free port in the configured range
func (m *Manager) allocatePort() (int, error) {
	used, err := m.store.UsedPorts()
	if err != nil {
		return 0, err
	}
	for port := m.config.PortMin; port <= m.config.PortMax; port++ {
		if !slices.Contains(used, port) {
			return port, nil
		}
	}
	return 0, ErrNoPortsAvailable
}

// uidFor is an app's base uid: a contiguous uidBlockSize-wide block, one per
// app, spaced by port so blocks never overlap. Container uid 0 maps here.
func (m *Manager) uidFor(port int) int {
	return uidBlockStart + (port-m.config.PortMin)*uidBlockSize
}
