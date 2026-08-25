package control

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/archive"
	"heckel.io/hostit/nodeapi"
	"heckel.io/hostit/store"
)

// archiveNode is a NodeAgent whose only real method is ArchiveWorkspace, so the
// export handler is testable without a node or btrfs.
type archiveNode struct {
	nodeapi.NodeAgent
	gotFormat archive.Format
	body      string
	err       error
}

func (n *archiveNode) ArchiveWorkspace(_ string, format archive.Format) (io.ReadCloser, error) {
	n.gotFormat = format
	if n.err != nil {
		return nil, n.err
	}
	return io.NopCloser(strings.NewReader(n.body)), nil
}

func exportServer(t *testing.T, node nodeapi.NodeAgent) *Server {
	t.Helper()
	s := newTestServer(t)
	require.NoError(t, s.apps.Store().AddApp(&store.App{Name: "expapp", Port: 10000, Host: store.HostLocal}))
	s.SetNode(node)
	return s
}

// No format asked for: a .zip, named for the app, streamed straight through.
func TestExportDefaultsToZip(t *testing.T) {
	t.Parallel()
	node := &archiveNode{body: "PK-ZIP-BYTES"}
	s := exportServer(t, node)

	rr := request(t, s.API(), "GET", "/api/apps/expapp/export", "", testToken)
	require.Equal(t, 200, rr.Code, rr.Body.String())
	assert.Equal(t, archive.Zip, node.gotFormat, "no format -> zip")
	assert.Equal(t, "application/zip", rr.Header().Get("Content-Type"))
	assert.Contains(t, rr.Header().Get("Content-Disposition"), `"expapp.zip"`)
	assert.Equal(t, "PK-ZIP-BYTES", rr.Body.String(), "the archive streams through untouched")
}

// ?format=tar: a gzipped tar, named .tar.gz.
func TestExportTarFormat(t *testing.T) {
	t.Parallel()
	node := &archiveNode{body: "gzip"}
	s := exportServer(t, node)

	rr := request(t, s.API(), "GET", "/api/apps/expapp/export?format=tar", "", testToken)
	require.Equal(t, 200, rr.Code)
	assert.Equal(t, archive.TarGz, node.gotFormat)
	assert.Equal(t, "application/gzip", rr.Header().Get("Content-Type"))
	assert.Contains(t, rr.Header().Get("Content-Disposition"), `"expapp.tar.gz"`)
}

// A node error (app not on this node, snapshot failed) surfaces before any bytes,
// so it can still be a real status rather than a half-written download.
func TestExportNodeErrorSurfaces(t *testing.T) {
	t.Parallel()
	node := &archiveNode{err: errors.New("workspace is not on this node")}
	s := exportServer(t, node)

	rr := request(t, s.API(), "GET", "/api/apps/expapp/export", "", testToken)
	assert.GreaterOrEqual(t, rr.Code, 400, "an error is not a 200 with a broken body")
	assert.NotContains(t, rr.Body.String(), "PK", "no archive bytes on the error path")
}
