package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"heckel.io/hostit/store"
	"heckel.io/hostit/user"
)

const (
	// The floors refuse caps too small to run anything: podman rejects tiny
	// --memory values outright, a sub-tenth CPU cannot finish a health check,
	// and a too-small disk budget EDQUOTs on the first write. Owner DEFAULTS
	// are not floored here; these only guard the explicit per-app overrides.
	minMemoryLimitMB = 64
	minDiskLimitMB   = 256
	minCPUMilli      = 100
)

// apiLimitsPatch is the PATCH body: 0/absent leaves a field alone, -1 clears
// the override, a positive value sets it. The -1 convention keeps "skip" and
// "clear" distinguishable without pointer-typed JSON fields.
type apiLimitsPatch struct {
	MemoryMB int `json:"memory_mb"`
	DiskMB   int `json:"disk_mb"`
	CPUMilli int `json:"cpu_milli"`
}

// handleAppLimitsUpdate sets an app's per-app resource limit OVERRIDES. The
// owner (or an admin) edits RAM and disk WITHIN the owner's pool -- the pool
// is the cap, and it binds admins too, so there is one invariant instead of a
// bypass. CPU stays admin-set (it is a shared cap, not a pooled reservation),
// and an app-scoped token is refused outright: it is the app's own agent, and
// an assistant must not raise its own caps. Disk applies live (the node
// re-caps the budget qgroup); memory and CPU are recorded and take effect at
// the next container recreation (reboot, power cycle or deploy) --
// deliberately no auto-reboot, since editing a number must not secretly
// restart a tenant's app.
// maxAppLimitMB is a sanity ceiling on a per-app override: far above any real
// app, far below where summing a few would overflow the pool-fit arithmetic.
const maxAppLimitMB = 1 << 30

func (s *Server) handleAppLimitsUpdate(w http.ResponseWriter, r *http.Request, c *caller) {
	if c.appScope != "" {
		writeError(w, http.StatusForbidden, errors.New("an app token cannot edit its own limits"))
		return
	}
	a, err := s.ownerApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	var req apiLimitsPatch
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	patch := func(current, requested, floor int, what string) (int, error) {
		switch {
		case requested == 0:
			return current, nil
		case requested == -1:
			return 0, nil
		case requested < floor:
			return 0, fmt.Errorf("%s must be at least %d (or -1 to clear the override)", what, floor)
		case requested > maxAppLimitMB:
			return 0, fmt.Errorf("%s is unreasonably large", what)
		default:
			return requested, nil
		}
	}
	memoryMB, err := patch(a.MemoryLimitMB, req.MemoryMB, minMemoryLimitMB, "memory_mb")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	diskMB, err := patch(a.DiskLimitMB, req.DiskMB, minDiskLimitMB, "disk_mb")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	cpuMilli, err := patch(a.CPUMilli, req.CPUMilli, minCPUMilli, "cpu_milli")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.CPUMilli != 0 && !c.isAdmin() {
		writeError(w, http.StatusForbidden, errors.New("the CPU cap is set by an admin"))
		return
	}
	// The pool-fit check reads every sibling app's limits, so it must be atomic
	// with the write, or two concurrent updates each pass and overcommit the pool.
	s.limitsMu.Lock()
	if err := s.checkPoolFits(a, memoryMB, diskMB); err != nil {
		s.limitsMu.Unlock()
		writeError(w, http.StatusForbidden, err)
		return
	}
	if err := s.apps.Store().UpdateAppLimits(a.Name, memoryMB, diskMB, cpuMilli); err != nil {
		s.limitsMu.Unlock()
		writeAppError(w, err)
		return
	}
	s.limitsMu.Unlock()
	// Record and assert the EFFECTIVE limits (override where set, else the
	// owner's defaults), so the desired state, the API and the node agree
	// immediately -- a disconnected node picks them up on its next reconcile.
	effMemory, effDisk, effCPU := s.appLimits(a.Name)
	s.apps.RecordLimits(a.Name, effMemory, effDisk, effCPU)
	s.node.SetMemoryLimit(a.Name, effMemory)
	s.node.SetDiskLimit(a.Name, effDisk)
	s.node.SetCPULimit(a.Name, effCPU)
	a, err = s.apps.App(a.Name) // re-read so the response shows what landed
	if err != nil {
		writeAppError(w, err)
		return
	}
	resp := s.appResponseFor(c, a, s.firstActiveDomain(a.Name))
	writeJSON(w, http.StatusOK, s.withState([]*apiAppResponse{resp})[0])
}

// checkPoolFits refuses new overrides that would push the SUM of the owner's
// apps' effective limits past their pool. The new values are the app's
// prospective overrides (0 = inherit), everything else counts at its current
// effective value; ownerless (admin-token) apps have no pool.
func (s *Server) checkPoolFits(a *store.App, memoryLimitMB, diskLimitMB int) error {
	if a.OwnerID == "" {
		return nil
	}
	owner, err := s.users.User(a.OwnerID)
	if err != nil {
		return err
	}
	limits, err := s.users.Limits(owner)
	if err != nil {
		return err
	}
	usedMemory, usedDisk, err := s.poolReserved(a.OwnerID, a.Name)
	if err != nil {
		return err
	}
	newMemory, newDisk, _ := user.EffectiveAppLimits(limits,
		&store.App{MemoryLimitMB: memoryLimitMB, DiskLimitMB: diskLimitMB})
	if limits.MemoryPoolMB > 0 && usedMemory+newMemory > limits.MemoryPoolMB {
		return fmt.Errorf("this would allocate %d MB of a %d MB memory pool (%d MB already allocated to other apps); lower another app's limit or ask an admin to raise the pool",
			usedMemory+newMemory, limits.MemoryPoolMB, usedMemory)
	}
	if limits.DiskPoolMB > 0 && usedDisk+newDisk > limits.DiskPoolMB {
		return fmt.Errorf("this would allocate %d MB of a %d MB disk pool (%d MB already allocated to other apps); lower another app's limit or ask an admin to raise the pool",
			usedDisk+newDisk, limits.DiskPoolMB, usedDisk)
	}
	return nil
}

// poolReserved sums the effective memory and disk limits of an owner's apps,
// excluding one (the app being edited counts at its NEW values, not its old).
func (s *Server) poolReserved(ownerID, excludeApp string) (memoryMB, diskMB int, err error) {
	owner, err := s.users.User(ownerID)
	if err != nil {
		return 0, 0, err
	}
	limits, err := s.users.Limits(owner)
	if err != nil {
		return 0, 0, err
	}
	apps, err := s.apps.Store().AppsByOwner(ownerID)
	if err != nil {
		return 0, 0, err
	}
	for _, a := range apps {
		if a.Name == excludeApp {
			continue
		}
		mem, disk, _ := user.EffectiveAppLimits(limits, a)
		memoryMB += mem
		diskMB += disk
	}
	return memoryMB, diskMB, nil
}

// apiLimitOverrides is the admin-set per-app overrides as stored; zero means
// "no override" (the effective value then comes from the owner's defaults).
type apiLimitOverrides struct {
	MemoryMB int `json:"memory_mb"`
	DiskMB   int `json:"disk_mb"`
	CPUMilli int `json:"cpu_milli"`
}
