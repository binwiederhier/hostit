package main

import "github.com/urfave/cli/v2"

// newControlApp is the hostit-control CLI: the control plane as its own binary
// and systemd service. The daemon that used to be "hostit serve" lives here;
// the hostit binary keeps the CLI and the in-container agent.
func newControlApp() *cli.App {
	return &cli.App{
		Name:     "hostit-control",
		Usage:    "hostit's control plane: web app, REST API, registry, placement, certificates",
		Commands: []*cli.Command{cmdServe, cmdNode},
	}
}
