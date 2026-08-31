package cluster

import (
	"os"
	"testing"
)

func TestTrustedPeerUID(t *testing.T) {
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
