---
theme: seriph
title: hostit multi-node -- one front door, many machines
info: |
  The multi-node design: splitting the all-in-one daemon into four services --
  hostit-control, hostit-node, hostit-proxy, and the in-container hostit-agent. Responsibilities, database structure, the
  key flows as sequence diagrams, and the internal RPC and its auth model.
  Companion to plans/260807-hostit-multinode.md and 260815-hostit-nodeagent.md.
layout: cover
background: https://cover.sli.dev
class: text-center
transition: slide-left
mdc: true
---

# hostit multi-node

### One front door, many machines

<div class="mt-8 opacity-60">
Four services -- control, node, proxy, and the in-container agent --
colocatable on one host or split across machines
</div>

<div class="abs-br m-6 text-sm opacity-40">
design: <code>plans/260807-hostit-multinode.md</code> &middot; phase 1: <code>plans/260815-hostit-nodeagent.md</code>
</div>

<style>
h1 {
  background-color: #10b981;
  background-image: linear-gradient(45deg, #34d399 20%, #0e7490 80%);
  background-size: 100%;
  -webkit-background-clip: text;
  -webkit-text-fill-color: transparent;
}
</style>

---

# The problem

Today one root daemon on one droplet **is** the platform: TLS, proxy, dashboard,
API, assistant -- and every Unix user, container, port and subvolume apps are
made of.

<v-clicks>

- One machine's CPU, RAM, disk and port range is the **ceiling for every app**
- A busy app and a noisy neighbor share one core
- The goal is capacity, **not** HA: no failover, no live migration, registry stays single-writer

</v-clicks>

<v-click>

<div class="mt-6 p-4 border border-emerald-700 rounded">
<b>Non-negotiable:</b> hostit stays deployable on <b>one server</b> (a $6
droplet): all four services colocated, zero extra configuration.
Localhost-network hops between them are fine -- the one-server story is the
requirement, not zero hops.
</div>

</v-click>

---

# Four binaries, four services

One host (the default) or split out. `hostit-node` and `hostit-proxy` **dial control**.

<div class="grid grid-cols-2 gap-3 mt-2 text-xs">

<div class="p-2 border border-emerald-700 rounded">

#### `hostit-control` -- control plane

Serves the **web app + REST API**, **owns the SQLite database** (apps, users,
tokens, snapshot metadata, domains, nodes), name validation and limits,
placement, cert management, the assistant. Accepts node/proxy connections.

</div>

<div class="p-2 border border-sky-700 rounded">

#### `hostit-node` -- the machine half

Dials control; executes every node-local operation on its own machine: Unix
users + homes, containers + units, btrfs subvolumes/snapshots/quotas, port +
uid allocation, nft rules, `os.OpenRoot` files, self-maintenance loops.
**Stateless** about the platform.

</div>

<div class="p-2 border border-amber-700 rounded">

#### `hostit-proxy` -- dumb data plane

Dials control, caches the **routing table** (`<app>.<base>` &rarr;
`<node-ip>:port`, dashboard &rarr; control) and cert material, and serves from
the cache -- **apps keep serving while control or a node daemon restarts**.
N proxies on different hosts, or one colocated with control (most likely).

</div>

<div class="p-2 border border-violet-700 rounded">

#### `hostit-agent` -- inside the container

Unchanged meaning: the PID-1 binary in every app container -- runs the app
process, leaves the state breadcrumb, handles start/stop/restart signals.

</div>

</div>

---

# Why the node half cannot just be "remoted"

The tempting shortcut -- keep `app.Manager` on the control plane, SSH the commands over --
throws away the safety model.

<v-clicks>

- **`os.OpenRoot` needs the real path on the real machine.** Its guarantee is the
  kernel refusing to follow a symlink out of the opened root. A "remote open" is
  just a string the control plane hopes the node honored.
- **btrfs is local syscalls.** A snapshot is a reflink on one filesystem; a quota
  is an `EDQUOT` at write time. There is nothing to call remotely -- the operation
  *is* the filesystem.
- **podman runs where the container is.** exec, stats, idmapped rootfs mounts,
  netns -- plus the locking and caching the Manager does in-process.

</v-clicks>

<v-click>

So the **whole node-local half moves** behind one interface -- `NodeAgent` --
with a `localNodeAgent` (today's in-process code) and a `remoteNodeAgent`
(RPC client to a `hostit-agent`).

</v-click>

---

# Target architecture

```mermaid {scale: 0.45}
flowchart TB
    browser[Browser / agent / ssh]

    subgraph edge["Data plane"]
        proxy["hostit-proxy<br/><i>cached routes + certs</i>"]
    end

    subgraph ctrl["Control plane host"]
        control["hostit-control<br/><i>web app, API, placement</i>"]
        store[("SQLite registry")]
    end

    subgraph nodeA["Hosting node A"]
        nodeDA["hostit-node"]
        a1["container: hostit-agent<br/>app blog, port 10000"]
    end

    subgraph nodeB["Hosting node B"]
        nodeDB["hostit-node"]
        a2["container: hostit-agent<br/>app stats, port 10000"]
    end

    browser -->|"HTTPS :443"| proxy
    proxy -->|"dashboard/API"| control
    proxy -->|"nodeA:10000"| a1
    proxy -->|"nodeB:10000"| a2
    control --> store
    nodeDA -. "dials control<br/>mTLS cert" .-> control
    nodeDB -. "dials control" .-> control
    proxy -. "dials control<br/>routes + certs" .-> control
    nodeDA --> a1
    nodeDB --> a2

    style control fill:#047857,color:#fff
    style store fill:#1f252d,color:#fff
    style proxy fill:#b45309,color:#fff
    style nodeDA fill:#0369a1,color:#fff
    style nodeDB fill:#0369a1,color:#fff
```

Dashed = dialed control connections (per-node mTLS certs). App traffic
never traverses control -- the proxy serves node targets from its **local
cache**, so control and node daemons restart without dropping a request.

---

# Database structure

The registry stays central and single-writer on `hostit-control`. One new table, two
app columns.

```mermaid {scale: 0.34}
erDiagram
    node ||--o{ app : hosts
    node {
        text id PK "e.g. node-b"
        text address "internal RPC + proxy target"
        int  capacity_apps "soft cap for placement"
        int  free_mem_mb "from last heartbeat"
        int  free_disk_mb "from last heartbeat"
        int  app_count "from last heartbeat"
        bool btrfs_capable "snapshots and quotas available"
        text version "node build, for upgrade ordering"
        text health "up, degraded, down"
        text last_seen "heartbeat timestamp"
    }
    app {
        text name PK
        text host FK "node id; local means in-process"
        int  port "allocated by the hosting node"
        int  uid "allocated by the hosting node"
    }
```

---

# Consistency model

<v-clicks>

- **Nodes own local resources; the registry records the outcome.** Only the node
  can see its own free ports and uid blocks, so `Provision` allocates and
  *returns* them; control writes `(host, port, uid)` into the app row.
- **Snapshot subvolumes are node-local, metadata is central**: the id/label rows
  live in the `snapshot` table; retention policy runs on control and drives
  `SnapshotDelete` on the node.
- **Reconciliation over consensus.** Control restart: reload the `node` table,
  resume heartbeats -- containers never stopped serving (and the proxy keeps
  routing from its cache). Node restart: `hostit-node`
  comes up stateless, runs its reconcile loops, and desired state is re-asserted
  lazily by the next `Ensure`/`Deploy`.
- **Drift surfaces, then heals**: `States()` feeds control's state cache, so a
  mismatch shows on the dashboard and is corrected by the next lifecycle call --
  no distributed protocol.

</v-clicks>

---

# Flow: creating an app

Placement picks a node; the node allocates and builds; control records what
came back. The node is stateless -- the registry is the only durable record.

```mermaid {scale: 0.36}
sequenceDiagram
    participant U as Browser / agent
    participant P as hostit-control
    participant S as SQLite registry
    participant N as hostit-node (node B)

    U->>P: POST /api/apps {"name":"blog"}
    P->>P: validate name, check owner's app limit
    P->>S: read node heartbeats
    P->>P: placement picks least-loaded node
    P->>N: Provision(spec: name, owner, keys, mem, disk)
    N->>N: allocate local port + uid block
    N->>N: useradd, subvolume, keys, scaffold, port rule
    N->>N: podman create + start (background)
    N-->>P: {port: 10000, uid: 1000000}
    P->>S: AddApp host=node-b port=10000 uid=1000000
    P->>S: mint app-scoped token
    P-->>U: 201 {url, ssh, agent_token}
```

---

# Flow: serving an HTTP request

The proxy serves from its locally cached routing table -- no control-plane
round trip on the hot path, and no interruption while control restarts.

```mermaid {scale: 0.45}
sequenceDiagram
    participant V as Visitor
    participant P as hostit-proxy (:443)
    participant S as SQLite registry
    participant A as App container (node B)

    V->>P: GET https://blog.apps.example.com/
    P->>P: TLS (cert material synced from control)
    P->>P: cached route: blog -> node-b:10000
    alt app exists and its node is up
        P->>A: proxy to nodeB.addr:10000
        A-->>V: the app's response
    else unknown app, node down
        P-->>V: 404 "There is nothing here"
    end
```

---

# Flow: SSH via one front door

One SSH endpoint: the front door host (where the proxy lives). The login shell
asks control which node hosts the app, then jumps.
For a `local` app there is no jump -- `podman exec` in-process, exactly today.

```mermaid {scale: 0.38}
sequenceDiagram
    participant U as User
    participant PD as sshd on the front door
    participant SH as hostit-shell
    participant P as hostit-control
    participant ND as sshd on node B
    participant C as Container on node B

    U->>PD: ssh blog@apps.example.com
    PD->>PD: authorized_keys check
    PD->>SH: exec login shell
    SH->>P: which node hosts blog?
    P-->>SH: node B at nodeB.addr
    SH->>ND: ProxyJump (infrastructure credential)
    ND->>C: podman exec into hostit-app-blog
    C-->>U: shell inside the app's container
```

---

# SSH credentials: two separate trust edges

<v-clicks>

- The **tenant's keys never move**: the managed `authorized_keys` lives where the
  app user lives -- on the node, written by `SetKeys`. That authenticates the end
  user, same as today.
- The **front door needs its own hop credential** to reach each node's
  container-exec path: a key trusted by each node's sshd (or the same mTLS
  identity as the RPC channel). The user never sees it.
- This credential is a **new trust edge**: scoped to container exec (never a node
  root shell), rotatable, and guarded like the node certificates. Compromising the front door
  was already game-over on one box; multi-node widens that blast radius to every
  node's containers.

</v-clicks>

---

# Flow: file write + deploy across the wire

Control is a pass-through; the bytes land through `os.OpenRoot` **on the
node**, where that guarantee actually exists.

```mermaid {scale: 0.36}
sequenceDiagram
    participant AG as Agent / assistant
    participant P as hostit-control (REST API)
    participant N as hostit-node (node B)
    participant H as App home on node B
    participant C as Container agent (PID 1)

    AG->>P: PUT /api/apps/blog/files/bin/server?mode=755
    P->>P: look up host for blog (node B)
    P->>N: WriteFile(blog, bin/server, stream, 0755)
    N->>H: os.OpenRoot(home), stream, chown, rename
    N-->>P: ok
    AG->>P: POST /api/apps/blog/deploy
    P->>N: Deploy(blog)
    alt only run: changed
        N->>C: SIGHUP, restart run command
    else container args changed
        N->>N: recreate container, start
    end
    N-->>P: deployed
```

The assistant's `AppOps` keeps its exact shape -- only the adapter behind it
resolves the app's node first.

---

# Flow: snapshot -- node cuts, control records

```mermaid {scale: 0.34}
sequenceDiagram
    participant U as Owner / assistant
    participant P as hostit-control
    participant N as hostit-node (node B)
    participant FS as btrfs on node B
    participant S as SQLite registry

    U->>P: POST /api/apps/blog/snapshot {"label":"before upgrade"}
    P->>N: SnapshotCreate(blog, label, auto=false)
    N->>N: snapshot.pre hook (abort on failure)
    N->>FS: read-only CoW subvolume
    N->>N: snapshot.post hook (best effort)
    N-->>P: {id, label, createdAt, auto}
    P->>S: AddSnapshot(...)
    P->>P: retention picks ids to prune
    loop each pruned id
        P->>N: SnapshotDelete(blog, id)
        N->>FS: delete subvolume
        P->>S: DeleteSnapshot(id)
    end
    P-->>U: snapshot created
```

---

# IPC: the NodeAgent RPC surface

~25 primitives -- the node-local half of `app.Manager` as verbs. The spec travels
on every call; the node keeps nothing between calls.

<div class="grid grid-cols-2 gap-4 mt-2 text-sm">

<div>

**Lifecycle**
`Provision(spec) -> {port, uid}` &middot; `Deprovision` &middot; `Fork` (always placed on the source node)

**Deploy & process control**
`Deploy` / `Up` &middot; `Ensure` &middot; `Down` &middot; `Start` / `Stop` / `Restart` &middot;
`PowerOn` / `PowerOff` / `Reboot` &middot; `Status` &middot; `Logs` &middot; `Exec` &middot; `TerminalCommand`

**Files** (each through `os.OpenRoot` on the node)
`ListFiles` &middot; `ReadFile` &middot; `WriteFile` (streamed) &middot; `DeleteFile` &middot; `FileExists` &middot; `ExtractTar`

</div>

<div>

**Limits & keys**
`SetDiskLimit` &middot; `SetMemoryLimit` &middot; `SetKeys`

**Snapshots**
`SnapshotCreate -> metadata` &middot; `SnapshotDelete` &middot; `Rollback`

**Node-level**
`Heartbeat -> {freeMem, freeDisk, appCount, btrfsCapable, version, health}` &middot;
`States(names)` (batch, feeds control's state cache)

**Deliberately NOT on the wire**
`Readme` / `Description` / `SetDescription` / `ListSnapshots` -- compositions of
`ReadFile`/`WriteFile` + the registry, assembled on control

</div>

</div>

---

# IPC: transport and auth

<v-clicks>

- **Dedicated internal RPC**: the `NodeAgent` interface over HTTP+JSON, streamed
  for file bodies and logs, on a **private interface / VPN only**.
- **Nodes and proxies dial control**, not the other way around: control accepts,
  a node needs no public listener, and commands multiplex over the node's own
  connection. The proxy's channel is a subscription: routing table + certs.
- **Not the public agent REST API, structurally.** The internal surface is a
  superset with the most privileged verbs on the platform -- `Provision` creates
  a Unix user, `SetKeys` rewrites `authorized_keys`, `Deprovision` deletes a
  home. A separate channel means tenants *physically cannot address it*, instead
  of being policy-fenced on a shared one.
- **Auth: per-node mTLS certificate** (CN = node id), minted by
  `hostit-control node add` and configured as plain files on the node
  (`node-cert-file` / `node-key-file` / `cluster-ca-cert-file`). Shipped as
  mTLS-only; there is no bearer-token fallback and no join protocol.
- **Why not SO_PEERCRED?** On one box the kernel vouches for the calling uid over
  the unix socket. There is no kernel in the middle of a TCP connection --
  cross-node identity must be a cryptographic credential.
- **Versioned contract**: `node.version` from the heartbeat; the proxy tolerates
  agents one version behind (roll agents first).

</v-clicks>

---

# Risks, called out

<v-clicks>

- **A down node takes its apps offline** -- capacity, not HA. Must be explicit in
  the dashboard. Optional later: "evacuate" from last snapshots (a data-loss
  window decision).
- **Forks always land on the source node** (reflink = local); a hot node can
  accumulate forks until move/rebalance exists. A cross-node move is a full
  copy, never implied instant.
- **Network partition**: control marks the node `down` and the proxies drop its
  routes, even though its containers are fine. Heartbeat thresholds must
  tolerate blips; RPC timeouts must never wedge a control goroutine.
- **Certs are managed only by control, terminated only by proxies** -- hosting
  nodes never hold private keys. The proxy tier is the ingress bottleneck.

</v-clicks>

---

# Phased rollout

<v-clicks>

- **Phase 1 -- the seam, no behavior change** *(work plan: `260815-hostit-nodeagent.md`)*:
  `uid` column + backfill; split create/delete into control-plane vs
  `provision`/`deprovision`; define `NodeAgent` + `localNodeAgent`; route every
  caller through a one-entry `{"local": ...}` map. Single box byte-identical.
- **Phase 2 -- the binary split, one host**: `hostit-control`, `hostit-node`,
  `hostit-proxy` as separate systemd services over a localhost socket; the
  proxy serves from its cached routes. Then **2b**: the `node` table,
  config-file mTLS certs, a second machine dials in; place by hand.
- **Phase 3 -- invisible multi-node**: heartbeats, least-loaded placement,
  SSH ProxyJump.
- **Phase 4 -- move & rebalance**: snapshot-ship-provision-flip; optional
  evacuate-on-failure.

</v-clicks>

<v-click>

<div class="mt-4 p-3 border border-emerald-700 rounded text-sm">
Four binaries, four systemd services: <code>hostit-control</code>,
<code>hostit-node</code>, <code>hostit-proxy</code> -- colocatable on one host
-- plus the unchanged in-container <code>hostit-agent</code>. New
<code>node</code> package for the dial-in RPC; <code>app</code> keeps
NodeAgent + the local implementation.
</div>

</v-click>
