package control

import (
	"log/slog"

	"heckel.io/hostit/node"
	"heckel.io/hostit/store"
)

// serverPolicy answers the per-app questions that need the user tables: the
// complete SSH key set and the owner's resource limits. It is what makes the
// desired document control asserts (Manager.DesiredState) the whole truth
// rather than only what the app row happens to hold.
type serverPolicy struct{ s *Server }

var _ AppPolicy = (*serverPolicy)(nil)

// Keys is the app's own keys plus every standing profile key (its owner's and
// each collaborator's) -- the exact set authorized_keys should contain.
func (p *serverPolicy) Keys(a *store.App) []string {
	appKeys, err := p.s.apps.Store().AppKeys(a.Name)
	if err != nil {
		slog.Warn("Cannot read an app's own keys", "app", a.Name, "error", err)
	}
	profileKeys, err := p.s.appProfileKeys(a)
	if err != nil {
		slog.Warn("Cannot resolve an app's profile keys", "app", a.Name, "error", err)
	}
	return append(appKeys, profileKeys...)
}

// Limits are the app's own admin-set overrides where present, else the
// owner's (falling back to the global defaults for an app with no owner) --
// resolved fresh from the registry every time, so a limit changed while a
// node was away is asserted on the next reconcile instead of reverting to
// whatever a control process cached at boot. The override check is what keeps
// a daemon restart from quietly un-applying an admin's per-app cap.
func (p *serverPolicy) Limits(a *store.App) (memoryMB, diskMB int) {
	limits, err := p.s.users.Defaults()
	if err != nil {
		slog.Warn("Cannot read the default limits", "error", err)
		return 0, 0
	}
	if a.OwnerID != "" {
		if owner, err := p.s.users.User(a.OwnerID); err == nil {
			if owned, err := p.s.users.Limits(owner); err == nil {
				limits = owned
			}
		}
	}
	memoryMB, diskMB = limits.MemoryMB, limits.DiskMB
	if a.MemoryLimitMB > 0 {
		memoryMB = a.MemoryLimitMB
	}
	if a.DiskLimitMB > 0 {
		diskMB = a.DiskLimitMB
	}
	// Resolved here, not on the node: nothing is uncapped, so a zero limit is
	// the default cap. Leaving the node to reinterpret the zero made control
	// report "no limit" for an app the filesystem was hard-capping.
	return memoryMB, node.EffectiveDiskCapMB(diskMB)
}
