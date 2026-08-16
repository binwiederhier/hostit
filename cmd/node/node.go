package main

import (
	"github.com/urfave/cli/v2"
	"heckel.io/hostit/config"
	"heckel.io/hostit/node"
)

// newNodeApp is the hostit-node command line: serve and join.
func newNodeApp(version string) *cli.App {
	return &cli.App{
		Name:  "hostit-node",
		Usage: "hostit's machine half: runs apps on this host and serves control's node RPC",
		Commands: []*cli.Command{
			{
				Name:  "serve",
				Usage: "Run the node daemon (requires root)",
				Action: func(c *cli.Context) error {
					return node.Serve(c.String("config"), version)
				},
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: config.DefaultServerConfigFile, Usage: "server config file (shared with hostit-control when colocated)"},
				},
			},
			{
				Name:  "join",
				Usage: "Enroll this machine with control: exchange a one-time join token for this node's mTLS certificate",
				Action: func(c *cli.Context) error {
					return node.Join(c.String("config"), c.String("control"), c.String("token"))
				},
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: config.DefaultServerConfigFile, Usage: "node config file"},
					&cli.StringFlag{Name: "control", Required: true, Usage: "control's node listener, host:port"},
					&cli.StringFlag{Name: "token", Required: true, Usage: "join token from `hostit-control node add`"},
				},
			},
		},
	}
}
