# TODO

Things worth doing, with enough context to pick up cold. Not a backlog of
everything imaginable -- if it is not written down here it is not planned.
Shipped work lives in CHANGELOG.md and the git history; "Done (recent)" below
keeps only the last few days for context.

## Resource allocation

Per-app editing shipped 2026-08-20 (admin-only PATCH + Settings card, CPU cap
via --cpus, persisted overrides; plans/260820-per-app-resources.md). What
remains is the pool model that would let owners edit within bounds:

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

- **Decide the shape first.** The broker design's own build order starts with a
  scoped, revocable static token from the upstream service, precisely so
  hostit's first cut needs no OAuth client at all -- worth taking seriously,
  because the OAuth half (dynamic client registration, PKCE, refresh rotation,
  encrypted per-owner custody in control) is most of its cost. Open questions
  hostit owns regardless of shape: what encrypts a stored credential at rest,
  how control decides which nodes need a push and when a node purges one, and
  whose credential a collaborator-shared app uses.
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

## Smaller things

- **MCP bridge: return images as image content.** `read_file` returns text, so
  an image read through it is byte salad -- which is why attached images ride
  the sandbox's stdin as blocks (2026-08-20 fix) instead of being fetched by
  path. MCP has an image content type and Claude Code renders image
  tool-results to real vision, so teaching the bridge to answer image files
  with an image block would let the agent LOOK at any image already in the app
  (screenshots in public/, logos), not just ones attached to the current
  message. Composes with the stdin path, does not replace it: an attached
  image should be unconditionally visible, not contingent on a tool call.
  Touches appcli's mcp server and the sandbox's tool-result parsing.

- **Private apps: only the owner can reach them.** hostit apps are public URLs.
  That is fine for a blog and wrong for a personal dashboard holding a connected
  Google account -- one URL guess away from being someone else's mail reader.
  Enforce at the proxy: an app marked private serves only a request carrying the
  owner's (or a named collaborator's) hostit session, everything else gets 403.
  The proxy already holds the routing table control pushes it, so the flag rides
  along the same path. This is the companion to the connections work
  (`plans/260819-connections.md`); connections are not finished without it, and
  it is useful on its own.

- **A redirect (alias) domain type.** Today a custom domain only routes traffic
  to its app: `store.Domain` (store/types.go) has no redirect field, and the
  proxy (proxy/cli.go, proxy/service.go) only does the http->https hop. So apex
  canonicalization (professornoodle.com to www.professornoodle.com) and legacy
  hostname redirects have to live in each app, which every app reinvents:
  yayagram now does an apex->www 301 in its own handler, websrv does the
  heckel.io WordPress-URL redirects. Give a domain a `RedirectTo` (empty routes
  to the app as today; set issues a 301): add the column plus migration, an
  optional `redirect_to` on `POST /api/apps/{app}/domains` (and a CLI
  `--redirect-to www.example.com`, with a `--redirect-to-primary` convenience
  targeting the app's own canonical domain), and a check in the proxy that 301s
  before routing, right next to the http->https redirect it already owns. Cert
  issuance is unchanged (the same `_acme-challenge` delegation). Then apex->www
  is a platform concern instead of per-app code, and the app-level redirect
  hacks retire. Prompted 2026-08-20 by moving professornoodle.com onto hostit,
  where the bare apex had no native way to reach www.

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

- **Make hostit-control unprivileged.** The audit this used to ask for is
  done: control drives none of podman, systemctl, useradd, nftables or btrfs any
  more (the node does), and it binds no privileged port (the proxy owns :443).
  Two things still need privilege, both machine-shaped work sitting in the wrong
  process: screenshot previews drive podman, and the assistant sandbox spawns
  `claude -p` as the app's uid. Move both to the node and control becomes a
  process holding a database, a certificate manager and an HTTP handler --
  runnable as its own user. The cluster socket already trusts control's own uid
  rather than root, so the transport does not stand in the way.

- **Log following.** `GET /api/apps/{app}/logs?lines=N` is a snapshot. An agent
  watching a slow start has to poll.

- **Long jobs.** `POST /api/apps/{app}/run` is bounded at five minutes, so a first
  `npm install` on a small box can outlast it. Anything longer has to become a
  `prepare:` step, which is fine but not obvious.

- **Next release checklist: finish the shell-path move.** The release that
  drops the legacy `/usr/bin/hostit-shell` + `/usr/bin/hostit-enter` copies
  must move the sudoers grant (`hostit.sudoers` still names
  `/usr/bin/hostit-enter`) and hostit-shell's sudo target to
  `/usr/lib/hostit/bin` in the SAME commit, or app entry breaks. (ARCH-5 in
  `plans/260820-hostit-review-findings.md`.)

- **Review follow-ups (2026-08-20).** From
  `plans/260820-hostit-review-findings.md`, all LOW/accepted: write down the
  control-is-a-SPOF-for-interactive-surfaces shape in an ops doc; raise the
  node package's 14% unit coverage by extracting more pure decision logic; a
  one-place /run/hostit socket inventory in the docs; next subsystem starts as
  its own package instead of growing control (7.7k LOC).

- **There are a lot of zombie processes.** Find who is not reaping: suspects
  are the in-container agent (PID 1 must reap everything -- check its SIGCHLD
  handling around run:/exec/terminal children), podman exec sessions from the
  web terminal, the assistant sandbox containers, and the preview screenshot
  runs. `ps -eo stat,ppid,comm | grep ^Z` on stage/prod, group by PPID, then
  fix the parent.

- **Re-measure the slow prod snapshot.** Manual snapshots on prod took >2min;
  the prime suspect (quota rescans stalling the btrfs transaction) was removed
  wholesale by the squota migration (2026-08-16, live on prod). Time one bare
  `btrfs subvolume snapshot -r` next to the API call on prod; if it is now
  sub-second, delete this item.

- **hostit-node hangs on stop -- REPRODUCE BEFORE CHASING.** Seen once
  (2026-08-16); has not recurred through many deploys since, and the shutdown
  path changed (the signal handler closes the live connection). If it recurs:
  `systemctl stop hostit-node` + SIGQUIT goroutine dump, then fix the shutdown
  ordering. Do not go hunting without a fresh reproduction.

## Deferred

Explored and deliberately not being done, kept for the reasoning rather than the
intent. Each says what was measured, so picking one up later starts from
evidence -- and so the same idea is not re-proposed and re-investigated in six
months.

- **API path harmonization (/v1/self vs /api).** The app socket speaks /v1/self
  while the public API speaks /api; the relay work (2026-08-20) kept both on
  purpose. Any future rename must keep /v1/self answering, because bind-mounted
  container binaries upgrade late. Explicitly parked ("let's leave that alone
  for this round"); revisit only with a concrete win in hand.

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

Kept briefly for context; prune when stale. Everything older is in CHANGELOG.md.

- **CLI round + reviews (2026-08-20, v0.17.0+main).** `hostit control apps` ->
  `app` (plural aliased); new `hostit node status`, `hostit proxy status`,
  `hostit proxy route list`, each answering from the daemon's OWN state over a
  new root-only socket (works exactly when control is down); lipgloss tables
  across every CLI list; bash+zsh tab completion for all binaries (the front
  door asks the sibling that owns the commands). Full security/style/
  architecture review: no HIGH findings; admin-token 16-char floor enforced;
  mkdeb.sh removed; declaration-grouping sweep; e2e suites sweep their stale
  apps. Findings + status: `plans/260820-hostit-review-findings.md`. Bug found
  via the new status output and fixed: the state poll skipped app-less nodes,
  freezing their LAST SEEN (empty nodes now answer Heartbeat()).

- **The node owns the app socket (2026-08-20, the high-priority bug).** An app
  on a node-only host had no daemon socket at all: no SSH, no in-container CLI,
  no MCP bridge. hostit-node now serves /run/hostit/hostit.sock on every host,
  authenticates by SO_PEERCRED against its own mirror (which carries each
  hosted app's uid) and relays to control over the cluster link -- never
  answering locally, so control keeps every guard; an archived app's deploy is
  refused with "archived" THROUGH the relay, and a test drives that. Control's
  own socket moved to /run/hostit/control.sock (operator CLI, assistant
  sandbox). Proven on stage: a stage-2 app ran `hostit logs|status|deploy` from
  inside for the first time.

- **The browser terminal runs its pty on the app's node (2026-08-20).** Third
  bug of the socket family, found live in a browser demo: control executed the
  node-supplied "runuser <app>" on its own host, where the user does not exist
  -- the terminal had never worked for a remote-node app. NodeAgent.Terminal
  replaces TerminalCommand: a live session streamed over a raw yamux stream on
  the existing cluster connection (hand-rolled 101 upgrade, framed input, raw
  output), bridged to the browser websocket by control. Covered end to end
  over a real duplex in nodelink's tests.

- **The binary split (2026-08-20).** hostit-app (package appcli) is the
  in-container command set, shipped at /usr/lib/hostit/bin/hostit-app and
  mounted into containers as /usr/bin/hostit -- tenants type what they always
  typed. hostit became the front door (`hostit control|node|proxy ...` execs
  the sibling; `hostit apps` is a deprecated alias), the apps commands moved
  onto hostit-control beside their registry, and the login shell moved to
  /usr/lib/hostit/bin with a usermod sweep on node start (old path shipped one
  release so a failed sweep strands nobody). The lesson that cost an hour on
  stage: the mount SOURCE is the host path and the exec is the CONTAINER path;
  conflating them crash-looped every app at PID 1, and a test now pins it.

- **Archive and unarchive an app (2026-08-19).** A shelved app powers off and
  refuses to run: the guard sits on `routingAgent.routeRunnable`, the one place
  control reaches a node for a verb that would start something, so a verb added
  later cannot forget it. Snapshot history kept under `retention.Archived`.
  `archived` is its own registry column, not `powered_off`, so leaving the
  archive returns the app to the power state it had rather than to a guess.

- **Per-release CHANGELOG, dashboard list view, snapshot cadence
  (2026-08-19).** CHANGELOG.md covers every tag and `make release` refuses a
  tag the file does not describe. Cards/list toggle on the dashboard. Snapshots
  every three hours by default, per-app `snapshot.interval`, staggered by name
  hash; Settings gained a Snapshots section.

- **Assistant model picker: models, not backends (2026-08-18).** An option is a
  (backend, model) pair derived from the configured credentials by a backend
  registry (`assistant/backend.go`); a new provider is one file. The YAML model
  lists, per-user allowlist and admin access table are gone.

- **btrfs simple quotas (2026-08-16, live everywhere).** Classic qgroups NEVER
  enforced (seeding from the shared base marks quota state inconsistent;
  verified 300MB past a 200MB cap). squota mode enforced from startup, with
  automatic migration; rescans gone wholesale.

- **Multi-node, and the fused daemon's removal (v0.13.x -> v0.14.0).** Four
  binaries: control (registry, web, API, placement, certs), node (machine
  work), proxy (TLS + routing from a pushed table), hostit (front door).
  Machine work always crosses the cluster link; a same-host member dials the
  root-only cluster socket, remote members use mTLS. Design history:
  `plans/260807-hostit-multinode.md`, `plans/260815-hostit-nodeagent.md`,
  `plans/260816-hostit-package-architecture.md`.
