// Package app manages the lifecycle of hostit apps: Unix users, SSH keys, port
// allocation and the initial scaffold in the app's home directory. All direct
// system interaction is behind the SystemOps interface so it can be faked in tests.
package app

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"time"

	"heckel.io/hostit/config"
	"heckel.io/hostit/store"
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
	CreateUser(username, home string) error
	DeleteUser(username string) error
	WriteAuthorizedKeys(username, home string, keys []string) error
	WriteScaffold(username, home string, files map[string]string) error
	ChownToUser(username, path string) error
	ApplyPortRules(rules []PortRule) error
	ImageExists(tag string) bool
	BuildImage(contextDir, tag string) error
}

// Runner executes a command on the host as root; the daemon performs all
// container and service work itself, on behalf of app users
type Runner interface {
	Run(args ...string) (string, error)
	// RunTimeout is for calls whose answer is nice to have but must not block a
	// request: podman serializes on its own lock, so a slow create or pull would
	// otherwise stall everything that asks for state
	RunTimeout(timeout time.Duration, args ...string) (string, error)
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
}

// Manager creates and deletes apps and everything that belongs to them, and
// runs their containers as root with per-app uid mappings
type Manager struct {
	config *config.Config
	store  *store.Store
	ops    SystemOps
	runner Runner

	// memoryMB and diskMB cache per-app limits, so redeploys and quota checks
	// keep them; the authoritative values come from the owner's limits
	memoryMB map[string]int
	diskMB   map[string]int

	// stateCache holds the last measured state of every app, so listing apps
	// answers from memory instead of waiting on podman
	stateCache      map[string]State
	stateFresh      time.Time
	stateRefreshing bool

	mu      sync.Mutex // Protects memoryMB, diskMB
	stateMu sync.Mutex // Protects stateCache, stateFresh, stateRefreshing
	buildMu sync.Mutex // Serializes image builds; two at once OOM a small host
	execMu  sync.Mutex // Serializes /run commands; they are builds, and the box has one core
}

// NewManager creates a Manager
func NewManager(conf *config.Config, s *store.Store, ops SystemOps, runner Runner) *Manager {
	return &Manager{
		config:     conf,
		store:      s,
		ops:        ops,
		runner:     runner,
		memoryMB:   make(map[string]int),
		diskMB:     make(map[string]int),
		stateCache: make(map[string]State),
	}
}

// CreateApp registers a new app: it allocates a port, creates the Unix user with
// SSH access and scaffolds the home directory. Its authorized_keys are the union
// of the request keys and the owner's profile keys; an app with neither is fine,
// since apps are driven through the API and SSH is opt-in.
func (m *Manager) CreateApp(name string, opts *CreateOptions) (*store.App, error) {
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

	// Create the user, install keys and scaffold the home directory
	home := filepath.Join(m.config.AppsDir, name)
	if err := m.ops.CreateUser(name, home); err != nil {
		return nil, fmt.Errorf("cannot create user %s: %w", name, err)
	}
	cleanup := func() {
		_ = m.ops.DeleteUser(name)
	}
	if err := m.ops.WriteAuthorizedKeys(name, home, sshKeys); err != nil {
		cleanup()
		return nil, fmt.Errorf("cannot write authorized keys for %s: %w", name, err)
	}
	if err := m.ops.WriteScaffold(name, home, m.scaffoldFiles(name, port)); err != nil {
		cleanup()
		return nil, fmt.Errorf("cannot write scaffold for %s: %w", name, err)
	}

	// Register the app; roll back the user if this fails
	app := &store.App{Name: name, Port: port, Host: store.HostLocal, OwnerID: opts.OwnerID}
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
	m.ReconcilePortRules()

	// Start the scaffolded demo app in the background, so the URL serves
	// something without the API call waiting for a container (and, on the app
	// user's first app, an image build) to come up
	go func() {
		// How long this took is the question asked whenever an app "would not
		// start": the API returns at once, and the wait is podman's queue behind
		// whatever else the host is doing
		started := time.Now()
		if _, err := m.Up(name); err != nil {
			slog.Warn("Cannot start demo app; the app exists but serves nothing yet",
				"app", name, "took", time.Since(started).Round(time.Second), "error", err)
			return
		}
		slog.Info("Demo app started", "app", name, "took", time.Since(started).Round(time.Second))
	}()
	return app, nil
}

// DeleteApp stops the app's user session, deletes the Unix user including the home
// directory, and removes the app from the registry
func (m *Manager) DeleteApp(name string) error {
	if _, err := m.store.App(name); err != nil {
		return err
	}
	// Stop the app first: a running container keeps processes alive, and
	// userdel refuses to remove a user that still has any
	if _, err := m.runner.Run("systemctl", "disable", "--now", unitName(name)); err != nil {
		slog.Warn("Cannot disable the app's unit; reconciling at next start", "app", name, "error", err)
	}
	// The unit lingers in "failed" otherwise, and a Restart=always unit that
	// systemd still knows about keeps retrying a container that is gone
	_, _ = m.runner.Run("systemctl", "reset-failed", unitName(name))
	_, _ = m.runner.Run("podman", "rm", "--force", containerName(name))
	if err := m.ops.DeleteUser(name); err != nil {
		return fmt.Errorf("cannot delete user %s: %w", name, err)
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
