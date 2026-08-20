package node

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/nodeapi"
	"heckel.io/hostit/nodelink"
	"heckel.io/hostit/store"
)

// The app socket belongs to the NODE, on every host. It authenticates by
// SO_PEERCRED against the node's own mirror (which holds exactly this node's
// apps, uid included) and relays to control over the cluster link -- never
// answering from its own machinery, so control keeps every guard. See
// plans/260820-app-socket-and-split.md.

// linkTo builds a ControlLink whose client lands on the given test server, the
// way the duplex session's client lands on control.
func linkTo(t *testing.T, backend http.Handler) *nodelink.ControlLink {
	t.Helper()
	srv := httptest.NewServer(backend)
	t.Cleanup(srv.Close)
	link := nodelink.NewControlLink()
	link.SetClient(&http.Client{Transport: rewriteTo(srv.URL)})
	return link
}

type rewriteTo string

func (base rewriteTo) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(string(base), "http://")
	return http.DefaultTransport.RoundTrip(req)
}

// mirrorWith seeds a node mirror store holding one app with the given uid.
func mirrorWith(t *testing.T, name string, uid int) *store.Store {
	t.Helper()
	s, err := store.NewStore(filepath.Join(t.TempDir(), "node.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.ReplaceNodeMirror([]*store.App{
		{ID: "a1", Name: name, Port: 10000, Host: store.HostLocal, OwnerID: "u1", UID: uid, CreatedAt: time.Now()},
	}, nil))
	return s
}

// asUID serves the handler with the peer uid a real unix connection would have
// carried, so the resolution logic is testable without forging kernel creds.
func asUID(handler http.Handler, uid int, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req = req.WithContext(context.WithValue(req.Context(), appSocketPeerUIDKey{}, uid))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

// The node serves a real, world-connectable unix socket, and a caller the
// mirror does not know is refused by peer credential -- the test process's own
// uid is nobody's app, which proves the kernel creds actually flow end to end.
func TestNodeServesTheAppSocket(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "hostit.sock")
	st := mirrorWith(t, "blog", 1234567)
	closer, err := ServeAppSocket(path, st, nodelink.NewControlLink())
	require.NoError(t, err)
	t.Cleanup(func() { _ = closer.Close() })

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o666), info.Mode().Perm(), "apps connect as their own uid, so the file is world-connectable")

	client := http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", path)
		},
	}}
	resp, err := client.Get("http://app/v1/self")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Contains(t, string(body), "no app for uid", "the test process's uid is nobody's app")
}

// A known uid resolves to its app from the node's own mirror, and the request
// is relayed with the app named; an unknown uid never reaches control at all.
func TestNodeMapsPeerUIDToItsApp(t *testing.T) {
	t.Parallel()
	var seen struct {
		path, app string
		hit       bool
	}
	link := linkTo(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.hit, seen.path, seen.app = true, r.URL.Path, r.Header.Get(nodeapi.AppRelayHeader)
		w.WriteHeader(http.StatusOK)
	}))
	handler := appSocketHandler(mirrorWith(t, "blog", 4242), link)

	rr := asUID(handler, 4242, "GET", "/v1/self/logs?lines=7")
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.True(t, seen.hit)
	assert.Equal(t, nodeapi.AppRelayPrefix+"/v1/self/logs", seen.path, "the path is prefixed, otherwise verbatim")
	assert.Equal(t, "blog", seen.app, "the node names the app it resolved")

	seen.hit = false
	rr = asUID(handler, 9999, "GET", "/v1/self")
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.False(t, seen.hit, "an unknown uid is refused locally, not relayed")
}

// The node relays rather than answering: control's response -- status, body --
// comes back verbatim, so control's refusals (an archived app's 409) are the
// app's refusals. Down-link requests fail as a retryable 502, and the operator
// API is not served here at all.
func TestNodeRelaysRatherThanAnswering(t *testing.T) {
	t.Parallel()
	link := linkTo(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error": "app is archived; unarchive it first"}`))
	}))
	handler := appSocketHandler(mirrorWith(t, "blog", 4242), link)

	rr := asUID(handler, 4242, "POST", "/v1/self/deploy")
	assert.Equal(t, http.StatusConflict, rr.Code, "control's status is the app's status")
	assert.Contains(t, rr.Body.String(), "archived", "control's refusal arrives verbatim")

	// Between control connections the link has no client: a clean 502.
	down := appSocketHandler(mirrorWith(t, "blog", 4242), nodelink.NewControlLink())
	rr = asUID(down, 4242, "POST", "/v1/self/deploy")
	assert.Equal(t, http.StatusBadGateway, rr.Code)
	assert.Contains(t, rr.Body.String(), "unreachable")

	// Operator commands live on control's own socket now; the old CLI gets a
	// pointer, not a 401 that looks like a token problem.
	rr = asUID(handler, 0, "GET", "/api/apps")
	assert.Equal(t, http.StatusNotImplemented, rr.Code)
	assert.Contains(t, rr.Body.String(), "hostit-control")
}
