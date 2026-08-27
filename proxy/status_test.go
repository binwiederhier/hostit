package proxy

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/proxy/api"
)

// The status is answered from the proxy's own cache and link state; nothing is
// asked of control, so it works exactly when control is unreachable.
func TestProxyStatusReflectsTableAndLink(t *testing.T) {
	t.Parallel()
	p := New(&Config{ProxyID: "edge-1", ControlURL: "http://127.0.0.1:2586", ClusterURL: "10.0.0.1:2930", CacheDir: t.TempDir()})

	s := p.Status()
	assert.Equal(t, "edge-1", s.ProxyID)
	assert.Equal(t, Version, s.Version)
	assert.False(t, s.Connected, "no sink means not connected")
	assert.Zero(t, s.TableSeq)
	assert.Zero(t, s.Routes)

	require.NoError(t, p.ApplyRoutes(&api.Table{Seq: 7, Routes: []api.Route{
		{Host: "blog.example.com", Target: "10.0.0.2:10001"},
	}}))
	p.setSink(noSink{})
	s = p.Status()
	assert.True(t, s.Connected)
	assert.Equal(t, int64(7), s.TableSeq)
	assert.Equal(t, 1, s.Routes)

	// dropSink is what the link loop calls between connections; Connected must
	// follow it, not report the boxed nil as a live link.
	p.dropSink()
	assert.False(t, p.Connected())

	table := p.Routes()
	require.Len(t, table.Routes, 1)
	assert.Equal(t, "blog.example.com", table.Routes[0].Host)
	assert.Equal(t, int64(7), table.Seq)
}

// The status socket is root-only (0600) and serves both the status and the
// cached routing table.
func TestProxyStatusSocketServes(t *testing.T) {
	t.Parallel()
	p := New(&Config{ProxyID: "edge-1", ControlURL: "http://127.0.0.1:2586", ClusterURL: "10.0.0.1:2930", CacheDir: t.TempDir()})
	require.NoError(t, p.ApplyRoutes(&api.Table{Seq: 3, Routes: []api.Route{
		{Host: "blog.example.com", Target: "10.0.0.2:10001"},
	}}))
	path := filepath.Join(t.TempDir(), "proxy.sock")
	closer, err := ServeStatusSocket(path, p)
	require.NoError(t, err)
	t.Cleanup(func() { _ = closer.Close() })

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "operators only")

	client := http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", path)
		},
	}}
	resp, err := client.Get("http://proxy/v1/status")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var s Status
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&s))
	assert.Equal(t, "edge-1", s.ProxyID)
	assert.Equal(t, int64(3), s.TableSeq)

	resp, err = client.Get("http://proxy/v1/routes")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var table api.Table
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&table))
	require.Len(t, table.Routes, 1)
	assert.Equal(t, "blog.example.com", table.Routes[0].Host)
}
