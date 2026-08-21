// Administration: users, the two ways in that skip the approval queue, and the
// global default limits. Everything here sits behind requireAdmin (see api.go);
// the caller's own account surface is in account.go.
package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"heckel.io/hostit/assistant"
	"heckel.io/hostit/store"
)

func (s *Server) handleUsersList(w http.ResponseWriter, _ *http.Request, _ *caller) {
	users, err := s.users.Users()
	if err != nil {
		writeAppError(w, err)
		return
	}
	// One query for every owner's assistant usage, rather than one per user.
	usageByOwner, err := s.apps.Store().UsageByOwner()
	if err != nil {
		usageByOwner = nil // show zero usage rather than failing the whole list
	}
	resp := make([]*apiUserResponse, 0, len(users))
	for _, u := range users {
		count, err := s.apps.Store().AppCountByOwner(u.ID)
		if err != nil {
			writeAppError(w, err)
			return
		}
		r := newUserResponse(u, count)
		if usage, ok := usageByOwner[u.ID]; ok {
			r.AssistantTokens = usage.InputTokens + usage.OutputTokens + usage.CacheWriteTokens + usage.CacheReadTokens
			r.AssistantCostUSD = assistant.CostUSD(usage, assistant.DefaultCostModel)
		}
		resp = append(resp, r)
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleUsersInvite creates an approved account for someone who has not signed
// in yet, so an admin can hand out access directly instead of waiting for a
// request to approve
func (s *Server) handleUsersInvite(w http.ResponseWriter, r *http.Request, _ *caller) {
	var req apiInviteUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Role == "" {
		req.Role = store.RoleUser
	}
	u, err := s.users.Invite(req.Email, req.Role)
	if err != nil {
		writeUserError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newUserResponse(u, 0))
}

// handleDomainsList returns the email domains that skip the approval queue
func (s *Server) handleDomainsList(w http.ResponseWriter, _ *http.Request, _ *caller) {
	domains, err := s.users.AllowedDomains()
	if err != nil {
		writeAppError(w, err)
		return
	}
	resp := make([]*apiDomainResponse, 0, len(domains))
	for _, d := range domains {
		resp = append(resp, &apiDomainResponse{Domain: d.Domain, CreatedAt: d.CreatedAt})
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleDomainsAdd allows a whole email domain to sign up without approval
func (s *Server) handleDomainsAdd(w http.ResponseWriter, r *http.Request, _ *caller) {
	var req apiAddDomainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	d, err := s.users.AllowDomain(req.Domain)
	if err != nil {
		writeUserError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, &apiDomainResponse{Domain: d.Domain, CreatedAt: d.CreatedAt})
}

// handleDomainsDelete stops auto-approving a domain; accounts already approved
// under it are untouched
func (s *Server) handleDomainsDelete(w http.ResponseWriter, r *http.Request, _ *caller) {
	if err := s.users.DisallowDomain(r.PathValue("domain")); err != nil {
		writeUserError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "domain removed"})
}

// handleUsersUpdate changes role, status and per-user limit overrides; a null
// limit means "fall back to the global default"
func (s *Server) handleUsersUpdate(w http.ResponseWriter, r *http.Request, c *caller) {
	var req apiUpdateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	u, err := s.users.User(r.PathValue("id"))
	if err != nil {
		writeAppError(w, err)
		return
	}
	if req.Role != nil {
		if *req.Role != store.RoleAdmin && *req.Role != store.RoleUser {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid role %q", *req.Role))
			return
		}
		if u.ID == c.userID() && *req.Role != store.RoleAdmin {
			writeError(w, http.StatusBadRequest, errors.New("you cannot remove your own admin role"))
			return
		}
		u.Role = *req.Role
	}
	if req.Status != nil {
		switch *req.Status {
		case store.StatusPending, store.StatusActive, store.StatusDenied:
			u.Status = *req.Status
		default:
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid status %q", *req.Status))
			return
		}
	}
	if req.AppLimitSet {
		u.AppLimit = req.AppLimit
	}
	if req.MemoryMBSet {
		u.MemoryMB = req.MemoryMB
	}
	if req.DiskMBSet {
		u.DiskMB = req.DiskMB
	}
	if err := s.users.Update(u); err != nil {
		writeAppError(w, err)
		return
	}
	// A limit change must reach the user's running apps now: the qgroup cap and
	// the container memory cap key off what the manager has recorded, and
	// applyStoredLimits only re-syncs at daemon startup.
	if req.MemoryMBSet || req.DiskMBSet {
		if err := s.applyUserLimits(u); err != nil {
			writeAppError(w, err)
			return
		}
	}
	count, err := s.apps.Store().AppCountByOwner(u.ID)
	if err != nil {
		writeAppError(w, err)
		return
	}
	resp := newUserResponse(u, count)
	writeJSON(w, http.StatusOK, resp)
}

// applyUserLimits pushes a user's effective memory and disk limits onto each of
// their apps, mirroring what applyStoredLimits does for every app at startup.
func (s *Server) applyUserLimits(u *store.User) error {
	limits, err := s.users.Limits(u)
	if err != nil {
		return err
	}
	apps, err := s.apps.Store().AppsByOwner(u.ID)
	if err != nil {
		return err
	}
	for _, a := range apps {
		// A per-app override outranks the owner's defaults: editing the USER
		// must not clobber what an admin set on one specific app.
		memoryMB, diskMB := limits.MemoryMB, limits.DiskMB
		if a.MemoryLimitMB > 0 {
			memoryMB = a.MemoryLimitMB
		}
		if a.DiskLimitMB > 0 {
			diskMB = a.DiskLimitMB
		}
		// Recorded here as well as asserted on the node: control decides the
		// limits, so its own record is what the desired state and the API report
		// -- a node that is away still gets them on its next reconcile.
		s.apps.RecordLimits(a.Name, memoryMB, diskMB, a.CPUMilli)
		s.node.SetMemoryLimit(a.Name, memoryMB)
		s.node.SetDiskLimit(a.Name, diskMB)
	}
	return nil
}

// handleUsersDelete removes a user and all of their apps (including the Unix
// users and their containers)
func (s *Server) handleUsersDelete(w http.ResponseWriter, r *http.Request, c *caller) {
	id := r.PathValue("id")
	if id == c.userID() {
		writeError(w, http.StatusBadRequest, errors.New("you cannot delete your own account"))
		return
	}
	u, err := s.users.User(id)
	if err != nil {
		writeAppError(w, err)
		return
	}
	apps, err := s.apps.Store().AppsByOwner(u.ID)
	if err != nil {
		writeAppError(w, err)
		return
	}
	// Deleting a person must not quietly decide the fate of their apps, so the
	// caller says which they meant. An app with no owner would keep serving with
	// nobody able to manage it, which is the one outcome nobody wants.
	message := "user deleted"
	switch r.URL.Query().Get("apps") {
	case "transfer":
		target, err := s.transferTarget(r.URL.Query().Get("transfer_to"), u.ID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		moved, err := s.apps.Store().TransferApps(u.ID, target.ID)
		if err != nil {
			writeAppError(w, err)
			return
		}
		// The new owner's SSH keys are what should open these apps now
		if err := s.syncUserAppKeys(target.ID); err != nil {
			slog.Warn("Cannot update ssh keys after transfer", "to", target.Email, "error", err)
		}
		message = fmt.Sprintf("user deleted, %d app(s) transferred to %s", len(moved), target.Email)
	case "delete":
		for _, a := range apps {
			if err := s.apps.DeleteApp(a.Name); err != nil {
				writeAppError(w, err)
				return
			}
		}
		message = fmt.Sprintf("user and %d app(s) deleted", len(apps))
	default:
		writeError(w, http.StatusBadRequest, errors.New(`say what to do with this user's apps: apps=delete, or apps=transfer&transfer_to=<user id>`))
		return
	}
	if err := s.users.Delete(u.ID); err != nil {
		writeAppError(w, err)
		return
	}
	slog.Info("User deleted", "email", u.Email, "apps", len(apps), "by", c.userID())
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: message})
}

func (s *Server) handleSettingsGet(w http.ResponseWriter, _ *http.Request, _ *caller) {
	defaults, err := s.users.Defaults()
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiSettingsResponse{
		DefaultAppLimit: defaults.AppLimit,
		DefaultMemoryMB: defaults.MemoryMB,
		DefaultDiskMB:   defaults.DiskMB,
	})
}

func (s *Server) handleSettingsUpdate(w http.ResponseWriter, r *http.Request, c *caller) {
	var req apiUpdateSettingsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	defaults, err := s.users.Defaults()
	if err != nil {
		writeAppError(w, err)
		return
	}
	if req.DefaultAppLimit != nil {
		defaults.AppLimit = *req.DefaultAppLimit
	}
	if req.DefaultMemoryMB != nil {
		defaults.MemoryMB = *req.DefaultMemoryMB
	}
	if req.DefaultDiskMB != nil {
		defaults.DiskMB = *req.DefaultDiskMB
	}
	if err := s.users.SetDefaults(defaults); err != nil {
		writeAppError(w, err)
		return
	}
	// Echo the merged settings (limits + assistant) back, so one round-trip writes and reads.
	s.handleSettingsGet(w, r, c)
}

// transferTarget resolves who is to receive the apps, refusing anyone who
// cannot actually own them
func (s *Server) transferTarget(id, leavingID string) (*store.User, error) {
	if id == "" {
		return nil, errors.New("transfer_to is required when apps=transfer")
	}
	if id == leavingID {
		return nil, errors.New("cannot transfer a user's apps to themselves")
	}
	target, err := s.users.User(id)
	if err != nil {
		return nil, fmt.Errorf("no such user to transfer to: %s", id)
	}
	if target.Status != store.StatusActive {
		return nil, fmt.Errorf("%s is not an active account", target.Email)
	}
	return target, nil
}

func newUserResponse(u *store.User, appCount int) *apiUserResponse {
	return &apiUserResponse{
		ID:        u.ID,
		Email:     u.Email,
		Name:      u.Name,
		Role:      u.Role,
		Status:    u.Status,
		AppLimit:  u.AppLimit,
		MemoryMB:  u.MemoryMB,
		DiskMB:    u.DiskMB,
		AppCount:  appCount,
		CreatedAt: u.CreatedAt,
	}
}

// handleClusterStatus reports the cluster: its nodes and proxies with liveness,
// and what it is carrying. Admin-only, and the same shape `hostit-control
// status` prints -- one assembly (ClusterStatus), so the terminal and the
// dashboard can never disagree about the state of the cluster.
func (s *Server) handleClusterStatus(w http.ResponseWriter, _ *http.Request, _ *caller) {
	status, err := ClusterStatus(s.apps.Store(), time.Now())
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}
