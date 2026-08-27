package link

import (
	"crypto/tls"
	"log/slog"
	"os"
	"path/filepath"

	"heckel.io/hostit/cluster"
	"heckel.io/hostit/nodeconf"
	"heckel.io/hostit/store"
)

// Cluster credentials are plain files: each process presents a CA-signed
// certificate (CN = its node id, "control" for control) and trusts the
// cluster CA. Configured explicitly via node-cert-file / node-key-file /
// cluster-ca-cert-file, or -- unset -- the auto-minted colocated set under
// <dataDir>/ipc (root-only), which control creates on first listen and the
// colocated hostit-node reads back. Remote nodes get their pair from
// `hostit-control node add`; there is no enrollment protocol.

const (
	ipcDirName = "ipc"
	// LocalProxyFile is the colocated proxy's credential basename under the ipc
	// dir; hostit-proxy is pointed at these files by its config, the same way a
	// remote proxy is pointed at the pair `hostit-control proxy add` minted.
	LocalProxyFile = "proxy-local"
)

// ListenerCreds resolves control's node-listener TLS config: the configured
// cluster files when set, else the auto-minted colocated set under dataDir
// (created on first use). Control's own identity, hence the explicit paths --
// a node's are in its Config.
func ListenerCreds(certFile, keyFile, caFile, dataDir string) (*tls.Config, error) {
	if certFile != "" {
		cert, pool, err := cluster.LoadCreds(certFile, keyFile, caFile)
		if err != nil {
			return nil, err
		}
		return cluster.ServerTLS(cert, pool), nil
	}
	tlsConf, _, err := EnsureIPCCreds(dataDir)
	return tlsConf, err
}

// DialCreds resolves a node's client TLS config for dialing control: its
// configured cluster files when set, else the colocated pair under
// <DataDir>/ipc that control minted for this node id.
func DialCreds(conf *nodeconf.Config) (*tls.Config, error) {
	if conf.NodeCertFile != "" {
		cert, pool, err := cluster.LoadCreds(conf.NodeCertFile, conf.NodeKeyFile, conf.ClusterCACertFile)
		if err != nil {
			return nil, err
		}
		return cluster.ClientTLS(cert, pool), nil
	}
	return LoadNodeCreds(conf.DataDir, conf.NodeID)
}

// EnsureIPCCreds creates (once) and loads the CA plus the "control" and
// "local" identity certs; returns the TLS config for control's node listener
// and the CA itself (`node add` mints remote-node certs with it).
func EnsureIPCCreds(dataDir string) (*tls.Config, *cluster.CA, error) {
	dir := filepath.Join(dataDir, ipcDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	if _, err := os.Stat(filepath.Join(dir, "ca.pem")); err != nil {
		ca, err := cluster.NewCA()
		if err != nil {
			return nil, nil, err
		}
		if err := writePEMPair(dir, "ca", ca.CertPEM(), ca.KeyPEM()); err != nil {
			return nil, nil, err
		}
	}
	ca, err := LoadCA(dataDir)
	if err != nil {
		return nil, nil, err
	}
	// Mint each colocated identity that is missing rather than only on the
	// first start ever: an install that predates an identity (the proxy's, say)
	// grows it on the next restart instead of needing its ipc dir wiped.
	for _, id := range colocatedIdentities() {
		if _, err := os.Stat(filepath.Join(dir, id.file+".pem")); err == nil {
			continue
		}
		cert, err := ca.Issue(id.name, id.role)
		if err != nil {
			return nil, nil, err
		}
		certPEM, keyPEM, err := EncodeCert(cert)
		if err != nil {
			return nil, nil, err
		}
		if err := writePEMPair(dir, id.file, certPEM, keyPEM); err != nil {
			return nil, nil, err
		}
	}
	removeRetiredIdentities(dir)
	controlCert, err := tls.LoadX509KeyPair(filepath.Join(dir, "control.pem"), filepath.Join(dir, "control.key"))
	if err != nil {
		return nil, nil, err
	}
	return ca.ListenerTLS(controlCert), ca, nil
}

// retiredIdentities are pairs control used to mint for members sharing its
// host, before they moved to the member socket. They are private keys for an
// authentication path nothing takes any more, so control deletes them rather
// than leaving key material lying around to be found later and mistaken for
// something load-bearing.
//
// An upgrade restarts control before the node and proxy, so a member still
// running the old binary loses its credentials for the seconds until its own
// restart; it redials on a loop and comes back on the socket. Packages move
// together, so that window is the upgrade itself.
var (
	retiredIdentities = []string{store.HostLocal, LocalProxyFile}
)

func removeRetiredIdentities(dir string) {
	for _, name := range retiredIdentities {
		for _, ext := range []string{".pem", ".key"} {
			path := filepath.Join(dir, name+ext)
			if err := os.Remove(path); err == nil {
				slog.Info("Removed a credential no member uses any more", "file", path)
			}
		}
	}
}

// LoadCA reads the persisted CA (cert + signing key) back so `node add` can
// mint a remote node's certificate.
func LoadCA(dataDir string) (*cluster.CA, error) {
	dir := filepath.Join(dataDir, ipcDirName)
	certPEM, err := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, "ca.key"))
	if err != nil {
		return nil, err
	}
	return cluster.NewCAFromPEM(certPEM, keyPEM)
}

// LoadNodeCreds loads a node's identity ("local" on the colocated host) and
// returns the client TLS config for dialing control.
func LoadNodeCreds(dataDir, id string) (*tls.Config, error) {
	dir := filepath.Join(dataDir, ipcDirName)
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, id+".pem"), filepath.Join(dir, id+".key"))
	if err != nil {
		return nil, err
	}
	pool, err := cluster.LoadPool(filepath.Join(dir, "ca.pem"))
	if err != nil {
		return nil, err
	}
	return cluster.ClientTLS(cert, pool), nil
}

func writePEMPair(dir, name, certPEM, keyPEM string) error {
	if err := os.WriteFile(filepath.Join(dir, name+".pem"), []byte(certPEM), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name+".key"), []byte(keyPEM), 0o600)
}

// EncodeCert renders a tls.Certificate as PEM chain + PKCS8 key.
func EncodeCert(cert tls.Certificate) (certPEM, keyPEM string, err error) {
	return cluster.EncodeCert(cert)
}

// colocatedIdentity is one credential pair control mints for a process on its
// own host, so a single-box install enrolls nothing.
type colocatedIdentity struct {
	name string // the CN, and the id the peer registers under
	role string
	file string // the basename under the ipc dir
}

// colocatedIdentities is control's own listener identity, and nothing else.
// Members sharing this host used to get one each, which meant control minted
// certificates for itself and they had to wait for the files to appear -- a
// second precondition that once put a proxy on :443 with nothing to serve.
// They use the member socket now, where the kernel says who is calling.
func colocatedIdentities() []colocatedIdentity {
	return []colocatedIdentity{
		{name: cluster.ControlID, role: cluster.RoleNode, file: cluster.ControlID},
	}
}
