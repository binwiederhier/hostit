# TODO

Things worth doing, with enough context to pick up cold. Not a backlog of
everything imaginable -- if it is not written down here it is not planned.
Shipped work lives in CHANGELOG.md and the git history, not here.

**Ordered by priority** (top of each tier first). The ordering is a judgement
call about what this platform needs next, given that it now runs real
internet-facing projects: security gaps that expose tenant data outrank
features, cheap well-specified work outranks expensive open-ended work, and a
decision that unblocks two other items is worth making before either is built.

## Now (next few sessions)

### Security review 2026-09-04: what is still open

A five-slice review (auth/authz, connections+secrets, isolation+node,
proxy+cluster+store, assistant+web) ran on 2026-09-04. What it found and FIXED is
in the v0.39.1 changelog; what follows is what it found and did NOT fix, worst
first. Exploitability is stated from the tenant's side, because that is the
threat this platform actually carries: **none of the items below are reachable by
an anonymous visitor, and the two HIGHs need host-level access already.**

- **Cluster member identity is self-reported (HIGH, host-level).** On the
  same-host member socket, a uid-allowlisted peer sends `X-Hostit-Node` and
  `X-Hostit-Role` verbatim (`cluster/dial.go:peerFrom`), and the allowlist
  includes the *proxy* user (`cmd/control/servenode.go:TrustPeerUID`). Anything
  running as `hostit-proxy` -- the internet-facing TLS terminator, so the first
  thing an RCE in the data plane yields -- can register as the node and is handed
  `AppRelayHandler`, i.e. the `/v1` app surface for every app that node hosts,
  including `GET /v1/connections/{slug}/token`. Two independent reviewers found
  this. NOT tenant-reachable (a tenant is confined to their container's uid), but
  it turns one compromise into all of them. Fix shape: bind the claimable id and
  role to the uid that registered, so the proxy uid can only ever be its own
  proxy. This is the "F5 role-header" item, with a bigger blast radius than the
  note suggested.
- **`node remove` / `proxy remove` are not revocation (HIGH, holds a cert).**
  Connect-time authorize is `return true` for both roles AND the connect handler
  re-creates the row it was meant to have lost (`EnsureNode`/`EnsureProxy`), so a
  removed member that redials silently re-admits itself. Removal also only drops
  the in-memory agent; the session stays open, so a removed proxy keeps calling
  `/v1/cert` and receiving certificate chains **and private keys** indefinitely.
  There is no CRL, so re-minting the cluster CA is currently the only real
  revocation. The comments at `store/node.go:211` and `store/proxy.go:307` assert
  a control that does not exist and should be corrected with the fix.
- **The sandbox tool blocklist may be incomplete (HIGH, tenant-reachable if so).**
  `node/sandbox/service.go:disallowedBuiltins` claims to be "the COMPLETE set" of
  Claude Code built-ins for the pinned version, and the operator's Claude Max
  token is only out of reach if that is true. Checked against 2.1.259 (33 patches
  newer than the pinned 2.1.226) about twenty names are missing, some of them in
  that build's auto-approved set. Unconfirmed for 2.1.226 -- but the guard test
  compares the constant to a hardcoded copy of itself, so it is structurally
  incapable of catching a missing tool. Re-derive against the pinned version with
  the documented `jq '.tools'` procedure, then stop depending on a blocklist:
  assert the init event's advertised tool list at runtime and abort the turn if
  anything outside `mcp__hostit__*` appears.
- **Tool output is not framed as untrusted (HIGH, tenant/visitor-reachable).**
  `read_logs` returns text any internet visitor can put in an app's log, MCP
  results return third-party data, and both arrive as bare `tool_result` blocks
  with no provenance marker and nothing in the system prompt saying tool output
  is data rather than instruction. The model holds `write_file`, `run_command`
  (with outbound internet) and the app's granted connections, and the system
  prompt helpfully documents the exact curl that fetches a connection token.
  Confined to one app and its grants -- but that is a large radius for "debug my
  500". Cheapest first step: an explicit untrusted-content envelope around
  `read_logs` and MCP results, plus a paragraph in the system prompt.
- **`RotateKey` does not re-seal provider client secrets (MEDIUM, operator).**
  It walks `AllConnections()` only; `provider.client_secret` is sealed with the
  same key and is left behind. After a rotation every DB-defined OAuth provider
  silently stops refreshing and drops out of the menu, *and* the leaked key the
  rotation was for still decrypts them. Rotation must cover both tables.
- **Deleting a user orphans their connections (MEDIUM, retention).**
  `RemoveUser` cascades tokens, keys, collaborator and viewer rows but not
  `connection` / `provider` / `pending_viewer`, and `refreshDue` walks
  `AllConnections()` with no owner filter -- so a deleted person's Google or Slack
  refresh token is re-exchanged every ten minutes forever. The credential is no
  longer *reachable* (grants are owner-scoped since v0.39.1), so this is
  retention and upstream-liveness, not theft.
- **A stale routing table can outlive its revocations (MEDIUM, operator).** The
  proxy discards any table whose `Seq` is lower than the one it cached, and Seq
  comes from the control database. Restore control from a backup and every
  subsequent push is dropped while the proxy keeps serving the old table --
  including its private-app access sets, so removals never reach the data plane.
  Consider a control-instance epoch, or accepting a differing table after N
  ignored pushes.
- **Smaller, all tenant-reachable, none of them crossing a tenant boundary:**
  custom-domain squatting (a tenant can claim a domain they do not own and block
  the rightful one -- denial only, activation still needs ACME);
  `POST /auth/logout` is registered outside `authenticated()` so it skips the
  same-origin check (nuisance CSRF, clears a session); a collaborator can mint an
  app-scoped token for an app they do not own (no escalation found -- the two
  tokens have identical reach -- but it contradicts the invariant stated at
  `server_handler_apps.go:215`); assistant markdown is rendered with DOMPurify's
  defaults, which keep `style` and form controls, so model output can paint a
  convincing overlay inside the dashboard origin (CSP blocks the exfil paths, so
  it is phishing-shaped).
- **Two that are only safe by accident, worth hardening before they bite:** the
  app-socket relay forwards tenant-controlled paths verbatim and is saved from
  `../` traversal into control's `/callback/` handlers only because a POST body
  cannot be rewound for the 307 (`node/appsocket.go:relay`); and grants are bound
  to an app NAME rather than its id, while a rename frees the name immediately
  and grants live 12h (`proxy/grant.go`).

### Connections: shipped -- remaining follow-ups

Shipped to prod (connections + MCP servers), twenty-three providers. Live OAuth
clients: GitHub, Discord, Slack (both `slack-bot` and `slack-user`), plus
Google's two on the login client. GitHub and Linear refresh per connection since
v0.29.0 (hybrid tokens). MCP servers are added by URL (see
`docs/features/mcp-servers.md`). `plans/260819-connections.md` is the original
design, superseded in places.

Still open:

- **`/.well-known/oauth-client` must be publicly reachable** wherever MCP is used:
  an authorization server fetches it to identify hostit, so a deploy that hides it
  behind auth breaks every MCP consent. An e2e test now guards that it stays
  reachable and unauthenticated; still untested against a real third-party MCP
  server's authorization flow (only against the fakes in `mcp/` and
  `control/mcp_test.go`).

Known limits, deliberately accepted (see docs/features/connections.md):
the key sits beside the database so this protects a copied database and not root;
`meta` is not encrypted; granting an app a credential grants it to everyone who
can run code in that app, collaborators included; and the assistant is told not
to print tokens rather than prevented, with redaction as a backstop.

### Secrets that are not in the app's web root

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

### DECIDED: the credential-brokering shape (both, deliberately)

Resolved by building it. The two plans were not actually alternatives, and the
answer is that the RIGHT shape depends on what is on the other end:

- **Broker the credential** (`plans/260818-app-capabilities.md`) for a vendor with
  its own SDK -- Google, Slack, GitHub, an IMAP mailbox. hostit holds the refresh
  token and hands out a short-lived access token; the app uses the vendor's SDK
  and hostit grows no per-vendor API surface. This is `connections/`.
- **Be the client** (`plans/260818-hostit-broker-design.md`) for MCP. hostit holds
  the token and makes the calls, because an MCP token is not scoped to the grant:
  it opens the whole server, so handing it over makes the grant decorative. This
  is `mcp/` + `control/mcp.go`.

What the broker plan got right and the build kept: one implementation for all MCP
servers, per-owner custody, an OAuth client that costs nothing per server. What it
got wrong: it wanted a registry and a control->node credential push. Neither was
needed -- the app calls control's existing socket, and control already holds the
credential, so there is nothing to push anywhere.

What it wanted to defer (the OAuth half: registration, PKCE, refresh rotation)
turned out to be the cheap part, because MCP replaced dynamic client registration
with Client ID Metadata Documents. hostit serves one JSON document and registers
with nobody.

Left open by the decision: per-tool grants. A grant is whole-server today, with
the tools listed in the UI so the owner sees what they are agreeing to.

Also settled since: providers are no longer operator-only. A user can bring
their own OAuth client (see docs/features/connections.md, "Three tiers of
provider"), which was the last thing making the catalog feel closed.

### App capabilities: credentials an app uses but never holds

Unblocked now that the credential-brokering shape is decided (above). People want
to build apps that use AI. Putting an API key in the app's environment makes the
tenant pay, makes the key a thing that leaks into a repo or a log, and leaves
hostit with no idea what was spent.

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

## Soon (small, self-contained)

### MCP bridge: return images as image content

`read_file` returns text, so an image read through it is byte salad -- which is
why attached images ride the sandbox's stdin as blocks (the 2026-08-21 fix)
instead of being fetched by path. MCP has an image content type and Claude Code
renders image tool-results to real vision, so teaching the bridge to answer
image files with an image block would let the agent LOOK at any image already
in the app (screenshots in `public/`, logos), not just ones attached to the
current message. Composes with the stdin path, does not replace it: an attached
image should be unconditionally visible, not contingent on a tool call.
Touches appcli's mcp server and the sandbox's tool-result parsing.

### Log following

`GET /api/apps/{app}/logs?lines=N` is a snapshot. An agent watching a slow
start has to poll. SSE or a websocket tail would fix it; note the node relay
(control does not hold the logs) is the interesting part, and the terminal's
existing duplex stream over the cluster link is the precedent.

### An MCP server people can actually point an agent at

Check against the app-capability work first -- this is an agent calling IN where
the capability work is an app calling OUT, and they must not invent two auth
stories. The broker design (`plans/260818-hostit-broker-design.md`) expects it to
be cheap once the broker exists: the same "call an approved tool as owner X"
function, wrapped in a server adapter instead of an HTTP relay.

Note the direction is now asymmetric: hostit is a good MCP **client** (`mcp/`)
and still not a **server** anyone can point at from outside. The client
work does not do this for you, but it does settle the auth story -- if hostit
serves MCP over HTTP it should be the same specs it already speaks as a client
(RFC 9728 metadata, PKCE, resource indicators) rather than a bearer token stapled
on, and `mcp/discovery.go` is the description of what such a server has to
publish.

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

### Long jobs

`POST /api/apps/{app}/run` is bounded at five minutes, so a first `npm install`
on a small box can outlast it. Anything longer has to become a `prepare:` step,
which is fine but not obvious. A job id plus a poll/stream endpoint would be
the honest fix.

## Later (real, but not now)

### Move the screenshot and assistant containers to the nodes

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

### Review follow-ups (2026-08-20)

From `plans/260820-hostit-review-findings.md`, all LOW/accepted:

- Write down the control-is-a-SPOF-for-interactive-surfaces shape in an ops doc
  (apps keep serving through a control outage; SSH, the terminal, deploys and
  the dashboard do not).
- Keep raising the node package's unit coverage by extracting pure decision
  logic (14% at the review, ~25% now; the machine stack needs
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
is the fix if it ever matters. (v0.26.0 put a lock around the limit-EDIT pool
check, closing that race; the CREATE path is still check-then-act.)


### hostit-node hangs on stop -- REPRODUCE BEFORE CHASING

Seen once (2026-08-16); has not recurred through many deploys since, and the
shutdown path changed (the signal handler closes the live connection). If it
recurs: `systemctl stop hostit-node` plus a SIGQUIT goroutine dump, then fix
the shutdown ordering. Do not go hunting without a fresh reproduction.

## Deferred

Explored and deliberately not being done, kept for the reasoning rather than the
intent. Each says what was measured, so picking one up later starts from
evidence -- and so the same idea is not re-proposed and re-investigated in six
months.

- **Redirects (both shapes). BUILT TWICE 2026-08-22, BOTH REVERTED.** The code
  is gone from the tree on purpose; this entry is the whole return on that day.
  Recovery patch: `~/Code/plans/260822-redirect-rules.patch` (applies cleanly to
  v0.19.1). Design: `~/Code/plans/260822-redirect-rules.md`.

  **What was built, in order.** First the shape this list used to describe: a
  `Domain.RedirectTo` field -- empty routes to the app, set answers a 301.
  Column + migration, `redirect_to` / `redirect_to_primary` on
  `POST /api/apps/{app}/domains`, a `PUT .../redirect` to repoint one without
  losing its certificate, CLI flags, a checkbox in the domains form, and the 301
  answered by hostit-proxy from its own table (so it survives control being
  down). Shipped to stage, green, driven in a browser. Then, on the objection
  below, a second version: redirect RULES as their own object -- a path regex
  with capture groups, per app, `app_redirect` table, a `redirect` service
  package, CLI, both data planes. Also green, also proven on stage.

  **Why the field shape is wrong, which is the main thing to remember.** A
  redirect modelled as a property of a domain looks cheap and is not:

  - The app association is vestigial. `example.com -> www.example.com` has
    nothing to do with which app serves www. A redirect from a hostname moved
    off hostit entirely has no app at all and cannot be expressed.
  - It costs an "except aliases" exclusion in FOUR places (`ActiveDomains`,
    `firstActiveDomain`, `reloadDomains`, `appUrls` in the web). Every one is a
    place a later reader must remember the exception. That is the signature of
    one concept wearing another's clothes.
  - It cannot grow a path dimension, ever.

  **The motivation on this list was half wrong.** It claimed the feature would
  retire "yayagram's apex->www 301 and websrv's WordPress-URL redirects". It
  retires the first. It CANNOT retire the second: websrv's redirects are regex
  path rewrites with capture groups, category slug maps and per-post anchor
  tables (`~/Code/website`, `server/service.go:rewritePath`). Check the actual
  code before quoting a motivating example -- this claim survived into docs and
  a changelog before anyone read websrv.

  **What the rule shape proved, and it is worth keeping.**

  - Ownership is NOT the hard part, and the instinct that it might be is wrong.
    Proving control of `example.com` is exactly what a `Domain` already is (the
    `_acme-challenge` delegation is the proof, the certificate is the artifact).
    A rule never re-proves anything; it references a hostname whose ownership is
    already established. The two concerns were always separable.
  - Cross-tenant safety wants three independent defenses, and the third is what
    makes the other two cheap: (1) the pattern is matched against the request
    PATH only, so no pattern can name a hostname -- structural, not a check that
    can be forgotten; (2) creation-time refusal of a `host` the app does not
    answer for; (3) control re-derives ownership every time it builds the
    routing table and attaches a rule only to its own app's routes, so a stale
    row (lost domain, rename, hand-edited db) cannot reach anyone else.
  - Rules must travel WITH the route (`proxyapi.Route.Redirects`) rather than
    being looked up by hostname. That is what makes "evaluated only after the
    host matched" structural rather than a convention.
  - Control must compile its own rule cache OUT OF the table it is about to
    push, or the single-host gate and the proxy gate will drift on what a rule
    means.
  - Go `regexp` is RE2 (linear, no backtracking), so tenant-supplied patterns on
    the shared data plane are affordable in a way they would not be under PCRE.
    Auto-anchor patterns (`^(?:...)$`) or `/about` silently swallows a subtree.

  **What was still unbuilt when it was reverted**, and what a real version needs:
  a web UI, rule reordering (creation order was evaluation order), the decision
  on whether `RedirectTo` folds into a rule (it is exactly a host-only rule, so
  the migration is mechanical) or the two live side by side, and user-owned
  rather than app-owned rules so a redirect for a hostname with no app can exist
  at all.

  **Operational note.** Stage ran both versions, so its database recorded orphan
  migrations (an `app_domain.redirect_to` column and an `app_redirect` table) the
  released code never adds. Harmless -- `migrate` only ever appends, and the
  orphans are never referenced -- but a rebuild of this feature must not reuse
  those migration slots on stage.

  **If it comes back**, start from the rules shape and the plan doc; do not
  re-derive the field shape. The live pain point that started this is still
  real: professornoodle.com's bare apex has no native way to reach www, and is
  handled in yayagram's own handler today.

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
