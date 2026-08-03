// hostit is a self-hosted mini-app platform: isolated apps as Unix users with
// SSH access, automatic subdomains and TLS, deployed via "hostit up".
package main

import (
	"fmt"
	"os"

	"heckel.io/hostit/cmd"
)

// Set by goreleaser via ldflags (-X main.version=... etc.)
var (
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	app := cmd.New()
	app.Version = version
	if commit != "" {
		app.Version = fmt.Sprintf("%s (%s, built %s)", version, commit, date)
	}
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err.Error())
		os.Exit(1)
	}
}
