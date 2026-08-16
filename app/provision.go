package app

import (
	"log/slog"
	"time"
)

// This file is the node-local half of app creation and deletion: everything
// that touches THIS machine (subvolumes, unix users, key files, skeletons,
// containers, qgroups). The registry bookkeeping stays in create()/DeleteApp()
// on the control side. The split is the NodeAgent seam of
// plans/260815-hostit-nodeagent.md: specs carry everything the node half
// needs, so it never reads the registry.

// startInBackground brings the app up without the API call waiting for a
// container (and, on the app user's first app, an image build) to come up.
func (m *Manager) startInBackground(name string, forking bool) {
	m.TrackedGo(func() {
		// How long this took is the question asked whenever an app "would not
		// start": the API returns at once, and the wait is podman's queue behind
		// whatever else the host is doing
		started := time.Now()
		if _, err := m.node.Up(name); err != nil {
			slog.Warn("Cannot start app; it exists but serves nothing yet",
				"app", name, "took", time.Since(started).Round(time.Second), "error", err)
			return
		}
		slog.Info("App started", "app", name, "forked", forking, "took", time.Since(started).Round(time.Second))
	})
}
