package main

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/urfave/cli/v2"
	"heckel.io/hostit/control"
	"heckel.io/hostit/controlconf"
	"heckel.io/hostit/node"
	"heckel.io/hostit/preflight"
	"heckel.io/hostit/preview"
	"heckel.io/hostit/run"
	"heckel.io/hostit/store"
	"heckel.io/hostit/user"
)

const (
	// settingSessionKey stores the generated cookie-signing key, so web sessions
	// survive restarts when the operator did not configure one
	settingSessionKey = "session_key"
	// dataDirMode lets app users traverse /var/lib/hostit to their own home
	// without reading it; the registry inside is 0600
	dataDirMode = 0o711
	// reconcileInterval paces the desired-state sweep across all nodes.
	reconcileInterval = 5 * time.Minute
	// appsDirMode is the directory holding the app subvolumes; app users traverse
	// it to reach their own files dir (home/app) inside their subvolume
	appsDirMode = 0o755
)

var (
	cmdServe = &cli.Command{
		Name:   "serve",
		Usage:  "Run the hostit daemon (requires root)",
		Action: execServe,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: controlconf.DefaultControlConfigFile, Usage: "control config file"},
		},
	}
)

func execServe(c *cli.Context) error {
	conf, err := controlconf.LoadConfig(controlconf.ResolveConfigFile(c.String("config"), controlconf.LegacyServerConfigFile))
	if err != nil {
		return err
	}
	if err := conf.Validate(); err != nil {
		return err
	}
	// Refuse to start on a host that cannot support the daemon (not root, a missing
	// command), rather than failing lazily on the first app operation.
	if err := preflight.CheckHost(); err != nil {
		return err
	}
	// 0711: app users must traverse this to reach their own home below it, but
	// must not be able to list it. What lives here is the registry -- every app's
	// agent token and the session signing key -- which is 0600 besides.
	if err := os.MkdirAll(conf.DataDir, dataDirMode); err != nil {
		return err
	}
	if err := os.Chmod(conf.DataDir, dataDirMode); err != nil {
		return err
	}
	if err := os.MkdirAll(conf.AppsDir, appsDirMode); err != nil {
		return err
	}
	if err := os.Chmod(conf.AppsDir, appsDirMode); err != nil {
		return err
	}
	// btrfs is mandatory: snapshots, rollback, fork and hard disk quotas are core.
	// Check it here, once the apps directory exists, and refuse to start
	// otherwise rather than silently running without those features.
	if err := preflight.RequireBtrfs(conf.AppsDir); err != nil {
		return err
	}
	s, err := store.NewStore(filepath.Join(conf.DataDir, "hostit.db"))
	if err != nil {
		return err
	}
	defer s.Close()
	// c.App.Version is what main stamped from the ldflags; keep the machine
	// half on the same string (it is part of each container's identity).
	node.Version = c.App.Version
	// The fused daemon IS the local node, so its machine services are the local
	// node's (the firewall table is named after it).
	manager := control.NewManager(conf, s, node.NewSystemServices(run.New(), store.HostLocal, "", nil))
	users := user.NewManager(conf, s)
	if err := ensureSessionKey(conf, s); err != nil {
		return err
	}
	// Split mode: a hostit-node owns this machine's app work; this daemon is
	// control-only and skips every machine-touching startup step and loop.
	splitNode := conf.ListenNode != ""
	// Quota accounting is the mechanism behind every per-app disk budget;
	// idempotent, so it simply runs at every start.
	if !splitNode {
		manager.EnableDiskBudgets()
	}
	// The daemon's file I/O goes through a raw (non-recursive) bind of the apps
	// dir: a running container's idmapped rootfs mount covers the subvolume path
	// in the host namespace, and root writing through that mapped view fails.
	if !splitNode {
		if err := manager.MountRawAppsView(filepath.Join(filepath.Dir(conf.SocketFile), "apps-raw")); err != nil {
			return err
		}
	} else {
		// The node mounts the raw view; this process still reads app files
		// through the same path when colocated.
		manager.UseRawAppsView(filepath.Join(filepath.Dir(conf.SocketFile), "apps-raw"))
	}
	// After the budgets exist: applying a stored limit re-ensures the app's
	// budget qgroup and its cap. Migration briefly runs on the 2048M default and
	// this corrects every group to the owner's real limit.
	if err := applyStoredLimits(s, manager, users, splitNode); err != nil {
		return err
	}
	// Build the workspace image and export its base rootfs once. This runs in the
	// background: it takes minutes on a small host, and the proxy must not wait.
	go func() {
		if splitNode {
			return // the node builds images and restarts agents
		}
		if err := manager.EnsureWorkspaceBase(); err != nil {
			slog.Warn("Cannot prepare workspace base rootfs; the first app deploy will retry", "error", err)
		}
		// Agents keep the behaviour of the binary they were exec'd from, so an
		// upgrade only reaches them on a restart. In the background: this costs
		// each app a moment, and the proxy should be up first.
		restarted, err := manager.RestartStaleAgents(c.App.Version)
		if err != nil {
			slog.Warn("Cannot restart apps after upgrade", "error", err)
		} else if len(restarted) > 0 {
			slog.Info("Restarted apps to pick up the new version", "apps", restarted, "version", c.App.Version)
		}
		// Once the apps are on the current image, its predecessors are dead weight
		manager.PruneOldWorkspaceImages()
	}()
	// Dashboard screenshot previews (app-preview: screenshot): a slow sweep
	// plus debounced shots after assistant changes, one at a time, each in a
	// locked-down podman container (the page content is untrusted).
	var previews *preview.Manager
	if conf.AppPreview == controlconf.AppPreviewScreenshot {
		previews = preview.New(run.New(), preview.Dir(conf.DataDir), func() ([]preview.App, error) {
			apps, err := s.Apps()
			if err != nil {
				return nil, err
			}
			names := make([]string, 0, len(apps))
			for _, a := range apps {
				names = append(names, a.Name)
			}
			states := manager.CachedStates(names)
			out := make([]preview.App, 0, len(apps))
			for _, a := range apps {
				out = append(out, preview.App{ID: a.ID, Name: a.Name, URL: manager.URL(a), Running: states[a.Name].Running})
			}
			return out, nil
		})
		// Strict egress isolation by default: the shot container reaches only the
		// app's resolved IP and the public internet, not the host/LAN/metadata.
		previews.SetIsolation(conf.AppPreviewIsolation != controlconf.AppPreviewIsolationOff, conf.AppPreviewAllowCIDRs)
	}
	srv := control.New(conf, manager, users)
	if previews != nil {
		srv.SetPreviews(previews)
	}

	// The registry is the source of truth: an app deleted while the daemon was
	// down, or whose unit outlived it, leaves systemd retrying something that no
	// longer exists
	if !splitNode {
		if removed := manager.ReconcileOrphans(); len(removed) > 0 {
			slog.Info("Cleaned up leftovers of apps that no longer exist", "apps", removed)
		}
	}

	// Periodically measure disk usage for the dashboard (btrfs qgroups do the
	// actual enforcement at write time)
	done := make(chan struct{})
	defer close(done)
	// Presume recorded intent until the first real measurement lands, so the
	// first page load after a restart does not see every app as stopped
	manager.SeedStates()
	// One-time: record uid block bases for rows created before the uid column
	manager.BackfillUIDs()
	// Re-assert the desired state on every node on a timer: a push that failed
	// mid-connection, a node that drifted, or anything that changed while a
	// node was away converges here without waiting for a reconnect. Control is
	// the source; this is how the registry keeps reaching the machines.
	if splitNode {
		go reconcileLoop(manager, srv, done)
	}
	// Hourly automatic snapshots are CONTROL's decision in both modes: the
	// sweep commands each app's node through the node agent (the local machine
	// when fused, the routing agent when split; a no-op off btrfs).
	go manager.AutoSnapshotLoop(time.Hour, done)
	if !splitNode {
		go manager.DiskUsageLoop(done)
		go manager.StateLoop(done)
		// Sweep stale qgroups (deleted subvolumes/apps whose gentle destroy lost
		// its race); enough of them slow quota rescans until app creates time out
		go manager.QgroupSweepLoop(6*time.Hour, done)
	}
	// The cluster listener. Proxies always dial in here; nodes do too when the
	// machine half runs elsewhere, in which case each node's RPC becomes this
	// process's NodeAgent, its States feed the cache, and every (re)connect
	// runs the rejoin sweep.
	if err := listenForMembers(conf, manager, srv, done, splitNode); err != nil {
		return err
	}
	// Control pushes the routing table to every connected proxy as it changes,
	// and asks each how it is on a timer -- which is both the liveness an
	// operator reads and how a removed proxy loses its session.
	go srv.RouteLoop(done)
	go srv.ProxyHeartbeatLoop(done)
	// Screenshot previews: the sweep loop plus the single shot worker
	if previews != nil {
		go previews.Loop(preview.SweepInterval, done)
	}
	// Retry pending/error custom domains so they verify once DNS is set up
	go srv.DomainRetryLoop(time.Minute, done)

	// Shut down gracefully on SIGINT/SIGTERM
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		srv.Stop()
	}()
	return srv.Run()
}

// ensureSessionKey persists a generated session key, so web logins survive
// restarts even when the operator did not configure one
func ensureSessionKey(conf *controlconf.Config, s *store.Store) error {
	if conf.SessionKey != "" {
		return nil
	}
	settings, err := s.Settings()
	if err != nil {
		return err
	}
	if key, ok := settings[settingSessionKey]; ok && key != "" {
		conf.SessionKey = key
		return nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	conf.SessionKey = hex.EncodeToString(b)
	return s.SetSetting(settingSessionKey, conf.SessionKey)
}

// reconcileLoop re-asserts every connected node's desired state periodically.
// The interval is a compromise: often enough that a missed update heals on its
// own, rare enough that the sweep is not itself load (each pass is a registry
// read plus one RPC per node).
func reconcileLoop(manager *control.Manager, srv *control.Server, done <-chan struct{}) {
	slog.Info("Starting node reconcile loop", "interval", reconcileInterval)
	defer slog.Info("Stopping node reconcile loop")
	for {
		select {
		case <-time.After(reconcileInterval):
		case <-done:
			return
		}
		desired, err := manager.DesiredState("")
		if err != nil {
			slog.Warn("Cannot build the desired state for the reconcile sweep", "error", err)
			continue
		}
		manager.ReconcileNodes(desired)
		// Proxies get the same treatment on the same timer: a push they missed
		// heals here rather than waiting for the next routing change.
		srv.PushRoutes()
	}
}

// applyStoredLimits primes the app manager with each app owner's memory and disk
// limits, which live in the user records rather than in the app registry
func applyStoredLimits(s *store.Store, apps *control.Manager, users *user.Manager, recordOnly bool) error {
	registered, err := s.Apps()
	if err != nil {
		return err
	}
	defaults, err := users.Defaults()
	if err != nil {
		return err
	}
	for _, a := range registered {
		limits := defaults
		if a.OwnerID != "" {
			owner, err := users.User(a.OwnerID)
			if err == nil {
				if limits, err = users.Limits(owner); err != nil {
					return err
				}
			}
		}
		apps.SetMemoryLimit(a.Name, limits.MemoryMB) // record-only either way
		if recordOnly {
			apps.RecordDiskLimit(a.Name, limits.DiskMB)
		} else {
			apps.SetDiskLimit(a.Name, limits.DiskMB)
		}
	}
	return nil
}
