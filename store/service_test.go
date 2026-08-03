package store

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddAndGetApp(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{Name: "blog", Port: 10000, Host: HostLocal}))
	app, err := s.App("blog")
	require.NoError(t, err)
	assert.Equal(t, "blog", app.Name)
	assert.Equal(t, 10000, app.Port)
	assert.Equal(t, HostLocal, app.Host)
	assert.False(t, app.CreatedAt.IsZero())
}

func TestAppNotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	_, err := s.App("nope")
	require.ErrorIs(t, err, ErrAppNotFound)
}

func TestAddAppDuplicateName(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{Name: "blog", Port: 10000, Host: HostLocal}))
	require.Error(t, s.AddApp(&App{Name: "blog", Port: 10001, Host: HostLocal}))
}

func TestAddAppDuplicatePort(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{Name: "blog", Port: 10000, Host: HostLocal}))
	require.Error(t, s.AddApp(&App{Name: "wiki", Port: 10000, Host: HostLocal}))
}

func TestApps(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{Name: "wiki", Port: 10001, Host: HostLocal}))
	require.NoError(t, s.AddApp(&App{Name: "blog", Port: 10000, Host: HostLocal}))
	apps, err := s.Apps()
	require.NoError(t, err)
	require.Len(t, apps, 2)
	assert.Equal(t, "blog", apps[0].Name) // Sorted by name
	assert.Equal(t, "wiki", apps[1].Name)
}

func TestRemoveApp(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{Name: "blog", Port: 10000, Host: HostLocal}))
	require.NoError(t, s.RemoveApp("blog"))
	_, err := s.App("blog")
	require.ErrorIs(t, err, ErrAppNotFound)
	require.ErrorIs(t, s.RemoveApp("blog"), ErrAppNotFound)
}

func TestUsedPorts(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{Name: "blog", Port: 10000, Host: HostLocal}))
	require.NoError(t, s.AddApp(&App{Name: "wiki", Port: 10005, Host: HostLocal}))
	ports, err := s.UsedPorts()
	require.NoError(t, err)
	assert.Equal(t, []int{10000, 10005}, ports)
}

func TestPersistence(t *testing.T) {
	t.Parallel()
	filename := filepath.Join(t.TempDir(), "hostit.db")
	s, err := NewStore(filename)
	require.NoError(t, err)
	require.NoError(t, s.AddApp(&App{Name: "blog", Port: 10000, Host: HostLocal}))
	require.NoError(t, s.Close())
	s2, err := NewStore(filename)
	require.NoError(t, err)
	defer s2.Close()
	app, err := s2.App("blog")
	require.NoError(t, err)
	assert.Equal(t, 10000, app.Port)
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(filepath.Join(t.TempDir(), "hostit.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = s.Close()
	})
	return s
}
