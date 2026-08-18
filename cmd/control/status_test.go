package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/urfave/cli/v2"
	"heckel.io/hostit/control"
	"heckel.io/hostit/controlconf"
)

// The command exists and is discoverable: an operator looking for "how is the
// cluster" should find it without reading the source.
func TestControlHasAStatusCommand(t *testing.T) {
	app := newControlApp("v0.0.0-test")
	var found bool
	for _, cmd := range app.Commands {
		if cmd.Name == "status" {
			found = true
			assert.Contains(t, cmd.Usage, "cluster")
		}
	}
	assert.True(t, found, "hostit-control status must exist")
}

// The output has to answer the question at a glance: who is in the cluster,
// which of them stopped reporting, and what is being carried. A member gone
// quiet is the whole reason to run this, so it is called out in words rather
// than left as a timestamp to subtract.
func TestStatusOutputNamesMembersAndCallsOutSilence(t *testing.T) {
	now := time.Now()
	status := &control.Status{
		Snapshot: now,
		Nodes: []*control.MemberStatus{
			{Name: "worker-1", Address: "10.0.0.2", Apps: 3, LastSeen: now.Add(-4 * time.Second)},
			{Name: "worker-2", Address: "10.0.0.3", Apps: 0, LastSeen: now.Add(-20 * time.Minute), Stale: true},
		},
		Proxies: []*control.MemberStatus{
			{Name: "local", Version: "v0.13.0 (abc123, built today)", Routes: 7, LastSeen: now.Add(-8 * time.Second)},
		},
		Apps:   &control.AppTotals{Total: 3, PoweredOff: 1, Snapshots: 5, DiskUsedMB: 2048, Unplaced: 1},
		People: &control.PeopleTotals{Total: 4, Admins: 1, Pending: 2},
	}

	var out bytes.Buffer
	app := newControlApp("v0.0.0-test")
	app.Writer = &out
	printStatus(cli.NewContext(app, nil, nil), status)
	got := out.String()

	assert.Contains(t, got, "NODES (2)")
	assert.Contains(t, got, "worker-1")
	assert.Contains(t, got, "10.0.0.2")
	assert.Contains(t, got, "3 apps")
	assert.Contains(t, got, "PROXIES (1)")
	assert.Contains(t, got, "7 routes")
	assert.Contains(t, got, "v0.13.0", "the build is shown")
	assert.NotContains(t, got, "built today", "but not its whole build string")

	assert.Contains(t, got, "seen 8s ago", "a live member reads as an age")
	assert.Contains(t, got, "LAST SEEN 20m0s ago", "a quiet one is shouted")
	assert.Contains(t, got, "have not reported recently", "and summarized at the end")

	assert.Contains(t, got, "3 total, 1 powered off, 5 snapshots, 2.0 GB on disk")
	assert.Contains(t, got, "not routable", "an app on an unregistered node is flagged")
	assert.Contains(t, got, "4 total, 1 admins, 2 awaiting approval")
}

// Enrolling a member on another machine when control admits none produces
// instructions that cannot work: the printed control-url would carry an empty
// port. Refuse with the fix instead.
func TestEnrollingARemoteMemberNeedsAListener(t *testing.T) {
	conf := controlconf.NewConfig()
	assert.Empty(t, conf.ListenCluster, "a single-box install admits no remote members")
}
