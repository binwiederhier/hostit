# TODO

Things worth doing, with enough context to pick up cold. Not a backlog of
everything imaginable -- if it is not written down here it is not planned.

## Open questions

- is "hostit apps" api really necessary?
- what is v1/self and why is it not just the same api as the main api?

## Resource allocation

Today an app's RAM/disk caps are fixed at creation from the owner's defaults
(user.memory_mb / user.disk_mb -> container --memory and the btrfs budget
qgroup), CPU is uncapped, and nothing exposes any of it for editing.

- **Per-app resource editing.** There is no way to change an app's RAM, disk or
  CPU allocation after creation. Add PATCH-able per-app limits (memory_mb,
  disk_mb, cpu) in the API and the app Settings view. The plumbing mostly
  exists: SetMemoryLimit recreates the container with the new cap, SetDiskLimit
  re-caps the budget qgroup live; CPU needs a new --cpus/CPUWeight knob on the
  container.
- **Per-user RAM and disk pool.** Replace (or back) the per-app defaults with a
  per-user POOL: a user has, say, 2 GB RAM and 10 GB disk total, and their apps
  draw from it. Creating an app (or raising its limits) reserves from the pool;
  the create/edit is refused when the pool is exhausted. Registry: pool columns
  on the user row; enforcement at create/edit time (containers and qgroups keep
  enforcing per app at runtime).
- **User-editable within the pool.** The owner can edit each app's allocation
  themselves in the app's Settings, as long as the user's pool covers it --
  admins set pools, users divide them. Admin UI keeps setting pools per user.

## App capabilities: credentials an app uses but never holds

People want to build apps that use AI. Putting an API key in the app's
environment makes the tenant pay, makes the key a thing that leaks into a repo
or a log, and leaves hostit with no idea what was spent. Once a second want
appears (read-only GitHub, read-only database) the one-off version is clearly
wrong: three bespoke endpoints, three grant models, three audit trails.

So the thing to build is not an AI feature: **hostit holds the credential, the
app uses a capability, and nothing secret enters the container**. AI is the
first capability; GitHub is the second, and exists mainly to prove the
abstraction is real. Full design, including what each capability carries and why
a database does NOT fit the same mechanism:
`plans/260818-app-capabilities.md`.

- **Identity is already solved.** An app calls hostit over the unix socket its
  CLI uses; SO_PEERCRED gives the uid, `store.AppByUID` gives the app, the row
  gives the owner and its grants. An app on a remote node reaches its own node's
  socket and the node relays over the cluster link, the way the snapshot and
  usage callbacks already travel.
- **Budget is the owner's** (decided): `user.Limits` already carries per-person
  limits and assistant usage is already recorded by owner (`UsageByOwner`), with
  per-request attribution to the calling app so an owner can see which app spent
  what. Over budget is a clean error, the way a full disk gives EDQUOT.
- **Refuse cleanly when unconfigured.** If `controlconf.AssistantAvailable()` is
  false the capability does not exist, and says so -- an instance with no
  backend must not look broken to an app.
- **Not done until documented and proven**: `hostit info` (so an agent building
  an app discovers it), the user manual in `web/src/pages/Docs.jsx`, and a real
  app that uses it end to end (a translation app is a good first one).
- **Open before coding**: is streaming in v1 (SSE through the node relay is the
  difference between a small feature and a real one), and are owner-provided
  credentials (GitHub, needing profile-level OAuth) in scope or is v1
  operator-provided AI only.

## The hostit binary: two tools sharing a name

`cmd/agent/cmd.go` says it outright -- "two tools that happen to share a
binary", switched by `insideContainer()`. Inside a container `hostit` is PID 1
(the agent supervising the app's run command), the tenant CLI (deploy/logs/
restart over the peercred socket) and the MCP server. On the host it is the
operator's REST client (`hostit apps`) plus the shell/enter helpers. That is why
`hostit` reads as a peer of `hostit-control` and `hostit-node` when it is not:
one half of it is, and the other half belongs inside containers.

Related to the open question above about whether `hostit apps` is necessary at
all -- answering that first may shrink this.

The stronger argument is least privilege, not naming. The binary bind-mounted
into every container -- where the tenant is root -- also contains control's
TLS/ACME, OAuth, podman/systemd/nftables/store, assistant and admin-API code.
None of that should be present there. This is defense in depth, NOT a fix for a
live hole: the container already contains the tenant (userns to an unprivileged
host uid, no host podman or store, peercred socket).

- **Split it.** Keep a minimal container-only binary (supervise `run:`, reap
  zombies, static-serve, talk to the peercred socket) shipped by `hostit-node`
  and bind-mounted into containers, never a host command. Give the host its own
  CLI carrying `apps` plus dispatch to the components. Reaping may also be the
  answer to the zombie-processes entry below.
- **The host CLI should know what is installed**, so `hostit control ...` and
  `hostit node ...` work and a component that is not installed says so in a
  sentence naming what this machine is. Options: a wrapper script (cheapest, but
  its help cannot know what it dispatches to), or in-binary dispatch that execs
  the sibling (preferred: one help text, no extra process, and it can hide
  commands for components this host does not run).
- **Blocked on a name** for the host command. Renaming breaks muscle memory and
  scripts, so decide before starting.

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

## Snapshots and quotas

- **DONE: btrfs simple quotas (squotas).** Shipped on the multinode branch,
  live on stage 2026-08-16. It turned out to be a correctness fix, not just a
  performance one: classic qgroups NEVER enforced the budgets, because seeding
  an app from the shared base marks the fs quota state inconsistent and the
  kernel stops enforcing until a rescan completes (which churn keeps
  re-triggering -- so effectively never; verified 300MB written past a 200MB
  cap). EnableDiskBudgets now ensures squota mode at startup, migrating a
  classic-qgroups pool automatically (disable + enable-simple via ioctl, no
  btrfs-progs 6.7 needed; kernel 6.7+ required -- stage and prod run 6.8).
  Migration resets usage accounting: pre-existing extents are never counted.
  Rescans are gone wholesale, which should also fix the >2min prod snapshot
  item below (re-measure after the prod release).
- **Default snapshot cadence: every 3 hours, spread across apps.** Hourly
  auto-snapshots of every app at once spike the pool (and the cleaner);
  default to 3h per app instead and STAGGER apps across the interval so
  snapshot load is flat. Make the interval configurable per app in hostit.yml
  (snapshot.interval; 0 disables) and editable in the app Settings UI, next
  to the existing snapshot.pre/post hook commands -- surface those hooks in
  the UI too.

## Smaller things

- **Remove assistant-models / assistant-model from the config?** Probably
  subsumed by the model-picker rework below: the available models should be
  derived from which credentials are configured, not hand-listed in YAML. The
  app-capabilities work needs the same answer -- what an app may call should
  follow from what is configured, not a second hand-written list.

- **Assistant model picker: show models, not backends.** Instead of just
  "claude.ai" in the UI, list the actual models, driven by which credentials
  are configured. Anthropic API key configured -> "Haiku 4.5", "Sonnet 5",
  "Opus 5" (anthropic logo icon). claude.ai token (claude-code-oauth-token)
  configured -> "Sonnet 5", "Opus 5", "Fable 5" (claude icon). Both -> both
  sets, separated by a divider, distinguished by icon; only one -> only that
  set. Example (only the claude.ai token):
  `<claude icon> Fable 5 / Opus 5 / Sonnet 5`; with both, the same list
  followed by a divider and `<anthropic logo> Opus 5 / Sonnet 5 / Haiku 4.5`.

- **CHANGELOG.md per release.** Keep a CHANGELOG.md listing changes per
  version, updated with every release. Retroactively create the history for
  all existing tags from the commit messages and TODO.md's evolution through
  git history (git log --first-parent per tag range is a good starting point).

- **Can htop inside the container show only the container's resources?** Today
  it reads the host's /proc: all cores, all memory, everyone's load. Options:
  lxcfs-style /proc masking (fuse overlay for /proc/meminfo, /proc/cpuinfo,
  /proc/stat scaled to the cgroup limits), or podman's --systemd/cgroup
  namespace support; check what modern podman + crun offer out of the box
  (cgroup v2 namespaces hide other containers' PIDs already; the meminfo/cpu
  view is the missing piece).

- **hostit-node hangs on stop -- REPRODUCE BEFORE CHASING.** Seen once, during
  the 2026-08-16 stage deploy. It has not recurred: prod's node reports
  NRestarts=0 and restarted cleanly through five deploys on 2026-08-18, and the
  shutdown path has changed since (the signal handler closes the live
  connection, and the fused removal took the machine loops out of control).
  Either it is fixed or it needs a fresh reproduction; do not go hunting on the
  strength of the note below alone. What was seen: stopping
  exit path -- suspects: the control dial loop's redial sleep, a loop stuck
  in a long btrfs command, or the duplex session not unblocking Accept on
  close. Reproduce with `systemctl stop hostit-node` + goroutine dump
  (SIGQUIT), then fix the shutdown ordering.

- **There are a lot of zombie processes.** Find who is not reaping: suspects
  are the in-container agent (PID 1 must reap everything -- check its SIGCHLD
  handling around run:/exec/terminal children), podman exec sessions from the
  web terminal, the assistant sandbox containers, and the preview screenshot
  runs. `ps -eo stat,ppid,comm | grep ^Z` on stage/prod, group by PPID, then
  fix the parent.

- **Why does a read-only snapshot on prod take >2 minutes?** Taking a manual
  snapshot should be a metadata CoW operation (sub-second on an idle pool; the
  create path proves it). Suspects, in likely order: the snapshot waits on the
  current btrfs transaction, which the kernel cleaner (processing deleted
  subvolumes) or a quota rescan can hold for a long time on 1 vCPU (the stage
  e2e post-mortems measured 45s under a rescan backlog); qgroup accounting on
  snapshot create with many qgroups; or the pre/post snapshot hooks in the
  container, which run synchronously. Measure on prod: time a bare `btrfs
  subvolume snapshot -r` next to the API call, check `btrfs quota rescan -s`,
  the cleaner's CPU time, and qgroup counts (the stale-qgroup sweep from the
  multinode branch is not on prod yet -- prod may be carrying the same debt
  stage had). Is btrfs "that bad"? No -- but quotas + churn debt make its
  transaction commits that bad.

- **Make hostit-control unprivileged.** The audit this used to ask for is
  done: control drives none of podman, systemctl, useradd, nftables or btrfs any
  more (the node does), and it binds no privileged port (the proxy owns :443).
  Two things still need privilege, both machine-shaped work sitting in the wrong
  process: screenshot previews drive podman, and the assistant sandbox spawns
  `claude -p` as the app's uid. Move both to the node and control becomes a
  process holding a database, a certificate manager and an HTTP handler --
  runnable as its own user. The cluster socket already trusts control's own uid
  rather than root, so the transport does not stand in the way.


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

- **Reconcile sweeps orphaned unix accounts (2026-08-16).** An orphan's gid
  squats the uid block its old port maps to, so the next app allocated that port
  failed to create at all ("groupadd: GID already exists", hit live by a fork
  e2e). `Machine.reconcileUsers` removes accounts whose home is in THIS node's
  pool and whose id matches no mirror row; the pool scoping is load-bearing,
  since colocated nodes share one /etc/passwd.

- **Snapshot records survive a reconnect (2026-08-16).** On rejoin control reads
  the node's own records before pushing the mirror back, so a snapshot that
  completed as the connection dropped is recovered rather than overwritten by
  control's older list.

- **Multi-node, and then the fused daemon's removal (v0.13.x -> v0.14.0,
  2026-08-18).** Four binaries, each its own service: `hostit-control` (registry,
  web app, API, placement, certificates, retention decisions), `hostit-node`
  (this machine's app work), `hostit-proxy` (TLS and routing from a table
  control pushes it), `hostit` (CLI + in-container agent). control holds no
  machinery at all: machine work always crosses the cluster link, including to
  the node sharing its host. A genuinely remote node runs on stage; prod cut
  over and runs the three processes.

  A member sharing control's host dials a root-only unix socket
  (`cluster-socket`), presenting no credentials -- the kernel identifies the
  caller. mTLS on `listen-cluster` is for members on other machines only.
  `behind-proxy` is gone (the proxy terminates TLS in every deployment), and
  `listen-node` became `listen-cluster` (it admits proxies too; the old key is
  still read). Design history: `plans/260807-hostit-multinode.md`,
  `plans/260815-hostit-nodeagent.md`,
  `plans/260816-hostit-package-architecture.md`.

- **Idmapped-rootfs storage shipped end to end (v0.10.0, 2026-08-14).** Apps run
  `--rootfs <subvol>:idmap` on root-owned trees; creation is a ~0.3-0.6s
  metadata snapshot (was ~4s + 47MB metadata); the chown machinery is gone;
  crun >= 1.29 / podman >= 4.3 preflighted; deployed to stage AND prod with the
  one-time `storage-idmap` migration.
- **Delete answers instantly; churn is safe (v0.10.2 era, 2026-08-14).** Rows
  first, teardown in background; the port/uid stays reserved until done; the
  gentle qgroup destroy never syncs the pool (a sync stalled concurrent creates
  ~12s); a same-name recreate waits out the dying unix user instead of 409ing.
- **Poweroff is recorded intent (store flag), not inferred from systemd** -- a
  never-enabled fresh unit also reads "disabled", which made brand-new apps
  look powered off for their first seconds (terminal refused, no reconnect).
  One-time backfill seeds the flag on upgrade.
- **Create dialog spins; deleted apps leave nothing behind** (the stub-dir
  creator was homefs's defensive MkdirAll, removed with the idmap work).

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

- **E2E settings residue (2026-08-16).** The mode-filtering test
  restricts assistant settings globally and a run killed by -timeout skips its
  cleanup, so the NEXT run started against a restricted catalog. The suite now
  resets those settings once per run and the test restores the shipped default
  instead of echoing what it found, so residue self-heals. (The original note
  here blamed t.Parallel; the assistant tests are not parallel.)

- **e2e: TestAppPreviewModeContract failed once in-suite, unreproduced.** One
  full run failed it after 1.8s -- too fast for the usual 1-vCPU contention
  timeout, so probably an API error on an early call (create or file write).
  It passed standalone (10.9s) and in the next full run, and the output was
  filtered down to pass/fail lines, so the message was lost. If it recurs,
  capture the full -v output: the assertion line will name the call.
