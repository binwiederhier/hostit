package main

import (
	"github.com/urfave/cli/v2"
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
					&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: node.DefaultConfigFile, Usage: "node config file"},
				},
			},
		},
	}
}
