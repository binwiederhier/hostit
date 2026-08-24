package node

import (
	"path/filepath"
	"strings"
	"testing"

	"heckel.io/hostit/controlconf"
	"heckel.io/hostit/nodeconf"
	"heckel.io/hostit/workspace"
)

// Tenant isolation rests on three constants in three packages agreeing. Comments
// cannot enforce that; this test does. If any of these break, a container either
// cannot reach its socket or -- worse -- can see apps-raw and its neighbours'
// files again.
func TestScopedSocketMountInvariants(t *testing.T) {
	t.Parallel()

	// 1. Inside a container the socket dir mounts at ContainerRunDir, so the
	//    in-container CLI's DefaultSocketFile must live directly in it.
	if got := filepath.Dir(controlconf.DefaultSocketFile); got != workspace.ContainerRunDir {
		t.Fatalf("controlconf.DefaultSocketFile is under %q, but the mount target is %q", got, workspace.ContainerRunDir)
	}

	// 2. The host login shell dials HostAppSocketFile; the node serves at
	//    nodeconf's SocketFile. They must be the same host path.
	if controlconf.HostAppSocketFile != nodeconf.NewConfig().SocketFile {
		t.Fatalf("login shell dials %q but the node serves %q", controlconf.HostAppSocketFile, nodeconf.NewConfig().SocketFile)
	}

	// 3. The host socket sits in its OWN subdir -- the mount source -- and the raw
	//    apps view must NOT be inside that subdir, or every container sees it.
	mountSource := filepath.Dir(controlconf.HostAppSocketFile)
	rawView := RawAppsViewDir(controlconf.HostAppSocketFile)
	if rawView == mountSource || strings.HasPrefix(rawView, mountSource+"/") {
		t.Fatalf("raw apps view %q is inside the mounted subdir %q -- neighbours' files are exposed", rawView, mountSource)
	}
}
