package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"heckel.io/hostit/node"
)

func TestHeartbeatReportsBuildAndBtrfs(t *testing.T) {
	t.Parallel()
	m, _, runner := newTestDeployManager(t)
	runner.returns("stat -f", "btrfs\n")
	hb := m.Heartbeat()
	assert.Equal(t, node.Version, hb.Version)
	assert.True(t, hb.BtrfsCapable)
}
