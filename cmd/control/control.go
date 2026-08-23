package main

import "github.com/urfave/cli/v2"

// newControlApp is the hostit-control CLI: the control plane as its own binary
// and systemd service. The daemon that used to be one fused "hostit serve" lives here;
// the hostit binary keeps the CLI and the in-container agent.
func newControlApp(version string) *cli.App {
	return &cli.App{
		Name:  "hostit-control",
		Usage: "hostit's control plane: web app, REST API, registry, placement, certificates",
		// The version is load-bearing, not decoration: serve stamps it into
		// every container this daemon creates and records it as the agents'
		// version, so an empty one would make the stale-agent check match
		// forever and agents would stop being restarted on upgrades.
		Version:              version,
		Commands:             []*cli.Command{cmdServe, cmdStatus, cmdApp, cmdNode, cmdProxy, cmdConnections},
		EnableBashCompletion: true,
	}
}

// cmdConnections is the operator side of credential storage. Everything else
// about a connection belongs to its owner; only re-keying is the operator's.
var cmdConnections = &cli.Command{
	Name:  "connections",
	Usage: "Operator actions on stored credentials",
	Subcommands: []*cli.Command{
		{
			Name:   "rotate-key",
			Usage:  "Re-seal every stored credential under a fresh key",
			Action: execConnectionsRotateKey,
		},
	},
}
