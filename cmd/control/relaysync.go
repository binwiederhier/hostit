package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/urfave/cli/v2"

	"heckel.io/hostit/system/relay"
	"heckel.io/hostit/system/unixuser"
)

const (
	// relayStubShell is the login shell every relay stub gets: it hands the ssh
	// session to the forwarder via sudo. relayStubGroup scopes the sudoers grant
	// that lets a stub reach the forwarder, and the sshd forwarding lockdown.
	relayStubShell = "/usr/lib/hostit/bin/hostit-relay-shell"
	relayStubGroup = "hostit-relay"
)

// cmdRelaySync is the root reconcile the control plane drives via sudo. It reads
// a relay.Spec (JSON) on stdin, ensures the relay key, applies the spec (routes,
// known_hosts, per-app keys, and the stub accounts), and prints the relay public
// key so control can add it to remote apps' authorized_keys. Kept a one-shot
// helper (not a daemon) so the only root surface is a single sudoers grant.
var cmdRelaySync = &cli.Command{
	Name:   "relay-sync",
	Hidden: true,
	Action: execRelaySync,
}

func execRelaySync(c *cli.Context) error {
	var spec relay.Spec
	if err := json.NewDecoder(os.Stdin).Decode(&spec); err != nil {
		return fmt.Errorf("cannot read relay spec: %w", err)
	}
	syncer := relay.New(stubUsers{unixuser.New(relayStubShell, relayStubGroup)}, relay.DefaultPaths())
	pub, err := syncer.EnsureKey()
	if err != nil {
		return fmt.Errorf("cannot ensure relay key: %w", err)
	}
	if err := syncer.Apply(&spec); err != nil {
		return fmt.Errorf("cannot apply relay spec: %w", err)
	}
	fmt.Println(pub) // control reads this to key remote apps' authorized_keys
	return nil
}

// stubUsers adapts unixuser.Service to relay.StubOps: only the List return type
// differs (relay keeps its own Account type so it does not import unixuser).
type stubUsers struct{ *unixuser.Service }

func (s stubUsers) List() ([]relay.Account, error) {
	accts, err := s.Service.List()
	if err != nil {
		return nil, err
	}
	out := make([]relay.Account, len(accts))
	for i, a := range accts {
		out[i] = relay.Account{Name: a.Name, Home: a.Home}
	}
	return out, nil
}
