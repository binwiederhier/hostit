# TODO

Things worth doing, with enough context to pick up cold. Not a backlog of
everything imaginable -- if it is not written down here it is not planned.
Shipped work lives in CHANGELOG.md and the git history; "Done (recent)" below
keeps only the last few days for context.

**Ordered by priority** (top of each tier first), reassessed 2026-08-21 after
v0.18.0. The ordering is a judgement call about what this platform needs next,
given that it now runs real internet-facing projects: security gaps that expose
tenant data outrank features, cheap well-specified work outranks expensive
open-ended work, and a decision that unblocks two other items is worth making
before either is built.

## Now (next few sessions)

### 1. A redirect (alias) domain type

Cheapest real win on the list: the design is already settled, it retires
per-app hacks, and it has a live pain point (moving professornoodle.com onto
hostit in August left the bare apex with no native way to reach www).

Today a custom domain only routes traffic to its app: `store.Domain`
(store/types.go) has no redirect field, and the proxy (proxy/cli.go,
proxy/service.go) only does the http->https hop. So apex canonicalization and
legacy hostname redirects live in each app, which every app reinvents: yayagram
does an apex->www 301 in its own handler, websrv does the heckel.io
WordPress-URL redirects.

Give a domain a `RedirectTo` (empty routes to the app as today; set issues a
301): add the column plus migration, an optional `redirect_to` on
`POST /api/apps/{app}/domains` (and a CLI `--redirect-to www.example.com`, with
a `--redirect-to-primary` convenience targeting the app's own canonical
domain), and a check in the proxy that 301s before routing, right next to the
http->https redirect it already owns. Cert issuance is unchanged (the same
`_acme-challenge` delegation).

### 2. Finish the shell-path move (a release-sized cleanup, now safe)

VERIFIED SAFE 2026-08-21: **zero** passwd entries still name the old path on
prod, stage-1 or stage-2, and two releases (v0.17.0, v0.18.0) have shipped
since the usermod sweep landed. The bridge has done its job.

Drop the legacy `/usr/bin/hostit-shell` and `/usr/bin/hostit-enter` copies from
the packages -- and in the SAME commit move the sudoers grant
(`hostit.sudoers` still names `/usr/bin/hostit-enter`) and hostit-shell's sudo
target to `/usr/lib/hostit/bin`, or app entry breaks. Also drop the sweep
itself (`unixuser.SweepShellPaths`, called from node startup) once the paths
are gone. (ARCH-5 in `plans/260820-hostit-review-findings.md`.)

### 3. Secrets that are not in the app's web root

`env:` values live in `hostit.yml`, which sits in the app's home and is served
if someone points a web server at the wrong directory. A real secret store (or
at least a separate file that is never in `public/`, e.g. `.hostit/secrets` with
mode 0600 read by the agent at start) would make it safe to put credentials in
an app. Second-mildest exposure on this list after private apps: it needs the
tenant to misconfigure their own server, but the footgun is loaded by default.

Worth designing alongside the capability work below -- a capability is "hostit
holds the credential", a secret is "the tenant holds it and hostit stores it
carefully"; both need per-app custody and encryption at rest, and inventing
that twice would be a mistake.

## Next (decide, then build)

### 4. Decide the credential-brokering shape

The decision is cheap and unblocks two other items (#6 capabilities and #10 the
outside-in MCP server, which must not invent a second auth story). Building
either shape is expensive, which is exactly why the decision comes first and on
its own.

Two plans disagree, deliberately:

- `plans/260818-app-capabilities.md` -- **capability per integration**. hostit
  holds the credential and offers a named capability (AI first, read-only
  GitHub second to prove the abstraction). Fewer moving parts, one
  implementation per thing.
- `plans/260818-hostit-broker-design.md` -- **one broker**. hostit becomes a
  generic MCP client: an owner connects an MCP server once and approves
  specific tools, each app is granted a subset, and the app POSTs
  `{server, tool, args}` to a loopback listener in its own container. One
  implementation total, but it needs a registry, an OAuth client, per-owner
  secret custody and a control->node credential push.

The broker's own build order starts with a scoped, revocable static token from
the upstream service, precisely so hostit's first cut needs no OAuth client at
all -- worth taking seriously, because the OAuth half (dynamic client
registration, PKCE, refresh rotation, encrypted custody) is most of its cost.

Questions hostit owns regardless of shape: what encrypts a stored credential at
rest, how control decides which nodes need a push and when a node purges one,
and whose credential a collaborator-shared app uses.

### 5. App capabilities: credentials an app uses but never holds

Blocked on #5. People want to build apps that use AI. Putting an API key in the
app's environment makes the tenant pay, makes the key a thing that leaks into a
repo or a log, and leaves hostit with no idea what was spent.

What is already solved, whichever shape wins:

- **Identity.** An app calls hostit over the unix socket its CLI uses;
  SO_PEERCRED gives the uid, `store.AppByUID` gives the app, the row gives the
  owner and its grants. An app on a remote node reaches its own node's socket
  and the node relays over the cluster link, the way the snapshot and usage
  callbacks already travel.
- **Budget is the owner's** (decided): `user.Limits` carries per-person limits
  and assistant usage is already recorded by owner (`UsageByOwner`), with
  per-request attribution to the calling app. Over budget is a clean error, the
  way a full disk gives EDQUOT. The pool model shipped in v0.18.0 is the
  precedent for how that should feel.
- **Refuse cleanly when unconfigured.** If `controlconf.AssistantAvailable()`
  is false the capability does not exist and says so -- an instance with no
  backend must not look broken to an app.

Not done until documented and proven: the per-app `/info` (so an agent building
an app discovers it), the user manual in `web/src/pages/Docs.jsx`, and a real
app that uses it end to end (a translation app is a good first one).

Open before coding: is streaming in v1 (SSE through the node relay is the
difference between a small feature and a real one), and are owner-provided
credentials (GitHub, needing profile-level OAuth) in scope or is v1
operator-provided AI only.

### 6. Dev/stage -> promote to prod (the "we work in prod" problem)

The biggest missing *feature*, and the one with the largest design surface --
which is why it sits below the decision above rather than competing with it.

Right now the only copy of an app is the live one, so every edit (and every
assistant change) is in production. Give each app an optional **staging**
environment -- its own container + subdomain (`stage.<app>.<base>` or
`<app>-stage`) sharing nothing live -- where changes and deploys land first,
then a **Promote** action swaps it into prod atomically (blue/green: build and
verify on stage, then flip the proxy, keeping the old prod as instant
rollback). Ties into the fork primitive (a stage is a fork that promotes back).
Likely a `hostit promote` verb plus a store notion of two environments per app.

Write a plan in `plans/` before writing code: the interactions with pools
(does a stage env draw from the owner's pool?), snapshots, domains and the
assistant all need deciding up front.

## Soon (small, self-contained)

### 7. MCP bridge: return images as image content

`read_file` returns text, so an image read through it is byte salad -- which is
why attached images ride the sandbox's stdin as blocks (the 2026-08-21 fix)
instead of being fetched by path. MCP has an image content type and Claude Code
renders image tool-results to real vision, so teaching the bridge to answer
image files with an image block would let the agent LOOK at any image already
in the app (screenshots in `public/`, logos), not just ones attached to the
current message. Composes with the stdin path, does not replace it: an attached
image should be unconditionally visible, not contingent on a tool call.
Touches appcli's mcp server and the sandbox's tool-result parsing.

### 8. Log following

`GET /api/apps/{app}/logs?lines=N` is a snapshot. An agent watching a slow
start has to poll. SSE or a websocket tail would fix it; note the node relay
(control does not hold the logs) is the interesting part, and the terminal's
existing duplex stream over the cluster link is the precedent.

### 9. An MCP server people can actually point an agent at

Check against #5 first -- this is an agent calling IN where the capability work
is an app calling OUT, and they must not invent two auth stories. The broker
design carries this as its item #6 and expects it to be cheap once the broker
exists: the same "call an approved tool as owner X" function, wrapped in a
server adapter instead of an HTTP relay.

`hostit mcp` already exists (`appcli/mcp.go`) but it is hidden, stdio-only, and
built for one caller: it runs INSIDE the assistant sandbox as the app's uid and
reaches the daemon over the peercred socket, which is what scopes it to a
single app. So it cannot be used from a laptop. What is missing is the
outside-in version: a user runs `hostit mcp` (or points Claude Desktop at a
URL) authenticated by their API token, and gets hostit's tools for the apps
that token can reach.

Open: token-scoped (one app) vs account-scoped (all of them, with the app as a
tool argument); stdio for a local binary vs streamable HTTP so there is nothing
to install; and whether the tool set is literally `assistant/tools.go:ToolDefs`
reused, which would keep the two surfaces from drifting.

### 10. Long jobs

`POST /api/apps/{app}/run` is bounded at five minutes, so a first `npm install`
on a small box can outlast it. Anything longer has to become a `prepare:` step,
which is fine but not obvious. A job id plus a poll/stream endpoint would be
the honest fix.

## Later (real, but not now)

### 11. Move the screenshot and assistant containers to the nodes

Both of control's remaining podman users are machine-shaped work sitting in
the wrong process: the **screenshot previews** run a chrome container (plus an
nftables egress filter, also machine work), and the **assistant sandbox**
spawns `claude -p` as the app's own uid. Moving both is what finally takes
podman -- and nftables, and root -- off the control host: control becomes a
process holding a database, a certificate manager and an HTTP handler,
runnable as its own user. The cluster socket already trusts control's own uid
rather than root, so the transport does not stand in the way.

**Do them together, or the win does not arrive.** Moving previews alone leaves
the assistant sandbox behind, so control still needs podman and still runs
containers over untrusted-adjacent input; the dependency is only removed when
the last user goes. (Raised 2026-08-21 as "could screenshots run on the nodes
so we would not need podman on control" -- yes, but only as half of this.)

Shape, once decided:

- A `Screenshot(spec)` verb on nodeapi returning PNG bytes: the node runs the
  container and the egress filter, control keeps the scheduling, the rate
  limiting, the storage and the serving. The shot code is already isolated
  behind two injectable fields (`ready`, `capture` in preview/service.go), so
  the seam exists.
- The assistant sandbox is harder: it resolves an app's uid from the REGISTRY
  (so it works for an app on any node) and mounts control's own socket for the
  MCP bridge. On the node it would use the node's own mirror and the app
  socket the node already serves -- which is arguably more correct, since the
  sandbox for a stage-2 app currently runs on control's host.
- Watch the assistant's streaming: control publishes SSE to the browser from
  the sandbox's event stream, so the events would have to cross the cluster
  link (the terminal's duplex stream over that link is the precedent).

Real hardening, but it buys defense-in-depth rather than closing an open hole,
which is why it sits here rather than in the Now tier.

### 12. Review follow-ups (2026-08-20)

From `plans/260820-hostit-review-findings.md`, all LOW/accepted:

- Write down the control-is-a-SPOF-for-interactive-surfaces shape in an ops doc
  (apps keep serving through a control outage; SSH, the terminal, deploys and
  the dashboard do not).
- Keep raising the node package's unit coverage by extracting pure decision
  logic (14% at the review, 21.5% after v0.18.0; the machine stack needs
  root/podman/btrfs, so e2e carries the rest).
- A one-place `/run/hostit` socket inventory in the docs (five sockets now:
  hostit.sock 0666 app-facing, cluster.sock, control.sock, node.sock,
  proxy.sock, all 0600).
- The next subsystem starts as its own package instead of growing control
  (7.7k LOC).

Also from the v0.18.0 pass (`plans/260820-per-app-resources.md`): the pool
checks are check-then-act without an owner-level lock, so two concurrent
creates can overshoot a pool by one app's allocation. Accepted -- it
self-corrects on the next edit and cannot run away -- but an owner-scoped lock
is the fix if it ever matters.

### 13. Could a static app skip the container entirely?

Today every app gets a container, a unix user, a subvolume and a systemd unit,
even one that is just files on disk. `mode: static` is already served by hostit
itself, so for that mode the container may be buying nothing but startup cost,
memory and a quota's worth of bookkeeping. Worth asking what a container-less
static app would still need (the app's own uid for file ownership, snapshots,
disk accounting, the assistant's run_command and SSH -- which is where it
probably gets interesting, since both assume a container to enter). If the
answer is "only SSH and run_command", a static app could stay container-less
until something asks for one.

Speculative: measure the actual cost of an idle static app's container before
designing anything.

### 14. hostit-node hangs on stop -- REPRODUCE BEFORE CHASING

Seen once (2026-08-16); has not recurred through many deploys since, and the
shutdown path changed (the signal handler closes the live connection). If it
recurs: `systemctl stop hostit-node` plus a SIGQUIT goroutine dump, then fix
the shutdown ordering. Do not go hunting without a fresh reproduction.

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
  EXPLORED 2026-08-19 on stage (podman 4.9.3, crun 1.14.1, Ubuntu 24.04).

  **Status update 2026-08-21:** the trigger condition below has now been
  reached -- per-app CPU caps exist (new apps default to 0.5 cores), so
  `cpu.max` no longer reads `max`. On the current 1-core prod and stage boxes
  this changes nothing an eye can see (`nproc` reads 1 either way; the cap
  throttles rather than removing cores). It becomes a real lie on a
  multi-core node: an app capped at 0.5 cores would still see `nproc=8`, and
  nginx `worker_processes auto` or a JVM sizing its heap off that count would
  provision for eight. Re-read this section before hostit runs on a bigger box.

  What is true today:

  - The container ALREADY has a private cgroup namespace: `/proc/self/cgroup` is
    `0::/` and `/sys/fs/cgroup/memory.max` is exactly the app's limit.
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
  - Masking cpuinfo changes tenant app BEHAVIOUR, not just what htop draws (see
    the status update above).
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

## Closed by measurement (2026-08-21)

Kept briefly so they are not re-proposed; delete after a few weeks.

- **"The slow prod snapshot"** -- was >2min; the squota migration (2026-08-16)
  was the fix. Re-measured on prod after v0.18.0: a bare
  `btrfs subvolume snapshot -r` takes **0.036s** and the full API call
  (`POST /api/apps/draw/snapshots`, network included) **0.21s**. Closed.
- **"There are a lot of zombie processes"** -- re-checked on prod and stage-1
  after v0.18.0: **zero** zombies on both (`ps -eo stat | grep ^Z`). Whatever
  it was, the agent/exec/terminal reaping changes since have covered it.
  Closed; reopen with a fresh count if it returns.

## Done (recent)

Kept briefly for context; prune when stale. Everything older is in CHANGELOG.md.

- **Private apps (2026-08-21).** An app can be reachable only by its owner, its
  collaborators and admins, chosen at creation or flipped in Settings, and it
  holds on every hostname the app answers to (custom domains included). The
  design in `plans/260821-private-apps.md` had to change during the build: it
  assumed the proxy could ask control about the visitor's SESSION, but the
  session cookie is `__Host-` prefixed and a browser never sends it to an app
  subdomain, so there was nothing to ask about. Instead the visitor bounces once
  through the web app, where the session does apply, and comes back with a
  signed per-app grant; the grant asserts identity only, so every request
  re-checks live access and revocation is immediate. The grant is stripped
  before the request reaches the app. `proxyapi.Route` gained `Private` and the
  proxy hands those requests to control (the same fallthrough an unknown
  hostname takes), so no session handling entered the data plane. Previews are
  skipped for private apps rather than given a bypass.

- **Per-app resources and per-user pools (2026-08-21, v0.18.0).** Owners edit
  their apps' RAM and disk within a per-user pool (admins set pools, the pool
  binds admins too); CPU is a new admin-set cap via `--cpus`; new apps default
  to 128 MB / 256 MB / 0.5 cores while every pre-existing app and owner was
  pinned at their old budget by migration. One `user.EffectiveAppLimits`
  resolves override-else-default for every consumer. The UI grew one resource
  language (icon + used/total pairs, yellow at 75%, red at 90%) across the
  dashboard, app page and admin views, and `/info` now tells an agent its own
  budget. Plan: `plans/260820-per-app-resources.md`.

- **CLI round + reviews (2026-08-20, v0.17.0).** `hostit control app`
  (singular, plural aliased); `hostit node status`, `hostit proxy status`,
  `hostit proxy route list`, each answering from the daemon's OWN state over a
  new root-only socket (works exactly when control is down); lipgloss tables;
  bash+zsh completion for all binaries. Full security/style/architecture
  review, no HIGH findings: `plans/260820-hostit-review-findings.md`.

- **The node owns the app socket + the terminal runs on the app's node
  (2026-08-20, v0.17.0).** An app on a node-only host had no daemon socket at
  all: no SSH, no in-container CLI, no MCP bridge. hostit-node now serves
  /run/hostit/hostit.sock on every host and relays to control over the cluster
  link, never answering locally, so control keeps every guard. The browser
  terminal's pty moved to the app's node over the same link.

- **The binary split (2026-08-20, v0.17.0).** hostit-app is the in-container
  command set (mounted in as /usr/bin/hostit), hostit is the operator's front
  door, and the app commands live on hostit-control. The lesson that cost an
  hour on stage: the mount SOURCE is the host path and the exec is the
  CONTAINER path; a test now pins it.

- **btrfs simple quotas (2026-08-16, live everywhere).** Classic qgroups NEVER
  enforced (seeding from the shared base marks quota state inconsistent;
  verified 300MB written past a 200MB cap). squota mode enforced from startup,
  with automatic migration; rescans gone wholesale -- which also fixed the slow
  snapshots above.
