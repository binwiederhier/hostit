package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/urfave/cli/v2"

	"heckel.io/hostit/cluster"
	"heckel.io/hostit/cmd/util"
	"heckel.io/hostit/control/config"
	"heckel.io/hostit/node/link"
	"heckel.io/hostit/nodeapi"
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
				Name:      "add",
				Usage:     "Register a new node and mint its mTLS certificate",
				ArgsUsage: "<name>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "address", Usage: "optional: the node's address, which it otherwise reports itself on connect"},
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
)

func execNodeAdd(c *cli.Context) error {
	if c.NArg() != 1 {
		return errors.New("usage: hostit-control node add <name>")
	}
	name := c.Args().First()
	if name == store.HostLocal {
		return fmt.Errorf("%q is the colocated node; its certificate is minted automatically", name)
	}
	if !nodeapi.ValidName(name) {
		return fmt.Errorf("invalid node name %q", name)
	}
	conf, s, err := nodeStore(c)
	if err != nil {
		return err
	}
	defer s.Close()
	// Mint the node's identity from the cluster CA. Possession of this pair is
	// membership (plus the registry row below); there is no join protocol.
	// A member on another machine dials the mTLS listener, so enrolling one
	// without that listener configured produces instructions that cannot work:
	// the printed control-url would carry no port at all.
	if conf.ListenCluster == "" {
		return fmt.Errorf("control accepts no remote members: set listen-cluster (e.g. 10.0.0.1:2930) and restart hostit-control first")
	}
	ca, err := link.LoadCA(conf.DataDir)
	if err != nil {
		return fmt.Errorf("cannot load the cluster CA (has hostit-control started once?): %w", err)
	}
	cert, err := ca.Issue(name, cluster.RoleNode)
	if err != nil {
		return err
	}
	certPEM, keyPEM, err := link.EncodeCert(cert)
	if err != nil {
		return err
	}
	if err := s.EnsureNode(name, c.String("address")); err != nil {
		return err
	}
	fmt.Printf("Node %q registered. It reports its own address when it connects,\n", name)
	fmt.Printf("so this is all it needs. On the new machine, save the three PEM blocks below\n")
	fmt.Printf("(e.g. under /etc/hostit/) and point the node config at them:\n\n")
	fmt.Printf("  node-id: %s\n  control-url: <this-host>:%s\n", name, portOf(conf.ListenCluster))
	fmt.Printf("  node-cert-file: /etc/hostit/node.pem\n  node-key-file: /etc/hostit/node.key\n  cluster-ca-cert-file: /etc/hostit/cluster-ca.pem\n\n")
	fmt.Printf("# node.pem\n%s\n# node.key\n%s\n# cluster-ca.pem\n%s\n", certPEM, keyPEM, ca.CertPEM())
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
	conf, err := config.LoadConfig(config.ResolveConfigFile(c.String("config"), config.LegacyServerConfigFile))
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
