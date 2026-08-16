package node

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
	// legacyConfigFile is the pre-split shared file, still read when a node has
	// no config of its own yet (see Serve).
	legacyConfigFile = "/etc/hostit/server.yml"
	// localNodeID is the colocated node: control mints its credentials itself.
	localNodeID = "local"
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
	// ListenNode is the retired name for ControlURL, from when both halves
	// shared control's struct. Honored so an existing node keeps connecting.
	ListenNode string `yaml:"listen-node"`

	// Cluster credentials: this node's certificate and the CA both sides
	// trust, minted by `hostit-control node add`. Empty on a colocated node,
	// which reads what control minted under DataDir/ipc.
	NodeCertFile      string `yaml:"node-cert-file"`
	NodeKeyFile       string `yaml:"node-key-file"`
	ClusterCACertFile string `yaml:"cluster-ca-cert-file"`

	// DataDir holds the registry mirror control pushes (and the colocated
	// credentials); AppsDir is the btrfs pool the app subvolumes live in;
	// SocketFile is where the in-container CLI reaches this node.
	DataDir    string `yaml:"data-dir"`
	AppsDir    string `yaml:"apps-dir"`
	SocketFile string `yaml:"socket-file"`
}

// NewConfig returns a node config with the packaged defaults; only where to
// dial has none.
func NewConfig() *Config {
	return &Config{
		NodeID:     localNodeID,
		DataDir:    "/var/lib/hostit",
		AppsDir:    "/var/lib/hostit/apps",
		SocketFile: "/run/hostit/hostit.sock",
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
	if conf.ControlURL == "" {
		conf.ControlURL = conf.ListenNode // the retired key
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
	return nil
}
