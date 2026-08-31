// Package users names the system users hostit's services run as and resolves
// their uids. The names are fixed by the package, not configured: a deployment
// always runs control as hostit-control and the proxy as hostit-proxy, each
// owning only its own state so the internet-facing proxy cannot read control's
// registry, CA or secrets. The node stays root -- it does the machine work
// (podman, btrfs, nftables, unix users) that needs it; hostit-node exists for
// symmetry and future use.
package users

import (
	"os/user"
	"strconv"
)

const (
	Control = "hostit-control"
	Proxy   = "hostit-proxy"
	Node    = "hostit-node"
)

// UID returns the uid of a system user by name, or -1 if the user does not
// exist. Callers read -1 as "no such colocated member on this host" -- e.g. the
// node admits the local proxy's uid at the firewall only when the user is
// present, and a remote or root proxy leaves it absent.
func UID(name string) int {
	u, err := user.Lookup(name)
	if err != nil {
		return -1
	}
	id, err := strconv.Atoi(u.Uid)
	if err != nil {
		return -1
	}
	return id
}
