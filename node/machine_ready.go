package node

import (
	"net"
	"strconv"
	"time"
)

// Readiness tuning for waitForApp; a package var so a deploy can wait a sensible
// while for a slow boot without a test having to.
var (
	appReadyDeadline    = 20 * time.Second
	appReadyDialTimeout = time.Second
	appReadyPoll        = 200 * time.Millisecond
)

// waitForApp blocks until the app's port accepts a TCP connection, so a deploy
// does not report success -- and the owner's live preview does not reload --
// before the freshly (re)started server is actually listening. Best effort: it
// gives up after appReadyDeadline whether or not the app came up, since a broken
// app must not wedge the deploy. Reports whether the port ever answered.
func (m *Machine) waitForApp(port int) bool {
	return m.waitForAppUntil(port, time.Now().Add(appReadyDeadline))
}

func (m *Machine) waitForAppUntil(port int, deadline time.Time) bool {
	if port == 0 {
		return false
	}
	// The app binds AppsBindAddress:$PORT; an unset or wildcard bind is reachable
	// on loopback, which is all this check needs.
	host := m.config.AppsBindAddress
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	for {
		conn, err := net.DialTimeout("tcp", addr, appReadyDialTimeout)
		if err == nil {
			_ = conn.Close()
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(appReadyPoll)
	}
}
