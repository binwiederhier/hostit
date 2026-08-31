package main

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/urfave/cli/v2"

	"heckel.io/hostit/cmd/util"
	"heckel.io/hostit/control/config"
)

// cmdProxy is the hostit-control proxy registry: add (mint the proxy's mTLS
// certificate), list, remove (revoke). A proxy holds no apps, so unlike a node
// its row exists only to be the membership switch and a liveness record.
var (
	cmdProxy = &cli.Command{
		Name:  "proxy",
		Usage: "Manage data-plane proxies (enrollment, listing, removal)",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: config.DefaultControlConfigFile, Usage: "control config file"},
		},
		Subcommands: []*cli.Command{
			{
				Name:   "list",
				Usage:  "List registered proxies",
				Action: execProxyList,
			},
			{
				Name:      "remove",
				Usage:     "Unregister a proxy; its certificate stops being accepted",
				ArgsUsage: "<name>",
				Action:    execProxyRemove,
			},
		},
	}
)

func execProxyList(c *cli.Context) error {
	_, s, err := nodeStore(c)
	if err != nil {
		return err
	}
	defer s.Close()
	proxies, err := s.Proxies()
	if err != nil {
		return err
	}
	if len(proxies) == 0 {
		fmt.Println("No proxies.")
		return nil
	}
	rows := make([][]string, 0, len(proxies))
	for _, p := range proxies {
		seen := "never"
		if !p.LastSeen.IsZero() {
			seen = p.LastSeen.Format(time.RFC3339)
		}
		// The version, not the whole build string: the commit and timestamp make
		// the row wrap (see `status` for the same trim).
		rows = append(rows, []string{p.Name, dashIfEmpty(shortVersion(p.Version)), strconv.Itoa(p.Routes), seen})
	}
	fmt.Println(util.Render([]string{"NAME", "VERSION", "ROUTES", "LAST SEEN"}, rows))
	return nil
}

func execProxyRemove(c *cli.Context) error {
	if c.NArg() != 1 {
		return errors.New("usage: hostit-control proxy remove <name>")
	}
	name := c.Args().First()
	_, s, err := nodeStore(c)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.DeleteProxy(name); err != nil {
		return err
	}
	fmt.Printf("Proxy %q unregistered. Its certificate is no longer accepted, and the\n", name)
	fmt.Printf("running daemon drops its session at the next heartbeat (within 30s).\n")
	return nil
}
