# hostit architecture

How the pieces fit, what isolates what, and what happens during the flows that
matter. The README covers using hostit; this covers building on it.

## The whole thing

One binary, running as root, is the entire control plane. It terminates TLS,
proxies to apps, serves the web app and REST API, creates Unix users and
containers, and answers a unix socket for the CLI inside each app.

```mermaid
flowchart TB
    subgraph internet[" "]
        browser[Browser]
        agent[AI assistant<br/>app-scoped token]
        ssh_client[ssh / scp / rsync]
    end

    subgraph host["Host (one droplet)"]
        daemon["hostit serve<br/><i>root daemon</i>"]
        sshd["sshd"]
        store[("SQLite<br/>/var/lib/hostit")]
        nft["nftables<br/><i>per-app port rules</i>"]

        subgraph apps["App containers (podman, one per app)"]
            app1["hostit-app-blog<br/>uid block @1000000"]
            app2["hostit-app-stats<br/>uid block @1065536"]
        end
    end

    browser -->|"HTTPS :443"| daemon
    agent -->|"HTTPS /api/apps/blog/*"| daemon
    ssh_client -->|"SSH :22"| sshd

    daemon -->|"proxy to 127.0.0.1:port"| app1
    daemon -->|"proxy to 127.0.0.1:port"| app2
    daemon --> store
    daemon -->|"useradd, podman, systemd"| apps
    daemon --> nft
    sshd -->|"login shell:<br/>hostit-shell"| app1

    app1 -.->|"/run/hostit/hostit.sock<br/>SO_PEERCRED"| daemon

    style daemon fill:#047857,color:#fff
    style store fill:#1f252d,color:#fff
```

The dotted line is the only way in from an app: a unix socket where the kernel,
not the caller, says which uid is calling. An app can therefore ask about itself
and act on itself, and cannot name another app.

## What isolates what

Each app is four things created together: a Unix user, a home directory, a
container, and a loopback port with a firewall rule.

```mermaid
flowchart TB
    subgraph one["One app: blog"]
        user["Unix user 'blog' (base uid 1000000)<br/>shell: /usr/bin/hostit-shell"]
        home["/var/lib/hostit/apps/blog<br/>mode 0750, owned by blog"]

        subgraph container["podman container hostit-app-blog"]
            direction TB
            idmap["--uidmap 0:1000000:65536<br/><i>container root IS the app's base uid</i>"]
            pid1["PID 1: hostit agent<br/>supervises the run: command"]
            mounts["/home/blog       (the home, bind-mounted)<br/>/usr/bin/hostit  (the binary)<br/>/run/hostit      (the daemon socket dir)"]
            net["own netns (slirp4netns)<br/>no peers, no host loopback"]
        end

        port["published 127.0.0.1:10000<br/>nftables: uid 0 and the app's base uid only"]
    end

    user --- home
    home --- container
    container --- port

    style container fill:#eceff1,stroke:#047857
    style idmap fill:#fff
```

Four boundaries, each doing one job:

| Boundary | Mechanism | What it stops |
|---|---|---|
| App vs app | separate uid, container, netns | reading files, seeing processes, reaching ports |
| App vs host | uidmap so container root is an unprivileged host uid | an escape landing as root |
| App vs daemon | SO_PEERCRED on the socket; app-scoped tokens on the API | acting as another app |
| Tenant vs operator | `hostit-shell` login shell; no host shell, no SSH forwarding | tunnelling out, reaching host services |

Each app owns a **65536-wide contiguous uid/gid block** (`uidFor(port) = 1000000 +
(port - PortMin) * 65536`), so container root maps to the app's base host uid and the
whole block maps one-to-one. Contiguity is load-bearing: it lets podman **idmap-mount**
the image (instant, no copy) instead of chowning a private copy. `MigrateToBlockUIDs`
is a one-off migration from the older single-uid scheme (the app must be stopped, since
`usermod` refuses a uid change with live processes).

The daemon holds the privilege. Nothing an app does escalates: it starts as an
unprivileged uid and every path back into hostit identifies it by that uid.

## Creating an app

The API answers as soon as the app exists; the container comes up behind it, so
the first request is never blocked on an image build.

```mermaid
sequenceDiagram
    participant U as Browser / agent
    participant D as hostit daemon
    participant S as SQLite
    participant OS as useradd / podman / systemd

    U->>D: POST /api/apps {"name":"blog"}
    D->>D: validate name, check the owner's app limit
    D->>S: allocate a free port
    D->>OS: groupadd + useradd --no-create-home, then mkdir home 0750
    D->>OS: write authorized_keys (owner's profile keys)
    D->>OS: scaffold hostit.yml, README.md, .hushlogin
    D->>S: register app + mint an app-scoped token
    D->>OS: nftables: port 10000 to uid 0 and the app base uid
    D-->>U: 201 {url, ssh, agent_token}

    Note over D,OS: ...and in the background
    D->>OS: podman create + systemctl start hostit-app@blog
    OS-->>D: container running, stub page served
```

An app that fails to start still exists: its URL shows hostit's "not running"
page rather than a dead hostname, and the owner can fix `hostit.yml` and deploy.

## Serving a request

```mermaid
sequenceDiagram
    participant V as Visitor
    participant P as hostit proxy (:443)
    participant S as SQLite
    participant A as App container

    V->>P: GET https://blog.apps.example.com/
    P->>P: TLS (wildcard cert, or issued on demand)
    P->>P: resolve host: "app.base" subdomain, else a custom domain
    P->>S: look up the app's port
    alt app exists and is running
        P->>A: proxy to 127.0.0.1:10000
        A-->>V: the app's response
    else no such app, or app is stopped/unreachable
        P-->>V: 404 "There is nothing here"
    end
```

By design there is no 502 path: no-such-host, lookup errors, and a proxy failure
because the app is down all return the **same** 404 "nothing here" page, so a stopped
app is indistinguishable from a free name.

## Custom domains

An app is reachable at `<app>.<base>` out of the box; an owner can also attach their
own hostname (`GET/POST /api/apps/{name}/domains`, `POST .../domains/{domain}/verify`,
`DELETE .../domains/{domain}`). Each domain belongs to exactly one app (`app_domain`
table, PK = domain) and carries a status: `pending` -> `active` -> `error`. Active
domains live in an in-memory `host -> app` cache (`domainCache`) the proxy consults
after the subdomain lookup misses.

TLS for these is the interesting part. The daemon runs **two** certmagic configs: the
base `s.magic` for the platform's own hostnames and app subdomains, and a **second**
`s.domainMagic` on its own cache for custom domains, with `GetCertificate` falling back
from the base cache to `domainMagic` on an SNI miss. In wildcard/DNS-01 mode
`domainMagic` uses a `DNS01Solver` whose `OverrideDomain` is a fixed name in a zone
hostit controls -- `_acme-challenge.acme.<BaseDomain>`. The owner delegates their
challenge by pointing a CNAME `_acme-challenge.<domain>` at that name, so hostit can
answer DNS-01 for a zone it does not control, even when the box is not publicly
reachable. In on-demand mode `domainMagic` is just aliased to `magic` (HTTP-01). Both
are nil when TLS is off, and custom domains then route over plain HTTP.

```mermaid
sequenceDiagram
    participant O as Owner
    participant D as hostit daemon
    participant DNS as Owner's DNS
    participant CA as Let's Encrypt

    O->>D: POST /api/apps/blog/domains {"domain":"www.acme.com"}
    D-->>O: pending + the DNS records to create
    O->>DNS: A www.acme.com to server IP (or CNAME to blog.apps.example.com)
    O->>DNS: CNAME _acme-challenge.www.acme.com to _acme-challenge.acme.apps.example.com
    O->>D: POST /api/apps/blog/domains/www.acme.com/verify
    D->>DNS: delegationReady? LookupCNAME _acme-challenge.www.acme.com
    alt delegation not in place
        D-->>O: still pending (CA never contacted)
    else delegation present
        D->>CA: domainMagic.ManageSync (DNS-01 via the delegated name)
        CA-->>D: cert issued
        D-->>O: active
    end
```

`issueDomainCert` guards against overlap (`s.issuing`), and a transient re-issue failure
on an already-active domain does **not** de-route it. A background `DomainRetryLoop`
periodically retries non-active domains, and `manageExistingDomains` re-obtains active
certs at startup. `validateCustomDomain` rejects malformed names, the platform's own
hostnames, existing app subdomains, and anything under `BaseDomain`.

## SSH login

The session never touches a host shell. sshd runs the app user's login shell,
which is hostit's own, and that execs into the container through a root helper.

```mermaid
sequenceDiagram
    participant U as User
    participant SD as sshd
    participant SH as hostit-shell (as uid 1001)
    participant D as hostit daemon
    participant SU as sudo hostit-enter (root)
    participant C as Container

    U->>SD: ssh blog@apps.example.com
    SD->>SD: authorized_keys (managed block + the user's own)
    SD->>SH: exec login shell as uid 1001
    SH->>D: /v1/self over the unix socket
    D->>D: SO_PEERCRED: the kernel says uid 1001 = blog
    D-->>SH: this is "blog", here is its URL and port
    SH->>D: ensure the container is running
    SH->>U: print the hostit banner
    SH->>SU: sudo -n hostit-enter $TERM
    SU->>SU: target derived from SUDO_UID, never from arguments
    SU->>C: exec podman exec -it hostit-app-blog bash -l
    C-->>U: a shell inside the app's own container
```

`hostit-enter` ignores its arguments when choosing a container, so an app user
who calls it directly with someone else's name still lands in their own.

## An agent deploying

The token in the pasted prompt reaches exactly one app's endpoints.

```mermaid
sequenceDiagram
    participant AG as AI assistant
    participant D as hostit daemon
    participant H as App home
    participant C as Container agent (PID 1)

    AG->>D: GET /api/apps/blog/info (Bearer hostit_...)
    D->>D: map token to app scope, refuse anything outside /api/apps/blog/
    D-->>AG: state, README, files, hostit.yml, and the full guide

    AG->>D: GET /api/apps/blog/assistant/transcript
    D-->>AG: prior built-in-assistant work, rendered as markdown

    AG->>D: PUT /api/apps/blog/files/bin/server?mode=755
    D->>H: stream to a temp file inside the app's root, chown, rename
    AG->>D: PUT /api/apps/blog/files/hostit.yml
    AG->>D: POST /api/apps/blog/deploy

    alt container config unchanged (only run: differs)
        D->>C: SIGHUP
        C->>C: re-read hostit.yml, restart the run command
    else container create args changed (env, memory, ...)
        D->>D: recreate the container, then start it
    end
    D-->>AG: deployed
```

Every path in the file operations resolves through `os.OpenRoot` on the app's
home, so a symlink the app planted cannot walk the daemon out of it.

## The in-browser workspace

The same deploy loop above can run without an external agent: each app's page is a
workspace with a hosted assistant, a terminal, and a live preview.

- **Hosted assistant (`assistant/`).** A chat drives an Anthropic Messages loop
  whose ten tools -- list/read/write files, run a command, read logs, deploy, refresh
  the preview, and `snapshot`/`list_snapshots`/`rollback` -- are exactly one app's REST
  surface, so the model is confined the same way an app-scoped token is. The turn is
  **server-owned**: `POST` starts it as a background goroutine (not tied to the
  request), each step is persisted, and every watcher subscribes over **SSE**, so the
  run survives a reload and shows up on every device viewing the app. A per-app lock
  allows one turn at a time; the transcript lives in SQLite (`assistant_session`).
  Thinking blocks are streamed to the UI but never persisted (`withoutThinking`
  strips them before saving). Chat file uploads land under the app's `uploads/`
  (`POST .../assistant/upload`, 10 MB cap); image attachments are read back and
  base64-embedded as vision blocks the model sees, other files are noted by path.
  Rate limits are **per-user across all their apps** (concurrent runs + hourly, admin
  token exempt); a context window, a subscriber cap, and a same-origin gate on the SSE
  connection keep it from being a lever for abuse. It is inert without an Anthropic API
  key in the config. Requests carry **prompt-cache breakpoints** (`cache_control`) on
  the system prompt, the tools block, and the tail of the conversation, so Anthropic
  reuses the large stable prefix across turns instead of re-reading it every message.

  An external agent can pick up where the hosted one left off:
  `GET /api/apps/{app}/assistant/transcript` renders the stored session as markdown
  (`RenderTranscript`), and the `/info` guide instructs a pasted-in agent to read it
  right after `/info` so it resumes with prior context instead of starting cold. The
  browser-facing `GET /api/apps/{name}/assistant` returns the same session as structured
  items for the web UI. `enabled:false` comes back cleanly when no key is configured.

- **Browser terminal.** `GET /api/apps/{app}/terminal` upgrades to a WebSocket and
  bridges it to a pty running the app's login shell (`runuser` into the container),
  binary both ways with a text frame carrying the window size. It is the same shell
  and isolation as an SSH session, owner-only, with an origin check on the upgrade
  so a tenant's own app page cannot open one on a signed-in owner's behalf.

- **Live preview + state.** The page iframes the app (owner-only, via a `frame-src`
  the CSP allows for the base domain's subdomains) and reloads it, cache-busted,
  whenever the app is redeployed -- picked up from the app process's start time
  changing. Live CPU/RAM/disk come from one `podman stats` read behind the state
  cache; lifecycle actions post the same verbs the CLI does.

- **Activity log (Logs tab).** User-initiated actions (create, fork, snapshot,
  rollback, domain add/remove, description, token, lifecycle) are recorded to an
  `app_event` row, attributed to the caller's email, via `s.logAction` in the
  handlers. `GET /api/apps/{app}/events` returns the last 100 for the Logs tab,
  which shows them above a live tail of the app's own container output
  (`GET .../logs`). The log is trimmed to the newest `maxAppEvents` per app.

## Data

SQLite, one connection, WAL. Ordered migrations that record their version in the
same transaction, so a failure rolls back whole and a success never replays.

```mermaid
erDiagram
    user ||--o{ app : owns
    user ||--o{ token : has
    user ||--o{ user_key : has
    app ||--o{ app_key : "extra ssh keys"
    app ||--o| token : "app-scoped agent token"
    app ||--o{ snapshot : has
    app ||--o{ app_domain : "custom domains"
    app ||--o| assistant_session : "chat transcript"
    app ||--o{ app_event : "activity log"

    user {
        text id PK
        text email UK
        text role "admin | user"
        text status "pending | active | denied"
        int app_limit "null = global default"
        int memory_mb "null = global default"
        int disk_mb "null = global default"
    }
    app {
        text name PK
        int port UK
        text owner_id FK
        int disk_mb
    }
    token {
        text id PK
        text hash UK
        text app_name "empty = account-wide"
        text secret "app tokens only, shown again on the app page"
    }
    snapshot {
        text id PK
        text app_name FK
        text label
        bool auto "deploy/hourly/rollback = true"
    }
    app_domain {
        text domain PK
        text app_name FK
        text status "pending | active | error"
    }
    assistant_session {
        text app_name PK
        blob transcript "one JSON blob per app"
    }
    allowed_domain {
        text domain PK
    }
    setting {
        text key PK
        text value "global default limits"
    }
```

`allowed_domain` is the email **allow-list** (auto-approve sign-in for a domain),
distinct from `app_domain`, the per-app custom domains above. `setting` holds the global
default limits the admin edits (`PATCH /api/settings`); the per-user override columns fall
back to it when null. The registry is root-only (0600 in a 0700 directory): it holds every
app's agent token in the clear, deliberately, so an app's page can show it again.

## Snapshots and quotas (btrfs)

When the app-homes path (`/var/lib/hostit/apps`) is a btrfs filesystem -- a loopback
image mounted there -- each app home is a **subvolume**. That makes two things cheap
and exact:

- **Snapshots** are copy-on-write: an instant, space-shared, atomic (crash-consistent)
  copy at `apps/.snapshots/<app>/<id>`, so it does not count against the app's quota.
  hostit snapshots **hourly** (`SnapshotLoop`) and **before every deploy**, both labelled
  `"Automated snapshot"` / `"Automated snapshot before deploy"` and flagged `auto`; owners
  and the assistant `snapshot` tool take labelled manual ones. A restic-style GFS policy
  (`applyRetention`, heavily unit-tested; default `Last:50 Daily:7 Weekly:4 Monthly:3`,
  UTC buckets) thins them all -- manual and automatic alike, so none lives forever. A
  `snapshot.pre`/`post` pair in `hostit.yml` runs in the container to quiesce a database
  first (pre failing aborts the snapshot). Rollback takes a safety snapshot, stops the
  app, replaces the home subvolume with a writable copy of the chosen snapshot, restores
  ownership and quota, and starts it again. A per-app lock (`appLocks`) serializes
  deploy/snapshot/rollback/delete so they never interleave on one home.
- **Fork** duplicates an existing app (`POST /api/apps/{name}/fork`,
  `{new_name, snapshot_id?}`): the new app's home is seeded from a writable CoW snapshot
  of the source (its current home, or a named snapshot of it) instead of the demo
  scaffold, then chowned to the new app's uid. It gets its own port/uid/user/subdomain/
  container and a fresh agent token. Requires btrfs (`ErrSnapshotsUnavailable`, 501,
  otherwise).
- **Quotas** are a btrfs **qgroup** limit per subvolume equal to the app's `disk_mb`,
  so a write past it fails with EDQUOT at write time -- a hard limit, not the periodic
  measure-and-stop used on other filesystems. Usage is read from the qgroup.

The daemon detects btrfs once and caches it; on any other filesystem the subvolume and
qgroup paths are skipped and hostit keeps the plain-directory, soft-quota behavior, so
it still runs anywhere. The btrfs work lives in the `app` package (`btrfs.go`,
`snapshot.go`); snapshot metadata is a `snapshot` table in the registry.

## Where the code lives

Service packages at the root, thin `main.go`, no `internal/`:

| Package | Owns |
|---|---|
| `server` | HTTP: proxy, REST API, agent API, OAuth, sessions, unix socket |
| `app` | apps as system objects: users, containers, files, quotas, ports |
| `user` | people: accounts, roles, limits, tokens, SSH keys, allowed domains |
| `store` | SQLite: schema, migrations, queries |
| `appctl` | the `hostit.yml` contract and the client for the app-side CLI |
| `agent` | PID 1 inside a container: supervises the app, rotates its log |
| `assistant` | the in-browser AI agent: an Anthropic Messages loop whose tools are one app's REST surface |
| `cmd` | the CLI: `serve`, the app commands, `admin`, and the SSH plumbing |
| `client` | Go client for the REST API, used by `hostit apps` |
| `web` | React + Vite SPA (dashboard, app workspace, admin); built into `server/site/` |

The same binary is all of these; which commands it offers depends on where it
runs. Inside a container it presents only the app's own commands.

Within a package, code is split per concern into small files rather than a few
grab-bags: `store` has one file per entity (`app.go`, `snapshot.go`, `domain.go`,
`event.go`, `token.go`, `setting.go`, ...), `app` separates deploy from
`reconcile.go`, `migrate.go` and the pure `retention.go` engine, and `server`
keeps the agent guide (`agentguide.go`), the assistant adapters (`assistantops.go`)
and each feature's handlers in their own file.

The SPA is a separate React 19 / Vite app under `web/`; `make web` builds it and copies
the output into `server/site/`, which `server/web.go` bakes into the binary via
`//go:embed` (a placeholder is checked in so the package always compiles). The SPA, REST
API, agent API, OAuth, terminal WebSocket, and assistant SSE are all same-origin, served
by the one root daemon.
