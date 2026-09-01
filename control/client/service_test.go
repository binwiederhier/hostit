package client

import (
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/control"
	"heckel.io/hostit/control/apptest"
	"heckel.io/hostit/control/config"
	"heckel.io/hostit/control/user"
	"heckel.io/hostit/node"
	nodeconf "heckel.io/hostit/node/config"
	"heckel.io/hostit/store"
)

const (
	testToken     = "secr3t"
	testPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIC24brF98CyUY18aeOGGQY3+wILYYnUUBQqICmMTvTGL test@host"
)

func TestCreateAndListApps(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, testToken)
	created, err := c.CreateApp("blog", nil)
	require.NoError(t, err)
	assert.Equal(t, "blog", created.Name)
	assert.Equal(t, "https://blog.apps.example.com", created.URL)
	assert.Equal(t, "blog", created.SSH.User)
	apps, err := c.Apps()
	require.NoError(t, err)
	require.Len(t, apps, 1)
	assert.Equal(t, "blog", apps[0].Name)
}

func TestGetAndDeleteApp(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, testToken)
	_, err := c.CreateApp("blog", []string{testPublicKey})
	require.NoError(t, err)
	a, err := c.App("blog")
	require.NoError(t, err)
	assert.Equal(t, 10000, a.Port)
	require.NoError(t, c.DeleteApp("blog"))
	_, err = c.App("blog")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestSetKeys(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, testToken)
	_, err := c.CreateApp("blog", nil)
	require.NoError(t, err)
	require.NoError(t, c.SetKeys("blog", []string{testPublicKey}))
	require.Error(t, c.SetKeys("blog", []string{"junk"}))
}

func TestBadToken(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, "wrong")
	_, err := c.Apps()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sign in")
}

func newTestClient(t *testing.T, token string) *Client {
	t.Helper()
	conf := config.NewConfig()
	conf.BaseDomain = "apps.example.com"
	conf.AdminToken = testToken
	conf.AppsDir = t.TempDir()
	s, err := store.NewStore(filepath.Join(t.TempDir(), "hostit.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = s.Close()
	})
	// Control does no machine work: a node has to be there for anything to
	// happen, so the test registers an in-process one.
	apps := control.NewManager(conf, s)
	nodeStore, err := store.NewStore(filepath.Join(t.TempDir(), "node.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = nodeStore.Close() })
	apps.NodeRegistry().Register(store.HostLocal, node.NewMachine(&nodeconf.Config{
		NodeID:     store.HostLocal,
		DataDir:    conf.DataDir,
		AppsDir:    conf.AppsDir,
		SocketFile: conf.SocketFile,
	}, nodeStore, apptest.NewNopServices()))
	srv := control.New(conf, apps, user.NewManager(conf, s))
	httpServer := httptest.NewServer(srv.API())
	t.Cleanup(httpServer.Close)
	return New(httpServer.URL, token)
}

func TestNewSocketTalksOverTheSocketWithoutAuthorization(t *testing.T) {
	t.Parallel()
	// A fake daemon on a unix socket, recording what the client sent
	type received struct {
		path, auth string
	}
	requests := make(chan received, 1)
	socketFile := filepath.Join(t.TempDir(), "hostit.sock")
	listener, err := net.Listen("unix", socketFile)
	require.NoError(t, err)
	httpServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- received{path: r.URL.Path, auth: r.Header.Get("Authorization")}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	})}
	go func() {
		_ = httpServer.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = httpServer.Close()
	})
	c := NewSocket(socketFile)
	apps, err := c.Apps()
	require.NoError(t, err)
	assert.Empty(t, apps)
	got := <-requests
	assert.Equal(t, "/api/apps", got.path)
	// Peer credentials authenticate the socket caller; a token header would only
	// suggest the daemon looks at one
	assert.Empty(t, got.auth)
}

func TestNewSetsBearerToken(t *testing.T) {
	t.Parallel()
	auths := make(chan string, 1)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(httpServer.Close)
	c := New(httpServer.URL, "tok123")
	_, err := c.Apps()
	require.NoError(t, err)
	assert.Equal(t, "Bearer tok123", <-auths)
}

func TestLifecycleFromTheClient(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, testToken)
	_, err := c.CreateApp("blog", nil)
	require.NoError(t, err)

	// Everything the app's own page can do, a token can do from anywhere
	require.NoError(t, c.Restart("blog"))
	require.NoError(t, c.Stop("blog"))
	require.NoError(t, c.Start("blog"))
	// Deploying needs a hostit.yml, which this bare app has not got: the point is
	// that the client carries the server's reason back rather than swallowing it
	_, err = c.Deploy("blog")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hostit.yml")

	out, err := c.Logs("blog", 20)
	require.NoError(t, err)
	assert.NotNil(t, out)

	res, err := c.Run("blog", "echo hi", 0)
	require.NoError(t, err)
	assert.Equal(t, 0, res.ExitCode)
}

func TestSetVisibility(t *testing.T) {
	t.Parallel()
	c := newTestClient(t, testToken)
	_, err := c.CreateApp("blog", nil)
	require.NoError(t, err)

	a, err := c.App("blog")
	require.NoError(t, err)
	assert.False(t, a.Private, "apps are created public")

	require.NoError(t, c.SetVisibility("blog", true))
	a, err = c.App("blog")
	require.NoError(t, err)
	assert.True(t, a.Private)

	require.NoError(t, c.SetVisibility("blog", false))
	a, err = c.App("blog")
	require.NoError(t, err)
	assert.False(t, a.Private)
}
