package node

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"heckel.io/hostit/btrfs"
	"heckel.io/hostit/container"
	"heckel.io/hostit/firewall"
	"heckel.io/hostit/nodeapi"
	"heckel.io/hostit/run"
	"heckel.io/hostit/ssh"
	"heckel.io/hostit/store"
	"heckel.io/hostit/systemd"
	"heckel.io/hostit/unixuser"
)

// Control builds a mirror by reading its registry and sends it afterwards, so
// two concurrent mutations can arrive out of order. Applying the older one
// would drop a just-created app from this mirror, and every verb for it would
// then fail with "app not found" until the next mutation pushed again (seen on
// stage during a parallel e2e run). The node applies only newer payloads.
func TestSyncIgnoresAStaleMirror(t *testing.T) {
	t.Parallel()
	m := newSyncTestMachine(t)
	two := []*store.App{{ID: "id1", Name: "one", Port: 10000}, {ID: "id2", Name: "two", Port: 10001}}
	one := two[:1]

	require.NoError(t, m.Sync(&nodeapi.SyncState{Seq: 5, Apps: two}))
	require.NoError(t, m.Sync(&nodeapi.SyncState{Seq: 4, Apps: one})) // the racing, older push

	apps, err := m.store.Apps()
	require.NoError(t, err)
	assert.Len(t, apps, 2, "the stale mirror must not drop the newer app")

	// A genuinely newer payload still applies.
	require.NoError(t, m.Sync(&nodeapi.SyncState{Seq: 6, Apps: one}))
	apps, err = m.store.Apps()
	require.NoError(t, err)
	assert.Len(t, apps, 1)
}

// The sequence belongs to control's process, so a reconnect (or a restarted
// control) starts counting again; the node must not mistake that for stale.
func TestSyncAcceptsALowerSequenceAfterReconnect(t *testing.T) {
	t.Parallel()
	m := newSyncTestMachine(t)
	require.NoError(t, m.Sync(&nodeapi.SyncState{Seq: 99, Apps: []*store.App{{ID: "id1", Name: "one", Port: 10000}}}))

	m.ResetSyncSeq()
	require.NoError(t, m.Sync(&nodeapi.SyncState{Seq: 1, Apps: []*store.App{
		{ID: "id1", Name: "one", Port: 10000}, {ID: "id2", Name: "two", Port: 10001},
	}}))

	apps, err := m.store.Apps()
	require.NoError(t, err)
	assert.Len(t, apps, 2, "a fresh connection's first push always applies")
}

// newSyncTestMachine builds a Machine over a temp store; the sync path needs
// nothing else (no host interaction, no services).
func newSyncTestMachine(t *testing.T) *Machine {
	t.Helper()
	conf := NewConfig()
	conf.DataDir, conf.AppsDir = t.TempDir(), t.TempDir()
	s, err := store.NewStore(filepath.Join(t.TempDir(), "node.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return NewMachine(conf, s, &Services{
		Runner: run.Nop{}, Btrfs: btrfs.New(run.Nop{}), Systemd: systemd.New(run.Nop{}),
		Container: container.New(run.Nop{}), User: nopUser{}, SSH: ssh.New(), Firewall: nopFirewall{},
	})
}

// The privileged services do nothing here: these tests are about the machine's
// own bookkeeping, not about touching the host.
type nopUser struct{}

func (nopUser) Exists(string) bool                            { return false }
func (nopUser) List() ([]unixuser.Account, error)             { return nil, nil }
func (nopUser) LookupUID(string) (int, error)                 { return 1001, nil }
func (nopUser) LookupIDs(string) (int, int, error)            { return 1001, 1001, nil }
func (nopUser) Create(string, string, int) error              { return nil }
func (nopUser) CreateStub(string, string) error               { return nil }
func (nopUser) Rename(string, string) error                   { return nil }
func (nopUser) KillProcesses(string) error                    { return nil }
func (nopUser) Delete(string) error                           { return nil }
func (nopUser) WriteSkeleton(string, map[string]string) error { return nil }

type nopFirewall struct{}

func (nopFirewall) Apply([]firewall.Rule) error { return nil }
