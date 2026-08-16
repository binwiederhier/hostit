package node

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/app"
)

func TestJoinTokenRoundTrip(t *testing.T) {
	t.Parallel()
	ca, err := NewCA()
	require.NoError(t, err)
	token, hash, err := MintJoinToken("worker-2", ca)
	require.NoError(t, err)

	name, secret, caFP, err := ParseJoinToken(token)
	require.NoError(t, err)
	assert.Equal(t, "worker-2", name)
	assert.Equal(t, ca.Fingerprint(), caFP, "the token pins the CA so the join cannot be MITMed")
	assert.Equal(t, hash, TokenHash(secret), "the store holds only the hash")
	assert.NotContains(t, token, "worker-2:", "token is one opaque paste string")

	_, _, _, err = ParseJoinToken("garbage")
	require.Error(t, err)
}

func TestIssueFromCSR(t *testing.T) {
	t.Parallel()
	ca, err := NewCA()
	require.NoError(t, err)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	require.NoError(t, err)
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})

	// The CN comes from the token-authenticated name, never from the CSR.
	certPEM, err := ca.IssueFromCSR("worker-2", csrPEM)
	require.NoError(t, err)
	block, _ := pem.Decode([]byte(certPEM))
	require.NotNil(t, block)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	assert.Equal(t, "worker-2", cert.Subject.CommonName)
	_, err = cert.Verify(x509.VerifyOptions{Roots: ca.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	require.NoError(t, err)
}

// fakeJoinStore burns one configured token hash.
type fakeJoinStore struct {
	hash string
	name string
	used bool
}

func (f *fakeJoinStore) ConsumeNodeJoinToken(hash string, _ time.Time) (string, error) {
	if f.used || hash != f.hash {
		return "", assert.AnError
	}
	f.used = true
	return f.name, nil
}

func TestJoinHandlerExchangesTokenForCert(t *testing.T) {
	t.Parallel()
	ca, err := NewCA()
	require.NoError(t, err)
	token, hash, err := MintJoinToken("worker-2", ca)
	require.NoError(t, err)
	st := &fakeJoinStore{hash: hash, name: "worker-2"}
	srv := httptest.NewServer(JoinHandler(ca, st))
	defer srv.Close()

	dir := t.TempDir()
	require.NoError(t, joinOver(srv.Client(), srv.URL, token, dir))

	// The node ends up with its own cert pair and the CA, ready to dial.
	_, err = LoadNodeCreds(dir, "worker-2")
	require.NoError(t, err)

	// The token is single-use.
	require.Error(t, joinOver(srv.Client(), srv.URL, token, t.TempDir()))
}

func TestJoinVerifiesThePinnedCA(t *testing.T) {
	t.Parallel()
	ca, err := NewCA()
	require.NoError(t, err)
	rogue, err := NewCA()
	require.NoError(t, err)
	// A token minted against the REAL CA, presented to a rogue control: the
	// pinned fingerprint must not match and the join must fail before the
	// token is sent anywhere.
	token, _, err := MintJoinToken("worker-2", ca)
	require.NoError(t, err)

	rogueCert, err := rogue.Issue("control")
	require.NoError(t, err)
	ln := newTLSListener(t, rogueCert, rogue)
	defer ln.Close()
	go func() {
		_ = http.Serve(ln, JoinHandler(rogue, &fakeJoinStore{}))
	}()

	err = Join(ln.Addr().String(), token, t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fingerprint")
}

func TestJoinAgainstRealListener(t *testing.T) {
	t.Parallel()
	ca, err := NewCA()
	require.NoError(t, err)
	token, hash, err := MintJoinToken("worker-2", ca)
	require.NoError(t, err)
	st := &fakeJoinStore{hash: hash, name: "worker-2"}

	controlCert, err := ca.Issue("control")
	require.NoError(t, err)
	ln := newTLSListener(t, controlCert, ca)
	defer ln.Close()
	go func() {
		_ = http.Serve(ln, JoinHandler(ca, st))
	}()

	dir := t.TempDir()
	require.NoError(t, Join(ln.Addr().String(), token, dir))
	_, err = LoadNodeCreds(dir, "worker-2")
	require.NoError(t, err)
	for _, f := range []string{"worker-2.pem", "worker-2.key", "ca.pem"} {
		assert.FileExists(t, filepath.Join(dir, ipcDirName, f))
	}
}

// newTLSListener serves TLS the way control's node listener does: chain
// includes the CA, client certs verified if given.
func newTLSListener(t *testing.T, cert tls.Certificate, ca *CA) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	return tls.NewListener(ln, ca.ListenerTLS(cert))
}

func TestLoadCARoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// EnsureIPCCreds bootstraps the CA once; LoadCA gives control the signing
	// key back for join-time issuance.
	_, ca1, err := EnsureIPCCreds(dir)
	require.NoError(t, err)
	ca2, err := LoadCA(dir)
	require.NoError(t, err)
	assert.Equal(t, ca1.Fingerprint(), ca2.Fingerprint())

	// A cert signed by the loaded CA verifies against the original's pool.
	cert, err := ca2.Issue("worker-9")
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	require.NoError(t, err)
	_, err = leaf.Verify(x509.VerifyOptions{Roots: ca1.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	require.NoError(t, err)
}

func TestConnectHandlerRejectsHeaderIdentityOverTLS(t *testing.T) {
	t.Parallel()
	// Over TLS the identity is the client cert CN, full stop: with the node
	// listener accepting cert-less connections (for /join), a header fallback
	// would let any joiner-to-be claim any node id.
	h := ConnectHandler(func(string) bool { return true }, nil, func(string, app.NodeAgent) {})
	req := httptest.NewRequest("POST", connectPath, nil)
	req.TLS = &tls.ConnectionState{} // TLS, but no peer certificates
	req.Header.Set("X-Hostit-Node", "victim")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestConnectHandlerAuthorizesTheNode(t *testing.T) {
	t.Parallel()
	// A valid certificate for an UNREGISTERED node (removed after joining) is
	// refused: registration is checked at connect time, which is what makes
	// `node remove` an effective revocation.
	h := ConnectHandler(func(id string) bool { return id == "known" }, nil, func(string, app.NodeAgent) {})
	req := httptest.NewRequest("POST", connectPath, nil)
	req.Header.Set("X-Hostit-Node", "unknown") // non-TLS local-socket path
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
