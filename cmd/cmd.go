// Package cmd wires up the hostit CLI: the daemon ("serve"), the app-side deploy
// commands ("up", "down", ...) and the remote admin client ("admin ...").
package cmd

import (
	"github.com/urfave/cli/v2"
)

// New creates the hostit CLI application
func New() *cli.App {
	return &cli.App{
		Name:  "hostit",
		Usage: "self-hosted mini-app platform: isolated apps with SSH access, subdomains and TLS",
		Commands: []*cli.Command{
			cmdServe,
			cmdUp,
			cmdDown,
			cmdRestart,
			cmdStatus,
			cmdLogs,
			cmdInfo,
			cmdAdmin,
			cmdShell,
			cmdAgent,
		},
	}
}
