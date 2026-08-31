package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/urfave/cli/v2"

	"heckel.io/hostit/cmd/util"
	"heckel.io/hostit/control/config"
	"heckel.io/hostit/store"
)

// cmdNode is the hostit-control node registry: add (mint the node's mTLS
// certificate), list, remove (revoke). These act on the registry SQLite
// directly -- same host, root-only file, WAL keeps the daemon's writes safe
// alongside.
var (
	cmdNode = &cli.Command{
		Name:  "node",
		Usage: "Manage app-running nodes (enrollment, listing, removal)",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: config.DefaultControlConfigFile, Usage: "control config file"},
		},
		Subcommands: []*cli.Command{
			{
				Name:   "list",
				Usage:  "List registered nodes",
				Action: execNodeList,
			},
			{
				Name:      "remove",
				Usage:     "Unregister a node; its certificate stops being accepted",
				ArgsUsage: "<name>",
				Action:    execNodeRemove,
			},
		},
	}
)

func execNodeList(c *cli.Context) error {
	_, s, err := nodeStore(c)
	if err != nil {
		return err
	}
	defer s.Close()
	nodes, err := s.Nodes()
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		fmt.Println("No nodes.")
		return nil
	}
	rows := make([][]string, 0, len(nodes))
	for _, n := range nodes {
		seen := "never"
		if !n.LastSeen.IsZero() {
			seen = n.LastSeen.Format(time.RFC3339)
		}
		rows = append(rows, []string{n.Name, dashIfEmpty(n.Address), seen})
	}
	fmt.Println(util.Render([]string{"NAME", "ADDRESS", "LAST SEEN"}, rows))
	return nil
}

func execNodeRemove(c *cli.Context) error {
	if c.NArg() != 1 {
		return errors.New("usage: hostit-control node remove <name>")
	}
	_, s, err := nodeStore(c)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.RemoveNode(c.Args().First())
}

// nodeStore opens the config and registry the node commands act on.
func nodeStore(c *cli.Context) (*config.Config, *store.Store, error) {
	conf, err := config.LoadConfig(c.String("config"))
	if err != nil {
		return nil, nil, err
	}
	s, err := store.NewStore(filepath.Join(conf.DataDir, "hostit.db"))
	if err != nil {
		return nil, nil, err
	}
	return conf, s, nil
}

// portOf extracts the port half of a listen address for display.
func portOf(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[i+1:]
		}
	}
	return addr
}
