package main

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/urfave/cli/v2"

	"heckel.io/hostit/control"
	"heckel.io/hostit/control/config"
	"heckel.io/hostit/node"
	"heckel.io/hostit/preview"
	"heckel.io/hostit/store"
	"heckel.io/hostit/system/preflight"
	"heckel.io/hostit/system/run"
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
			&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: config.DefaultControlConfigFile, Usage: "control config file"},
		},
	}
)

func execServe(c *cli.Context) error {
	conf, err := config.LoadConfig(config.ResolveConfigFile(c.String("config"), config.LegacyServerConfigFile))
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
	manager := control.NewManager(conf, s)
	users := user.NewManager(conf, s)
	if err := ensureSessionKey(conf, s); err != nil {
		return err
	}

	// The Server and the shutdown channel exist before any machine work: they
	// cost nothing to build (control.New starts no listeners) and the cluster
	// listener below needs both.
	srv := control.New(conf, manager, users)
	done := make(chan struct{})
	defer close(done)
	// The cluster listener goes up FIRST, before anything that touches the
	// machine. Everything below can take minutes on a real host -- a quota
	// migration, a limit sweep over every app -- and systemd reports this unit
	// active the moment the process starts, so a proxy started alongside it
	// would bind :443 while control was still busy, with no credentials, no
	// routes and (on a first install) no cached certificates to serve. It
	// answered handshakes with a self-signed certificate instead, which took
	// prod down on 2026-08-17. Listening early costs nothing and means a member
	// can connect and be configured while control settles.
	if err := listenForMembers(conf, manager, srv, done); err != nil {
		return err
	}
	// Every machine-touching startup step belongs to hostit-node now: quota
	// accounting, the raw apps view, the workspace image, restarting agents
	// after an upgrade. This process holds the registry and decides; it owns no
	// machinery to do any of that with.
	//
	// The stored limits are still control's to assert -- they come from the user
	// tables -- and reach the node through the desired state on connect.
	if err := applyStoredLimits(s, manager, users); err != nil {
		return err
	}
	// Dashboard screenshot previews (app-preview: screenshot): a slow sweep
	// plus debounced shots after assistant changes, one at a time, each in a
	// locked-down podman container (the page content is untrusted).
	var previews *preview.Manager
	if conf.AppPreview == config.AppPreviewScreenshot {
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
				out = append(out, preview.App{ID: a.ID, Name: a.Name, URL: manager.URL(a), Running: states[a.Name].Running, Private: a.Private})
			}
			return out, nil
		})
		// Strict egress isolation by default: the shot container reaches only the
		// app's resolved IP and the public internet, not the host/LAN/metadata.
		previews.SetIsolation(conf.AppPreviewIsolation != config.AppPreviewIsolationOff, conf.AppPreviewAllowCIDRs)
		// Let private apps be shot: the browser presents an app-bound grant so the
		// proxy serves the app instead of the sign-in page. nil on failure (no
		// grant signer) just means that app is skipped, as before.
		previews.SetPreviewCookie(func(a preview.App) *http.Cookie {
			c, err := srv.PreviewCookie(a.Name)
			if err != nil {
				return nil
			}
			return c
		})
	}
	if previews != nil {
		srv.SetPreviews(previews)
	}

	// Presume recorded intent until the first real measurement lands, so the
	// first page load after a restart does not see every app as stopped
	manager.SeedStates()
	// One-time: record uid block bases for rows created before the uid column
	manager.BackfillUIDs()
	// Re-assert the desired state on every node on a timer: a push that failed
	// mid-connection, a node that drifted, or anything that changed while a
	// node was away converges here without waiting for a reconnect. Control is
	// the source; this is how the registry keeps reaching the machines.
	go reconcileLoop(manager, srv, done)
	// Automatic snapshots are CONTROL's decision: the sweep commands each app's
	// node through the node agent (a no-op off btrfs). Each app has its own
	// cadence and its own slot within it, so the loop owns its tick.
	go manager.AutoSnapshotLoop(done)
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
func ensureSessionKey(conf *config.Config, s *store.Store) error {
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

// applyStoredLimits primes the app manager with each app's effective limits:
// its own admin-set overrides where present, else its owner's defaults from
// the user records. CPU has no owner default; the override is the whole story.
func applyStoredLimits(s *store.Store, apps *control.Manager, users *user.Manager) error {
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
		// Recorded, not applied: control decides the limits and the node
		// enforces them, so they reach the machine through the desired state
		// on the next connect or reconcile rather than from here.
		memoryMB, diskMB, cpuMilli := user.EffectiveAppLimits(limits, a)
		apps.RecordLimits(a.Name, memoryMB, diskMB, cpuMilli)
	}
	return nil
}
