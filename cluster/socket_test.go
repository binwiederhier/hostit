package cluster

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTrustedPeerUID(t *testing.T) {
	saved := extraTrustedUIDs
	t.Cleanup(func() { extraTrustedUIDs = saved })
	// Control's own uid and root are always admitted.
	if !trustedPeerUID(os.Getuid()) {
		t.Error("control's own uid must be trusted")
	}
	if !trustedPeerUID(0) {
		t.Error("root must be trusted")
	}
	// An unrelated uid (the colocated proxy's, before registration) is not.
	const proxyUID = 4242
	if trustedPeerUID(proxyUID) {
		t.Fatal("an unregistered uid must not be trusted")
	}
	// After control registers the proxy's uid it is admitted: the proxy runs as
	// its own user now and no longer shares control's uid.
	TrustPeerUID(proxyUID)
	if !trustedPeerUID(proxyUID) {
		t.Error("a uid registered via TrustPeerUID must be trusted")
	}
}

// The cluster member socket must be world-openable (0666) so a colocated proxy
// running as its own user can connect and reach the peer-cred gate; a status
// socket stays root-only (0600). A regression here took prod HTTPS down: the
// proxy could not open a 0600 socket owned by hostit-control, so the peer gate
// -- which would have admitted it -- never ran.
func TestListenSocketMode(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o666} {
		path := filepath.Join(t.TempDir(), "s.sock")
		ln, err := ListenSocket(path, mode)
		if err != nil {
			t.Fatalf("ListenSocket(%o): %v", mode, err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != mode {
			t.Errorf("socket mode = %o, want %o", got, mode)
		}
		_ = ln.Close()
	}
}
