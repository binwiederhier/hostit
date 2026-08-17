package cluster

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
)

// Cluster credentials are plain files: each member presents a CA-signed
// certificate (CN = its id, OU = its role) and trusts the cluster CA. Every
// member is configured the same way -- a cert file, a key file and the CA file
// -- whether it is a node, a proxy, or control itself.

// DialCreds is a member's client config for dialing control: it presents the
// identity and pins the cluster CA.
func DialCreds(certFile, keyFile, caFile string) (*tls.Config, error) {
	cert, pool, err := LoadCreds(certFile, keyFile, caFile)
	if err != nil {
		return nil, err
	}
	return ClientTLS(cert, pool), nil
}

// LoadCreds loads an identity pair and the CA pool; all three files must be
// set together, since a half-configured triple only surfaces later as an
// opaque TLS failure at dial time.
func LoadCreds(certFile, keyFile, caFile string) (tls.Certificate, *x509.CertPool, error) {
	if keyFile == "" || caFile == "" {
		return tls.Certificate{}, nil, fmt.Errorf("a cluster certificate needs its key file and the cluster CA file")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	pool, err := LoadPool(caFile)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return cert, pool, nil
}

// LoadPool reads a CA certificate file into a pool.
func LoadPool(caPath string) (*x509.CertPool, error) {
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
