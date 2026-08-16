// hostit-control is hostit's control plane: the web app, REST API, registry,
// placement and certificate management. The machine half is hostit-node; the
// data plane is hostit-proxy; the in-container PID 1 stays the hostit binary's
// agent. See plans/260807-hostit-multinode.md.
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
	app := newControlApp()
	app.Version = version
	if commit != "" {
		app.Version = fmt.Sprintf("%s (%s, built %s)", version, commit, date)
	}
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err.Error())
		os.Exit(1)
	}
}
