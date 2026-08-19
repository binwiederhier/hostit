package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFKsFollowRename proves the per-app tables key on app_id: a raw name change
// on the app row (what RenameApp does) leaves every attached row in place and
// reachable by the new name, reporting the new name -- no per-app table is touched.
func TestFKsFollowRename(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{Name: "blog", Port: 10000, OwnerID: "u_1"}))

	// Attach one row of each per-app kind to "blog".
	require.NoError(t, s.SetAppKeys("blog", []string{"ssh-ed25519 AAAA key"}))
	require.NoError(t, s.AddSnapshot(&Snapshot{ID: "sn_1", AppName: "blog", CreatedAt: time.Unix(1_700_000_000, 0)}))
	require.NoError(t, s.AddDomain(&Domain{Domain: "blog.example.com", AppName: "blog", Status: DomainActive, CreatedAt: time.Unix(1_700_000_000, 0)}))
	require.NoError(t, s.AddEvent(&Event{AppName: "blog", CreatedAt: time.Unix(1_700_000_000, 0), Action: "created"}))
	require.NoError(t, s.AddToken(&Token{UserID: "u_1", Hash: "h1", AppName: "blog", Secret: "sek"}))
	require.NoError(t, s.SaveAssistantSession("blog", `{"msgs":1}`))

	// Rename the way RenameApp will: change app.name; also carry the assistant
	// session's PK mirror (the one table that keys its PK on the name).
	_, err := s.db.Exec(`UPDATE app SET name = 'shop' WHERE name = 'blog'`)
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE assistant_session SET app_name = 'shop' WHERE app_id = (SELECT id FROM app WHERE name = 'shop')`)
	require.NoError(t, err)

	// Everything is reachable by the new name; nothing lingers under the old one.
	keys, err := s.AppKeys("shop")
	require.NoError(t, err)
	assert.Len(t, keys, 1)
	assert.Empty(t, mustKeys(t, s, "blog"))

	snaps, err := s.Snapshots("shop")
	require.NoError(t, err)
	require.Len(t, snaps, 1)
	assert.Equal(t, "shop", snaps[0].AppName) // reports the new name via the id join

	domains, err := s.Domains("shop")
	require.NoError(t, err)
	require.Len(t, domains, 1)
	assert.Equal(t, "shop", domains[0].AppName)
	active, err := s.ActiveDomains()
	require.NoError(t, err)
	assert.Equal(t, []string{"blog.example.com"}, active["shop"])

	events, err := s.AppEvents("shop", 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "shop", events[0].AppName)

	tokens, err := s.TokensByApp("shop")
	require.NoError(t, err)
	require.Len(t, tokens, 1)
	assert.Equal(t, "shop", tokens[0].AppName)

	transcript, err := s.LoadAssistantSession("shop")
	require.NoError(t, err)
	assert.Equal(t, `{"msgs":1}`, transcript)

	// The old name resolves to nothing.
	snaps, err = s.Snapshots("blog")
	require.NoError(t, err)
	assert.Empty(t, snaps)
}

func mustKeys(t *testing.T, s *Store, app string) []string {
	t.Helper()
	keys, err := s.AppKeys(app)
	require.NoError(t, err)
	return keys
}
