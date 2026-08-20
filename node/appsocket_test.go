package node

import (
	"testing"
)

// The app socket belongs to the NODE, not to control.
//
// Today hostit-control serves /run/hostit/hostit.sock, so a host running only
// hostit-node has none: apps placed there lose SSH (the login shell calls the
// socket before it greets anyone), the whole in-container CLI, the MCP bridge
// and connection tokens. Prod is single-host so it has never shown there; stage
// reproduces it exactly. See TODO.md (high priority) and
// docs/slides/presentations/app-api.md.
//
// The contract these tests pin:
//
//  1. The node listens on its configured socket path, on EVERY host -- one code
//     path, not "only when control is elsewhere", because two paths for two
//     deployment shapes is what produced this bug.
//  2. It authenticates the caller by SO_PEERCRED and maps the uid to an app from
//     its own mirror registry, which already carries App.UID.
//  3. It RELAYS to control rather than answering locally. The node implements
//     deploy and logs itself, and answering them here would bypass control's
//     guards -- an archived app would become deployable from inside its own
//     container.
//  4. Control accepts the relayed request only over the cluster link, and only
//     for an app the calling node actually hosts (the check nodelink's callback
//     handler already makes).

func TestNodeServesTheAppSocket(t *testing.T) {
	t.Skip("TODO: the node does not serve the app socket yet -- this is the contract it has to meet")
}

func TestNodeMapsPeerUIDToItsApp(t *testing.T) {
	t.Skip("TODO: peercred -> uid -> app from the node's mirror registry")
}

func TestNodeRelaysRatherThanAnswering(t *testing.T) {
	t.Skip("TODO: an archived app must still be refused, which only control knows")
}

func TestControlRefusesTheRelayOffTheClusterLink(t *testing.T) {
	t.Skip("TODO: the same identity arriving on the public API must be refused")
}
