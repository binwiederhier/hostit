# TODO

Things worth doing, with enough context to pick up cold. Not a backlog of
everything imaginable -- if it is not written down here it is not planned.

## Open questions

- is "hostit apps" api really necessary?
- what is v1/self and why is it not just the same api as the main api?

## Bugs

- **A full disk wedges silently instead of surfacing "out of disk".** Filling the
  disk produced no hostit-level error, the dashboard disk-usage stats froze (never
  updated for 5-10 min), and the only signal was podman failing in the terminal
  view: `Error: saving container ... state: database or disk is full`. Root causes,
  all confirmed in the code:
  - The registry DB (`/var/lib/hostit/hostit.db`, `DataDir`) and the apps
    (`/var/lib/hostit/apps/<id>`, `AppsDir`) share one filesystem/btrfs pool, so an
    app filling its subvolume fills the space the daemon's SQLite and podman's
    container-state DB write to. The terminal error is podman's *host*-level
    disk-full, not a per-app event.
  - **No host free-space guard exists anywhere** -- nothing calls `Statfs`/checks
    `Bavail`. hostit only ever sets per-app btrfs qgroup caps.
  - A qgroup cap is not a reservation: an app with `disk_mb: 0` (unlimited -- what
    `callerDiskLimit` returns when the owner/admin default is 0) has *no* cap and can
    eat the whole pool; and even set caps can be oversubscribed (sum of quotas >
    capacity), so the host fills while every app is "within quota".
  - **The btrfs qgroup only covers the app HOME subvolume, not the container's
    writable overlay layer.** Confirmed live on stage 2026-08-12: `thatphilguy-fork`
    had a 10MB home quota, yet a tenant `dd`'d ~8GB into `/usr/bin/aa` and
    `/usr/bin/bb` *inside the container*, which lands in
    `/var/lib/containers/storage/overlay/<layer>/diff` -- unquota'd -- and filled the
    host to 100%. Recovery required `rm`-ing those two files directly from the
    overlay diff (podman itself could not even remove the container: its own state DB
    write failed with "database or disk is full"), then deleting the app. So any real
    per-app disk cap has to bound the *container writable layer* too (e.g. a storage
    quota / `--storage-opt size=`, or move the writable layer onto a quota'd btrfs
    subvolume), not just the mounted home.
  - Per-app enforcement is EDQUOT delivered to the *app's own* write() calls inside
    the container, so the app sees ENOSPC, not hostit -- nothing surfaces it.
  - `RefreshDiskUsage` swallows the registry write failure: `store.UpdateAppUsage`
    throws `database or disk is full`, which is caught and only `slog.Warn`d
    (`app/quota.go:47`), so the dashboard keeps serving the last good numbers forever.
  - **Approach decided + BOTH mechanisms validated on stage (2026-08-13): a real
    hard cap, no passive monitoring.** The gap is the container's writable layer
    (the home already has a btrfs qgroup). **Recommended: switch podman to the
    btrfs storage driver -- every layer is then a real subvolume, and hostit caps
    the container's layer with `btrfs qgroup limit -e <cap>`** (exclusive bytes,
    because the layer is a snapshot of the image; a referenced limit counts the
    shared ~2GB image and wedges the container). Validated: dd inside the
    container wrote exactly ~50MiB then "Disk quota exceeded". No reboot; cost is
    a storage-driver migration (images rebuild, containers recreate) plus a btrfs
    pool for /var/lib/containers (grow apps.btrfs or a second loop image).
    Alternative (works, but needs a maintenance reboot): ext4 journaled usrquota
    per app uid on the root fs (offline `tune2fs -O quota` + linux-modules-extra +
    quota pkg). Dead ends verified: overlay `--storage-opt size` demands XFS
    regardless of backing fs; old-style ext4 quota unsupported by the kernel.
    Full findings, pool-layout options, code and ansible sketches in
    `plans/260813-hostit-disk-hard-cap.md`. Also reconsider the `disk_mb: 0`
    (unlimited) default. Reported 2026-08-12.

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
- **State and quota collection** become cross-node: the state cache and the disk
  quota walk currently assume everything is on this box; they move behind the
  agent's heartbeat/report path.

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
- **Persist apt-installed packages across a container recreate.** Packages a tenant
  `apt-get install`s live in the container's writable layer, so a deploy/reboot that
  recreates the container loses them. Options: a per-app overlay/commit, or a
  documented `prepare:` step that reinstalls them on every build. (The image-version
  half of this -- pinning each app to the image tag it was built with -- is done; see
  Done below.)

## Done (recent)

Kept briefly for context; prune when stale.

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

- Btrfs storage model: per-app home subvolumes, snapshots (manual + auto), rollback
  (atomic, safety-snapshotted), hard qgroup disk quotas (EDQUOT), GFS retention,
  `hostit.yml` snapshot hooks, and **fork** (duplicate an app from a snapshot of its
  home). API + CLI + assistant tools + snapshot dialog.
- Web: in-browser terminal, built-in assistant, dark mode toggle, Apps switcher,
  mobile top-bar fold.
- Custom domains: attach an owner's hostname to an app, with DNS-01 (CNAME-delegated)
  certificates that work even for an internally-deployed server.
- Chat file uploads: drag-and-drop or "+" to save files into the app's uploads/, with
  images shown to the model as vision.
