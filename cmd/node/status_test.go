package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"heckel.io/hostit/node"
)

func TestRenderNodeStatus(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	renderNodeStatus(&buf, &node.Status{
		NodeID: "stage-2", Version: "1.2.3", ControlURL: "10.0.0.1:2930", Connected: true,
		Apps: []node.StatusApp{{Name: "blog", UID: 101000, Port: 10000}},
	})
	out := buf.String()
	for _, want := range []string{"stage-2", "1.2.3", "10.0.0.1:2930", "connected", "NAME", "blog", "101000", "10000"} {
		assert.Contains(t, out, want)
	}
}

// A down link is the one thing this command exists to show; it must not read
// like a detail.
func TestRenderNodeStatusSaysDisconnectedLoudly(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	renderNodeStatus(&buf, &node.Status{NodeID: "stage-2", ControlURL: "10.0.0.1:2930"})
	assert.Contains(t, buf.String(), "NOT CONNECTED")
	assert.Contains(t, buf.String(), "none", "an empty mirror says so instead of an empty table")
}
