# Flows

The sequences that matter, as they happen in the code. Custom-domain issuance and the
assistant turn are their own subsystems; see [`../subsystems/`](../subsystems/).

## Startup preflight

Before the daemon touches anything it verifies the prerequisites it cannot run
without, so a misconfigured host fails loudly at boot rather than lazily on the first
app operation (`cmd/serve.go:execServe`, `cmd/preflight.go`).

```mermaid
flowchart TB
    start["hostit serve"] --> cfg["load + validate config"]
    cfg --> root{"running as root?"}
    root -- no --> fail1["refuse: must run as root"]
    root -- yes --> bins{"podman, btrfs, nft, systemctl,<br/>useradd/usermod/... all present?"}
    bins -- missing --> fail2["refuse: name every missing command"]
    bins -- all present --> dirs["mkdir DataDir 0711, AppsDir 0755"]
    dirs --> btrfs{"AppsDir on btrfs?"}
    btrfs -- no --> fail3["refuse: app homes must be btrfs"]
    btrfs -- yes --> ok["open store, then start"]
    ok --> bg["background: build workspace image,<br/>restart stale agents, prune old images"]
    ok --> rec["reconcile orphaned units/containers"]
    ok --> listen["open listeners + loops (state, disk, snapshot, domain retry)"]

    style fail1 fill:#b91c1c,color:#fff
    style fail2 fill:#b91c1c,color:#fff
    style fail3 fill:#b91c1c,color:#fff
    style ok fill:#047857,color:#fff
```

btrfs is mandatory (`cmd/preflight.go:requireBtrfs`): snapshots, rollback, fork and
hard disk quotas are core, so hostit refuses to run without them rather than silently
degrading. There are no one-off data migrations on this path; the only post-upgrade
work is `RestartStaleAgents`, which brings each app up on the new binary because a
running agent keeps the behaviour of the binary it was exec'd from
(`app/upgrade.go`).

## Creating an app

The API answers as soon as the app exists; the container comes up behind it, so the
first request is never blocked on an image build (`app/service.go:create`,
`server/server_handler_apps.go`).

```mermaid
sequenceDiagram
    participant U as Browser / agent
    participant D as hostit daemon
    participant S as SQLite
    participant OS as useradd / podman / systemd

    U->>D: POST /api/apps {"name":"blog"}
    D->>D: validate name, check the owner's app limit
    D->>S: allocate a free port
    D->>OS: btrfs subvolume for the home (id-keyed), then useradd --no-create-home
    D->>OS: write authorized_keys (owner's profile keys)
    D->>OS: write skeleton hostit.yml, README.md, .hushlogin
    D->>S: register app + mint an app-scoped token
    D->>OS: nftables: port 10000 to uid 0 and the app base uid
    D-->>U: 201 {url, ssh, agent_token}

    Note over D,OS: ...and in the background
    D->>OS: podman create + systemctl start hostit-app@<id>
    OS-->>D: container running, stub page served
```

An app that fails to start still exists: its URL shows hostit's "not running" page
rather than a dead hostname, and the owner can fix `hostit.yml` and deploy. Fork is
the same flow, except the home is seeded from a writable btrfs snapshot of the source
instead of the skeleton (`app/service.go:Fork`).

## Serving a request

```mermaid
sequenceDiagram
    participant V as Visitor
    participant P as hostit proxy (:443)
    participant S as SQLite
    participant A as App container

    V->>P: GET https://blog.apps.example.com/
    P->>P: TLS (wildcard cert, or issued on demand)
    P->>P: resolve host: web hostname? "<app>.<base>"? custom domain?
    P->>S: look up the app's port
    alt app exists and is running
        P->>A: proxy to 127.0.0.1:10000
        A-->>V: the app's response
    else no such app, or app is stopped/unreachable
        P-->>V: 404 "There is nothing here"
    end
```

By design there is no 502 path: no-such-host, lookup errors, and a proxy failure
because the app is down all return the **same** 404 "nothing here" page
(`server/proxy.go:newProxyHandler`, `proxyTo`), so a stopped app is indistinguishable
from a free name. A request whose host matches the web hostname is handed to the
REST/web handler instead of being proxied.

## Logging in over SSH

The session never touches a host shell. sshd runs the app user's login shell, which is
hostit's own, and that execs into the container through a root helper
(`cmd/shell.go`, `cmd/enter.go`).

```mermaid
sequenceDiagram
    participant U as User
    participant SD as sshd
    participant SH as hostit-shell (as uid 1000000)
    participant D as hostit daemon
    participant SU as sudo hostit-enter (root)
    participant C as Container

    U->>SD: ssh blog@apps.example.com
    SD->>SD: authorized_keys (managed block + the user's own)
    SD->>SH: exec login shell as the app's uid
    SH->>D: GET /v1/self over the unix socket
    D->>D: SO_PEERCRED: the kernel says this uid = blog
    D-->>SH: this is "blog", here is its URL and port
    SH->>D: POST /v1/self/ensure (make sure the container is up)
    SH->>U: print the hostit banner
    SH->>SU: sudo -n hostit-enter $TERM
    SU->>SU: target container derived from SUDO_UID, never from arguments
    SU->>C: exec podman exec -it hostit-app-<id> bash -l
    C-->>U: a shell inside the app's own container
```

`hostit-enter` ignores its arguments when choosing a container, so an app user who
calls it directly with someone else's name still lands in their own
(`cmd/enter.go:execEnter`). scp, rsync and `ssh host <command>` get no banner; only an
interactive human session does (`cmd/shell.go:execShell`).

## An agent deploying

An app-scoped token reaches exactly one app's endpoints; the daemon refuses anything
outside `/api/apps/<that-app>/` (`server/server_handler_agent.go`,
`server/auth.go`).

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
    D->>H: stream to a temp file inside the app's os.OpenRoot, chown, rename
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

Every path in the file operations resolves through `os.OpenRoot` on the app's home, so
a symlink the app planted cannot walk the daemon out of it (`app/files.go`); see
[`isolation.md`](isolation.md). The deploy decides between an in-place SIGHUP and a
container recreate by diffing the create arguments (`app/deploy.go`), so a
run-command-only change costs no container restart.
</content>
