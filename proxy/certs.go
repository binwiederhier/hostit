package proxy

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
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
	return nil, fmt.Errorf("no certificate for %s", sni)
}

func nearExpiry(cert *tls.Certificate) bool {
	return cert.Leaf != nil && time.Until(cert.Leaf.NotAfter) < certRefreshMargin
}

func (p *Proxy) fetchCert(sni string) (*tls.Certificate, error) {
	resp, err := p.client.Get(p.conf.InternalURL + internalCertPath + "?sni=" + url.QueryEscape(sni))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("cert fetch for %s: status %d", sni, resp.StatusCode)
	}
	var mat struct {
		CertPEM string `json:"cert_pem"`
		KeyPEM  string `json:"key_pem"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&mat); err != nil {
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
