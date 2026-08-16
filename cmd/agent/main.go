// hostit is a self-hosted mini-app platform: isolated apps as Unix users with
// SSH access, automatic subdomains and TLS, deployed via "hostit up".
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
	// No node.Version here: this binary is the CLI and the in-container agent;
	// it creates no containers, and importing the machine stack to set a string
	// it never reads put btrfs, podman, nftables and unixuser inside every app
	// container's bind mount.
	app := New(ver)
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err.Error())
		os.Exit(1)
	}
}
