// Package apptest provides no-op test doubles for the app package, kept out of
// production code: NewNopServices builds an app.Services whose host-touching
// members (users, ssh keys, firewall) do nothing, so a test can build a Manager
// without touching the host.
package apptest

import (
	"os"

	"heckel.io/hostit/app"
	"heckel.io/hostit/btrfs"
	"heckel.io/hostit/container"
	"heckel.io/hostit/firewall"
	"heckel.io/hostit/run"
	"heckel.io/hostit/systemd"
)

// NewNopServices returns an app.Services that touches nothing: btrfs, systemd and
// container run on a no-op runner, and the privileged services (users, ssh keys,
// firewall) are no-op fakes.
func NewNopServices() *app.Services {
	runner := run.Nop{}
	return &app.Services{
		Btrfs:     btrfs.New(runner),
		Systemd:   systemd.New(runner),
		Container: container.New(runner),
		User:      nopUser{},
		SSH:       nopSSH{},
		Firewall:  nopFirewall{},
		Runner:    runner,
	}
}

// nopUser is a unixuser.Interface that does nothing; useful for tests and dry runs
type nopUser struct{}

func (nopUser) Exists(username string) bool { return false }

func (nopUser) LookupUID(username string) (int, error) { return 1001, nil }

func (nopUser) LookupIDs(username string) (uid, gid int, err error) { return 1001, 1001, nil }

func (nopUser) Create(username, home string, uid int) error { return nil }

func (nopUser) SetHome(username, home string) error { return nil }

func (nopUser) Rename(oldName, newName string) error { return nil }

func (nopUser) KillProcesses(username string) error { return nil }

func (nopUser) Delete(username string) error { return nil }

func (nopUser) WriteSkeleton(username, home string, files map[string]string) error { return nil }

func (nopUser) ChownIn(root *os.Root, username, rel string) error { return nil }

// nopSSH is an ssh.Interface that does nothing
type nopSSH struct{}

func (nopSSH) WriteAuthorizedKeys(root *os.Root, username string, keys []string) error { return nil }

// nopFirewall is a firewall.Interface that does nothing
type nopFirewall struct{}

func (nopFirewall) Apply(rules []firewall.Rule) error { return nil }
