package app

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An app says how often it wants snapshotting in its own hostit.yml, and the
// three answers that matter are "say nothing" (take the default), "say a
// duration" and "say zero" (stop snapshotting me).
func TestSnapshotIntervalFromConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		yaml     string
		want     time.Duration
		disabled bool
	}{
		{
			name: "unset takes the default",
			yaml: "mode: static\n",
			want: DefaultSnapshotInterval,
		},
		{
			name: "a duration is honoured",
			yaml: "mode: static\nsnapshot:\n  interval: 45m\n",
			want: 45 * time.Minute,
		},
		{
			name: "hours",
			yaml: "mode: static\nsnapshot:\n  interval: 12h\n",
			want: 12 * time.Hour,
		},
		{
			name:     "zero disables",
			yaml:     "mode: static\nsnapshot:\n  interval: 0\n",
			disabled: true,
		},
		{
			name: "hooks still parse alongside it",
			yaml: "mode: static\nsnapshot:\n  interval: 6h\n  pre: sqlite3 x .backup\n  post: rm -f y\n",
			want: 6 * time.Hour,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := LoadConfig([]byte(tt.yaml))
			require.NoError(t, err)
			got, err := c.SnapshotInterval()
			require.NoError(t, err)
			if tt.disabled {
				assert.Zero(t, got, "zero means the app opts out of automatic snapshots")
				return
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// A typo in the interval is a config error the owner sees, not a value silently
// ignored -- the same treatment an unknown key already gets.
func TestSnapshotIntervalRejectsNonsense(t *testing.T) {
	t.Parallel()
	_, err := LoadConfig([]byte("mode: static\nsnapshot:\n  interval: soon\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "snapshot.interval")

	_, err = LoadConfig([]byte("mode: static\nsnapshot:\n  interval: -1h\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "snapshot.interval")
}

// Hooks with no interval keep working exactly as before this field existed.
func TestSnapshotHooksUnaffected(t *testing.T) {
	t.Parallel()
	c, err := LoadConfig([]byte("mode: static\nsnapshot:\n  pre: flush\n  post: cleanup\n"))
	require.NoError(t, err)
	assert.Equal(t, "flush", c.Snapshot.Pre)
	assert.Equal(t, "cleanup", c.Snapshot.Post)
	d, err := c.SnapshotInterval()
	require.NoError(t, err)
	assert.Equal(t, DefaultSnapshotInterval, d)
}
