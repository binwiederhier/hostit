package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventLog(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	// Events attach to an app by its id, so the apps must exist first (as they
	// always do in production: an action is logged only after its app is created).
	require.NoError(t, s.AddApp(&App{Name: "blog", Port: 10000}))
	require.NoError(t, s.AddApp(&App{Name: "other", Port: 10001}))

	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < 3; i++ {
		require.NoError(t, s.AddEvent(&Event{AppName: "blog", CreatedAt: base.Add(time.Duration(i) * time.Minute), Actor: "phil@example.com", Level: "info", Action: "created", Detail: "did thing"}))
	}
	require.NoError(t, s.AddEvent(&Event{AppName: "other", CreatedAt: base, Action: "created", Detail: "elsewhere"}))

	// Newest first, scoped to the app.
	events, err := s.AppEvents("blog", 100)
	require.NoError(t, err)
	require.Len(t, events, 3)
	assert.True(t, events[0].CreatedAt.After(events[1].CreatedAt))
	assert.Equal(t, "phil@example.com", events[0].Actor)
	assert.Equal(t, "created", events[0].Action)

	// The limit is honored.
	events, err = s.AppEvents("blog", 2)
	require.NoError(t, err)
	assert.Len(t, events, 2)

	// Removing an app forgets its events, not another app's.
	require.NoError(t, s.RemoveApp("blog"))
	events, err = s.AppEvents("blog", 100)
	require.NoError(t, err)
	assert.Empty(t, events)
	events, err = s.AppEvents("other", 100)
	require.NoError(t, err)
	assert.Len(t, events, 1)
}
