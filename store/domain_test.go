package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDomainRoundTrip(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{Name: "blog", Port: 10000, Host: HostLocal}))

	require.NoError(t, s.AddDomain(&Domain{Domain: "blog.example.com", AppName: "blog", Status: DomainPending, CreatedAt: time.Now()}))

	got, err := s.Domain("blog.example.com")
	require.NoError(t, err)
	assert.Equal(t, "blog", got.AppName)
	assert.Equal(t, DomainPending, got.Status)
	assert.Nil(t, got.ActiveAt)

	list, err := s.Domains("blog")
	require.NoError(t, err)
	require.Len(t, list, 1)

	now := time.Now()
	require.NoError(t, s.SetDomainStatus("blog.example.com", DomainActive, "", &now))
	got, err = s.Domain("blog.example.com")
	require.NoError(t, err)
	assert.Equal(t, DomainActive, got.Status)
	require.NotNil(t, got.ActiveAt)

	require.NoError(t, s.DeleteDomain("blog.example.com"))
	_, err = s.Domain("blog.example.com")
	assert.ErrorIs(t, err, ErrAppDomainNotFound)
}

func TestAddDomainRejectsDuplicate(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{Name: "blog", Port: 10000, Host: HostLocal}))
	require.NoError(t, s.AddApp(&App{Name: "other", Port: 10001, Host: HostLocal}))
	require.NoError(t, s.AddDomain(&Domain{Domain: "x.example.com", AppName: "blog", Status: DomainPending, CreatedAt: time.Now()}))

	err := s.AddDomain(&Domain{Domain: "x.example.com", AppName: "other", Status: DomainPending, CreatedAt: time.Now()})
	assert.ErrorIs(t, err, ErrAppDomainExists)
}

func TestRemoveAppDeletesDomains(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{Name: "blog", Port: 10000, Host: HostLocal}))
	require.NoError(t, s.AddDomain(&Domain{Domain: "x.example.com", AppName: "blog", Status: DomainPending, CreatedAt: time.Now()}))

	require.NoError(t, s.RemoveApp("blog"))
	_, err := s.Domain("x.example.com")
	assert.ErrorIs(t, err, ErrAppDomainNotFound)
}

func TestActiveDomainsReturnsAllPerApp(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{Name: "blog", Port: 10000, Host: HostLocal}))
	require.NoError(t, s.AddDomain(&Domain{Domain: "a.example.com", AppName: "blog", Status: DomainActive, CreatedAt: time.Now().Add(-2 * time.Hour)}))
	require.NoError(t, s.AddDomain(&Domain{Domain: "b.example.com", AppName: "blog", Status: DomainActive, CreatedAt: time.Now().Add(-1 * time.Hour)}))
	require.NoError(t, s.AddDomain(&Domain{Domain: "c.example.com", AppName: "blog", Status: DomainPending, CreatedAt: time.Now()}))

	active, err := s.ActiveDomains()
	require.NoError(t, err)
	assert.Equal(t, []string{"a.example.com", "b.example.com"}, active["blog"], "all active domains, oldest first; pending excluded")
}

func TestAllDomains(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{Name: "blog", Port: 10000, Host: HostLocal}))
	require.NoError(t, s.AddDomain(&Domain{Domain: "a.example.com", AppName: "blog", Status: DomainActive, CreatedAt: time.Now()}))
	require.NoError(t, s.AddDomain(&Domain{Domain: "b.example.com", AppName: "blog", Status: DomainPending, CreatedAt: time.Now()}))

	all, err := s.AllDomains()
	require.NoError(t, err)
	assert.Len(t, all, 2)
}
