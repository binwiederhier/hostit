package node

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	"heckel.io/hostit/store"
)

// Colocated-interim credential files: control mints the CA and both identity
// certs under <dataDir>/ipc (root-only), and the colocated hostit-node reads
// its own pair from there. Real remote nodes get certs via join tokens
// (Phase 2b); the wire and verification are identical either way.

const ipcDirName = "ipc"

// EnsureIPCCreds creates (once) and loads the CA plus the "control" and
// "local" identity certs; returns the TLS config for control's node listener
// and the CA itself (the join handler signs with it).
func EnsureIPCCreds(dataDir string) (*tls.Config, *CA, error) {
	dir := filepath.Join(dataDir, ipcDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	if _, err := os.Stat(filepath.Join(dir, "ca.pem")); err != nil {
		ca, err := NewCA()
		if err != nil {
			return nil, nil, err
		}
		if err := writePEMPair(dir, "ca", ca.CertPEM(), ca.KeyPEM()); err != nil {
			return nil, nil, err
		}
		for _, id := range []string{controlID, store.HostLocal} {
			cert, err := ca.Issue(id)
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

// LoadCA reads the persisted CA (cert + signing key) back for join-time
// certificate issuance.
func LoadCA(dataDir string) (*CA, error) {
	dir := filepath.Join(dataDir, ipcDirName)
	certPEM, err := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, "ca.key"))
	if err != nil {
		return nil, err
	}
	return NewCAFromPEM(certPEM, keyPEM)
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
	return ClientTLS(cert, pool), nil
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
