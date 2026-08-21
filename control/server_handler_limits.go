package control

import (
	"encoding/json"
	"fmt"
	"net/http"
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

// handleAppLimitsUpdate sets an app's per-app resource limit OVERRIDES.
// Admin-only until per-user pools exist: an owner who can raise their own
// caps has no cap at all. Disk applies live (the node re-caps the budget
// qgroup); memory and CPU are recorded and take effect at the next container
// recreation (reboot, power cycle or deploy) -- deliberately no auto-reboot,
// since editing a number must not secretly restart a tenant's app.
func (s *Server) handleAppLimitsUpdate(w http.ResponseWriter, r *http.Request, c *caller) {
	a, err := s.apps.App(r.PathValue("name"))
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
	if err := s.apps.Store().UpdateAppLimits(a.Name, memoryMB, diskMB, cpuMilli); err != nil {
		writeAppError(w, err)
		return
	}
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

// apiLimitOverrides is the admin-set per-app overrides as stored; zero means
// "no override" (the effective value then comes from the owner's defaults).
type apiLimitOverrides struct {
	MemoryMB int `json:"memory_mb"`
	DiskMB   int `json:"disk_mb"`
	CPUMilli int `json:"cpu_milli"`
}
