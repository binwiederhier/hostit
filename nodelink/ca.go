package nodelink

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

const (
	// caValidity and certValidity are deliberately long: this is an internal,
	// operator-owned trust domain, and node removal is registry + revocation,
	// not certificate expiry.
	caValidity   = 10 * 365 * 24 * time.Hour
	certValidity = 5 * 365 * 24 * time.Hour
)

// CA is the control-owned certificate authority for node identities. It signs
// one certificate per node with the node id in the CN, so mTLS verification
// IS node authentication -- there is no steady-state bearer token.
type CA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

// NewCA creates a fresh CA (done once, at control's first start).
func NewCA() (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "hostit-control-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(caValidity),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{cert: cert, key: key}, nil
}

// Issue signs an identity certificate whose CN is the given id (a node id, or
// "control" for the accepting side). Both EKUs, so one cert serves whichever
// TLS role its holder plays.
func (c *CA) Issue(id string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: id},
		DNSNames:     []string{id},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(certValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: tmpl}, nil
}

// IssueFromCSR signs a certificate for the CSR's public key. The CN is the
// token-authenticated node name -- whatever subject the CSR asked for is
// ignored, so a CSR can never claim another node's identity.
func (c *CA) IssueFromCSR(id string, csrPEM []byte) (string, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil {
		return "", fmt.Errorf("no CSR in request")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return "", err
	}
	if err := csr.CheckSignature(); err != nil {
		return "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: id},
		DNSNames:     []string{id},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(certValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, csr.PublicKey, c.key)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), nil
}

// Fingerprint identifies this CA (SHA256 of its certificate DER, hex); it is
// embedded in join tokens as the joining node's only trust anchor.
func (c *CA) Fingerprint() string {
	sum := sha256.Sum256(c.cert.Raw)
	return hex.EncodeToString(sum[:])
}

// ListenerTLS is control's node-listener config: present the chain INCLUDING
// the CA (so a joining node can verify its pinned fingerprint), verify a
// client cert when one is given. The join route is the only one that runs
// without a client cert; everything else re-checks r.TLS.PeerCertificates.
func (c *CA) ListenerTLS(cert tls.Certificate) *tls.Config {
	chain := make([][]byte, 0, len(cert.Certificate)+1)
	chain = append(chain, cert.Certificate...)
	chain = append(chain, c.cert.Raw)
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: chain, PrivateKey: cert.PrivateKey, Leaf: cert.Leaf}},
		ClientCAs:    c.Pool(),
		ClientAuth:   tls.VerifyClientCertIfGiven,
		MinVersion:   tls.VersionTLS13,
	}
}

// NewCAFromPEM reconstructs a CA from its persisted PEM pair.
func NewCAFromPEM(certPEM, keyPEM []byte) (*CA, error) {
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, fmt.Errorf("no CA certificate PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, err
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("no CA key PEM")
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := keyAny.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("CA key is not ECDSA")
	}
	return &CA{cert: cert, key: key}, nil
}

// Pool returns the trust pool containing (only) this CA.
func (c *CA) Pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(c.cert)
	return pool
}

// ServerTLS is the accepting side's config: present cert, REQUIRE a client
// cert signed by the CA. mTLS is mandatory -- there is no token fallback.
func ServerTLS(cert tls.Certificate, clientCAs *x509.CertPool) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    clientCAs,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}
}

// ClientTLS is the dialing side's config: present the node cert, verify the
// server against the CA. ServerName pins the accepting identity.
func ClientTLS(cert tls.Certificate, rootCAs *x509.CertPool) *tls.Config {
	return &tls.Config{
		// Unconditional: the node has exactly one identity, so skip Go's
		// acceptable-CA filtering (which can silently send no cert at all).
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return &cert, nil
		},
		RootCAs:    rootCAs,
		ServerName: controlID,
		MinVersion: tls.VersionTLS13,
	}
}

// CertPEM/KeyPEM render the CA itself for persistence.
func (c *CA) CertPEM() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.cert.Raw}))
}

func (c *CA) KeyPEM() string {
	der, err := x509.MarshalPKCS8PrivateKey(c.key)
	if err != nil {
		return ""
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}
