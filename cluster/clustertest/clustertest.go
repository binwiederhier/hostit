// Package clustertest generates a throwaway cluster CA and member certs for
// TESTS ONLY. Production hostit no longer runs a CA -- the operator issues
// certs from an external CA (openssl) -- so this lives out of the shipped
// binary and only simulates that operator for the mTLS tests.
package clustertest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
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

type CA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

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

func (c *CA) Issue(id, role string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	subject := pkix.Name{CommonName: id}
	if role != "" {
		subject.OrganizationalUnit = []string{role}
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      subject,
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

func (c *CA) Pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(c.cert)
	return pool
}
