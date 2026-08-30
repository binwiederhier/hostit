// Package nodeconf is the hostit-node configuration, kept apart from the node
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
	// DefaultNodeSocketFile is the node's root-only status socket, next to the
	// app socket and control's socket under /run/hostit.
	DefaultNodeSocketFile = "/run/hostit/node.sock"
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
	// trust, minted by `hostit-control node add`. Empty on a colocated node,
	// which reads what control minted under DataDir/ipc.
	NodeCertFile      string `yaml:"node-cert-file"`
	NodeKeyFile       string `yaml:"node-key-file"`
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
	// SSHHostKeyFile is this node's sshd public host key, reported to control for
	// the relay gateway's known_hosts. Defaults to the standard ed25519 host key.
	SSHHostKeyFile string `yaml:"ssh-host-key-file"`
	// ListenMetrics is an optional Prometheus /metrics listener (empty = off).
	ListenMetrics string `yaml:"listen-metrics"`
	// Relay gateway (frontend) paths -- where control writes routes/keys and
	// where the frontend stub accounts live. Empty disables the stub reconcile.
	SSHRoutesFile string `yaml:"ssh-routes-file"`
	RelayKeysDir  string `yaml:"relay-keys-dir"`
	RelayStubsDir string `yaml:"relay-stubs-dir"`
	// AppsAllowedAddresses are the addresses allowed to reach a published app
	// port -- in practice the proxies, which are what dials an app; control
	// never does. Ignored on a loopback node; required on a remote one, or its
	// apps would be reachable from anything that can route to it.
	AppsAllowedAddresses []string `yaml:"apps-allowed-addresses"`
	// LocalProxyUID is the uid a colocated unprivileged hostit-proxy runs as, so
	// the per-app loopback firewall admits it alongside root and the app itself.
	// 0 (the default) means the proxy is root.
	LocalProxyUID int `yaml:"local-proxy-uid"`

	// DataDir holds the registry mirror control pushes (and the colocated
	// credentials); AppsDir is the btrfs pool the app subvolumes live in;
	// SocketFile is the HOST path the node serves the app socket on. It lives in
	// its own subdir (default /run/hostit/app) so ONLY that subdir is mounted
	// into a container -- the container reaches it at /run/hostit/hostit.sock,
	// while apps-raw and the operator sockets, a level up, stay out of reach.
	DataDir    string `yaml:"data-dir"`
	AppsDir    string `yaml:"apps-dir"`
	SocketFile string `yaml:"socket-file"`
	// NodeSocketFile is the node's own root-only status socket, which is what
	// `hostit node status` reads. Named like control's control-socket-file: the
	// unprefixed socket-file already means the app socket.
	NodeSocketFile string `yaml:"node-socket-file"`
}

// NewConfig returns a node config with the packaged defaults; only where to
// dial has none.
func NewConfig() *Config {
	return &Config{
		NodeID:         localNodeID,
		DataDir:        "/var/lib/hostit",
		AppsDir:        "/var/lib/hostit/apps",
		SocketFile:     "/run/hostit/app/hostit.sock",
		NodeSocketFile: DefaultNodeSocketFile,
		SSHHostKeyFile: "/etc/ssh/ssh_host_ed25519_key.pub",
		SSHRoutesFile:  "/var/lib/hostit/ssh-routes",
		RelayKeysDir:   "/var/lib/hostit/relay-keys",
		RelayStubsDir:  "/var/lib/hostit/relay-stubs",
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
	for _, f := range []string{c.NodeCertFile, c.NodeKeyFile, c.ClusterCACertFile} {
		if f != "" {
			set++
		}
	}
	if set != 0 && set != 3 {
		return errors.New("node-cert-file, node-key-file and cluster-ca-cert-file must be set together")
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
