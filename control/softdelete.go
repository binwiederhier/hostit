package control

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"heckel.io/hostit/store"
)

// Soft delete: an owner-deleted app is shelved for a grace period instead of
// being removed at once. It is gone from the owner's view -- hidden entirely,
// unlike an archived app -- but stays listed for admins, so an accidental delete
// can be undone. A background sweep removes it for real once soft-delete-duration
// has passed; with a zero grace the removal happens promptly, but still async.

const (
	// softDeleteReapInterval is how often the sweep looks for apps whose grace
	// has run out. Frequent enough that a zero-grace delete (also kicked off on
	// the delete itself) never lingers, and fine for a multi-day grace.
	softDeleteReapInterval = 5 * time.Minute
)

// SoftDeleteApp marks an app for deletion after the grace period rather than
// removing it now. It is powered off so it stops holding memory, and it stops
// routing and SSH at once, so from the outside it is already gone.
func (m *Manager) SoftDeleteApp(name string) error {
	if _, err := m.store.App(name); err != nil {
		return err
	}
	if err := m.node.Down(name); err != nil {
		// The soft-delete stamp below is what hides it either way; a node that is
		// away cannot stop the removal.
		slog.Warn("Cannot power off an app being soft-deleted", "app", name, "error", err)
	}
	if err := m.store.SetAppSoftDeleted(name, time.Now()); err != nil {
		return err
	}
	m.PushMirror()      // stop routing to it
	m.refreshSSHRelay() // and drop its SSH route
	return nil
}

// RestoreSoftDeleted brings a soft-deleted app back as an ordinary powered-off
// app, cancelling its scheduled deletion. Deliberately not started, the same as
// coming out of the archive.
func (m *Manager) RestoreSoftDeleted(name string) error {
	if _, err := m.store.App(name); err != nil {
		return err
	}
	if err := m.store.SetAppSoftDeleted(name, time.Time{}); err != nil {
		return err
	}
	m.PushMirror()
	m.refreshSSHRelay()
	return nil
}

// ReapSoftDeleted hard-deletes every soft-deleted app whose grace has elapsed.
// It runs on a timer and once (asynchronously) right after a delete, so a
// zero-grace delete is carried out at once without the request waiting on it.
func (s *Server) ReapSoftDeleted() {
	cutoff := time.Now().Add(-s.config.SoftDeleteGrace())
	apps, err := s.apps.Store().Apps()
	if err != nil {
		slog.Warn("Cannot list apps to reap soft-deleted ones", "error", err)
		return
	}
	for _, a := range apps {
		if a.SoftDeletedAt.IsZero() || a.SoftDeletedAt.After(cutoff) {
			continue
		}
		if err := s.apps.DeleteApp(a.Name); err != nil {
			slog.Warn("Cannot reap soft-deleted app", "app", a.Name, "error", err)
			continue
		}
		if s.assistant != nil {
			s.assistant.DropSession(a.Name) // now it is really gone: forget its session + transcript
		}
		slog.Info("Reaped soft-deleted app", "app", a.Name, "soft_deleted_at", a.SoftDeletedAt)
	}
}

// SoftDeleteReapLoop reaps expired soft-deleted apps on an interval until done.
func (s *Server) SoftDeleteReapLoop(done <-chan struct{}) {
	t := time.NewTicker(softDeleteReapInterval)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			s.ReapSoftDeleted()
		}
	}
}

// softDeletedUnix is the app's soft-delete time as unix seconds, or 0 when the
// app is live -- the shape the API response carries.
func softDeletedUnix(a *store.App) int64 {
	if a.SoftDeletedAt.IsZero() {
		return 0
	}
	return a.SoftDeletedAt.Unix()
}

// handleAppsRestore cancels a soft-deleted app's scheduled deletion (admin
// only): it comes back powered off, like leaving the archive, and its owner
// sees it again.
func (s *Server) handleAppsRestore(w http.ResponseWriter, r *http.Request, c *caller) {
	name := r.PathValue("name")
	if err := s.apps.RestoreSoftDeleted(name); err != nil {
		writeAppError(w, err)
		return
	}
	slog.Info("Soft-deleted app restored", "app", name, "by", c.userID())
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "app restored"})
}

// handleAppsPurge hard-deletes a pending-deletion app right now, skipping the
// rest of its grace period (admin only) -- the admin "Delete now" action. It
// refuses an app that is not pending deletion, so it cannot be used to bypass
// the grace on a live app.
func (s *Server) handleAppsPurge(w http.ResponseWriter, r *http.Request, c *caller) {
	name := r.PathValue("name")
	a, err := s.apps.Store().App(name)
	if err != nil {
		writeAppError(w, err)
		return
	}
	if a.SoftDeletedAt.IsZero() {
		writeError(w, http.StatusBadRequest, errors.New("app is not pending deletion"))
		return
	}
	if err := s.apps.DeleteApp(name); err != nil {
		writeAppError(w, err)
		return
	}
	if s.assistant != nil {
		s.assistant.DropSession(name) // now it is really gone: forget its session + transcript
	}
	slog.Info("Soft-deleted app purged early", "app", name, "by", c.userID())
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "app deleted"})
}
