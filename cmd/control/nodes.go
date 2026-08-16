package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/urfave/cli/v2"
	"heckel.io/hostit/config"
	"heckel.io/hostit/node"
	"heckel.io/hostit/store"
)

const (
	// joinTokenTTL is how long a minted join token stays redeemable; enrollment
	// is an operator copy-pasting between two shells, not an automation window.
	joinTokenTTL = time.Hour
)

// cmdNode is the hostit-control node registry: add (mint a join token), list,
// remove (revoke). These act on the registry SQLite directly -- same host,
// root-only file, WAL keeps the daemon's writes safe alongside.
var cmdNode = &cli.Command{
	Name:  "node",
	Usage: "Manage app-running nodes (enrollment, listing, removal)",
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: config.DefaultServerConfigFile, Usage: "server config file"},
	},
	Subcommands: []*cli.Command{
		{
			Name:      "add",
			Usage:     "Register a new node and mint its one-time join token",
			ArgsUsage: "<name>",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "address", Usage: "the node's IP or hostname, as the proxy will reach its apps"},
			},
			Action: execNodeAdd,
		},
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

func execNodeAdd(c *cli.Context) error {
	if c.NArg() != 1 {
		return errors.New("usage: hostit-control node add <name>")
	}
	name := c.Args().First()
	if name == store.HostLocal {
		return fmt.Errorf("%q is the colocated node; it needs no enrollment", name)
	}
	conf, s, err := nodeStore(c)
	if err != nil {
		return err
	}
	defer s.Close()
	token, err := mintNodeJoinToken(s, conf.DataDir, name, c.String("address"))
	if err != nil {
		return err
	}
	fmt.Printf("Node %q registered. On the new machine, run:\n\n", name)
	fmt.Printf("  hostit-node join --control <this-host>:%s --token %s\n\n", portOf(conf.ListenNode), token)
	fmt.Printf("The token is single-use and expires in %s.\n", joinTokenTTL)
	return nil
}

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
	for _, n := range nodes {
		status := "pending"
		if !n.JoinedAt.IsZero() {
			status = "joined " + n.JoinedAt.Format("2006-01-02")
		}
		seen := "never"
		if !n.LastSeen.IsZero() {
			seen = n.LastSeen.Format(time.RFC3339)
		}
		fmt.Printf("%-20s %-20s %-18s last seen %s\n", n.Name, n.Address, status, seen)
	}
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

// mintNodeJoinToken registers the pending node and returns the one-time token
// (the registry keeps only the hash).
func mintNodeJoinToken(s *store.Store, dataDir, name, address string) (string, error) {
	ca, err := node.LoadCA(dataDir)
	if err != nil {
		return "", fmt.Errorf("cannot load the node CA (has hostit-control started once?): %w", err)
	}
	token, hash, err := node.MintJoinToken(name, ca)
	if err != nil {
		return "", err
	}
	if err := s.CreateNode(name, address, hash, time.Now().Add(joinTokenTTL)); err != nil {
		return "", err
	}
	return token, nil
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
