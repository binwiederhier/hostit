package node

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

	nodelink "heckel.io/hostit/node/link"
)

// The status is the node's own view: identity from its config, the link's
// live state, and the mirror's app rows -- nothing asked of control.
func TestNodeStatusReportsLinkAndMirror(t *testing.T) {
	t.Parallel()
	conf := &Config{NodeID: "stage-2", ControlURL: "10.0.0.1:2930"}
	st := mirrorWith(t, "blog", 4242)
	link := nodelink.NewControlLink()

	s, err := nodeStatus(conf, st, link, "1.2.3")
	require.NoError(t, err)
	assert.Equal(t, "stage-2", s.NodeID)
	assert.Equal(t, "1.2.3", s.Version)
	assert.Equal(t, "10.0.0.1:2930", s.ControlURL)
	assert.False(t, s.Connected, "no client on the link means not connected")
	require.Len(t, s.Apps, 1)
	assert.Equal(t, "blog", s.Apps[0].Name)
	assert.Equal(t, 4242, s.Apps[0].UID)
	assert.Equal(t, 10000, s.Apps[0].Port)

	link.SetClient(&http.Client{})
	s, err = nodeStatus(conf, st, link, "1.2.3")
	require.NoError(t, err)
	assert.True(t, s.Connected)
}

// The status socket is root-only (0600): it is the operator's, unlike the app
// socket next to it, which is world-connectable and peercred-gated.
func TestNodeStatusSocketIsRootOnlyAndServes(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "node.sock")
	closer, err := ServeStatusSocket(path, &Config{NodeID: "n1", ControlURL: "c:2930"}, mirrorWith(t, "blog", 4242), nodelink.NewControlLink(), "9.9.9")
	require.NoError(t, err)
	t.Cleanup(func() { _ = closer.Close() })

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "operators only; apps have their own socket")

	client := http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", path)
		},
	}}
	resp, err := client.Get("http://node/v1/status")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var s Status
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&s))
	assert.Equal(t, "n1", s.NodeID)
	assert.Equal(t, "9.9.9", s.Version)
	require.Len(t, s.Apps, 1)
	assert.Equal(t, "blog", s.Apps[0].Name)
}
