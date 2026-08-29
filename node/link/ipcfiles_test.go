package link

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/cluster"
	"heckel.io/hostit/node/config"
)

// Cluster identity comes from configuration: a node (or control) points at its
// cert/key pair and the cluster CA, and that is the whole trust setup -- no
// enrollment protocol. Unset, both fall back to the auto-minted colocated
// files under <data-dir>/ipc, so a single-host split needs no config at all.
func TestCredsFromConfiguredFiles(t *testing.T) {
	t.Parallel()
	ca, err := cluster.NewCA()
	require.NoError(t, err)
	cert, err := ca.Issue("worker-9", cluster.RoleNode)
	require.NoError(t, err)
	certPEM, keyPEM, err := EncodeCert(cert)
	require.NoError(t, err)
	dir := t.TempDir()
	caFile := filepath.Join(dir, "cluster-ca.pem")
	certFile := filepath.Join(dir, "worker-9.pem")
	keyFile := filepath.Join(dir, "worker-9.key")
	require.NoError(t, os.WriteFile(caFile, []byte(ca.CertPEM()), 0o600))
	require.NoError(t, os.WriteFile(certFile, []byte(certPEM), 0o600))
	require.NoError(t, os.WriteFile(keyFile, []byte(keyPEM), 0o600))

	conf := config.NewConfig()
	conf.NodeID = "worker-9"
	conf.NodeCertFile = certFile
	conf.NodeKeyFile = keyFile
	conf.ClusterCACertFile = caFile

	dial, err := DialCreds(conf)
	require.NoError(t, err)
	require.NotNil(t, dial.GetClientCertificate)
	clientCert, err := dial.GetClientCertificate(nil)
	require.NoError(t, err)
	assert.NotEmpty(t, clientCert.Certificate)
	assert.NotNil(t, dial.RootCAs)
	listen, err := ListenerCreds(certFile, keyFile, caFile, dir)
	require.NoError(t, err)
	assert.NotEmpty(t, listen.Certificates)
	assert.NotNil(t, listen.ClientCAs)
}

// With no cluster files configured, control mints the colocated set under
// <data-dir>/ipc on first use and the local node reads its pair from there.
// Control still mints its OWN listener identity and the cluster CA, because
// members on other machines authenticate against them. What it no longer mints
// is a certificate per same-host member: those dial the member socket, where
// the kernel identifies the caller, so there is nothing to issue and nothing
// for a member to wait for.
func TestControlMintsItsOwnIdentityButNotTheMembers(t *testing.T) {
	t.Parallel()
	conf := config.NewConfig()
	conf.DataDir = t.TempDir()

	listen, err := ListenerCreds("", "", "", conf.DataDir)
	require.NoError(t, err)
	assert.NotEmpty(t, listen.Certificates)

	ipc := filepath.Join(conf.DataDir, "ipc")
	assert.FileExists(t, filepath.Join(ipc, "ca.pem"), "the CA remains: remote members chain to it")
	assert.FileExists(t, filepath.Join(ipc, "control.pem"))
	assert.NoFileExists(t, filepath.Join(ipc, "local.pem"), "a same-host node needs no certificate")
	assert.NoFileExists(t, filepath.Join(ipc, "proxy-local.pem"), "nor does a same-host proxy")
}

// Control used to mint a certificate for the node and the proxy sharing its
// host. They dial the member socket now, so those pairs are private keys for a
// path nothing takes -- and stale key material on disk is exactly what gets
// found later and mistaken for something in use. Startup clears them, and
// leaves what is still load-bearing: the CA remote members chain to, and
// control's own listener identity.
func TestStartupRemovesTheRetiredColocatedCredentials(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	ipc := filepath.Join(dataDir, "ipc")
	require.NoError(t, os.MkdirAll(ipc, 0o700))
	for _, name := range []string{"local.pem", "local.key", "proxy-local.pem", "proxy-local.key"} {
		require.NoError(t, os.WriteFile(filepath.Join(ipc, name), []byte("stale"), 0o600))
	}

	_, _, err := EnsureIPCCreds(dataDir)
	require.NoError(t, err)

	assert.NoFileExists(t, filepath.Join(ipc, "local.pem"))
	assert.NoFileExists(t, filepath.Join(ipc, "local.key"))
	assert.NoFileExists(t, filepath.Join(ipc, "proxy-local.pem"))
	assert.NoFileExists(t, filepath.Join(ipc, "proxy-local.key"))
	assert.FileExists(t, filepath.Join(ipc, "ca.pem"))
	assert.FileExists(t, filepath.Join(ipc, "control.pem"))
}
