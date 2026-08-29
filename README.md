# hostit

[![CI](https://github.com/binwiederhier/hostit/actions/workflows/ci.yml/badge.svg)](https://github.com/binwiederhier/hostit/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

> **WARNING: hostit is EXPERIMENTAL software.** It is young, it moves fast, and
> upgrades may include one-way storage migrations. Do not host anything you
> cannot afford to lose or rebuild, and keep backups that live outside the box.

**hostit** is a tiny self-hosted mini-app platform, built to be driven by AI agents
(or humans) over SSH and a REST API. Each app gets:

- its own **container**: SSH sessions land INSIDE it (root in there, `apt install`
  away), the app runs in it, and other apps are invisible -- processes, files,
  networks and loopback ports included (podman with a per-app uid mapping, so
  container root is the app's own unprivileged user, plus nftables)
- **SSH access** (normal ssh/scp/sftp/rsync via the host's sshd; keys via the API)
- a **subdomain** (`myapp.apps.example.com`) with **automatic Let's Encrypt TLS**
- two ways to run: `mode: static` (hostit serves `public/`) or `mode: app` (your
  command, supervised by the hostit agent) -- deployed with a single `hostit deploy`
- a workspace with **python3, go, Node.js (with npm), PHP and sqlite3**
  preinstalled (and root, so `apt-get install` anything else)
- **connections** it can be granted -- an OAuth account, a pasted credential or an
  MCP tool server -- read from its own socket instead of holding a secret

Apps can be shared (the owner adds **collaborators** by email, who get full working
access while delete/rename stay owner-only). Multi-user: people sign in with Google,
an admin approves them, and each user gets their own apps within admin-adjustable
limits, plus per-user API tokens for their agent -- which is the point:
`hostit control app add myapp` with a user token is all an AI agent needs.

The intended workflow: tell your agent "create an app on my host and deploy this" --
it calls the REST API to get an SSH login, pushes the code, writes `hostit.yml`, runs
`hostit deploy`, and the app is live with a cert. The same thing can happen entirely
in the browser: each app's page is a workspace with a built-in AI assistant, a file
editor with live preview, a terminal, snapshots, an activity/output log, and settings.

## How it works

hostit runs as three cooperating processes (even on one machine): **hostit-control**
(the registry, web app, REST API, placement and certificates), **hostit-node** (this
machine's app work -- containers, Unix users, btrfs subvolumes, port rules), and
**hostit-proxy** (terminates TLS on `:443` and routes each subdomain to its app from
a cached table). Each member dials control over mTLS; control never dials back, so a
control restart does not stop apps serving.

Each app is four things created together: a Unix user, a btrfs subvolume that is the
container's entire filesystem (the app's files live at `/home/app` inside it), a
podman container whose root is mapped to that user's unprivileged uid, and a loopback
port that nftables restricts to that uid. SSH logins are handed to
`/usr/lib/hostit/bin/hostit-shell`, which execs the session into the app's container,
so users never get a host shell, and an escape inside the container lands on the
app's own uid rather than on root.

**[docs/architecture/](docs/architecture/)** has the diagrams: the
[components](docs/architecture/overview.md), what
[isolates what](docs/architecture/isolation.md), and the
[sequence diagrams](docs/architecture/flows.md).

## Install (server)

Requirements: a Linux host with systemd, sshd, `podman` **>= 4.3** (plus `uidmap`,
`passt` or `slirp4netns`, `dbus-user-session`), `crun` **>= 1.29**, `nftables`, and
`btrfs-progs`. hostit must run as **root**, and its apps directory must be on
**btrfs** (all of this is mandatory). On start it preflights everything and refuses
to run -- naming exactly what to fix -- if it is not root, a required command is
missing, podman or crun are too old, or the apps path is not btrfs.

Optional: `app-preview: screenshot` replaces the dashboard cards' live iframes with
sandboxed headless-chrome screenshots (see `control.yml.example`).

### crun drop-in

App containers run their root-owned subvolume via an **idmapped rootfs mount**
(`--rootfs <subvolume>:idmap`), which needs a newer crun than most distributions
ship (Ubuntu 24.04 has 1.14.1, which hard-fails). The fix is a two-minute drop-in of
the official static binary; podman is pointed at it via `containers.conf`, so the
distribution package stays untouched:

```sh
curl -L -o /usr/local/lib/hostit-crun \
  https://github.com/containers/crun/releases/download/1.29.1/crun-1.29.1-linux-amd64
chmod 0755 /usr/local/lib/hostit-crun
mkdir -p /etc/containers/containers.conf.d
cat > /etc/containers/containers.conf.d/50-hostit-crun.conf <<'EOF'
[engine]
runtime = "crun"
[engine.runtimes]
crun = ["/usr/local/lib/hostit-crun", "/usr/bin/crun"]
EOF
```

### Packages

hostit ships **three packages**, one per component (there is no combined `hostit`
package). Build them with goreleaser, then install all three plus podman:

```sh
make release-snapshot                          # goreleaser; produces dist/*.deb
sudo apt install ./dist/hostit-control_*_linux_amd64.deb \
                 ./dist/hostit-node_*_linux_amd64.deb \
                 ./dist/hostit-proxy_*_linux_amd64.deb podman

# Each package installs an /etc/hostit/<component>/<component>.yml.example:
sudo cp /etc/hostit/control/control.yml.example /etc/hostit/control/control.yml
sudo cp /etc/hostit/node/node.yml.example       /etc/hostit/node/node.yml
sudo cp /etc/hostit/proxy/proxy.yml.example     /etc/hostit/proxy/proxy.yml
sudo $EDITOR /etc/hostit/control/control.yml     # set base-domain + admin-token

# node.yml and proxy.yml work as-is on a single colocated host (control mints
# their mTLS credentials on first start). Start control first, then node, proxy:
sudo systemctl enable --now hostit-control hostit-node hostit-proxy
```

An update is just installing the newer packages (`dpkg -i --force-confold ...` to
keep your configs) and restarting the three services. Upgrading from a release older
than **v0.11**: install a v0.11.x release first (it
carries the one-time storage migrations, since removed) and start it once, then
upgrade onward.

### DNS

Point a wildcard at the host (both records are required):

```
apps.example.com.    A  <host-ip>
*.apps.example.com.  A  <host-ip>
```

### Harden sshd (the one thing the package cannot do for you)

Add this to `sshd_config` (a drop-in in `/etc/ssh/sshd_config.d/` works) and restart
sshd. App users log in for one reason -- to reach their own container -- and
forwarding is the one thing sshd offers that reaches past it (a tenant could
otherwise tunnel to the cloud metadata service or probe host-local services). scp,
sftp and rsync are unaffected.

```
Match Group hostit-apps
    AllowTcpForwarding no
    AllowStreamLocalForwarding no
    AllowAgentForwarding no
    X11Forwarding no
    PermitTunnel no
    GatewayPorts no
    PermitUserRC no

# Included at the TOP of sshd_config, so reset the context for what follows
Match all
```

### Ansible (recommended for real deployments)

A small, self-contained example role lives in
[`deploy/ansible/`](deploy/ansible/): copy the inventory and vars, set
`hostit_domain` and `hostit_admin_token`, and run it. It installs the packages,
writes the configs, hardens sshd, sets up the btrfs loopback (`hostit_btrfs: true`)
and enables the services. Keep secrets in an Ansible Vault.

## Quickstart

On the server itself the CLI needs no configuration: run as root, it talks to the
daemon's unix socket and acts as the global admin. For a remote daemon, point it at
the REST API (`HOSTIT_HOST` + `HOSTIT_TOKEN`, an account or admin token):

```sh
# Only for a REMOTE daemon; locally (as root) the unix socket just works
export HOSTIT_HOST=https://hostit.apps.example.com
export HOSTIT_TOKEN=...

hostit control app add blog                            # reachable through the API only
hostit control app add blog -k ~/.ssh/id_ed25519.pub   # ...plus SSH with your key
hostit control app list
hostit control app deploy blog                         # apply its hostit.yml and start it
hostit control app logs -n 50 blog
hostit control app run blog "cd src && go build -o ../bin/blog ."
hostit control app remove blog                         # deletes user + ALL app data
```

New apps start from a skeleton with a demo page and are started right away, so the
URL serves something immediately. hostit never generates a key pair: an app with no
keys is managed through the API, and SSH starts working as soon as a key is added.

**Let an AI agent build it.** A user creates an app, copies the prompt from its page,
and pastes it into their own Claude Code (or any agent). The token in that prompt is
**scoped to that one app**, and the agent needs no prior knowledge of hostit: `GET
/api/apps/<app>/info` returns the app's state *and* the full instruction set (every
endpoint, the `hostit.yml` format, what is installed). The same verbs run inside the
app's container without the `control app` prefix and without a token (`hostit
deploy`, `hostit status`, `hostit logs -f`, `hostit guide`), where the daemon knows
which app you are from the uid asking. See
[apps-that-think.md](docs/features/apps-that-think.md),
[bring-your-own-agent.md](docs/features/bring-your-own-agent.md) and
[rest-api.md](docs/features/rest-api.md).

## Configuration essentials

All server config is `control.yml` (see the annotated
[`control.yml.example`](control.yml.example)). Only `base-domain` and `admin-token`
are required. Without Google credentials the web login returns 501 and the REST API
plus CLI keep working with the admin token.

**Google login.** Create an OAuth client (Web application) at
<https://console.cloud.google.com/apis/credentials> with JavaScript origin
`https://hostit.<base-domain>` and redirect URI
`https://hostit.<base-domain>/auth/callback`, then:

```yaml
google-client-id: "1234567890-abc123.apps.googleusercontent.com"
google-client-secret: "GOCSPX-..."
admin-emails: [you@example.com]   # become active admins on first login
```

Full details -- roles, per-user limits, the approval queue, "Add a user" and "Allow a
domain" shortcuts -- are in
[docs/features/accounts-roles.md](docs/features/accounts-roles.md).

**Built-in assistant.** The in-browser chat that builds apps is off until the server
has an AI key. Setting either key is the whole switch; the model picker follows from
which keys are set:

```yaml
anthropic-api-key: sk-ant-...       # metered Anthropic API (pay per token)
claude-code-oauth-token: ...        # additionally offer the operator's Claude
                                    # subscription, run per turn as `claude -p`
                                    # in a locked-down sandbox (claude setup-token)
```

Either way the assistant's only tools are one app's own REST surface. Which models
people may pick and who may use it are set per user on the Admin page. See
[docs/features/builtin-assistant.md](docs/features/builtin-assistant.md).

**Wildcard TLS (optional, recommended).** By default hostit obtains one certificate
per app on first request; set `dns-provider: route53` plus the `aws-*` keys (see
`control.yml.example`) and it obtains a single `*.<base-domain>` certificate instead,
so new apps serve HTTPS instantly. The IAM policy, the scoping caveat, and how this
ties into per-app custom domains are in
[docs/features/custom-domains.md](docs/features/custom-domains.md).

## Deploying an app

SSH in as the app user, upload files, describe the app in `hostit.yml` -- either
`mode: static` (hostit serves `public/`) or `mode: app` with a `run:` command (and an
optional `prepare:` build step, which runs on every deploy on the machine that runs
it, so no cross-compiler is needed):

```yaml
mode: app             # your command in the container; MUST listen on 0.0.0.0:$PORT
prepare: cd src && go build -o ../bin/myapp .
run: ./bin/myapp
env: { FOO: bar }
```

Then `hostit deploy` (apply and (re)start; survives reboots), `hostit status`,
`hostit logs -f`, `hostit start|stop|restart` (the `run:` command) and `hostit
poweron|poweroff|reboot` (the container). Changing `env:` recreates the container
(keeps all files and installed packages); other changes only restart the app. Every
app's home has a place for each kind of thing (`public/`, `bin/`, `log/`, `src/`,
`docs/`, `hostit.yml`, `README.md`). Full reference:
[docs/features/deploy.md](docs/features/deploy.md) and
[apps-lifecycle.md](docs/features/apps-lifecycle.md).

## Features

Each links to its full page under [docs/features/](docs/features/):

- [apps-lifecycle.md](docs/features/apps-lifecycle.md) -- create, list, rename, delete, and where files live in an app
- [deploy.md](docs/features/deploy.md) -- `hostit.yml`, the `static` and `app` modes, deploying
- [ssh-access.md](docs/features/ssh-access.md) -- ssh / scp / sftp / rsync into an app's container
- [private-apps.md](docs/features/private-apps.md) -- public vs private apps, viewers and collaborators
- [connections.md](docs/features/connections.md) / [connections-catalog.md](docs/features/connections-catalog.md) -- OAuth accounts and pasted credentials, granted per app and read from the app's own socket
- [mcp-servers.md](docs/features/mcp-servers.md) -- MCP tool servers added by URL; hostit holds the token and makes the calls
- [custom-domains.md](docs/features/custom-domains.md) -- serve an app on your own hostname (DNS-01 certs) plus wildcard TLS setup
- [snapshots-rollback.md](docs/features/snapshots-rollback.md) -- automatic and manual btrfs snapshots, rollback (auto before every deploy and assistant turn)
- [fork.md](docs/features/fork.md) -- duplicate an app from its current state or a snapshot
- [export-download.md](docs/features/export-download.md) -- download a workspace or one snapshot as a .zip / .tar.gz
- [archiving.md](docs/features/archiving.md) -- shelve an app instead of deleting it
- [quotas-limits.md](docs/features/quotas-limits.md) -- hard disk (btrfs qgroup), memory and CPU caps, per-user pools
- [logs.md](docs/features/logs.md) -- the activity feed and live app output
- [builtin-assistant.md](docs/features/builtin-assistant.md) -- the in-browser AI chat that builds apps
- [apps-that-think.md](docs/features/apps-that-think.md) -- an app asking a model a question at runtime, with no key of its own
- [bring-your-own-agent.md](docs/features/bring-your-own-agent.md) -- drive an app with your own agent via a scoped token
- [browser-workspace.md](docs/features/browser-workspace.md) / [terminal.md](docs/features/terminal.md) / [web-dashboard.md](docs/features/web-dashboard.md) -- the in-browser file editor, terminal and dashboard
- [accounts-roles.md](docs/features/accounts-roles.md) -- accounts, roles, invites, approval, admin controls
- [rest-api.md](docs/features/rest-api.md) -- the REST API and account/app tokens

**Connections in one paragraph:** apps often need to act as *you* elsewhere (read
your calendar, post to Slack, query a database). hostit holds the secret so the app
does not: attach it once on the **Connections** page, grant it to apps, revoke it
anytime. An app reads what it holds over its own container API (no token or hostname
needed), e.g. `curl http://127.0.0.1:2586/api/container/connections/work-calendar/token`.
Secrets are sealed with AES-256-GCM under a key beside the database;
`hostit control connections rotate-key` re-seals everything under a fresh key.

## Security model

The boundaries, so it is clear what hostit does and does not promise:

- **Between apps.** Separate Unix users, containers and network stacks. Ports are
  published on loopback and nftables restricts each to root and the owning uid, so
  one app cannot reach another's port even over an SSH tunnel.
- **Between an app and the host.** SSH sessions exec straight into the container;
  there is no host shell. The workload runs as the app's own unprivileged uid
  (container root is mapped to it), so an escape lands on that uid, not on root.
- **Between an app's files and the daemon.** The app owns its whole subvolume; every
  file operation hostit performs there as root goes through chained `os.OpenRoot`s,
  so a symlink out of the subvolume is refused by the kernel rather than followed.
- **Tokens.** An **app-scoped token** can only reach `/api/<its app>/`. An **account
  token** can do anything its owner can. The **admin token** in `control.yml` is
  unlimited -- treat it like a root password.

Full write-up (the web-app/tenant same-origin boundary, `/var/lib/hostit`, the threat
model) is in
[docs/subsystems/security-isolation.md](docs/subsystems/security-isolation.md).

> **Self-hosting disclaimer.** hostit runs as root, terminates TLS, and hands tenants
> root inside their own container. It is provided as-is, with no warranty (see
> [LICENSE](LICENSE)); operators are responsible for their own hardening. The
> boundaries above are the model's promises, not a guarantee against every container
> escape or misconfiguration.

## Notes

- App names are `[a-z][a-z0-9-]*` (max 32 chars), doubling as Unix usernames and DNS
  labels; a reserved-name list blocks `root`, `api`, `www`, etc.
- `tls: off` runs the proxy on plain HTTP (`listen-http`), for development or behind
  an existing TLS-terminating proxy.
- The app CLI talks to the daemon via `/run/hostit/hostit.sock`, authenticated by the
  kernel (SO_PEERCRED); app users can only ever act on their own app.
- The workspace image is built once for the whole host, then exported into a
  read-only base subvolume; each app's container runs its own persistent root
  filesystem, an instant snapshot of that base, so `apt-get` installs survive
  redeploys. On small hosts, give the machine swap: an `apt`-based image build inside
  a container gets OOM-killed on a 512 MB box.
- Scale-out to multiple runner nodes behind one proxy is partly built (control,
  node and proxy are already separate processes); see [TODO.md](TODO.md).

## Development

```sh
make test vet fmt
make web            # build the React app into control/site (embedded at compile time)

# End-to-end tests against a running server (creates and deletes e2e-* apps):
HOSTIT_HOST=https://hostit.apps.example.com HOSTIT_TOKEN=... make e2e
```

Layout follows the ntfy conventions: one thin `main` per binary under `cmd/`
(`control`, `node`, `proxy`, `agent`, `app`), component packages at the root
(`control/`, `node/`, `nodeapi/`, `proxy/`) plus service packages (`agent/`,
`assistant/`, `store/`, `user/`, `client/`, `config/`, ...), and the web app in
`web/` (Vite + React, no UI framework). Internals are documented under
[docs/subsystems/](docs/subsystems/).

### Releasing

The web assets are embedded at compile time (`go:embed control/site`), so a release is
a set of self-contained binaries/`.deb`s.

```sh
make release-snapshot   # local .debs in dist/ (for staging / a dev box)
git tag vX.Y.Z && GITHUB_TOKEN=$(gh auth token) make release   # tag + publish a GitHub release
```

The reference deployment is driven by Ansible with two environments -- a **staging**
host (installs a locally built snapshot) and a **prod** host (pins a released version,
pulls the `.deb`s from the GitHub release). The usual flow is snapshot -> deploy to
staging -> verify -> tag a release -> bump the prod version -> deploy to prod. See
[docs/subsystems/release-and-preflight.md](docs/subsystems/release-and-preflight.md).

## Contributing

Contributions are welcome. Run `make web` before committing any change under `web/`
(the built assets in `control/site` are tracked and embedded at compile time),
`cd web && npm test` for the frontend unit tests, keep to the existing style (ASCII
only; comments explain *why*), and open an issue to discuss larger changes before a
PR. See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Licensed under the [Apache License 2.0](LICENSE).
