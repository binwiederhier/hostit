package nodelink

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
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

const ipcDirName = "ipc"

// ListenerCreds resolves control's node-listener TLS config: the configured
// cluster files when set, else the auto-minted colocated set under dataDir
// (created on first use). Control's own identity, hence the explicit paths --
// a node's are in its Config.
func ListenerCreds(certFile, keyFile, caFile, dataDir string) (*tls.Config, error) {
	if certFile != "" {
		cert, pool, err := loadClusterFiles(certFile, keyFile, caFile)
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
		cert, pool, err := loadClusterFiles(conf.NodeCertFile, conf.NodeKeyFile, conf.ClusterCACertFile)
		if err != nil {
			return nil, err
		}
		return cluster.ClientTLS(cert, pool), nil
	}
	return LoadNodeCreds(conf.DataDir, conf.NodeID)
}

// loadClusterFiles loads an identity pair and the CA pool; all three files
// must be set together.
func loadClusterFiles(certFile, keyFile, caFile string) (tls.Certificate, *x509.CertPool, error) {
	if keyFile == "" || caFile == "" {
		return tls.Certificate{}, nil, fmt.Errorf("a cluster certificate needs its key file and the cluster CA file")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	pool, err := loadPool(caFile)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return cert, pool, nil
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
		for _, id := range []string{cluster.ControlID, store.HostLocal} {
			cert, err := ca.Issue(id, cluster.RoleNode)
			if err != nil {
				return nil, nil, err
			}
			certPEM, keyPEM, err := EncodeCert(cert)
			if err != nil {
				return nil, nil, err
			}
			if err := writePEMPair(dir, id, certPEM, keyPEM); err != nil {
				return nil, nil, err
			}
		}
	}
	ca, err := LoadCA(dataDir)
	if err != nil {
		return nil, nil, err
	}
	controlCert, err := tls.LoadX509KeyPair(filepath.Join(dir, "control.pem"), filepath.Join(dir, "control.key"))
	if err != nil {
		return nil, nil, err
	}
	return ca.ListenerTLS(controlCert), ca, nil
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
	pool, err := loadPool(filepath.Join(dir, "ca.pem"))
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

func loadPool(caPath string) (*x509.CertPool, error) {
	b, err := os.ReadFile(caPath)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("no CA certificate in %s", caPath)
	}
	return pool, nil
}

// EncodeCert renders a tls.Certificate as PEM chain + PKCS8 key.
func EncodeCert(cert tls.Certificate) (certPEM, keyPEM string, err error) {
	var chain, key []byte
	for _, der := range cert.Certificate {
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		return "", "", err
	}
	key = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return string(chain), string(key), nil
}
