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
	"heckel.io/hostit/node"
)

// fakeCertControl serves /internal/cert like control does, counting fetches.
func fakeCertControl(t *testing.T, sni string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	ca, err := node.NewCA()
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

	// An SNI nobody has material for fails the handshake (no wildcard guessing)
	_, err = p2.GetCertificate(&tls.ClientHelloInfo{ServerName: "unknown.example.com"})
	assert.Error(t, err)
}
