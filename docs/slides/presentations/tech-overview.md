---
theme: seriph
title: hostit -- a code overview
info: |
  A code overview of hostit: the package structure and the flows that matter, for a
  developer reading the codebase for the first time. Built from docs/.
layout: cover
background: https://cover.sli.dev
class: text-center
transition: slide-left
mdc: true
---

# hostit

### A code overview

<div class="mt-8 opacity-60">
The package structure, and the flows that matter, for reading the codebase
</div>

<div class="abs-br m-6 text-sm opacity-40">
heckel.io/hostit &middot; source of truth: <code>docs/</code>
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
transition: fade-out
---

# How to read this deck

hostit is a small self-hosted platform: each app gets its own container, a subdomain
with HTTPS, SSH, and a REST API an agent can drive. This deck is not the product tour
-- it is a map of the **code**.

- **The shape** -- one binary, a thin `main.go`, service packages at the root
- **The packages** -- what each one owns, and how `app.Manager` composes them
- **The seam** -- the injected service interfaces + `run.Runner`, why it tests without root
- **The flows** -- create, deploy, serve, supervise, snapshot, assist

<div class="mt-8 text-sm opacity-60">
It was written with AI, but it is laid out like a hand-written Go codebase (the ntfy
convention): every package has one job, and every job has a package.
</div>

---

# The shape: one binary, many roles

The same binary is the daemon, the CLI, and the in-container agent. A thin `main.go`
hands off to `cmd`; there is no `internal/`.

```mermaid
flowchart LR
  main["main.go<br/><i>~10 lines</i>"] --> cmd
  cmd -->|"serve"| server["server (daemon)<br/>proxy + REST + socket"]
  cmd -->|"apps / admin"| client["client<br/>REST calls"]
  cmd -->|"agent (hidden)"| agent["agent<br/>PID 1 in a container"]
  cmd -->|"deploy / static (hidden)"| appctl["appctl<br/>app-side, over the socket"]
  server --> app["app.Manager<br/>the lifecycle"]
```

<div class="mt-4 text-sm opacity-60">
Which subcommands matter depends on where the binary runs: on the host it is the
daemon; inside a container it is PID 1 and the app-side CLI. See <code>cmd/cmd.go</code>.
</div>

---
zoom: 0.78
---

# Package map: the spine

The request path and everything durable. Start here when reading top-down.

| Package | Owns |
|---|---|
| `server` | HTTP: TLS-terminating proxy, REST API, app-scoped agent API, OAuth, sessions, the peercred unix socket, terminal WebSocket, assistant SSE |
| `app` | an app's whole lifecycle; composes the service packages and holds naming, ports, identity |
| `store` | SQLite: schema, migrations, queries (one file per entity) |
| `user` | people: accounts, roles, limits, tokens, SSH keys, allowed domains |
| `config` | server config (`/etc/hostit/server.yml`) and its defaults |
| `cmd` | the CLI: `serve`, the app commands, `admin`, the hidden `agent`/`enter`/`shell` group, the startup preflight |
| `client` | Go client for the REST API, used by `hostit apps` |
| `web` | React 19 + Vite SPA; built into `server/site/` and embedded |

---
zoom: 0.78
---

# Package map: the host-tool services

Each wraps exactly one host tool. `app.Manager` calls them; none call each other.

<div class="grid grid-cols-2 gap-8 mt-2 text-sm">
<div>

| Package | Wraps |
|---|---|
| `btrfs` | subvolumes, RO snapshots, reflink, qgroup budgets |
| `container` | podman: create, exec, remove, image build, rootfs export |
| `systemd` | per-app unit lifecycle |
| `unixuser` | Unix user + home + skeleton |
| `ssh` | an app's `authorized_keys` block |

</div>
<div>

| Package | Wraps / owns |
|---|---|
| `firewall` | nftables per-app loopback rules |
| `run` | the shared `Runner` that shells out |
| `retention` | pure GFS snapshot policy (no I/O) |
| `snapshot` | orchestrates whole-app snapshot + rollback |
| `workspace` | container spec (`--rootfs` + uid map) and its storage: image build (input), per-tag bases, one subvolume per app (files at `home/app` inside) |
| `agent` &middot; `appctl` &middot; `assistant` | in-container supervisor &middot; the `hostit.yml` contract + app-side client &middot; the in-browser AI agent |

</div>
</div>

<div class="mt-4 text-sm opacity-60">
Full table with intent: <code>architecture/code-map.md</code>.
</div>

---
zoom: 0.9
---

# How `app.Manager` composes the services

`app.Manager` decides *what* an app needs and delegates the *how* to the service that
owns each host tool (`control/manager.go:NewManager`).

```mermaid {scale: 0.72}
flowchart TB
    mgr["app.Manager"]
    mgr --> shell
    mgr --> direct
    subgraph shell["shell out via run.Runner"]
      direction LR
      btrfs["btrfs"] ~~~ systemd["systemd"] ~~~ container["container"]
    end
    subgraph direct["touch the host directly (root)"]
      direction LR
      user["unixuser"] ~~~ ssh["ssh"] ~~~ firewall["firewall"]
    end
    style mgr fill:#047857,color:#fff
```

<div class="mt-2 text-sm opacity-60">
Each host tool sits behind its own interface, bundled in <code>app.Services</code>. The
daemon wires the real impls (<code>NewSystemServices</code>); tests pass
<code>apptest.NewNopServices</code>. btrfs/systemd/container shell out via the runner;
unixuser/ssh/firewall touch the host directly and need root.
</div>

---

# The seam: why it all tests without root

Every host mutation goes through one of two injected interfaces, so the Manager runs
in a normal `go test` with no root, no podman, no btrfs.

<div class="grid grid-cols-2 gap-6 mt-6">
<div class="p-4 rounded border border-gray-400 border-opacity-30">

**`run.Runner`**

The single choke point for shelling out. Tests pass a fake runner that returns
canned output and records the exact argv, so "did we call `btrfs subvolume create`?"
is an assertion, not a side effect.

</div>
<div class="p-4 rounded border border-gray-400 border-opacity-30">

**The service interfaces**

`useradd`, `nftables`, `authorized_keys`, btrfs, systemd, podman -- each host tool
behind its own interface, bundled in `app.Services`. Tests inject
`apptest.NewNopServices`. Same seam a future control-plane / app-node split would remote.

</div>
</div>

<div class="mt-6 text-sm opacity-60">
See <code>subsystems/seams-and-testing.md</code>. This is why almost every package has a
colocated <code>_test.go</code> and the suite runs anywhere.
</div>

---

# An app is four host resources, keyed by id

```mermaid
flowchart LR
  A["app<br/><i>opaque id</i>"] --> U["Unix user<br/>unixuser"]
  A --> S["THE app subvolume<br/>apps/&lt;id&gt; (btrfs, persistent)<br/>files at home/app inside"]
  A --> C["podman container<br/>runs --rootfs, root mapped to the uid"]
  A --> P["loopback port<br/>nftables pins it to the uid"]
```

Durable resources key on a stable opaque **id**, never the name. So a rename is
`usermod -l` plus one DB update: no data move, no container recreate.

<div class="mt-4 text-sm opacity-60">
The id-vs-name split is the single most load-bearing decision in the code. See
<code>subsystems/app-identity.md</code>.
</div>

---

# The SQLite schema: everything hangs off the app id

```mermaid {scale: 0.5}
erDiagram
    user {
        text id PK
        text email
    }
    app {
        text id PK "opaque, stable"
        text name "renamable"
        text owner_id FK
        int port
    }
    user ||--o{ app : owns
    user ||--o{ user_key : "SSH keys"
    app ||--o{ token : "app-scoped API tokens"
    app ||--o{ snapshot : "btrfs snapshots"
    app ||--o{ app_domain : "custom domains"
    app ||--o{ app_event : "audit log"
    app ||--|| app_usage : "assistant token spend"
    app ||--o| app_assistant : "assistant prefs"
    app ||--o| assistant_session : "chat history"
```

<div class="mt-2 text-sm opacity-60">
One `store.Store` (<code>store/</code>) wraps it all. Child rows key on
<code>app_id</code>, not name, so a rename is a single row update. Schema lives in
<code>store/migrate.go</code> (forward-only numbered migrations).
</div>

---
layout: section
transition: slide-up
---

# The flows

From here on: what actually happens when you create, deploy, serve, supervise,
snapshot and assist an app.

---

# Flow: creating an app

```mermaid {scale: 0.6}
sequenceDiagram
    actor User
    participant S as server
    participant M as app.Manager
    participant Sys as unixuser (root)
    User->>S: POST /api/apps {name}
    S->>M: CreateApp
    M->>M: allocate port, derive uid block, mint id
    M->>M: app subvolume: snapshot the image tag's base (root-owned, idmap-mounted)
    M->>Sys: create Unix user + group (home = the id-keyed files dir)
    M->>M: write skeleton (hostit.yml: mode static, public/index.html)
    M->>M: budget qgroup 1/<uid> over the subvolume
    M-->>S: app (running), agent token
    S-->>User: 201 + URL, already serving the placeholder
```

<div class="mt-2 text-sm opacity-60">
Entry: <code>control/manager.go:create</code>. A fresh app is reachable before you build
anything -- the skeleton ships a static placeholder page.
</div>

---

# Flow: deploying (hostit.yml -> running)

```mermaid {scale: 0.5}
sequenceDiagram
    actor Dev as Owner / agent
    participant S as server (REST)
    participant M as app.Manager
    participant Pod as podman/systemd
    participant Ag as agent (PID 1)
    Dev->>S: POST /api/apps/{app}/deploy
    S->>M: Up
    M->>M: loadConfig (read hostit.yml via os.Root)
    M->>M: pre-deploy snapshot (btrfs, whole app)
    M->>M: EnsureAppSubvolume (no-op if it exists), compute config hash
    alt config hash changed (or no container)
        M->>Pod: recreate container (--rootfs persists), restart unit
    else only run:/prepare:/mode: changed
        M->>Ag: SIGHUP (restart just the command)
    end
    M-->>Dev: "created and started" / "reloaded"
```

<div class="mt-2 text-sm opacity-60">
<code>app/deploy.go:apply</code> is the convergence step. Recreate is disruptive (ends
SSH sessions) but lossless -- the container runs the app's one persistent
subvolume, so files and installed packages survive. A reload just re-runs the
command. The hash decides which.
</div>

---

# Flow: serving a request

```mermaid {scale: 0.62}
sequenceDiagram
    actor Visitor
    participant P as proxy (daemon)
    participant App as app container
    Visitor->>P: GET https://blog.apps.example.com/
    P->>P: resolve host -> app -> loopback port
    P->>App: proxy to 127.0.0.1:port
    App-->>P: response
    P-->>Visitor: response
```

An unknown host, or a stopped app, gets the same "Nothing deployed here" page, so app
names cannot be enumerated. Custom domains resolve the same way via a cache.

<div class="mt-4 text-sm opacity-60">
Proxy + TLS: <code>server/service.go</code> (certmagic). Routing: the host header is the
whole lookup.
</div>

---

# Flow: the in-container agent (PID 1)

The `agent` package is a process supervisor. It runs the `run:` command, and reacts to
signals the daemon sends over podman.

<div class="grid grid-cols-2 gap-6 mt-4 text-sm">
<div class="p-4 rounded border border-gray-400 border-opacity-30">

**Steady state**

- runs `prepare:`, then the `run:`/`static` command
- reaps zombies (it is PID 1)
- writes a breadcrumb to `log/state` the daemon reads
- SIGHUP = reload the command; SIGUSR1/2 = pause/resume

</div>
<div class="p-4 rounded border border-gray-400 border-opacity-30">

**When the command keeps crashing**

- restarts with exponential backoff (2s -> 60s)
- after 5 rapid crashes, **gives up**: writes `failed`, idles
- a healthy run resets the backoff
- the daemon shows this as a red "App crashed"

</div>
</div>

<div class="mt-6 text-sm opacity-60">
<code>agent/service.go:Run</code>. The breadcrumb is the only channel from inside the
container to the UI's status dot.
</div>

---
zoom: 0.92
---

# Flow: snapshot and rollback

Snapshots are cheap btrfs subvolumes of the whole app (files AND installed
software); rollback is a crash-safe staged swap.

```mermaid {scale: 0.58}
flowchart LR
  subgraph rb["Rollback (snapshot/service.go)"]
    direction LR
    a["stage a writable<br/>copy of the target"] --> b["safety snapshot of the<br/>live app subvolume"]
    b --> c["power down: stop unit,<br/>remove the container"]
    c --> d["swap subvolumes<br/>(rename)"]
    d --> e["drop the old<br/>subvolume"]
    e --> f["Up: bring the<br/>app back"]
  end
```

<div class="mt-3 text-sm opacity-60">
The <code>snapshot</code> package composes btrfs/systemd/container and calls back into
<code>app.Manager</code> through a small <code>Host</code> interface. Retention is a pure,
tested GFS policy in <code>retention/</code>.
</div>

---

# Flow: the built-in assistant

An Anthropic Messages loop whose **tools are one app's own REST operations** -- so the
model is confined to that single app, the same way an agent token is.

```mermaid
flowchart LR
  chat["Browser chat"] -->|SSE| loop["assistant.Manager loop"]
  loop -->|tool call| ops["app REST ops<br/>(this app only)"]
  loop -->|"Claude.ai selected"| sandbox["claude -p in a sandbox<br/>tools over a peercred socket"]
```

<div class="mt-4 text-sm opacity-60">
A credential's presence is the whole switch: an API key enables API models; an OAuth
token additionally offers the Claude Max subscription. See
<code>subsystems/assistant-internals.md</code>.
</div>

---
layout: center
class: text-center
---

# Reading on

<div class="text-left inline-block mt-2">

- **The whole system** -- `architecture/` (overview -> isolation -> flows -> code-map)
- **One feature** -- `features/<name>.md`: what it is, why, the flow, the code
- **A tricky internal** -- `subsystems/` (identity, security, storage, assistant, ...)
- **Contributing** -- `CONTRIBUTING.md` (TDD, style, build/test)

</div>

<div class="mt-10 text-sm opacity-50">
Every claim here links to a file:symbol. Questions live next to the code -- start at
<code>architecture/overview.md</code>.
</div>
