package app

import (
	"os"

	"heckel.io/hostit/firewall"
	"heckel.io/hostit/ssh"
	"heckel.io/hostit/unixuser"
)

const (
	// userShellFile is the login shell for app users; it execs the SSH session
	// into the app container (see cmd/shell.go). Also used by exec.go's terminal.
	userShellFile = "/usr/bin/hostit-shell"
	// AppsGroup owns the sudoers grant that lets app users enter their own
	// container (and nothing else); see /etc/sudoers.d/hostit
	AppsGroup = "hostit-apps"
)

// systemOps is the real SystemOps implementation: a thin facade that composes the
// tool-scoped service packages (unixuser accounts, ssh keys, firewall port rules)
// and delegates to them, converting app-level types at the boundary. It must run as
// root. The SystemOps interface remains the Manager's injection seam so tests fake
// these operations wholesale.
type systemOps struct {
	firewall *firewall.Service
	ssh      *ssh.Service
	user     *unixuser.Service
}

var _ SystemOps = (*systemOps)(nil)

// NewSystemOps returns the real, root-requiring SystemOps implementation
func NewSystemOps() SystemOps {
	return &systemOps{
		firewall: firewall.New(),
		ssh:      ssh.New(),
		user:     unixuser.New(userShellFile, AppsGroup, homeMode),
	}
}

func (o *systemOps) UserExists(username string) bool { return o.user.Exists(username) }

func (o *systemOps) LookupUID(username string) (int, error) { return o.user.LookupUID(username) }

// LookupIDs returns the app's contiguous id block: its uid/gid (which become
// container root) and the block size that runs up from there.
func (o *systemOps) LookupIDs(username string) (IDs, error) {
	uid, gid, err := o.user.LookupIDs(username)
	if err != nil {
		return IDs{}, err
	}
	return IDs{UID: uid, GID: gid, Count: uidBlockSize}, nil
}

func (o *systemOps) CreateUser(username, home string, uid int) error {
	return o.user.Create(username, home, uid)
}

func (o *systemOps) RemapUser(username, home string, uid int) error {
	return o.user.Remap(username, home, uid)
}

func (o *systemOps) SetUserHome(username, home string) error {
	return o.user.SetHome(username, home)
}

func (o *systemOps) RenameUser(oldName, newName string) error {
	return o.user.Rename(oldName, newName)
}

func (o *systemOps) SyncGroupName(username string) error {
	return o.user.SyncGroupName(username)
}

func (o *systemOps) KillUserProcesses(username string) error {
	return o.user.KillProcesses(username)
}

func (o *systemOps) DeleteUser(username string) error {
	return o.user.Delete(username)
}

func (o *systemOps) WriteScaffold(username, home string, files map[string]string) error {
	return o.user.WriteScaffold(username, home, files)
}

func (o *systemOps) ChownToUserIn(root *os.Root, username, rel string) error {
	return o.user.ChownIn(root, username, rel)
}

// WriteAuthorizedKeys updates the app's authorized_keys; the managed-block merge
// and root-scoped write live in the ssh package.
func (o *systemOps) WriteAuthorizedKeys(username, home string, keys []string) error {
	return o.ssh.WriteAuthorizedKeys(username, home, keys)
}

// ApplyPortRules restricts each app port on loopback to root and the owning uid.
// The nftables work lives in the firewall package; this converts the app-level rule
// type and delegates.
func (o *systemOps) ApplyPortRules(rules []PortRule) error {
	fw := make([]firewall.Rule, len(rules))
	for i, r := range rules {
		fw[i] = firewall.Rule{Port: r.Port, UID: r.UID}
	}
	return o.firewall.Apply(fw)
}
