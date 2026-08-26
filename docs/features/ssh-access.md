# SSH access

## Description

Every hostit app is reachable over plain SSH using the app's name as the
username, e.g. `ssh blog@apps.example.com`. There is no separate hostit account
to make: the host's own `sshd` authenticates the session against the app user's
`authorized_keys`, and the login lands the caller *inside the app's container* --
their own filesystem, processes and ports, with `root` in there. The same login
carries `scp`, `sftp` and `rsync`, so an owner (or their agent) can copy files
straight into the app's home with ordinary tools and no hostit-specific client.

Keys come from two places, both managed without touching the server by hand: the
owner's **profile keys** (added on the Profile page or via the API, they open
*all* of that owner's apps) and per-app **request keys** (supplied when the app
is created or via `PUT /api/apps/{name}/keys`). An app created with no keys at
all is still fully usable through the REST API; SSH simply starts working the
moment a key is added.

## Why it exists

The design goal is that an app *is* a small Unix machine you can SSH into, not a
PaaS abstraction you push to. Landing directly in the container (rather than a
host shell or a restricted menu) means the toolchains, `apt`, the app's ports and
its files are all right there, which is what both humans and coding agents expect.

Doing this on top of the host's stock `sshd` (instead of an embedded SSH server)
keeps hostit out of the credential-handling and crypto business: key
distribution is the only thing hostit owns, and it owns it by writing a managed
block into each app user's `authorized_keys`. Two boundaries make that safe. The
managed block is delimited by markers so a key someone `scp`'d in by hand
survives every profile change hostit makes. And every write into the app's home
goes through chained `os.OpenRoot` handles (the app's subvolume root, then
`home/app` resolved inside it), because the tenant controls that whole tree
(they are root inside the container it backs); a
tenant who replaced `.ssh` -- or `home/app` itself -- with a symlink must not be
able to redirect the root daemon's key writes onto a host path.

The one thing `sshd` offers that would reach *past* the container is forwarding.
An app user logs in for exactly one reason -- to reach their own container -- so
all forwarding is turned off for the apps group; otherwise a tenant could tunnel
to the cloud metadata service (which on DigitalOcean carries `user-data`, often
secrets) or probe another app's loopback port. `scp`/`sftp`/`rsync` are
deliberately unaffected.

## User flows

1. The owner adds an SSH public key on the Profile page (or `POST
   /api/account/keys`, or `-k key.pub` when creating an app via the CLI).
2. hostit writes/merges that key into the `authorized_keys` of every app the
   owner owns.
3. The owner runs `ssh <app>@<ssh-host>`. The host `sshd` authenticates the key,
   and because the app user's login shell is `hostit-shell`, the session is
   handed to hostit rather than to a shell.
4. hostit identifies which app this is, ensures the container is running,
   prints a banner (only for an interactive human, never for `scp`/`rsync`),
   and `exec`s the caller into the app's container via a narrow sudo grant.
5. `scp`, `sftp` and `rsync` use the very same login; they see only their own
   protocol on the wire (no banner) and read/write the app's home.

```mermaid
sequenceDiagram
    actor User
    participant sshd as host sshd
    participant shell as hostit shell (login shell)
    participant appctl as hostit daemon (unix socket)
    participant enter as sudo hostit enter (root)
    participant podman
    User->>sshd: ssh blog@host (public key)
    sshd->>sshd: match key in blog's authorized_keys
    sshd->>shell: exec /usr/lib/hostit/bin/hostit-shell "$@"
    shell->>appctl: Self() + Ensure() (identify app, start container)
    shell->>User: login banner (interactive only)
    shell->>enter: exec sudo -n hostit-enter <TERM> [-c cmd]
    enter->>enter: derive container from SUDO_UID's home, not args
    enter->>podman: podman exec --interactive [--tty] <container> /bin/sh -l
    podman-->>User: shell inside the app container
```

## Multi-node SSH

On a single host, `ssh <app>@<hostit_domain>` just works: the base domain *is*
the node, and its `sshd` authenticates the app user locally. With MULTIPLE nodes
an app runs on whichever node was chosen for it, and its Unix user and
`authorized_keys` exist only there -- so the advertised SSH host has to point at
the right node. hostit supports two ways to do that; both are configured in
[`deploy/ansible`](../../deploy/ansible).

### Direct-to-node (default)

Each node reports its own reachable SSH hostname to control (in the cluster
heartbeat, stored on the node row), and control advertises
`ssh <app>@<that node's host>` for the app. The connection lands directly on the
node's own `sshd` -- control is never in the SSH path, so app SSH keeps working
even while the control *process* is down. Set each remote node's hostname in
Ansible; leave it unset on the single/colocated host, where it falls back to the
base domain:

```yaml
# host_vars/<node>.yml (or the inventory) -- per remote node
hostit_ssh_host: "node2.ssh.apps.example.com"   # an A record pointing at that node
```

The app page and the agent `/info` response then print the right per-node
command, e.g. `ssh blog@node2.ssh.apps.example.com`. When an app migrates to
another node the advertised host changes with it (a new `known_hosts` entry, not
a mismatch, since each node has its own hostname).

### Single-hostname relay gateway (optional, EXPERIMENTAL)

If you want ONE stable `ssh <app>@<hostit_domain>` regardless of which node runs
the app, enable the relay gateway on the control host. It routes by app name
through the host's own `sshd`: the app user's login shell resolves the app's node
from a local file and, for a *remote* app, hands off to a small privileged helper
(`hostit-relay`) that `ssh`es to the node with a control-held **relay key**; a
colocated app is entered locally exactly as before. `ssh`, `scp`, `rsync` and
`sftp` all work, and forwarding stays off. Enable it on the control host:

```yaml
hostit_ssh_relay: true
hostit_ssh_relay_from_address: "<control host's egress IP as the nodes see it>"
```

That generates a relay key (`/etc/hostit/relay_key`, root-only), grants the
`hostit-apps` group sudo to the relay helper, and makes control write the routing
files. No extra port and no SSH daemon of hostit's own -- it rides the system
`sshd`.

Trade-offs, deliberately accepted: the control host terminates the outer SSH and
holds a key that reaches every node, so a control-host compromise reaches every
app (it already holds the key material); the frontend authenticates the user and
the node then trusts the relay key, so a revoked key keeps working *on the relay
path* for a few seconds after the change (the direct path revokes at once); and
the control host must be up for the relay path (the direct-to-node path is
unaffected either way). It is OFF by default -- prefer direct-to-node unless a
single stable hostname is worth those trade-offs.

## Technical details

Key management (the daemon side):

- `ssh/service.go:Service.WriteAuthorizedKeys` writes through the chained files
  root the caller hands it
  (`control/manager.go:writeKeysIn` opens it via `homefs.Service.OpenRoot`); it
  refuses if `.ssh` is not a real directory (`ssh/service.go:ErrNotDirectory`).
  The results stay root-owned and world-readable (0755 `.ssh`, 0644
  `authorized_keys`): the host's `sshd` reads them as the app user (StrictModes
  accepts root-owned files), and through the container's idmapped rootfs
  disk-root IS container-root, so the tenant can still hand-edit their own keys.
  Public keys are not secrets.
- `ssh/keys.go:MergeAuthorizedKeys` rewrites only the delimited managed block
  (`managedBeginMarker`/`managedEndMarker`), preserving hand-added keys;
  `ssh/keys.go:keyMaterial` compares type+key so the same key with a different
  comment is deduped.
- `ssh/service.go:ValidateKeys` (wrapped by `app/util.go:validateKeys`) rejects
  unparseable keys before anything is written.
- The full key set for an app is app keys plus the owner's profile keys:
  `control/manager.go:Manager.create` composes the key set and the node's
  `Provision` writes it; `SetKeys` / `SyncKeys` (`node/machine.go`) rewrite it
  when profile keys change, through `writeKeysIn` and the `ssh` service.
- Profile keys live on the user: `store/userkey.go`, exposed through
  `user/service.go:Manager.AddKey` / `Manager.KeyStrings`, and pushed to every
  owned app by `control/server_handler_account.go:Server.syncUserAppKeys` when a
  key is added or deleted.
- The create/set-keys HTTP surface is
  `control/server_handler_apps.go:handleAppsCreate` (profile keys via
  `s.users.KeyStrings`, request keys from `apiCreateAppRequest.SSHKeys`) and
  `handleAppsSetKeys`; the request shapes are in `control/types.go`
  (`SSHKeys []string`).

The login path (what happens on connect):

- The app user is created with `hostit-shell` as its login shell and membership
  in the `hostit-apps` group: `unixuser/service.go:Service.Create` /
  `createUserArgs`.
- `hostit-shell` is a one-line wrapper that `exec`s `hostit shell`
  (`cmd/agent/shell.go:cmdShell` / `execShell`). It skips flag parsing so `sshd`'s
  `-c <command>` is passed through untouched, ensures the container is up via
  `appctl` (`ctl.Self()` / `ctl.Ensure()`), prints `loginBanner` only when
  `isTerminal(os.Stdin)` and there is no forced command, then `exec`s
  `sudo -n /usr/bin/hostit-enter <TERM> [args...]`.
- `hostit-enter` (`cmd/agent/enter.go:cmdEnter` / `execEnter`) is the privileged half.
  It must run as root, derives the caller from `SUDO_UID` (never from
  arguments), and resolves the target container from the caller's *home
  directory path* via `containerKeyFromHome` (`app.IDFromHomeDir` digs the id
  out of the `<id>/home/app` tail) -- containers are keyed on the app's stable id,
  and the app user's home is the id-keyed path, so a rename
  never changes it. It builds the `podman exec` argv itself, passing only a
  validated `TERM` and an optional single `-c` command, and runs with
  `minimalEnv()` (the caller's environment is not inherited).
- The sudo grant is `hostit.sudoers`: `%hostit-apps ALL=(root) NOPASSWD:
  /usr/bin/hostit-enter` -- one root-owned helper that ignores its args when
  choosing the target, so a member of the group can only ever reach their own
  app.

sshd forwarding hardening:

- `deploy/ansible/roles/hostit/templates/sshd-hostit-apps.conf.j2` emits a
  `Match Group hostit-apps` block that sets `AllowTcpForwarding no`,
  `AllowStreamLocalForwarding no`, `AllowAgentForwarding no`, `X11Forwarding
  no`, `PermitTunnel no`, `GatewayPorts no`, `PermitUserRC no`, then `Match all`
  to reset the context (the file is included at the top of `sshd_config`). The
  README documents the same block for hand installs.

## Other notes

- The SSH host reported to clients is resolved per app by
  `control/service.go:Server.sshHostFor(app.Host)`: the hosting node's own
  reported `ssh_host` (see Multi-node SSH above), falling back to
  `controlconf/config.go:Config.SSHHostname` (`ssh-host`, then the base domain).
  It is what the app page and the agent `/info` response print as the ready-made
  `ssh <app>@<host>` command (`control/server_handler_agent.go`, `apiSSHInfo`).
- hostit never generates a key pair; an app with no keys is API-only until a key
  is added. This is intentional (no private key to hand out or store).
- Whether SSH is usable at all is a property of the owner's profile, not the app,
  so the app page shows a "add a key to your profile first" state when there are
  none (`web/src/pages/AppDetail.jsx`).
- Per-app request keys are stored separately (`store.SetAppKeys` /
  `AppKeys`) and merged ahead of profile keys, so an app can have extra keys the
  owner's profile does not carry.
- Because the container is entered per the caller's own uid (container root maps
  to the app's unprivileged uid), an escape lands on that uid, not host root; see
  the README security model. Related features: `custom-domains.md` (how the app
  is reached over HTTP), `terminal.md` (the same login shell over a WebSocket),
  and `rest-api.md` (the file endpoints that avoid SSH entirely).
- Known sharp edge: `writeAuthorizedKeysIn` refuses a non-directory `.ssh`
  rather than clobbering it, so a tenant who turns `.ssh` into a file blocks
  their own managed-key updates until it is removed.
