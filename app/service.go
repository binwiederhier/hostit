// Package app manages the lifecycle of hostit apps: Unix users, SSH keys, port
// allocation and the initial scaffold in the app's home directory. All direct
// system interaction is behind the SystemOps interface so it can be faked in tests.
package app

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"heckel.io/hostit/config"
	"heckel.io/hostit/store"
)

var (
	// ErrAppExists is returned when the app name or a Unix user with that name already exists
	ErrAppExists = errors.New("app or user already exists")
	// ErrNoPortsAvailable is returned when the configured port range is exhausted
	ErrNoPortsAvailable = errors.New("no free ports in configured range")
	// ErrInvalid wraps all request validation errors (bad names, bad keys)
	ErrInvalid = errors.New("invalid request")
	// ErrLimitReached is returned when a user hit one of their resource limits
	ErrLimitReached = errors.New("limit reached")

	// appNameRegex limits names to things that are safe as Unix usernames and DNS labels
	appNameRegex = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,30}[a-z0-9])?$`)

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
	CreateUser(username, home string) error
	DeleteUser(username string) error
	EnableLinger(username string) error
	WriteAuthorizedKeys(username, home string, keys []string) error
	WriteScaffold(username, home string, files map[string]string) error
	WriteUserFile(username, home, relPath, content string, mode os.FileMode) error
	ApplyPortRules(rules []PortRule) error
	SharedImageExists(storeDir, tag string) bool
	BuildSharedImage(storeDir, contextDir, tag string) error
}

// UserRunner executes a command as the given (unprivileged) app user, with the
// environment needed for rootless podman and systemctl --user
type UserRunner interface {
	RunAsUser(username string, args ...string) (string, error)
}

// PortRule restricts loopback connects to an app port to root and the owning UID
type PortRule struct {
	Port int
	UID  int
}

// Credentials holds a generated SSH key pair; the private key is returned to the
// API caller exactly once and never stored
type Credentials struct {
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
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
// deploys their containers (as the app user) via the UserRunner
type Manager struct {
	config *config.Config
	store  *store.Store
	ops    SystemOps
	runner UserRunner
	podman string // Resolved podman path for unit files, cached

	// memoryMB and diskMB cache per-app limits, so redeploys and quota checks
	// keep them; the authoritative values come from the owner's limits
	memoryMB map[string]int
	diskMB   map[string]int
}

// NewManager creates a Manager
func NewManager(conf *config.Config, s *store.Store, ops SystemOps, runner UserRunner) *Manager {
	return &Manager{
		config:   conf,
		store:    s,
		ops:      ops,
		runner:   runner,
		memoryMB: make(map[string]int),
		diskMB:   make(map[string]int),
	}
}

// CreateApp registers a new app: it allocates a port, creates the Unix user with
// SSH access and scaffolds the home directory. If neither request keys nor owner
// profile keys exist, a key pair is generated and returned; otherwise Credentials
// is nil. The app's authorized_keys are the union of both key sets.
func (m *Manager) CreateApp(name string, opts *CreateOptions) (*store.App, *Credentials, error) {
	if opts == nil {
		opts = &CreateOptions{}
	}
	if err := m.validateName(name); err != nil {
		return nil, nil, err
	}
	if err := validateKeys(opts.RequestKeys); err != nil {
		return nil, nil, err
	}
	port, err := m.allocatePort()
	if err != nil {
		return nil, nil, err
	}

	// Generate a key pair only if nobody could log in otherwise
	var creds *Credentials
	appKeys := opts.RequestKeys
	if len(appKeys) == 0 && len(opts.ProfileKeys) == 0 {
		creds, err = generateKeyPair(name)
		if err != nil {
			return nil, nil, err
		}
		appKeys = []string{creds.PublicKey}
	}
	sshKeys := append(append([]string{}, appKeys...), opts.ProfileKeys...)

	// Create the user, enable lingering (so user units run at boot), install keys and scaffold
	home := filepath.Join(m.config.AppsDir, name)
	if err := m.ops.CreateUser(name, home); err != nil {
		return nil, nil, fmt.Errorf("cannot create user %s: %w", name, err)
	}
	cleanup := func() {
		_ = m.ops.DeleteUser(name)
	}
	if err := m.ops.EnableLinger(name); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("cannot enable linger for %s: %w", name, err)
	}
	if err := m.ops.WriteAuthorizedKeys(name, home, sshKeys); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("cannot write authorized keys for %s: %w", name, err)
	}
	if err := m.ops.WriteScaffold(name, home, m.scaffoldFiles(name, port)); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("cannot write scaffold for %s: %w", name, err)
	}
	// Point the user's podman at the shared image store, so the workspace image
	// is neither rebuilt nor duplicated per app
	if err := m.ops.WriteUserFile(name, home, storageConfRelPath, storageConf(m.imageStoreDir()), 0o644); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("cannot write storage config for %s: %w", name, err)
	}

	// Register the app; roll back the user if this fails
	app := &store.App{Name: name, Port: port, Host: store.HostLocal, OwnerID: opts.OwnerID}
	if err := m.store.AddApp(app); err != nil {
		cleanup()
		return nil, nil, err
	}
	if err := m.store.SetAppKeys(name, appKeys); err != nil {
		cleanup()
		_ = m.store.RemoveApp(name)
		return nil, nil, err
	}
	m.memoryMB[name] = opts.MemoryMB
	m.ReconcilePortRules()

	// Start the scaffolded demo app, so the URL serves something immediately
	// instead of a bad gateway page
	if _, err := m.Up(name); err != nil {
		slog.Warn("Cannot start demo app; the app exists but serves nothing yet", "app", name, "error", err)
	}
	return app, creds, nil
}

// DeleteApp stops the app's user session, deletes the Unix user including the home
// directory, and removes the app from the registry
func (m *Manager) DeleteApp(name string) error {
	if _, err := m.store.App(name); err != nil {
		return err
	}
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
