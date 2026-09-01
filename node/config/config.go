// Package config is the hostit-node configuration, kept apart from the node
// itself so a reader (the CLI resolving the daemon's socket) does not link the
// machine stack -- btrfs, podman, unixuser, nftables -- just to parse a file.
package config

import (
	"errors"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	// DefaultConfigFile is where a node's config lives on a package install;
	// like every component, it owns a directory under /etc/hostit.
	DefaultConfigFile = "/etc/hostit/node/node.yml"
	// localNodeID is the colocated node: control mints its credentials itself.
	localNodeID = "local"
	// DefaultNodeSocketFile is the node's root-only status socket, in the node's
	// own run dir alongside the app socket subdir.
	DefaultNodeSocketFile = "/run/hostit/node/node.sock"
)

// Config is the node's OWN configuration -- deliberately not the control
// plane's. A node holds no admin token, no base domain, no OAuth or TLS
// settings: it dials control and does what it is told, so the type cannot
// even express a control-plane setting. (Sharing one struct is what let
// control's required fields leak into a node's startup.)
type Config struct {
	// NodeID is this node's identity: the CN of its certificate and its name
	// in control's registry. "local" is the colocated node.
	NodeID string `yaml:"node-id"`
	// ControlURL is where control accepts node dial-ins, as host:port. Named
	// like the proxy's key because it is the same thing: a node DIALS control
	// and never listens itself. A URL is accepted and reduced to host:port.
	ControlURL string `yaml:"control-url"`

	// Cluster credentials: this node's certificate and the CA both sides
	// trust. Required on a REMOTE node (its control-url is a host:port dialing
	// control's mTLS listener); the CA-signed cert IS this node's membership.
	// Empty and unused on a colocated node, which reaches control over the unix
	// socket (control-url: unix:/run/hostit/control/cluster.sock).
	ClusterCertFile   string `yaml:"cluster-cert-file"`
	ClusterKeyFile    string `yaml:"cluster-key-file"`
	ClusterCACertFile string `yaml:"cluster-ca-cert-file"`

	// AppsBindAddress is where this node publishes app ports. Empty means
	// loopback, which is right when the proxy shares the host. A REMOTE node
	// sets its own private address: the proxy dials apps over the network, and
	// a loopback-published port is unreachable from another machine.
	AppsBindAddress string `yaml:"apps-bind-address"`
	// SSHHost is the hostname clients use to SSH to apps on THIS node. Reported
	// to control, which advertises it in an app's SSH command. Empty leaves
	// control advertising its base domain -- right for a colocated node, but a
	// remote node must set its own reachable address or its apps' SSH lands on
	// control, where the app user does not exist.
	SSHHost string `yaml:"ssh-host"`
	// ListenMetrics is an optional Prometheus /metrics listener (empty = off).
	ListenMetrics string `yaml:"listen-metrics"`
	// AppsAllowedAddresses are the addresses allowed to reach a published app
	// port -- in practice the proxies, which are what dials an app; control
	// never does. Ignored on a loopback node; required on a remote one, or its
	// apps would be reachable from anything that can route to it.
	AppsAllowedAddresses []string `yaml:"apps-allowed-addresses"`
	// OutboundAllowPrivateCIDRs is the node-side twin of control's flag of the
	// same name: internal CIDRs an app container MAY reach despite the default
	// egress block (the cloud metadata endpoint and all private space are dropped
	// otherwise -- an app must not read node metadata or dial another app's
	// backend). Set it to a corporate internal service or a private DNS resolver
	// apps need. Whitelist NARROWLY: a whole node network re-exposes apps to each
	// other. Drive it and control's outbound-allow-private-cidrs from one source.
	OutboundAllowPrivateCIDRs []string `yaml:"outbound-allow-private-cidrs"`

	// DataDir holds the registry mirror control pushes (and the colocated
	// credentials); AppsDir is the btrfs pool the app subvolumes live in;
	// SocketFile is the HOST path the node serves the app socket on. It lives in
	// its own subdir (default /run/hostit/node/app) so ONLY that subdir is mounted
	// into a container -- the container reaches it at /run/hostit/hostit.sock,
	// while apps-raw, a level up in the node's run dir, stays out of reach.
	DataDir    string `yaml:"data-dir"`
	AppsDir    string `yaml:"apps-dir"`
	SocketFile string `yaml:"socket-file"`
}

// NewConfig returns a node config with the packaged defaults; only where to
// dial has none.
func NewConfig() *Config {
	return &Config{
		NodeID:     localNodeID,
		DataDir:    "/var/lib/hostit/node",
		AppsDir:    "/var/lib/hostit/node/apps",
		SocketFile: "/run/hostit/node/app/hostit.sock",
	}
}

// LoadConfig reads a node config file over the defaults.
func LoadConfig(path string) (*Config, error) {
	conf := NewConfig()
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(b, conf); err != nil {
		return nil, err
	}
	conf.ControlURL = dialTarget(conf.ControlURL)
	return conf, nil
}

// dialTarget reduces a control-url to the host:port the mTLS dial needs, so a
// scheme copied from the proxy's config does not become an opaque dial error.
func dialTarget(url string) string {
	if _, rest, found := strings.Cut(url, "://"); found {
		return strings.TrimSuffix(rest, "/")
	}
	return url
}

// Validate checks what a node cannot start without.
func (c *Config) Validate() error {
	if c.ControlURL == "" {
		return errors.New("control-url is required: where this node dials control, e.g. 127.0.0.1:2930")
	}
	if c.NodeID == "" {
		return errors.New("node-id is required")
	}
	if c.DataDir == "" || c.AppsDir == "" || c.SocketFile == "" {
		return errors.New("data-dir, apps-dir and socket-file are required")
	}
	// All-or-none: a half-configured triple would otherwise surface later as an
	// opaque TLS failure at dial time.
	set := 0
	for _, f := range []string{c.ClusterCertFile, c.ClusterKeyFile, c.ClusterCACertFile} {
		if f != "" {
			set++
		}
	}
	if set != 0 && set != 3 {
		return errors.New("cluster-cert-file, cluster-key-file and cluster-ca-cert-file must be set together")
	}
	// Publishing off loopback without naming who may connect would put every
	// app's port on whatever networks this node is attached to.
	if c.AppsBindAddress != "" && len(c.AppsAllowedAddresses) == 0 {
		return errors.New("apps-bind-address requires apps-allowed-addresses: who may reach a published app port")
	}
	return nil
}

// LogUnitName is the systemd unit whose journal the admin "node logs" view
// reads. Fixed by the package: the unit is always hostit-node.
func (c *Config) LogUnitName() string {
	return "hostit-node"
}
