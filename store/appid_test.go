package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddAppAssignsID(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{Name: "blog", Port: 10000}))
	require.NoError(t, s.AddApp(&App{Name: "shop", Port: 10001}))

	blog, err := s.App("blog")
	require.NoError(t, err)
	shop, err := s.App("shop")
	require.NoError(t, err)

	// Every app is born with a non-empty, distinct id.
	assert.NotEmpty(t, blog.ID)
	assert.NotEmpty(t, shop.ID)
	assert.NotEqual(t, blog.ID, shop.ID)
}

func TestAddAppKeepsExplicitID(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{ID: "fixedid", Name: "blog", Port: 10000}))
	got, err := s.App("blog")
	require.NoError(t, err)
	assert.Equal(t, "fixedid", got.ID)
}

// An app row whose id is the empty string must never match another app's rows.
// Migration 12 makes ” a legitimate value ("not an app row, or not yet
// backfilled"), so `app_id = (SELECT id FROM app WHERE name = ?)` collapses to
// `app_id = ”` for such a row and matches every OTHER unbackfilled row -- which
// for SSH keys means one tenant's key landing in another's authorized_keys. The
// one-time backfill has since been deleted, so an instance upgraded from before
// it keeps those rows forever; the queries have to defend themselves.
func TestEmptyAppIDNeverCrossMatches(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	// AddApp assigns an id, so the legacy shape has to be written directly --
	// which is exactly how a database upgraded from before the backfill looks.
	_, err := s.db.Exec(`INSERT INTO app (id, name, port, host, owner_id, created_at, image_tag, uid, private) VALUES ('', 'legacy-a', 10000, 'local', 'u1', 0, '', 0, 0)`)
	require.NoError(t, err)
	_, err = s.db.Exec(`INSERT INTO app (id, name, port, host, owner_id, created_at, image_tag, uid, private) VALUES ('', 'legacy-b', 10001, 'local', 'u2', 0, '', 0, 0)`)
	require.NoError(t, err)
	require.NoError(t, s.SetAppKeys("legacy-b", []string{"ssh-ed25519 AAAA u2-key"}))

	keys, err := s.AppKeys("legacy-a")
	require.NoError(t, err)
	assert.Empty(t, keys, "another tenant's key must not resolve for this app")

	tokens, err := s.TokensByApp("legacy-a")
	require.NoError(t, err)
	assert.Empty(t, tokens, "nor another app's tokens")
}
