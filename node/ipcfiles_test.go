package node

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Cluster identity comes from configuration: a node (or control) points at its
// cert/key pair and the cluster CA, and that is the whole trust setup -- no
// enrollment protocol. Unset, both fall back to the auto-minted colocated
// files under <data-dir>/ipc, so a single-host split needs no config at all.
func TestCredsFromConfiguredFiles(t *testing.T) {
	t.Parallel()
	ca, err := NewCA()
	require.NoError(t, err)
	cert, err := ca.Issue("worker-9")
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

	conf := NewConfig()
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
func TestCredsFallBackToColocatedIPCFiles(t *testing.T) {
	t.Parallel()
	conf := NewConfig()
	conf.DataDir = t.TempDir()

	listen, err := ListenerCreds("", "", "", conf.DataDir) // control side: auto-mints
	require.NoError(t, err)
	assert.NotEmpty(t, listen.Certificates)
	dial, err := DialCreds(conf) // node side: reads the minted "local" pair
	require.NoError(t, err)
	require.NotNil(t, dial.GetClientCertificate)
	assert.FileExists(t, filepath.Join(conf.DataDir, "ipc", "ca.pem"))
}
