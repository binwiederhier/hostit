---
theme: seriph
title: hostit multi-node -- one front door, many machines
info: |
  The multi-node design: splitting the all-in-one daemon into a proxy/control
  node and stateless hosting nodes. Responsibilities, database structure, the
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
Splitting the all-in-one daemon into a proxy node and stateless hosting nodes --
while a single box stays exactly what it is today
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
<b>Non-negotiable:</b> a one-machine install keeps <b>zero</b> network hops, zero
serialization, zero behavior change. The multi-node machinery is present but
inert until a second node joins. hostit must stay deployable on a $6 droplet.
</div>

</v-click>

---

# Responsibilities: two roles, one binary

Which role a process plays is a subcommand -- `hostit serve` vs `hostit agent`.

<div class="grid grid-cols-2 gap-6 mt-4">

<div class="p-4 border border-emerald-700 rounded">

### Proxy node (control plane)

- **TLS + certs**: wildcard and custom domains (certmagic); nodes never see keys
- **Reverse proxy**: host &rarr; app &rarr; node
- **Registry**: SQLite, single writer -- apps, users, tokens, snapshots metadata, domains, nodes
- **Web app + REST API**, name validation, app limits
- **Placement**: least-loaded eligible node
- **Assistant**: Anthropic loop, SSE, rate limits
- Owns **no** app homes, runs **no** app containers

</div>

<div class="p-4 border border-sky-700 rounded">

### Hosting node (`hostit-agent`)

- **Stateless**: no registry, knows nothing of other nodes
- Unix users + homes (useradd, managed authorized_keys)
- Containers + units (podman, systemd), workspace image
- btrfs subvolumes, snapshots, qgroup quotas
- Port + uid allocation **on its own machine**
- nftables port rules; `os.OpenRoot` file layer
- Self-maintenance loops: reconciles, state, disk usage, qgroup sweep, preview shots

</div>

</div>

---

# Why the node half cannot just be "remoted"

The tempting shortcut -- keep `app.Manager` on the proxy, SSH the commands over --
throws away the safety model.

<v-clicks>

- **`os.OpenRoot` needs the real path on the real machine.** Its guarantee is the
  kernel refusing to follow a symlink out of the opened root. A "remote open" is
  just a string the proxy hopes the node honored.
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

```mermaid {scale: 0.5}
flowchart TB
    browser[Browser / agent / ssh]

    subgraph proxy["Proxy node (control plane)"]
        daemon["hostit serve"]
        store[("SQLite registry")]
        localnode["localNodeAgent<br/><i>in-process</i>"]
    end

    subgraph nodeA["Hosting node A"]
        agentA["hostit-agent"]
        a1["hostit-app-blog<br/>port 10000"]
    end

    subgraph nodeB["Hosting node B"]
        agentB["hostit-agent"]
        a2["hostit-app-stats<br/>port 10000"]
    end

    browser -->|"HTTPS :443 / SSH :22"| daemon
    daemon --> store
    daemon -.->|in-process call| localnode
    daemon -->|"NodeAgent RPC<br/>HTTP+JSON + node token"| agentA
    daemon -->|"NodeAgent RPC"| agentB
    daemon -->|"proxy to nodeB:10000"| a2
    agentA --> a1
    agentB --> a2

    style daemon fill:#047857,color:#fff
    style store fill:#1f252d,color:#fff
    style localnode fill:#065f46,color:#fff
    style agentA fill:#0369a1,color:#fff
    style agentB fill:#0369a1,color:#fff
```

Dashed = in-process Go call. On a single box only the proxy subgraph exists and
there are **no RPC lines at all**: `host == "local"` resolves to the in-process
agent, today's exact code path.

---

# Database structure

The registry stays central and single-writer on the proxy. One new table, two
app columns.

```mermaid {scale: 0.34}
erDiagram
    node ||--o{ app : hosts
    node {
        text id PK "e.g. node-b"
        text address "internal RPC + proxy target"
        text join_token "hashed; presented on every RPC"
        int  capacity_apps "soft cap for placement"
        int  free_mem_mb "from last heartbeat"
        int  free_disk_mb "from last heartbeat"
        int  app_count "from last heartbeat"
        bool btrfs_capable "snapshots and quotas available"
        text version "agent build, for upgrade ordering"
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
  *returns* them; the proxy writes `(host, port, uid)` into the app row.
- **Snapshot subvolumes are node-local, metadata is central**: the id/label rows
  live in the `snapshot` table; retention policy runs on the proxy and drives
  `SnapshotDelete` on the node.
- **Reconciliation over consensus.** Proxy restart: reload the `node` table,
  resume heartbeats -- containers never stopped serving. Node restart: the agent
  comes up stateless, runs its reconcile loops, and desired state is re-asserted
  lazily by the next `Ensure`/`Deploy`.
- **Drift surfaces, then heals**: `States()` feeds the proxy's state cache, so a
  mismatch shows on the dashboard and is corrected by the next lifecycle call --
  no distributed protocol.

</v-clicks>

---

# Flow: creating an app

Placement picks a node; the node allocates and builds; the proxy records what
came back. The node is stateless -- the registry is the only durable record.

```mermaid {scale: 0.36}
sequenceDiagram
    participant U as Browser / agent
    participant P as Proxy
    participant S as SQLite registry
    participant N as hostit-agent (node B)

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

The only change from today's `proxyTo`: the target `127.0.0.1:port` becomes
`node.address:port`. No 502 path -- a down node looks like a free name, as today.

```mermaid {scale: 0.45}
sequenceDiagram
    participant V as Visitor
    participant P as Proxy (:443)
    participant S as SQLite registry
    participant A as App container (node B)

    V->>P: GET https://blog.apps.example.com/
    P->>P: TLS (wildcard cert)
    P->>S: resolve host: app blog, host=node-b, port=10000
    alt app exists and its node is up
        P->>A: proxy to nodeB.addr:10000
        A-->>V: the app's response
    else unknown app, node down
        P-->>V: 404 "There is nothing here"
    end
```

---

# Flow: SSH via one front door

One SSH endpoint (the proxy). The login shell looks up the node and jumps.
For a `local` app there is no jump -- `podman exec` in-process, exactly today.

```mermaid {scale: 0.38}
sequenceDiagram
    participant U as User
    participant PD as sshd on the proxy
    participant SH as hostit-shell (proxy)
    participant P as Proxy control plane
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
- The **proxy needs its own hop credential** to reach each node's container-exec
  path: a proxy-held key trusted by each node's sshd (or the RPC's mTLS
  identity). The user never sees it.
- This credential is a **new trust edge**: scoped to container exec (never a node
  root shell), rotatable, and guarded like the RPC token. Compromising the proxy
  was already game-over on one box; multi-node widens that blast radius to every
  node's containers.

</v-clicks>

---

# Flow: file write + deploy across the wire

The proxy is a pass-through; the bytes land through `os.OpenRoot` **on the
node**, where that guarantee actually exists.

```mermaid {scale: 0.36}
sequenceDiagram
    participant AG as Agent / assistant
    participant P as Proxy (REST API)
    participant N as hostit-agent (node B)
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

# Flow: snapshot -- node cuts, proxy records

```mermaid {scale: 0.34}
sequenceDiagram
    participant U as Owner / assistant
    participant P as Proxy
    participant N as hostit-agent (node B)
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
on every call; the agent keeps nothing between calls.

<div class="grid grid-cols-2 gap-4 mt-2 text-sm">

<div>

**Lifecycle**
`Provision(spec) -> {port, uid}` &middot; `Deprovision` &middot; `Fork` (same-node reflink)

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
`States(names)` (batch, feeds the proxy's state cache)

**Deliberately NOT on the wire**
`Readme` / `Description` / `SetDescription` / `ListSnapshots` -- compositions of
`ReadFile`/`WriteFile` + the registry, assembled on the proxy

</div>

</div>

---

# IPC: transport and auth

<v-clicks>

- **Dedicated internal RPC**: the `NodeAgent` interface over HTTP+JSON, streamed
  for file bodies and logs. Listens on a **private interface / VPN only**.
- **Not the public agent REST API, structurally.** The internal surface is a
  superset with the most privileged verbs on the platform -- `Provision` creates
  a Unix user, `SetKeys` rewrites `authorized_keys`, `Deprovision` deletes a
  home. A separate channel means tenants *physically cannot address it*, instead
  of being policy-fenced on a shared one.
- **Auth: per-node bearer token**, minted at join time, stored hashed in the
  `node` table, presented on every RPC. **mTLS** as the stronger option.
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
- **Cross-node fork/move is a copy**, not a reflink -- send/receive or tar. The
  UI must not imply it is instant.
- **Network partition**: proxy marks the node `down` and stops routing even
  though its containers are fine. Heartbeat thresholds must tolerate blips;
  RPC timeouts must never wedge a proxy goroutine.
- **TLS stays only on the proxy** -- deliberate (nodes never hold private keys),
  but it is the single ingress bottleneck.

</v-clicks>

---

# Phased rollout

<v-clicks>

- **Phase 1 -- the seam, no behavior change** *(work plan: `260815-hostit-nodeagent.md`)*:
  `uid` column + backfill; split create/delete into control-plane vs
  `provision`/`deprovision`; define `NodeAgent` + `localNodeAgent`; route every
  caller through a one-entry `{"local": ...}` map. Single box byte-identical.
- **Phase 2 -- the wire**: `remoteNodeAgent` (HTTP+JSON), the `hostit-agent`
  subcommand wrapping a `localNodeAgent`, the `node` table, `hostit node add`,
  token auth. Join one node, place by hand.
- **Phase 3 -- invisible multi-node**: heartbeats, least-loaded placement,
  SSH ProxyJump.
- **Phase 4 -- move & rebalance**: snapshot-ship-provision-flip; optional
  evacuate-on-failure.

</v-clicks>

<v-click>

<div class="mt-4 p-3 border border-emerald-700 rounded text-sm">
New code lives per the existing layout: <code>app</code> (NodeAgent + local),
new <code>node</code> package (RPC client + server), <code>server</code>
(routing, placement, jump), <code>store</code> (node table), <code>cmd</code>
(<code>hostit agent</code>, <code>hostit node add/list/remove</code>). Same
binary, role by subcommand.
</div>

</v-click>
