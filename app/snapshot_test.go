package app

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSnapshotID(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 8, 7, 14, 5, 1, 0, time.UTC)
	assert.Equal(t, "20260807-140501-auto", snapshotID(ts, "auto"))
}
