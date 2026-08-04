package client

import (
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/app"
	"heckel.io/hostit/config"
	"heckel.io/hostit/server"
	"heckel.io/hostit/store"
	"heckel.io/hostit/user"
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
	assert.Contains(t, created.PrivateKey, "OPENSSH PRIVATE KEY")
	assert.Equal(t, "blog", created.SSH.User)
	apps, err := c.Apps()
	require.NoError(t, err)
	require.Len(t, apps, 1)
	assert.Equal(t, "blog", apps[0].Name)
	assert.Empty(t, apps[0].PrivateKey) // Private key only returned on creation
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
	srv := server.New(conf, app.NewManager(conf, s, app.NewNopSystemOps(), app.NewNopRunner()), user.NewManager(conf, s))
	httpServer := httptest.NewServer(srv.API())
	t.Cleanup(httpServer.Close)
	return New(httpServer.URL, token)
}
