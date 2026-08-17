package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// fallbackCertLifetime bounds the self-signed stand-in; it exists only
	// until control is reachable again.
	fallbackCertLifetime = 24 * time.Hour
	// maxFallbackCerts bounds those stand-ins: the SNI comes from whoever
	// connects, so minting one per name without a cap is a memory tap.
	maxFallbackCerts = 64

	// certRefreshMargin: refetch a cached certificate when it is this close to
	// expiry, so renewals control performs propagate before the old one dies.
	certRefreshMargin = 14 * 24 * time.Hour
	// certsDirName is where PEM pairs persist under the cache dir
	certsDirName = "certs"
)

// GetCertificate terminates TLS from cert material managed by control:
// memory cache first, then the disk cache, then a fetch from control (which
// also drives on-demand issuance for new custom domains). A control outage
// serves whatever is cached -- possibly stale, never nothing.
func (p *Proxy) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	sni := strings.ToLower(hello.ServerName)
	if sni == "" {
		return nil, fmt.Errorf("no sni")
	}
	p.certMu.Lock()
	cached, ok := p.certs[sni]
	p.certMu.Unlock()
	if ok && !nearExpiry(cached) {
		return cached, nil
	}
	if cert, err := p.fetchCert(sni); err == nil {
		p.storeCert(sni, cert)
		return cert, nil
	}
	// Control unreachable (or refused): serve the stale memory copy, else disk.
	if ok {
		return cached, nil
	}
	if cert, err := p.loadCertFromDisk(sni); err == nil {
		p.certMu.Lock()
		p.certs[sni] = cert
		p.certMu.Unlock()
		return cert, nil
	}
	// Nothing cached and control unreachable: answer with a self-signed cert
	// rather than failing the handshake. A browser warns, but the proxy can
	// then serve its own "unavailable" page and swap in the real certificate
	// the moment control returns -- where failing the handshake makes :443
	// silent, which looks like the whole platform is gone.
	return p.fallbackCert(sni)
}

// fallbackCert mints (once per name) a self-signed certificate so TLS can
// complete while the real material is unreachable.
func (p *Proxy) fallbackCert(sni string) (*tls.Certificate, error) {
	p.certMu.Lock()
	defer p.certMu.Unlock()
	if cert, ok := p.fallbacks[sni]; ok {
		return cert, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: sni},
		DNSNames:     []string{sni},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(fallbackCertLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	cert := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
	if p.fallbacks == nil {
		p.fallbacks = make(map[string]*tls.Certificate)
	}
	if len(p.fallbacks) < maxFallbackCerts {
		p.fallbacks[sni] = cert
	}
	slog.Warn("Serving a self-signed certificate: no cached material and control is unreachable", "sni", sni)
	return cert, nil
}

func nearExpiry(cert *tls.Certificate) bool {
	return cert.Leaf != nil && time.Until(cert.Leaf.NotAfter) < certRefreshMargin
}

// fetchCert asks control over the connection this proxy already holds. The
// request rides its own stream, so a slow issuance for one name does not block
// handshakes for any other.
func (p *Proxy) fetchCert(sni string) (*tls.Certificate, error) {
	mat, err := p.controlSink().CertFor(sni)
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair([]byte(mat.CertPEM), []byte(mat.KeyPEM))
	if err != nil {
		return nil, err
	}
	p.persistCert(sni, mat.CertPEM, mat.KeyPEM)
	return &cert, nil
}

func (p *Proxy) storeCert(sni string, cert *tls.Certificate) {
	p.certMu.Lock()
	defer p.certMu.Unlock()
	p.certs[sni] = cert
}

// certPath keeps one PEM bundle (chain + key) per SNI name; the name is a
// hostname, which is filesystem-safe by construction.
func (p *Proxy) certPath(sni string) string {
	return filepath.Join(p.conf.CacheDir, certsDirName, sni+".pem")
}

func (p *Proxy) persistCert(sni, certPEM, keyPEM string) {
	dir := filepath.Join(p.conf.CacheDir, certsDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	tmp := p.certPath(sni) + ".tmp"
	if err := os.WriteFile(tmp, []byte(certPEM+keyPEM), 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, p.certPath(sni))
}

func (p *Proxy) loadCertFromDisk(sni string) (*tls.Certificate, error) {
	b, err := os.ReadFile(p.certPath(sni))
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(b, b)
	if err != nil {
		return nil, err
	}
	return &cert, nil
}
