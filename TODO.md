# TODO

Things worth doing, with enough context to pick up cold. Not a backlog of
everything imaginable -- if it is not written down here it is not planned.

## Open questions

- is "hostit apps" api really necessary?
- what is v1/self and why is it not just the same api as the main api?

## Multi-node: a proxy node and hosting nodes

Today one machine is everything: it terminates TLS, proxies, holds the registry,
and runs every app. The next step is separating the two roles, so apps can spread
across machines while keeping one front door and one dashboard. The full design,
with flow and sequence diagrams and a 4-phase rollout, is in
`plans/260807-hostit-multinode.md`; this is the summary.

- **Proxy node**: TLS, the web app, the REST API, the registry, placement, and the
  assistant. Owns which app lives where, and proxies to the node hosting it
  instead of to loopback.
- **Hosting nodes**: run a `hostit-agent` that creates Unix users and containers,
  runs apps, and reports state. No public listener of their own.

The chosen shape and the decisions behind it:

- **`NodeAgent` interface, local + remote.** The node-local half of `app.Manager`
  (everything touching `Runner`, `SystemOps`, the `os.OpenRoot` file layer, and
  btrfs) becomes a `NodeAgent` interface with two implementations: `localNodeAgent`
  (today's in-process code) and `remoteNodeAgent` (RPC to another box). A single-box
  install is proxy + one `local` node in the same process -- zero network, no
  behavior change. This is why the split cannot just remote the `Runner`:
  `os.OpenRoot` containment, btrfs reflinks and podman all require the operation to
  happen where the app physically lives, so the whole node-local unit must move.
- **Transport: dedicated internal RPC** (HTTP+JSON behind the `NodeAgent`
  interface, per-node token or mTLS), NOT the public agent REST API -- the internal
  surface is a superset that includes root-level verbs (create a user, rewrite
  authorized_keys, delete a home) that must never be tenant-reachable.
- **Node registry.** `store.App.Host` (today always `store.HostLocal`) becomes a
  node identifier; a new `node` table records address, capacity and health via
  heartbeats. Placement is least-loaded (free memory, disk, app count).
- **Registry stays central; agents are stateless.** The proxy's SQLite store is the
  single source of truth; at create time the hosting node allocates its own local
  port/uid and returns them. Snapshot subvolumes are node-local but the metadata
  lives in the central registry. The app's stable **id** (already built; see Done
  below) is the natural registry key: name -> id -> (node, port).
- **SSH routing: single front door + ProxyJump.** One SSH endpoint on the proxy;
  `hostit-shell` looks up the app's node and jumps into the right container.
- **State and quota collection** become cross-node: the state cache and the
  per-app qgroup usage reads currently assume everything is on this box; they
  move behind the agent's heartbeat/report path.

## Web app

The dashboard can create, manage and delete apps and drive them in the browser
(chat, terminal). These round out the in-browser experience.

- **Installable PWA.** Make the dashboard (`apps.example.com`) installable to the home
  screen / dock. Add `web/public/manifest.webmanifest` (name, `display: standalone`,
  `theme_color`, `start_url: "/"`, icons) linked from `index.html`; 192/512px + a
  maskable icon generated from a hostit glyph; and a minimal service worker that
  caches the app shell (JS/CSS) but is **network-first** for `/api/*` and app previews
  so it never serves stale data. HTTPS is already in place; everything drops into
  `web/public/` and is embedded into the Go binary. No new runtime deps.
- **Align the chat input controls across multiple lines.** In the assistant chat
  input, when the textarea grows to multiple lines the "+" (attach) button, the model
  selector, and the send button drift out of vertical alignment. They should stay
  aligned (bottom-aligned to the input row) as the textarea grows. In
  `web/src/pages/AppAssistant.jsx` (the input row) / `web/src/styles.css`.
- **Ask host-vs-build in the new-app modal.** When creating an app, let the owner
  pick their intent: "just host my existing app" or "build one here". The choice
  sets the initial app-detail tab -- host leans on details/deploy, build opens the
  split chat+preview workspace -- so each person lands in the surface that fits what
  they came to do.
## Assistant (AI chat) cost

The built-in assistant calls the Anthropic Messages API with the operator's API
key, so every turn is metered pay-per-token and adds up fast. Per-user/per-app
token and dollar usage is now tracked (admin user list), which is the visibility
these levers act on. Prompt caching is already in place: the system prompt, tool
definitions and the conversation prefix are cache-marked, so repeat turns pay the
~10x-cheaper cache-read rate. The remaining levers, roughly by impact:

- **Model routing.** Default is `claude-sonnet-5` ($3/$15 per M in/out). Route
  simple turns (small edits, questions) to Haiku (~10x cheaper) and escalate to
  Sonnet only for hard work. Even a crude heuristic (cheap model unless the last
  turn used tools heavily / the ask is large) saves a lot.
- **Tune the thinking / effort budget.** Adaptive thinking bills at the output rate
  ($15/M). Dial effort down for routine turns; reserve high effort for hard ones.
- **Compact the transcript.** Context is capped at recent turns, but long sessions
  still grow the cached prefix (cache writes cost 1.25x). Summarize old turns into a
  short recap to keep the prefix small.
- **Spend caps.** Now that usage is tracked, add a per-user or per-app budget that
  warns (or soft-stops the built-in chat) at a threshold, turning the usage data
  into cost control, not just visibility.

## Smaller things

- **Share apps with other users or groups.** Today an app has exactly one owner,
  and only that owner (or an admin) can see or manage it. Add collaborators: grant
  another user, or a group of users, access to an app so it shows up on their
  dashboard and they can deploy, edit, SSH in and drive its API. Involves an
  ownership/ACL model beyond the single `OwnerID` (a per-app grants table, roles
  like viewer/editor), fanning that out to the dashboard listing, the SSH
  `authorized_keys` (a collaborator's profile keys join the app's), the app-scoped
  tokens, and the "own app" checks in the server. Groups would layer on top: a
  named set of users an app can be shared with at once. Ties into the future
  multi-node work only loosely; mostly a registry + authorization change.

- **Does the hostit daemon actually need to run as root?** It drives podman,
  systemctl, useradd/usermod, nftables and btrfs -- audit which of those truly
  require root vs could run under a dedicated user with specific capabilities or a
  narrow sudoers grant, and whether the attack surface of a root daemon can be cut.

- **Split the in-container agent into its own binary (`cmd/init`).** Today one binary
  is daemon + CLI + in-container agent, bind-mounted read-only into every app
  container and run as PID 1 via `hostit agent`. A separate, minimal `init` binary
  linking only the agent code (supervise `run:`, reap zombies, static-serve, talk to
  the peercred socket) would be bind-mounted instead, so the daemon's TLS/ACME,
  OAuth, podman/systemd/nft/store, assistant and admin-API code is not even present
  where the tenant is root. Defense-in-depth / least privilege, NOT a fix for a live
  hole: the container's isolation (userns to an unprivileged host uid, no host
  podman/nft/store, peercred socket) already contains the tenant, and the extra
  daemon subcommands are inert in the container today; nothing secret is compiled in
  (secrets live in `/etc/hostit/server.yml`). Trade-off: it costs the "one Go binary"
  property (a stated selling point in the intro deck + docs) and adds a second build
  target and a separately-versioned bind-mount. Preferred shape: keep the shared
  packages (`appctl`, `agent`, `run`) and add a thin `cmd/init/main.go` that links
  only the agent; `cmd/hostit` (or root `main.go`) stays the daemon/CLI; update
  `workspace.CreateArgs` to mount the init binary and the preflight/`RestartStaleAgents`
  accordingly. Worth doing if/when the in-container surface grows; skippable while the
  agent stays tiny. Discussed 2026-08-12.

- **Dev/stage -> promote to prod (the "we work in prod" problem).** Right now the
  only copy of an app is the live one, so every edit (and every assistant change) is
  in production. Give each app an optional **staging** environment -- its own
  container + subdomain (e.g. `stage.<app>.<base>` or `<app>-stage`) sharing nothing
  live -- where changes and deploys land first, then a **Promote** action swaps it
  into prod atomically (blue/green: build/verify on stage, then flip the proxy /
  rename, keeping the old prod as instant rollback). Ties into the fork primitive
  (a stage is a fork that promotes back). Big feature; likely a `hostit promote`
  verb + a store notion of two environments per app.
- **Secrets.** `env:` values live in `hostit.yml`, which sits in the app's home
  and is served if someone points a web server at the wrong directory. A real
  secret store (or at least a separate file that is never in `public/`) would
  make it safe to put credentials in an app.
- **Log following.** `GET /api/apps/{app}/logs?lines=N` is a snapshot. An agent
  watching a slow start has to poll.
- **Long jobs.** `POST /api/apps/{app}/run` is bounded at five minutes, so a first
  `npm install` on a small box can outlast it. Anything longer has to become a
  `prepare:` step, which is fine but not obvious.
## Done (recent)

Kept briefly for context; prune when stale.

- **Unified app storage: one subvolume per app.** The separate home subvolume was
  merged INTO the rootfs: an app is now ONE btrfs subvolume at `apps/<id>` -- the
  full OS tree its container runs (`--rootfs`), with the app's files at
  `home/app` inside it (host path == container `/home/app`; the home bind mount
  is gone, containers mount only the hostit binary and the socket dir). Snapshots
  and rollback therefore cover the WHOLE app -- data and installed software
  together -- and fork copies both in one CoW snapshot. The Unix account's home
  is `apps/<id>/home/app`, daemon file access goes through chained `os.Root`s
  (subvolume root, then `home/app` inside it), and the budget qgroup `1/<uid>`
  spans the one subvolume + snapshots. A second settings-gated migration
  ("storage-unified") reflink-copied each home into its rootfs, dropped the old
  home-shaped snapshots, and renamed the rootfs to `apps/<id>`; powered-off apps
  stay off (`RestartStaleAgents` skips disabled units).
- **Hard disk cap + rootfs storage (fixes the full-disk wedge reported
  2026-08-12).** App containers run a persistent per-app btrfs rootfs
  subvolume (snapshotted from a read-only per-image-tag base in
  `.bases/<tag>`; plain podman `--rootfs`, the image store is only the build
  input), and every app has one hierarchical budget qgroup (`1/<uid>`),
  hard-capped on **exclusive** bytes at `disk_mb` --
  a write past the cap fails with EDQUOT wherever the tenant writes (`/home/app`
  or `/usr` alike), and `disk_mb: 0` now means a 2 GB default, so nothing is
  unlimited. A one-time, settings-gated startup migration moved existing apps
  (kept home state, dropped pre-existing snapshots, built each rootfs, budgeted
  every app); the unification above then folded the home in. Remaining, accepted:
  budgets can oversubscribe the (bounded) apps
  pool; the host root fs and the daemon's SQLite live outside it and stay safe.
  Design: `plans/260813-hostit-disk-hard-cap.md` section 3c.
- **Persist apt-installed packages across a container recreate.** Solved by the
  rootfs work above: an app's subvolume, once created, is never recreated or reset,
  so `apt-get install`s survive deploys, reboots and daemon upgrades. A
  Containerfile change mints a new base for new apps only.

- **v0.8.8 (poweroff sticks).** Powering off an app disabled its unit, but any
  SSH/browser-terminal login called `Ensure`, which started the container back up
  -- and v0.8.7's terminal auto-reconnect made that an automatic loop. `Ensure`
  (the login path) now refuses a powered-off app (`ErrPoweredOff`); the shell says
  so and the browser terminal shows a note and stops reconnecting. A separate
  `PowerOn` (re-enables + starts) is what the explicit poweron verb uses. Validated
  on stage and prod.

- **v0.8.7 (deploy-race fix + robustness).** App systemd units are now `Type=notify`
  with containers created `--sdnotify=conmon`, so a deploy never SIGHUPs a container
  that has not finished starting (killed a spurious 500 on a loaded box; RestartStale
  Agents recreates existing containers with the conmon policy on upgrade). Uploads/
  deploys now chown the parent directories they create, not just the file, so
  `public/`/`uploads/` are owned by the app, not host root. Token-free local CLI over
  the peercred unix socket (`hostit apps ...` as root, no token); `apps power
  on|off|reboot` and `apps snapshot list|create|delete` grouped. Terminal auto-
  reconnect (backoff + countdown + manual button); paste-an-image into the chat;
  1-hour assistant prompt cache. Internal: extracted the `workspace` package, moved
  podman stats to `container/` and hostit.yml description surgery to `appctl`; dropped
  the `SystemOps` facade for injected mockable interfaces; removed the dead
  `disk-check-interval` config.

- **Semi-live app previews on the dashboard (option A).** Each running app's card
  shows a non-interactive thumbnail: the app in a sandboxed iframe rendered at a
  1280x800 desktop viewport, CSS-scaled to the card width via a measured
  ResizeObserver factor. `pointer-events: none` so a click falls through to the
  card's stretched link; powered-off/crashed apps show a muted placeholder. Logic
  unit-tested in `web/src/preview.js` / `preview.test.js`.

- **Claude Max / subscription assistant backend.** The assistant can drive a Claude
  Pro/Max subscription (`claude-code-oauth-token`) run in a locked-down podman sandbox,
  in addition to the metered Anthropic API; a credential's presence is the whole
  switch (no backend selector). See `assistant/sandbox.go`.
- **App modularization (refactor rounds 1 & 2).** `app/` split into single-purpose
  service packages -- `btrfs/`, `systemd/`, `container/`, `ssh/`, `unixuser/`, `run/`
  (shared Runner), `retention/` (tested GFS policy) -- composed by `app.Manager`.
  Over-quota shutdown removed (btrfs qgroups enforce it). Server handlers regrouped
  into `server/server_handler_<topic>.go` over the service packages; floating test
  files folded to mirror their source. CLI cleaned up: `hostit internal assistant`
  (was `assistant-poc`), internal commands hidden, regexes moved to their packages,
  skeleton + error page + workspace Containerfile moved to `go:embed`. Assistant
  package owns the Claude/Anthropic + subscription-sandbox integration. Web: app
  name in the title, fixed the stuck rename snackbar, always-fresh preview pane
  (proxy no-store on `?hostit_preview=`), and a friendlier "paused" step-limit note.

- **App id + cheap rename.** Every app has a stable opaque id; durable resources
  (home `apps/<id>`, snapshots, container `hostit-app-<id>`, unit, the per-app FK
  tables) key on it, so a rename is `usermod -l` + one DB update with no data move
  and no container recreate. One-off startup migration for existing apps. Custom
  domains and tokens follow the rename. `plans/260808-hostit-app-id-identity.md`.
- **Workspace image versioning + Node.js/PHP.** Node.js (npm) and PHP added to the
  image; each app is pinned to the image tag it was built with, so a Containerfile
  change only affects new apps and never recreates an existing one for that reason.
- **App-detail tabs + file browser.** The app page is tabbed: split chat+preview
  (assistant), a file browser/editor, an embedded terminal, a Logs tab (activity
  feed + live timestamped output), Snapshots, and Settings.
- **Assistant token/$ usage.** Every built-in-assistant turn's token usage is
  recorded per app (keyed by app id) and summed per user with a dollar cost in the
  admin user list. Only the built-in assistant, not a tenant's own agent.

- Btrfs storage model: per-app subvolumes, snapshots (manual + auto), rollback
  (atomic, safety-snapshotted), hard qgroup disk quotas (EDQUOT), GFS retention,
  `hostit.yml` snapshot hooks, and **fork** (duplicate an app from a snapshot).
  API + CLI + assistant tools + snapshot dialog.
- Web: in-browser terminal, built-in assistant, dark mode toggle, Apps switcher,
  mobile top-bar fold.
- Custom domains: attach an owner's hostname to an app, with DNS-01 (CNAME-delegated)
  certificates that work even for an internally-deployed server.
- Chat file uploads: drag-and-drop or "+" to save files into the app's uploads/, with
  images shown to the model as vision.
