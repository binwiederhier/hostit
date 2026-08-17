package proxy

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/nodelink"
)

// fakeCertControl serves /internal/cert like control does, counting fetches.
func fakeCertControl(t *testing.T, sni string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	ca, err := nodelink.NewCA()
	require.NoError(t, err)
	cert, err := ca.Issue(sni)
	require.NoError(t, err)
	var chain bytes.Buffer
	for _, der := range cert.Certificate {
		require.NoError(t, pem.Encode(&chain, &pem.Block{Type: "CERTIFICATE", Bytes: der}))
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	require.NoError(t, err)
	var key bytes.Buffer
	require.NoError(t, pem.Encode(&key, &pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	fetches := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/cert" || r.URL.Query().Get("sni") != sni {
			http.NotFound(w, r)
			return
		}
		fetches.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{"cert_pem": chain.String(), "key_pem": key.String()})
	}))
	return srv, fetches
}

func TestCertSourceFetchesCachesAndSurvivesControlDown(t *testing.T) {
	t.Parallel()
	control, fetches := fakeCertControl(t, "blog.example.com")
	cacheDir := t.TempDir()

	p := New(&Config{ControlURL: control.URL, CacheDir: cacheDir})
	hello := &tls.ClientHelloInfo{ServerName: "blog.example.com"}
	cert, err := p.GetCertificate(hello)
	require.NoError(t, err)
	require.NotNil(t, cert)

	// A second handshake is a memory hit, not a refetch
	_, err = p.GetCertificate(hello)
	require.NoError(t, err)
	assert.Equal(t, int64(1), fetches.Load())

	// Control goes down; a FRESH proxy (restart) still terminates from disk
	control.Close()
	p2 := New(&Config{ControlURL: control.URL, CacheDir: cacheDir})
	cert2, err := p2.GetCertificate(hello)
	require.NoError(t, err, "the persisted cert must serve while control is down")
	require.NotNil(t, cert2)

	// An SNI nobody has material for gets a self-signed stand-in rather than a
	// failed handshake: :443 answering with a warning beats :443 answering
	// nothing, which is indistinguishable from the platform being down. No
	// wildcard guessing -- the stand-in is minted for that exact name.
	fallback, err := p2.GetCertificate(&tls.ClientHelloInfo{ServerName: "unknown.example.com"})
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(fallback.Certificate[0])
	require.NoError(t, err)
	assert.Equal(t, []string{"unknown.example.com"}, leaf.DNSNames)
	assert.NotEqual(t, cert2.Certificate[0], fallback.Certificate[0])
}

// With control down AND no cached certificate, the handshake used to fail
// outright, so :443 answered nothing at all -- a wiped cache during a control
// outage took the whole edge down. A self-signed fallback keeps the port
// answering: the browser warns, but the proxy can then serve its own "app
// unavailable" page instead of a connection error, and recovers the real
// certificate as soon as control returns.
func TestGetCertificateFallsBackToASelfSignedCert(t *testing.T) {
	t.Parallel()
	p := New(&Config{ControlURL: "http://127.0.0.1:1", CacheDir: t.TempDir()})

	cert, err := p.GetCertificate(&tls.ClientHelloInfo{ServerName: "blog.apps.example.com"})
	require.NoError(t, err, "the handshake must complete even with nothing cached and control down")
	require.NotNil(t, cert)
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	require.NoError(t, err)
	assert.Contains(t, leaf.DNSNames, "blog.apps.example.com")

	// Reused for the next handshake rather than minted per connection.
	again, err := p.GetCertificate(&tls.ClientHelloInfo{ServerName: "blog.apps.example.com"})
	require.NoError(t, err)
	assert.Equal(t, cert.Certificate[0], again.Certificate[0])
}
