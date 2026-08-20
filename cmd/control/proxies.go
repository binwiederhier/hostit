package main

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/urfave/cli/v2"

	"heckel.io/hostit/clitable"
	"heckel.io/hostit/cluster"
	"heckel.io/hostit/controlconf"
	"heckel.io/hostit/nodeapi"
	"heckel.io/hostit/nodelink"
	"heckel.io/hostit/store"
)

// cmdProxy is the hostit-control proxy registry: add (mint the proxy's mTLS
// certificate), list, remove (revoke). A proxy holds no apps, so unlike a node
// its row exists only to be the membership switch and a liveness record.
var cmdProxy = &cli.Command{
	Name:  "proxy",
	Usage: "Manage data-plane proxies (enrollment, listing, removal)",
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: controlconf.DefaultControlConfigFile, Usage: "control config file"},
	},
	Subcommands: []*cli.Command{
		{
			Name:      "add",
			Usage:     "Register a new proxy and mint its mTLS certificate",
			ArgsUsage: "<name>",
			Action:    execProxyAdd,
		},
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

func execProxyAdd(c *cli.Context) error {
	if c.NArg() != 1 {
		return errors.New("usage: hostit-control proxy add <name>")
	}
	name := c.Args().First()
	if name == store.ProxyLocal {
		return fmt.Errorf("%q is the colocated proxy; its certificate is minted automatically", name)
	}
	if !nodeapi.ValidName(name) {
		return fmt.Errorf("invalid proxy name %q", name)
	}
	conf, s, err := nodeStore(c)
	if err != nil {
		return err
	}
	defer s.Close()
	// Same cluster CA as a node's: one trust domain for the whole cluster, with
	// the role in the certificate deciding what the holder may register as.
	// A member on another machine dials the mTLS listener, so enrolling one
	// without that listener configured produces instructions that cannot work:
	// the printed control-url would carry no port at all.
	if conf.ListenCluster == "" {
		return fmt.Errorf("control accepts no remote members: set listen-cluster (e.g. 10.0.0.1:2930) and restart hostit-control first")
	}
	ca, err := nodelink.LoadCA(conf.DataDir)
	if err != nil {
		return fmt.Errorf("cannot load the cluster CA (has hostit-control started once?): %w", err)
	}
	cert, err := ca.Issue(name, cluster.RoleProxy)
	if err != nil {
		return err
	}
	certPEM, keyPEM, err := nodelink.EncodeCert(cert)
	if err != nil {
		return err
	}
	if err := s.EnsureProxy(name); err != nil {
		return err
	}
	fmt.Printf("Proxy %q registered. On the proxy machine, save the three PEM blocks below\n", name)
	fmt.Printf("(e.g. under /etc/hostit/proxy/) and point the proxy config at them:\n\n")
	fmt.Printf("  proxy-id: %s\n  control-url: <this-host>:%s\n", name, portOf(conf.ListenCluster))
	fmt.Printf("  proxy-cert-file: /etc/hostit/proxy/proxy.pem\n  proxy-key-file: /etc/hostit/proxy/proxy.key\n  cluster-ca-cert-file: /etc/hostit/proxy/cluster-ca.pem\n\n")
	fmt.Printf("# proxy.pem\n%s\n# proxy.key\n%s\n# cluster-ca.pem\n%s\n", certPEM, keyPEM, ca.CertPEM())
	return nil
}

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
	fmt.Println(clitable.Render([]string{"NAME", "VERSION", "ROUTES", "LAST SEEN"}, rows))
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
