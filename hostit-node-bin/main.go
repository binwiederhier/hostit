// hostit-node is hostit's machine half: it owns this host's app containers,
// unix users, btrfs subvolumes and firewall rules, and serves control's
// NodeAgent RPC over one mTLS connection it dials itself.
package main

import (
	"fmt"
	"os"

	"heckel.io/hostit/noded"
)

// Set by goreleaser via ldflags (-X main.version=... etc.)
var (
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	app := noded.NewCLI()
	app.Version = version
	if commit != "" {
		app.Version = fmt.Sprintf("%s (%s, built %s)", version, commit, date)
	}
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err.Error())
		os.Exit(1)
	}
}
