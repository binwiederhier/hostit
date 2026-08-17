package nodeconf

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A node's config is its own type, not control's: it carries the eight keys a
// machine needs and cannot express a control-plane setting at all. That is
// what keeps control's contract (admin tokens, base domains, OAuth) from
// leaking into a host that has no business holding it.
func TestLoadConfigReadsTheNodesOwnKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.yml")
	require.NoError(t, os.WriteFile(path, []byte(`
node-id: worker-2
control-url: 10.0.0.1:2930
node-cert-file: /etc/hostit/node/node.pem
node-key-file: /etc/hostit/node/node.key
cluster-ca-cert-file: /etc/hostit/node/cluster-ca.pem
apps-dir: /srv/apps
`), 0o600))

	conf, err := LoadConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "worker-2", conf.NodeID)
	assert.Equal(t, "10.0.0.1:2930", conf.ControlURL)
	assert.Equal(t, "/srv/apps", conf.AppsDir)
	// Unset keys default rather than failing.
	assert.Equal(t, "/var/lib/hostit", conf.DataDir)
	assert.Equal(t, "/run/hostit/hostit.sock", conf.SocketFile)
}

// The colocated node names nothing but where to dial: its id defaults to the
// local node, its credentials come from what control minted under data-dir.
func TestLoadConfigDefaultsForAColocatedNode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.yml")
	require.NoError(t, os.WriteFile(path, []byte("control-url: 127.0.0.1:2930\n"), 0o600))

	conf, err := LoadConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "local", conf.NodeID)
	assert.Empty(t, conf.NodeCertFile)
	require.NoError(t, conf.Validate())
}

// control-url names the same thing the proxy's does, and takes either the
// host:port this dials or a URL copied from the proxy's config.
func TestLoadConfigAcceptsAControlURLWithAScheme(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.yml")
	require.NoError(t, os.WriteFile(path, []byte("control-url: https://10.0.0.1:2930\n"), 0o600))

	conf, err := LoadConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.1:2930", conf.ControlURL, "the dial target is host:port; a scheme is tolerated")
}

// Pre-split configs said listen-node (control's key, reused for the node's
// dial target). Honor it so an existing node keeps connecting.
func TestLoadConfigHonorsTheRetiredListenNodeKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.yml")
	require.NoError(t, os.WriteFile(path, []byte("node-id: stage-node-2\nlisten-node: 127.0.0.1:2930\n"), 0o600))

	conf, err := LoadConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:2930", conf.ControlURL)
}

func TestValidateRequiresSomewhereToDialAndAWholeCertTriple(t *testing.T) {
	conf := NewConfig()
	require.ErrorContains(t, conf.Validate(), "control-url")

	conf.ControlURL = "127.0.0.1:2930"
	conf.NodeCertFile = "/etc/hostit/node/node.pem" // key and CA missing
	require.ErrorContains(t, conf.Validate(), "node-key-file")
}

// The allowlist is named for what it guards -- the app ports -- not for who
// happens to be on it. The proxy is the component that dials an app; control
// never does, so naming the key after the control plane described the wrong
// hop and invited the wrong addresses.
func TestAppsAllowedAddressesGuardAPublishedPort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.yml")
	require.NoError(t, os.WriteFile(path, []byte(`
node-id: worker-2
control-url: 10.0.0.1:2930
apps-bind-address: 10.0.0.2
apps-allowed-addresses:
  - 10.0.0.1
`), 0o600))

	conf, err := LoadConfig(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"10.0.0.1"}, conf.AppsAllowedAddresses)

	// Publishing off loopback with nobody allowed is refused by name.
	conf.AppsAllowedAddresses = nil
	assert.ErrorContains(t, conf.Validate(), "apps-allowed-addresses")
}
