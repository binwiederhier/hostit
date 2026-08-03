// Package app manages the lifecycle of hostit apps: Unix users, SSH keys, port
// allocation and the initial scaffold in the app's home directory. All direct
// system interaction is behind the SystemOps interface so it can be faked in tests.
package app

import (
	"errors"
	"fmt"
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

// SystemOps abstracts the system-level operations the Manager needs; the real
// implementation (NewSystemOps) shells out to useradd, loginctl and friends.
type SystemOps interface {
	UserExists(username string) bool
	CreateUser(username, home string) error
	DeleteUser(username string) error
	EnableLinger(username string) error
	WriteAuthorizedKeys(username, home string, keys []string) error
	WriteScaffold(username, home string, files map[string]string) error
}

// Credentials holds a generated SSH key pair; the private key is returned to the
// API caller exactly once and never stored
type Credentials struct {
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
}

// Manager creates and deletes apps and everything that belongs to them
type Manager struct {
	config *config.Config
	store  *store.Store
	ops    SystemOps
}

// NewManager creates a Manager
func NewManager(conf *config.Config, s *store.Store, ops SystemOps) *Manager {
	return &Manager{
		config: conf,
		store:  s,
		ops:    ops,
	}
}

// CreateApp registers a new app: it allocates a port, creates the Unix user with
// SSH access and scaffolds the home directory. If no SSH keys are given, a key
// pair is generated and returned; otherwise Credentials is nil.
func (m *Manager) CreateApp(name string, sshKeys []string) (*store.App, *Credentials, error) {
	if err := m.validateName(name); err != nil {
		return nil, nil, err
	}
	if err := validateKeys(sshKeys); err != nil {
		return nil, nil, err
	}
	port, err := m.allocatePort()
	if err != nil {
		return nil, nil, err
	}

	// Generate a key pair if the caller did not bring their own
	var creds *Credentials
	if len(sshKeys) == 0 {
		creds, err = generateKeyPair(name)
		if err != nil {
			return nil, nil, err
		}
		sshKeys = []string{creds.PublicKey}
	}

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

	// Register the app; roll back the user if this fails
	app := &store.App{Name: name, Port: port, Host: store.HostLocal}
	if err := m.store.AddApp(app); err != nil {
		cleanup()
		return nil, nil, err
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
	return m.store.RemoveApp(name)
}

// SetKeys replaces the app's authorized SSH keys
func (m *Manager) SetKeys(name string, sshKeys []string) error {
	app, err := m.store.App(name)
	if err != nil {
		return err
	}
	if err := validateKeys(sshKeys); err != nil {
		return err
	}
	return m.ops.WriteAuthorizedKeys(app.Name, filepath.Join(m.config.AppsDir, app.Name), sshKeys)
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
