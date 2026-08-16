// hostit-proxy is hostit's data plane: it terminates TLS with certificate
// material managed by hostit-control and routes each request straight to its
// app from a locally cached table -- so apps keep serving while control or a
// node daemon restarts. See docs and plans/260807-hostit-multinode.md.
package main

import (
	"fmt"
	"os"

	"heckel.io/hostit/proxyd"
)

// Set by goreleaser via ldflags (-X main.version=... etc.)
var (
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	app := proxyd.NewCLI()
	app.Version = version
	if commit != "" {
		app.Version = fmt.Sprintf("%s (%s, built %s)", version, commit, date)
	}
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err.Error())
		os.Exit(1)
	}
}
