package workspace

import (
	"strconv"
	"strings"
	"testing"

	"heckel.io/hostit/controlconf"
)

// The in-container loopback API (controlconf.ContainerAPIAddr) must NOT share the
// app's own container port. An app listens on 0.0.0.0:containerPort, which covers
// every loopback address, so a shared port makes the app fail to bind with
// "address already in use" -- this regressed once, when the loopback was on :80.
func TestContainerAPIPortIsClearOfTheAppPort(t *testing.T) {
	t.Parallel()
	_, port, ok := strings.Cut(controlconf.ContainerAPIAddr, ":")
	if !ok || port == "" {
		t.Fatalf("ContainerAPIAddr %q has no port", controlconf.ContainerAPIAddr)
	}
	if port == strconv.Itoa(containerPort) {
		t.Fatalf("loopback API port %s == the app's container port %d; an app on 0.0.0.0:%d cannot bind",
			port, containerPort, containerPort)
	}
}
