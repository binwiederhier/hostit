package main

import (
	"github.com/urfave/cli/v2"
	"heckel.io/hostit/proxy"
)

// newProxyApp is the hostit-proxy command line: one job, serve.
func newProxyApp(version string) *cli.App {
	return &cli.App{
		Name:    "hostit-proxy",
		Version: version,
		Usage:   "hostit's data plane: terminates TLS and routes to apps from a cached table",
		Commands: []*cli.Command{{
			Name:  "serve",
			Usage: "Run the proxy",
			Action: func(c *cli.Context) error {
				proxy.Version = c.App.Version // reported in the proxy's heartbeat
				return proxy.Serve(c.String("config"))
			},
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: proxy.DefaultConfigFile, Usage: "proxy config file"},
			},
		}},
	}
}
