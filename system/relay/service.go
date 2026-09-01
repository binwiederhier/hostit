// Package relay is the SSH-relay frontend's root-side work: it keeps a set of
// stub Unix accounts and their authorized_keys matching a spec control computes,
// and it owns the frontend's relay keypair. It is deliberately small and
// decoupled from the node's machine so the unprivileged control plane can drive
// it through a tiny sudo helper (hostit-relay-sync) instead of running a whole
// node just to create a few users.
//
// The frontend accepts `ssh <app>@<domain>` for a REMOTE app by matching a stub
// account whose keys are the app's, then the hostit-relay forwarder hops to the
// real node. A colocated (local) app needs no stub: its own node IS the
// frontend, so control never lists it here.
package relay

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	"heckel.io/hostit/node/api"
	"heckel.io/hostit/system/relaypaths"
)

// StubOps is the subset of the unixuser service the reconcile needs: everything
// here is a root operation, which is why the frontend runs behind a sudo helper.
type StubOps interface {
	Exists(username string) bool
	// Home reports an account's home directory (ok false if it does not exist).
	// Used to confirm a stub is really homed where a migration expects it.
	Home(username string) (home string, ok bool)
	CreateStub(username, home string) error
	Delete(username string) error
	KillProcesses(username string) error
	List() ([]Account, error)
}

// Account is one Unix account as the host reports it (name + home); the reconcile
// filters stubs by home so it only ever touches accounts under the stubs dir.
type Account struct {
	Name string
	Home string
}

// Spec is what control hands the frontend: the routing table, known_hosts and
// each routed (remote) app's authorized_keys. Pure data, carried over the sudo
// helper's stdin so control never needs to write these root-owned files itself.
type Spec struct {
	Routes     string            `json:"routes"`
	KnownHosts string            `json:"known_hosts"`
	AppKeys    map[string]string `json:"app_keys"`
}

// Paths are the frontend file locations; defaults are the shared relaypaths, but
// a test points them at a temp dir.
type Paths struct {
	Routes     string
	KnownHosts string
	Keys       string
	Stubs      string
	Key        string
	PubKey     string
	// LegacyStubs is a previous release's stubs dir (before the relay frontend
	// moved from the node to control). Accounts still homed there are removed at
	// reconcile so control re-creates them under Stubs with the right home, shell
	// and group. Empty disables the migration.
	LegacyStubs string
}

// DefaultPaths returns the on-disk relay locations used in production.
func DefaultPaths() Paths {
	return Paths{
		Routes:      relaypaths.Routes,
		KnownHosts:  relaypaths.KnownHosts,
		Keys:        relaypaths.Keys,
		Stubs:       relaypaths.Stubs,
		Key:         relaypaths.Key,
		PubKey:      relaypaths.PubKey,
		LegacyStubs: relaypaths.LegacyStubs,
	}
}

// Syncer reconciles the frontend to a spec. Construct one with the real unixuser
// service (or a fake, in tests) and the paths.
type Syncer struct {
	users StubOps
	paths Paths
}

// New builds a Syncer over the given user ops and paths.
func New(users StubOps, paths Paths) *Syncer {
	return &Syncer{users: users, paths: paths}
}

// EnsureKey generates the frontend's relay keypair (private + public) if it is
// not already on disk, so the deploy no longer has to run ssh-keygen. Returns
// the public key line control adds to remote apps' authorized_keys so the
// frontend can ssh in as the app user. The private key is root-only.
func (s *Syncer) EnsureKey() (string, error) {
	if _, err := os.Stat(s.paths.Key); err == nil {
		return s.pubKey(), nil // already generated on a previous run
	} else if !os.IsNotExist(err) {
		return "", err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, "hostit-relay")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(s.paths.Key), 0755); err != nil {
		return "", err
	}
	if err := writeFileAtomic(s.paths.Key, string(pem.EncodeToMemory(block)), 0600); err != nil {
		return "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " hostit-relay\n"
	if err := writeFileAtomic(s.paths.PubKey, line, 0644); err != nil {
		return "", err
	}
	return s.pubKey(), nil
}

// pubKey returns the frontend's relay public key (one line), or "" if not yet
// generated.
func (s *Syncer) pubKey() string {
	data, err := os.ReadFile(s.paths.PubKey)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// Apply writes the routing table, known_hosts and per-app authorized_keys from
// the spec, then reconciles the stub accounts to match. All files are root-owned
// so a tenant cannot alter their own frontend authorized_keys.
func (s *Syncer) Apply(spec *Spec) error {
	if spec == nil {
		spec = &Spec{}
	}
	if err := writeFileAtomic(s.paths.Routes, spec.Routes, 0644); err != nil {
		return err
	}
	if err := writeFileAtomic(s.paths.KnownHosts, spec.KnownHosts, 0644); err != nil {
		return err
	}
	if err := s.writeKeys(spec.AppKeys); err != nil {
		return err
	}
	s.reconcileStubs()
	return nil
}

// writeKeys writes each routed app's authorized_keys to a per-app file the stub
// reconcile reads, and removes files for apps no longer routed.
func (s *Syncer) writeKeys(appKeys map[string]string) error {
	if err := os.MkdirAll(s.paths.Keys, 0755); err != nil {
		return err
	}
	for app, keys := range appKeys {
		if !api.ValidName(app) {
			continue // never let an unexpected name reach a root-level path/user op
		}
		if err := writeFileAtomic(filepath.Join(s.paths.Keys, app), keys, 0644); err != nil {
			slog.Warn("Cannot write a relay keys file", "app", app, "error", err)
		}
	}
	entries, err := os.ReadDir(s.paths.Keys)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if _, ok := appKeys[e.Name()]; !ok && !strings.HasSuffix(e.Name(), ".tmp") {
			_ = os.Remove(filepath.Join(s.paths.Keys, e.Name()))
		}
	}
	return nil
}

// reconcileStubs makes the frontend's stub accounts match the relay routes. For
// every routed (remote) app it ensures a stub user exists with the app's current
// authorized_keys; stubs for apps no longer routed are removed. It only ever
// touches users homed under the stubs dir, so a bug here cannot harm a real app.
func (s *Syncer) reconcileStubs() {
	routed := readRoutedApps(s.paths.Routes)

	// Migration: a previous release homed stubs elsewhere (and in a different
	// group), when the frontend was the node. Remove those so the loop below
	// re-creates them under the current home, shell and group.
	s.removeStubsUnder(s.paths.LegacyStubs)

	// Ensure a stub + current keys for each routed app.
	for app := range routed {
		if !api.ValidName(app) {
			continue // a stub is a real Unix user; never create one from an unexpected name
		}
		if err := s.ensureStub(app); err != nil {
			slog.Warn("Cannot ensure relay stub", "app", app, "error", err)
		}
	}
	// Remove stubs (users homed under the stubs dir) for apps no longer routed.
	accounts, err := s.users.List()
	if err != nil {
		return
	}
	prefix := filepath.Clean(s.paths.Stubs) + string(filepath.Separator)
	for _, a := range accounts {
		if !strings.HasPrefix(filepath.Clean(a.Home)+string(filepath.Separator), prefix) {
			continue // not a relay stub
		}
		if routed[a.Name] {
			continue
		}
		_ = s.users.KillProcesses(a.Name)
		if err := s.users.Delete(a.Name); err != nil {
			slog.Warn("Cannot remove a stale relay stub", "app", a.Name, "error", err)
			continue
		}
		_ = os.RemoveAll(filepath.Join(s.paths.Stubs, a.Name))
	}
}

// removeStubsUnder removes every stub account a previous release homed under dir
// (each subdirectory is one stub's home). Directory-driven, NOT group-driven: the
// old node frontend put its stubs in a different group, so List() (scoped to the
// current group) would miss them. A no-op when dir is absent.
func (s *Syncer) removeStubsUnder(dir string) {
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := filepath.Clean(dir) + string(filepath.Separator)
	for _, e := range entries {
		name := e.Name()
		if !api.ValidName(name) {
			continue // never delete a user from an unexpected name
		}
		// Only delete a real account that is actually homed here, so a same-named
		// account that exists for another reason is left alone.
		if home, ok := s.users.Home(name); ok && strings.HasPrefix(filepath.Clean(home)+string(filepath.Separator), prefix) {
			_ = s.users.KillProcesses(name)
			if err := s.users.Delete(name); err != nil {
				slog.Warn("Cannot remove a legacy relay stub", "app", name, "error", err)
				continue
			}
		}
		_ = os.RemoveAll(filepath.Join(dir, name))
	}
}

// ensureStub creates the stub account if missing and writes its authorized_keys
// from the per-app keys file. All files are root-owned (the tenant cannot alter
// their own frontend authorized_keys); root ownership plus no group/world write
// satisfies sshd StrictModes.
func (s *Syncer) ensureStub(app string) error {
	home := filepath.Join(s.paths.Stubs, app)
	if !s.users.Exists(app) {
		if err := s.users.CreateStub(app, home); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0755); err != nil {
		return err
	}
	// Suppress the frontend host's MOTD on an interactive relay login (it would
	// otherwise leak the control host's banner and stats to the tenant).
	_ = os.WriteFile(filepath.Join(home, ".hushlogin"), nil, 0644)
	keys, _ := os.ReadFile(filepath.Join(s.paths.Keys, app)) // absent -> deny all
	akPath := filepath.Join(home, ".ssh", "authorized_keys")
	if cur, err := os.ReadFile(akPath); err == nil && string(cur) == string(keys) {
		return nil // unchanged
	}
	tmp := akPath + ".tmp"
	if err := os.WriteFile(tmp, keys, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, akPath)
}

// readRoutedApps returns the set of apps in the routes file (one "app<TAB>host"
// line each). Missing file -> empty set (which removes every stub).
func readRoutedApps(path string) map[string]bool {
	routed := make(map[string]bool)
	data, err := os.ReadFile(path)
	if err != nil {
		return routed
	}
	for _, line := range strings.Split(string(data), "\n") {
		if app, _, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok && app != "" {
			routed[app] = true
		}
	}
	return routed
}

// writeFileAtomic writes content to path via a temp file and rename, creating
// the parent directory first.
func writeFileAtomic(path, content string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
