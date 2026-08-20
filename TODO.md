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

**Read `plans/260818-hostit-broker-design.md` alongside it** (2026-08-18, draft,
nothing built; a Slidev deck of the same material sits beside it). It answers
the same question -- an app using a credential it never holds -- but generalizes
the mechanism: instead of hostit growing one capability per integration, hostit
becomes a generic MCP client. An owner connects an MCP server once and approves
specific tools; each app is granted a subset of those; the app POSTs
`{server, tool, args}` to a loopback listener in its own container and never
sees a credential. The two plans disagree on shape, and that is the decision to
make before either is built: capability-per-integration (fewer moving parts, one
implementation per thing) versus one broker (one implementation total, but a
registry, an OAuth client, per-owner secret custody and a control->node
credential push). AI would still be a capability under either.

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

## Smaller things

- **BUG: an app on a REMOTE node has no daemon socket.** Found 2026-08-19 while
  proving the connections PoC; diagnosed 2026-08-19 after a wrong first guess
  (recorded because the wrong guess is instructive: the symptom looked exactly
  like a stale mount, and two rounds of mount forensics went nowhere).

  The app-side unix socket `/run/hostit/hostit.sock` -- the `/v1/self/*` surface
  -- is served by **hostit-control**. On a host running only hostit-node there
  is no such socket: stage-2's `/run/hostit` holds `apps-raw` and nothing else.
  Apps placed there therefore cannot use it, and everything that rides on it
  fails from inside the container:

  - the in-container CLI (`hostit deploy`, `logs`, `snapshot`),
  - the MCP bridge the sandboxed assistant backend uses
    (`assistant/sandbox.go` runs `hostit mcp --socket <path>`),
  - the connections token endpoint added on the `connections` branch.

  Evidence: on stage, apps on host `local` (phdemo, thatphilguy, tictactoe,
  xray) work; apps on `hostit-stage-2` (sockdebug, test123, e2e-filter-*) do
  not. The container's `/run/hostit` is inode 2260 -- stage-2's own directory,
  dated when that node was built -- while the control host's is 2036 and holds
  the socket.

  **Prod is unaffected because it is a single host**: control and node share a
  machine, so every app sits next to the socket. This is a gap that opened with
  the control/node split and only shows on a multi-node instance.

  The fix is for **hostit-node to serve the app socket locally and relay
  `/v1/self/*` to control over the cluster link**, which already carries traffic
  both ways (the snapshot and usage callbacks go node->control today). The node
  knows which app a peer uid is, so the peercred authentication stays where it
  is; only the answering moves.

- **Private apps: only the owner can reach them.** hostit apps are public URLs.
  That is fine for a blog and wrong for a personal dashboard holding a connected
  Google account -- one URL guess away from being someone else's mail reader.
  Enforce at the proxy: an app marked private serves only a request carrying the
  owner's (or a named collaborator's) hostit session, everything else gets 403.
  The proxy already holds the routing table control pushes it, so the flag rides
  along the same path. This is the companion to the connections work
  (`plans/260819-connections.md`); connections are not finished without it, and
  it is useful on its own.

- **Decide the credential-brokering shape before building either plan.** See
  the paragraph under "App capabilities" above. The broker design's own build
  order starts with a scoped, revocable static token from the upstream service,
  precisely so hostit's first cut needs no OAuth client at all -- worth taking
  seriously, because the OAuth half (dynamic client registration, PKCE, refresh
  rotation, encrypted per-owner custody in control) is most of its cost. Its
  open questions that hostit owns regardless of shape: what encrypts a stored
  credential at rest, how control decides which nodes need a push and when a
  node purges one, and whose credential a collaborator-shared app uses.

- **An MCP server people can actually point an agent at.** `hostit mcp` already
  exists (`cmd/agent/mcp.go`) but it is hidden, stdio-only, and built for one
  caller: it runs INSIDE the assistant sandbox as the app's uid and reaches the
  daemon over the peercred socket, which is what scopes it to a single app. So
  it cannot be used from a laptop.

  What is missing is the outside-in version: a user runs `hostit mcp` (or points
  Claude Desktop at a URL) authenticated by their API token, and gets hostit's
  tools for the apps that token can reach -- list apps, read/write files, deploy,
  logs, snapshots, create an app. Today an external agent gets the same job done
  by reading the prompt on the app page and making HTTP calls, which works but
  puts the whole API surface in the model's context.

  Open questions: token-scoped (one app) vs account-scoped (all of them, with
  the app as a tool argument); stdio for a local binary vs streamable HTTP so
  there is nothing to install; and whether the tool set is literally
  `assistant/tools.go:ToolDefs` reused, which would keep the two surfaces from
  drifting. Worth checking against both credential plans
  (`plans/260818-app-capabilities.md`, `plans/260818-hostit-broker-design.md`)
  -- those are about an app calling OUT, this one is about an agent calling IN,
  and they should not invent two auth stories. The broker design already carries
  this as its item #6 and expects it to be cheap once the broker exists: the
  same "call an approved tool as owner X" function, wrapped in a server adapter
  instead of an HTTP relay.

- **Could a static app skip the container entirely?** Today every app gets a
  container, a unix user, a subvolume and a systemd unit, even one that is just
  files on disk. `mode: static` is already served by hostit itself, so for that
  mode the container may be buying nothing but startup cost, memory and a
  quota's worth of bookkeeping. Worth asking what a container-less static app
  would still need (the app's own uid for file ownership, snapshots, disk
  accounting, the assistant's run_command and SSH -- which is where it probably
  gets interesting, since both assume a container to enter). If the answer is
  "only SSH and run_command", a static app could stay container-less until
  something asks for one.

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

## Deferred

Explored and deliberately not being done, kept for the reasoning rather than the
intent. Each says what was measured, so picking one up later starts from
evidence -- and so the same idea is not re-proposed and re-investigated in six
months.

- **Can htop inside the container show only the container's resources?**
  EXPLORED 2026-08-19 on stage (podman 4.9.3, crun 1.14.1, Ubuntu 24.04). What
  is true today:

  - The container ALREADY has a private cgroup namespace: `/proc/self/cgroup` is
    `0::/` and `/sys/fs/cgroup/memory.max` is exactly the app's limit
    (536870912 for a 512 MB app). The truth is present inside the container.
  - `/proc/meminfo`, `/proc/loadavg`, `/proc/uptime`, `/proc/cpuinfo` and
    `/proc/swaps` are the HOST's, unmasked.
  - htop 3.4.1 is not cgroup-aware: in a container capped at 256 MB it draws
    `Mem[243M/961M]` (the host's 961 MB), the host's load average, the host's
    swap, and the host's 13-day uptime. `free` agrees. So the tools people
    actually run read the files that lie, not the cgroup that tells the truth.
  - Modern podman/crun do NOT mask /proc out of the box. What the namespaces
    already give is PID isolation (htop lists 2 processes, not the host's) and
    cgroup isolation. The /proc view is the whole remaining gap.
  - A bind-mount over `/proc/meminfo` DOES work here -- mounting a hand-written
    file made `free` report 512 MB -- so the plumbing lxcfs relies on is
    available; lxcfs would only be supplying live numbers.

  Options, in cost order:

  1. **lxcfs** (5.0.4-1 in the Ubuntu repos, not installed; fuse3 already is).
     A daemon per node serving `/var/lib/lxcfs/proc/*`, bind-mounted into every
     app container. Live and correct for meminfo, cpuinfo, stat, uptime,
     loadavg, swaps. Cost: a new system dependency and a daemon to supervise,
     plus the known sharp edge that restarting lxcfs leaves existing mounts
     stale (ENOTCONN) until each container restarts.
  2. **Static per-app files.** No daemon: write a meminfo from the app's limit
     and bind-mount it. But the numbers never move, so MemFree/MemAvailable
     become a frozen lie -- arguably worse than the host's moving one -- and
     regenerating them on a timer is reimplementing lxcfs badly.
  3. **Do nothing to /proc, document the cgroup.** `cat
     /sys/fs/cgroup/memory.current` and `memory.max` are already correct, and
     the app page shows accurate RAM and disk. Cheapest, and leaves htop wrong.

  **The danger, measured.** Masking /proc only masks the FILES. With a fake
  meminfo bind-mounted, `free` reported 512 MB while the `sysinfo(2)` syscall
  and `sysconf(_SC_PHYS_PAGES)` both still reported the host's 961 MB in the
  same container. lxcfs has the same limitation (intercepting the syscall would
  need seccomp notify, which it does not do). So this does not make the
  container honest -- it makes it inconsistent: htop and free would say one
  thing while a language runtime sizing itself off the syscall says another, and
  "hostit reports two different memory totals" is a worse bug to be told about
  than "htop shows the host's".

  Other risks worth weighing:

  - It puts a FAILURE mode into a file that currently cannot fail. /proc/meminfo
    is the kernel's today and always reads; behind lxcfs, a daemon restart (a
    package upgrade, a crash) leaves every existing container's mount stale and
    reads return ENOTCONN until each container is restarted.
  - It is a privileged, tenant-reachable surface: a root FUSE daemon on the node
    serving files every tenant reads and can hammer. Today that read path is the
    kernel.
  - Masking cpuinfo changes tenant app BEHAVIOUR, not just what htop draws:
    nginx `worker_processes auto`, JVM heap sizing and older Go runtimes size
    themselves off the visible core count.
  - It is a new per-node dependency. If a node lacks the daemon and the mount
    source is missing, podman refuses to start the container -- a missing
    package wedges every app on that node unless the mount is conditional, and a
    conditional mount means the feature silently does not apply there.
  - Untested here: FUSE under this container setup specifically (per-app uid
    blocks via --uidmap plus an idmapped --rootfs). It needs proving before it
    is designed around.

  Set that against the payoff, which is htop drawing a nicer number: the app
  page already shows accurate memory and disk, and `/sys/fs/cgroup/memory.max`
  is correct inside the container today.

  Worth knowing before picking: the CPU half is NOT wrong yet. `cpu.max` reads
  `max`, so an app really can use every core, and `nproc` showing all of them is
  honest today. It only becomes a lie once per-app CPU limits exist (see
  Resource allocation above), which argues for doing this WITH that work rather
  than before it.

## Done (recent)

Kept briefly for context; prune when stale.

- **Archive and unarchive an app (2026-08-19).** A shelved app powers off and
  refuses to run: the guard sits on `routingAgent.routeRunnable`, the one place
  control reaches a node for a verb that would start something, so a verb added
  later cannot forget it. Stopping and inspecting still work. It takes no new
  snapshots (it cannot change) and its history is kept under
  `retention.Archived` -- monthly rollups for a year, with the newest snapshot
  as a floor so an archive never empties. `archived` is its own registry column,
  not `powered_off`, so leaving the archive returns the app to the power state
  it had rather than to a guess.

- **A per-release CHANGELOG (2026-08-19).** CHANGELOG.md now covers every tag
  back to v0.1.0, reconstructed from the git history. It stays true because
  `make release` depends on `changelog-check`, which refuses to build a tag the
  file does not describe -- writing the entry is part of releasing rather than
  something to remember afterwards.

- **Dashboard list view (2026-08-19).** A cards/list toggle beside the summary
  strip; the list adds the owner column cards never had. The choice lives in
  localStorage, being a per-device viewing preference rather than account state.
  The last column is uptime, not "last deploy": the API has no deploy timestamp
  and a column claiming one would lie after a plain reboot.

- **Snapshot cadence (2026-08-19).** Every app was snapshotted hourly on the
  same tick. Now three hours by default, per-app via `snapshot.interval` in
  hostit.yml (0 opts out), and staggered by a hash of the app name so the fleet
  does not move as one. An app two intervals behind stops waiting for its slot.
  Settings gained a Snapshots section for the interval and the pre/post hooks,
  which had never been in the UI at all.

- **Assistant model picker: models, not backends (2026-08-18, on stage).** The
  dropdown used to mix a backend ("Claude.ai") with a flat list of API models
  hand-listed in YAML, so a choice said nothing about which model ran or who
  paid, and the list could disagree with what the credentials could serve. An
  option is now a (backend, model) pair with a backend-prefixed id
  (`claude-opus-5` vs `anthropic-opus-5`), and the catalog is DERIVED from the
  configured credentials by a backend registry (`assistant/backend.go`): a new
  provider is one file, not an edit in the config, the dropdown, a validator and
  an admin page. `assistant-models`, `assistant-model`, the per-user allowlist
  and the admin "Assistant access" table are all gone; the default is hardcoded
  (the head of the catalog, so the menu order is the only preference) and the
  app's remembered choice still wins over it. Verified by e2e: a real turn on every catalog
  option, plus a cross-backend switch.

- **btrfs simple quotas (2026-08-16, live on stage and prod).** It turned out to be a correctness fix, not just a
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

  The migration is also why an upgraded pool reports near-zero disk usage for a
  while: squota counts extents written after it was enabled, so pre-existing app
  data is not in the total. Enforcement is unaffected.

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
