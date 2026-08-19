package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hostit.yml belongs to the tenant, so editing one field must leave everything
// else -- other keys, ordering, comments -- exactly as they wrote it. That rules
// out parse-and-remarshal, which would quietly delete every comment in the file.
func TestSetSnapshotConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		in    string
		set   SnapshotHooks
		want  string
		check func(t *testing.T, out string)
	}{
		{
			name: "adds a block when there is none",
			in:   "mode: static\n",
			set:  SnapshotHooks{Interval: "6h"},
			want: "mode: static\nsnapshot:\n  interval: 6h\n",
		},
		{
			name: "updates an existing interval in place",
			in:   "mode: static\nsnapshot:\n  interval: 3h\n",
			set:  SnapshotHooks{Interval: "30m"},
			want: "mode: static\nsnapshot:\n  interval: 30m\n",
		},
		{
			name: "adds a key to an existing block without touching its hooks",
			in:   "mode: static\nsnapshot:\n  pre: flush\n",
			set:  SnapshotHooks{Interval: "12h", Pre: "flush"},
			want: "mode: static\nsnapshot:\n  pre: flush\n  interval: 12h\n",
		},
		{
			name: "an empty value removes the key",
			in:   "mode: static\nsnapshot:\n  interval: 3h\n  pre: flush\n",
			set:  SnapshotHooks{Pre: "flush"},
			want: "mode: static\nsnapshot:\n  pre: flush\n",
		},
		{
			name: "clearing every field removes the block entirely",
			in:   "mode: static\nsnapshot:\n  interval: 3h\n",
			set:  SnapshotHooks{},
			want: "mode: static\n",
		},
		{
			name: "keys after the block stay put",
			in:   "mode: app\nsnapshot:\n  interval: 3h\nrun: ./server\n",
			set:  SnapshotHooks{Interval: "1h"},
			want: "mode: app\nsnapshot:\n  interval: 1h\nrun: ./server\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SetSnapshotConfig(tt.in, tt.set))
		})
	}
}

// The tenant's comments survive an edit, including one inside the block.
func TestSetSnapshotConfigKeepsComments(t *testing.T) {
	t.Parallel()
	in := "# what this app is\nmode: static\n\n# quiesce the db\nsnapshot:\n  pre: sqlite3 a.db \".backup b.db\"\n  interval: 3h\n"
	out := SetSnapshotConfig(in, SnapshotHooks{Interval: "8h", Pre: "sqlite3 a.db \".backup b.db\""})
	assert.Contains(t, out, "# what this app is")
	assert.Contains(t, out, "# quiesce the db")
	assert.Contains(t, out, "interval: 8h")
	assert.NotContains(t, out, "interval: 3h")
}

// Whatever is written must read back as the value that was set -- the pairing
// that would catch a quoting bug in either direction.
func TestSetSnapshotConfigRoundTrips(t *testing.T) {
	t.Parallel()
	hooks := SnapshotHooks{
		Interval: "45m",
		Pre:      `sqlite3 data/app.db ".backup data/app.snap.db"`,
		Post:     "rm -f data/app.snap.db",
	}
	out := SetSnapshotConfig("mode: static\n", hooks)
	c, err := LoadConfig([]byte(out))
	require.NoError(t, err, "the document it writes must still parse: %s", out)
	assert.Equal(t, hooks.Pre, c.Snapshot.Pre)
	assert.Equal(t, hooks.Post, c.Snapshot.Post)
	d, err := c.SnapshotInterval()
	require.NoError(t, err)
	assert.Equal(t, "45m0s", d.String())
}
