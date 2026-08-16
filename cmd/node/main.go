// hostit-node is hostit's machine half: it owns this host's app containers,
// unix users, btrfs subvolumes and firewall rules, and serves control's
// NodeAgent RPC over one mTLS connection it dials itself.
package main

import (
	"fmt"
	"os"
)

// Set by goreleaser via ldflags (-X main.version=... etc.)
var (
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	ver := version
	if commit != "" {
		ver = fmt.Sprintf("%s (%s, built %s)", version, commit, date)
	}
	app := newNodeApp(ver)
	app.Version = ver
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err.Error())
		os.Exit(1)
	}
}
