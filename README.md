# hostit

[![CI](https://github.com/binwiederhier/hostit/actions/workflows/ci.yml/badge.svg)](https://github.com/binwiederhier/hostit/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

**hostit** is a tiny self-hosted mini-app platform, built to be driven by AI agents
(or humans) over SSH and a REST API. One binary. Each app gets:

- its own **container**: SSH sessions land INSIDE it (root in there, `apt install`
  away), the app runs in it, and other apps are invisible -- processes, files,
  networks and loopback ports included (podman with a per-app uid mapping, so
  container root is the app's own unprivileged user, plus nftables)
- **SSH access** (normal ssh/scp/sftp/rsync via the host's sshd; keys via the API)
- a **subdomain** (`myapp.apps.example.com`) with **automatic Let's Encrypt TLS**
- two ways to run: `mode: static` (hostit serves `public/`) or `mode: app` (your
  command, supervised by the hostit agent) -- deployed with a single `hostit deploy`
- a workspace with **python3, go, Node.js (with npm), PHP and sqlite3**
  preinstalled (and root, so `apt-get install` anything else you need)

Multi-user: people sign in with Google, an admin approves them from a small web
app, and each user gets their own apps within admin-adjustable limits (app count,
container memory, soft disk quota). Per-user API tokens make the same REST API and
CLI available to their agent, which is the point: `hostit apps add myapp` with a
user token is all an AI agent needs.

The intended workflow: tell your agent "create an app on my host and deploy this"
-- it calls the REST API to get an SSH login, pushes the code, writes `hostit.yml`,
runs `hostit deploy`, and the app is live with a cert. The same thing can happen
entirely in the browser: each app's page is a workspace with tabbed views -- a
built-in AI assistant, a file editor with a live preview, a terminal, snapshots,
an activity/output log, and settings (see [Building in the browser](#building-in-the-browser)).

## How it works

One binary, running as root, is the whole control plane: it terminates TLS,
proxies each subdomain to its app, serves the web app and REST API, and creates
the Unix users and containers behind them.

Each app is four things created together: a Unix user, a home directory, a
podman container whose root is mapped to that user's unprivileged uid, and a
loopback port that nftables restricts to that uid. SSH logins are handed to
`/usr/bin/hostit-shell`, which execs the session into the app's container, so
users never get a host shell, and an escape inside the container lands on the
app's own uid rather than on root.

**[ARCHITECTURE.md](ARCHITECTURE.md)** has the diagrams: the components, what
isolates what, and sequence diagrams for creating an app, serving a request,
logging in over SSH, and an agent deploying.

## Install (server)

Requirements: a Linux host with systemd, sshd, `podman` (plus `uidmap`, `passt` or
`slirp4netns`, `dbus-user-session`), `nftables`, and `btrfs-progs`. hostit must run
as **root**, and its app homes must be on **btrfs** (both are mandatory). On start
it preflights these: it refuses to run if it is not root, if a required command is
missing, or if the app-homes path is not btrfs, naming exactly what to fix.

Via the .deb (ships the binary, `hostit-shell`, a systemd unit and an example
config):

```sh
make release-snapshot                         # goreleaser; or: make deb (plain dpkg-deb)
sudo apt install ./dist/hostit_*linux_amd64.deb podman
sudo cp /etc/hostit/server.yml.example /etc/hostit/server.yml
sudo $EDITOR /etc/hostit/server.yml           # set base-domain + admin-token
sudo systemctl enable --now hostit
```

Or manually:

```sh
make web && make build && sudo make install
sudo mkdir -p /etc/hostit
sudo cp server.yml.example /etc/hostit/server.yml
sudo $EDITOR /etc/hostit/server.yml
sudo cp hostit.service /etc/systemd/system/ && sudo cp hostit-shell /usr/bin/
sudo systemctl daemon-reload && sudo systemctl enable --now hostit
```

DNS: point a wildcard at the host (both records are required):

```
apps.example.com.    A  <host-ip>
*.apps.example.com.  A  <host-ip>
```

Releases are built with goreleaser (`.goreleaser.yml`); `scripts/mkdeb.sh` is a
git-free fallback.

### Deploying and updating

Everything the daemon needs is one config file plus the package, so a deploy is
"install the `.deb`, drop `/etc/hostit/server.yml`, harden sshd, enable the
service", and an update is just installing the newer package:

```sh
sudo dpkg -i hostit_<version>_linux_amd64.deb   # --force-confold to keep your config
sudo systemctl restart hostit
```

Because that is all it takes, an Ansible role (or any config-management tool) is
a natural fit, and the recommended way to run this for real: it makes the config,
the sshd drop-in, and the btrfs setup reproducible. A small, self-contained
example role lives in [`deploy/ansible/`](deploy/ansible/) -- copy the inventory
and vars, set `hostit_domain` and `hostit_admin_token`, and run it. Keep
`admin-token` and any OAuth/AI secrets in an Ansible Vault, not in plain vars.

### One thing the package cannot do for you

Add this to `sshd_config` (a drop-in in `/etc/ssh/sshd_config.d/` works) and
restart sshd:

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

App users log in for one reason: to reach their own container. Forwarding is the
one thing sshd offers that reaches past it -- a tenant can otherwise tunnel to
the cloud metadata service (on DigitalOcean that includes `user-data`, which
often carries secrets) or probe host-local services. scp, sftp and rsync are
unaffected.

## Security model

What each boundary is, so it is clear what hostit does and does not promise:

- **Between apps.** Separate Unix users, separate containers, separate network
  stacks. Ports are published on loopback and nftables restricts each to root and
  the owning uid, so one app cannot reach another's port even over an SSH tunnel.
- **Between an app and the host.** SSH sessions exec straight into the container;
  there is no host shell. The workload runs as the app's own unprivileged uid
  (container root is mapped to it), so an escape lands on that uid, not on root.
  `/var/lib/hostit` is root-only: it holds every app's agent token in the clear
  (deliberately, so the app's page can show it again) and the session signing key.
- **Between an app's files and the daemon.** The app owns its home directory, so
  every file operation hostit performs there as root goes through `os.OpenRoot`:
  a symlink out of the home is refused by the kernel rather than followed. That
  includes reading `hostit.yml` itself, which the tenant controls -- so pointing
  it at a symlink cannot walk the daemon out of the app.
- **Between tenants and the web app.** Apps are subdomains of the web app, which
  `SameSite=Lax` does not separate, so cookie-authenticated writes require a
  same-origin signal and the session cookie carries the `__Host-` prefix. Files
  read back through the API are always downloads, never rendered.

An **app-scoped token** can only reach `/api/<its app>/`. An **account token**
can do anything its owner can. The **admin token** in `server.yml` is unlimited
and belongs to the operator -- treat it like a root password.

> **Self-hosting disclaimer.** hostit runs as root, terminates TLS, and hands
> tenants root inside their own container. It is provided as-is, with no warranty
> (see [LICENSE](LICENSE)); operators are responsible for their own hardening --
> the `sshd_config` drop-in below, keeping podman/nftables current, and their host
> baseline. The boundaries above are the model's promises, not a guarantee against
> every container escape or misconfiguration.

## Users, roles and limits

- First Google login creates a **pending** account; an admin approves it (or the
  email is in `admin-emails`, which auto-creates an active admin).
- **Dashboard**: create/delete your apps, see usage vs limits, and copy the
  "use with your AI agent" snippet.
- **Profile**: SSH keys (they grant access to *all* your apps) and API tokens.
- **Admin**: approve/deny users, change roles, per-user limits, global defaults,
  and the two ways to skip the approval queue below.

### Letting people in without approving each one

Approving every sign-up by hand does not scale past a handful of people, so
admins have two shortcuts:

- **Add a user** (Admin -> Users): creates an approved account for an email
  address before its owner has ever signed in. Their first Google login finds it
  and fills in the name.
- **Allow a domain** (Admin -> Sign-up without approval): anyone signing in with
  a Google address in that domain is approved on the spot, so a whole company can
  onboard itself. Write it as `company.com` or `*@company.com`.

An allowed domain approves, it never promotes: those accounts are ordinary users
with the usual limits. Someone an admin has explicitly denied stays denied even
if their domain is allowed later, and removing a domain does not touch the
accounts already approved under it -- revoking access stays a per-user decision.

Limits are `app_limit` (enforced at create), `memory_mb` (podman `--memory`,
cgroup-enforced) and `disk_mb`. Disk is a **soft** quota: the daemon measures
each app periodically, shows usage, and stops apps that exceed it. ext4 without
project quotas cannot hard-cap, so nothing blocks a write mid-flight.

Without Google credentials configured, the web login returns 501 and the REST API
plus CLI keep working with the admin token.

### Setting up Google login

1. Go to <https://console.cloud.google.com/apis/credentials> and pick or create a
   project.
2. Configure the OAuth consent screen if prompted: External (or Internal for a
   Workspace org), an app name, your support/developer email. The default scopes
   (`openid`, `email`, `profile`) are all hostit needs, so no verification is
   required; "Testing" works if you add testers.
3. **Create credentials -> OAuth client ID -> Web application**:
   - Authorized JavaScript origin: `https://hostit.<base-domain>`
   - Authorized redirect URI: `https://hostit.<base-domain>/auth/callback`
4. Put the client ID and secret in `/etc/hostit/server.yml`, together with the
   emails that should be admins, and restart hostit:

   ```yaml
   google-client-id: "1234567890-abc123.apps.googleusercontent.com"
   google-client-secret: "GOCSPX-..."
   admin-emails:
     - you@example.com
   ```

Those admin emails become active admins on their first login; everyone else
lands in "pending" until an admin approves them under Admin -> Users.

### Wildcard TLS (optional, recommended)

By default hostit obtains one Let's Encrypt certificate **per app**, on that
app's first HTTPS request. Configure a DNS provider instead and it obtains a
single wildcard certificate for `*.<base-domain>`:

- new apps serve HTTPS instantly, with no ACME round-trip on the first request
- unknown subdomains reach the proxy (and its 404 page) instead of failing the
  TLS handshake
- the "50 certificates per registered domain per week" rate limit stops mattering

Wildcards require DNS-01 validation, so hostit needs permission to write TXT
records in your zone. AWS Route 53 is supported today:

```yaml
dns-provider: route53
aws-region: us-east-1
aws-access-key-id: "AKIA..."
aws-secret-key: "..."
aws-hosted-zone-id: "Z0123456789ABCDEFGHIJ"   # optional, saves a lookup
```

Credentials may also come from the usual AWS environment variables or an
instance role; leave the fields empty in that case. A minimal IAM policy, with
`<ZONE>` being your hosted zone ID:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    { "Effect": "Allow",
      "Action": ["route53:ListHostedZones", "route53:ListHostedZonesByName"],
      "Resource": "*" },
    { "Effect": "Allow",
      "Action": "route53:GetChange",
      "Resource": "arn:aws:route53:::change/*" },
    { "Effect": "Allow",
      "Action": ["route53:ChangeResourceRecordSets", "route53:ListResourceRecordSets"],
      "Resource": "arn:aws:route53:::hostedzone/<ZONE>" }
  ]
}
```

AWS cannot restrict `ChangeResourceRecordSets` to `_acme-challenge` names, so
this key can write any record in that zone. If that is too broad, delegate the
app subdomain to its own hosted zone and scope the policy to that one. Verify
with:

```sh
echo | openssl s_client -connect anything.apps.example.com:443 \
  -servername anything.apps.example.com 2>/dev/null | openssl x509 -noout -subject
# subject=CN = *.apps.example.com
```

Blanking the DNS settings returns hostit to per-app certificates; certificates
already in `<data-dir>/certs` keep working either way.

### Built-in assistant (optional)

The in-browser chat that builds and changes an app is off until the server has an
AI key. Two ways to power it, both configured in `server.yml`:

```yaml
# Metered Anthropic API (pay per token):
anthropic-api-key: sk-ant-...
assistant-model: claude-sonnet-5

# Additionally offer Claude.ai (a Claude Pro/Max subscription), run per turn as
# `claude -p` in a locked-down podman sandbox. Setting the token is the whole
# switch; there is no backend selector. Get the token with: claude setup-token
claude-code-oauth-token: ...
```

Either way the assistant's only tools are one app's own REST surface, mediated by
the daemon's peercred socket, so a turn can never touch another app or the host.
Which models people may pick, the default, and who may use the assistant are set
per user on the Admin page. Leave all of this unset and the chat UI hides itself;
SSH and your own agent still work.

## Where things go in an app

Every app's home has a place for each kind of thing, so neither a person nor an
agent has to guess:

```
public/      files served on the web -- static mode serves exactly this
bin/         binaries and scripts the app runs (run: ./bin/myapp)
log/         the app's output, written by hostit ("hostit logs" reads it)
src/         source, if the app keeps its source on the host
docs/        the app's own documentation, kept current by whoever changes it
hostit.yml   how the app runs
README.md    what the app is, and its worklog
```

Directories appear as you write into them. `mode: static` serves `public/`, and
only `public/`.

**Keep the source here.** Put it in `src/` and give `hostit.yml` a build step:

```yaml
mode: app
prepare: cd src && go build -o ../bin/myapp .
run: ./bin/myapp
```

`prepare:` runs before the app starts, on every deploy; a failed build leaves the
running app alone and puts the error in the logs. It builds on the machine that
runs it, so nobody needs a cross-compiler or a toolchain of their own -- which is
the point when the person deploying is talking to an assistant rather than a
terminal. It also keeps the app editable: the next session has source to work
with, not just a binary. Uploading a prebuilt binary to `bin/` still works and is
faster. The agent guide at `/api/apps/{app}/info` says all of this too.

## Create an app

Via the REST API (`https://hostit.<base-domain>`, Bearer token: the global admin
token or a user's own token) or the bundled client:

```sh
export HOSTIT_HOST=https://hostit.apps.example.com
export HOSTIT_TOKEN=...

hostit apps add blog                            # reachable through the API only
hostit apps add blog -k ~/.ssh/id_ed25519.pub   # ...plus SSH with your key
hostit apps list
hostit apps deploy blog                         # apply its hostit.yml and start it
hostit apps start|stop|restart blog
hostit apps logs -n 50 blog
hostit apps run blog "cd src && go build -o ../bin/blog ."
hostit apps remove blog                         # deletes user + ALL app data
```

Or with curl:

```sh
curl -s -H "Authorization: Bearer $HOSTIT_TOKEN" \
  -d '{"name": "blog", "ssh_keys": ["ssh-ed25519 AAAA... me@laptop"]}' \
  "$HOSTIT_HOST/api/apps"
```

The response contains the URL and the SSH login. hostit never generates a key
pair: an app with no keys is managed through the API, and SSH starts working as
soon as a key is added to the owner's profile. New apps are scaffolded with a
demo page and started right away, so the URL serves something immediately.

Everything lives under `/api`. One app's own endpoints are under
`/api/apps/{app}/` -- which is exactly what an app-scoped token may reach, so
the shape of the URL is the shape of the permission. Account and admin endpoints
are `/api/account` (+ `/keys`, `/tokens`), `/api/apps`, `/api/users`,
`/api/domains`, `/api/settings`, and `/api/health`.

## Let an AI agent run an app

This is what hostit is for: a user creates an app in the web app, copies the
prompt from its page, and pastes it into their own Claude Code (or any agent).
An account token drives every app you own through these commands; an app token
drives only its own app. The same commands run inside an app's container without
`apps` and without a token (`hostit deploy`, `hostit logs -f`, `hostit guide`), where
the daemon knows which app you are from the uid asking.

The token in that prompt is **scoped to that one app**, so it cannot touch the
user's other apps, their account, or anything admin.

The agent needs no prior knowledge of hostit, and the prompt is three lines:
`GET /api/apps/<app>/info` returns the app's state *and* the full instruction set
(every endpoint, the `hostit.yml` format, what is installed), so one URL plus
one token is the whole briefing. `GET /api/info` returns the same guide
without an app, for account-wide tokens. In shell terms:

```sh
export H=https://hostit.apps.example.com/api
export T=hostit_...        # created with the app, shown on the app's page

curl -H "Authorization: Bearer $T" $H/info              # how this all works
curl -H "Authorization: Bearer $T" $H/apps/myapp/info    # README, files, config, state
curl -H "Authorization: Bearer $T" "$H/apps/myapp/files?path=public"  # one directory
curl -H "Authorization: Bearer $T" "$H/apps/myapp/files/README.md?stat=1"  # size/mtime/MIME, no body

curl -X PUT -H "Authorization: Bearer $T" --data-binary @index.html \
     $H/apps/myapp/files/public/index.html                    # upload one file
curl -X PUT -H "Authorization: Bearer $T" --data-binary @myapp \
     "$H/apps/myapp/files/myapp?mode=755"                    # ...executable, for run: mode
tar cf - . | curl -X POST -H "Authorization: Bearer $T" \
     -H "Content-Type: application/x-tar" --data-binary @- $H/apps/myapp/files  # upload a tree
curl -X POST -H "Authorization: Bearer $T" -H "Content-Type: application/json" \
     -d '{"path":"assets"}' $H/apps/myapp/mkdir                # make a directory
curl -X POST -H "Authorization: Bearer $T" -H "Content-Type: application/json" \
     -d '{"from":"a.txt","to":"public/a.txt"}' $H/apps/myapp/move   # move/rename

curl -H "Authorization: Bearer $T" $H/apps/myapp/events   # activity log (create, deploy, snapshot, ...)

curl -X POST -H "Authorization: Bearer $T" -H "Content-Type: application/json" \
     -d '{"command":"cd src && go build -o ../bin/myapp ."}' $H/apps/myapp/run
curl -X POST -H "Authorization: Bearer $T" $H/apps/myapp/deploy    # apply hostit.yml, (re)start
curl -H "Authorization: Bearer $T" "$H/apps/myapp/logs?lines=50"   # why is it not up?
curl -X PUT -H "Authorization: Bearer $T" -H "Content-Type: application/json" \
     -d '{"readme":"# myapp\n\nWhat this is."}' $H/apps/myapp/readme
```

New apps start as a **stub**: a placeholder page served in `mode: static`, whose
README says so and lists what is installed. Each app's `README.md` is its
description and worklog: the agent reads it first
and writes back what it changed, so the next session (or a different agent)
knows what the app is. hostit's own instructions are not a file in the app --
they are the SSH login banner, `hostit guide`, and `/docs` -- so nothing in the
app directory competes with the app's own README. Agents are also asked to keep a one-line `description:` in
`hostit.yml`; the app's page puts it into the prompt, so the next agent starts
from what the app already is instead of from "this is a placeholder".

Actions are POST-only (app verbs `start`, `stop`, `restart`; container verbs
`poweron`, `poweroff`, `reboot`; and `deploy`); a GET answers 405 rather than
doing anything. SSH still works for anyone who prefers scp/rsync:
profile keys are written into a `# BEGIN hostit-managed keys` block in each
app's `authorized_keys`, so keys added there by hand are never clobbered.

Tokens come in two shapes. An **account token** (Profile -> API tokens) manages
everything you own, including creating apps. An **app token** is created with
each app, shown on its page, and can only touch that one app -- that is the one
that goes into a chat window.

## Building in the browser

You do not need to leave the browser. Each app's page is a workspace with tabs:

- **Assistant** -- a built-in AI chat that reads and writes the app's files, runs
  commands in its container, and deploys, beside a **live preview** on a draggable
  split. It is the same REST surface an external agent drives, but hosted: the turn
  runs server-side and streams back, so it survives a reload and shows up on every
  device viewing the app; the conversation is persisted per app. It needs an
  Anthropic API key in the server config; without one, the tab is hidden and apps
  run over SSH/CLI as usual.
- **Files** -- an in-browser editor with a file tree (upload, rename, move, delete,
  new file/folder), syntax highlighting, and an optional live preview pane. It
  reuses the file REST endpoints, so no SSH is needed for a quick change.
- **Terminal** -- the same login shell an SSH session gets, over a WebSocket, inline
  (with pop-out and fullscreen). "Connect via SSH" shows the `ssh`/`scp` command.
- **Snapshots** -- a timeline of point-in-time snapshots with rollback, fork and
  delete, and a "Take snapshot" action.
- **Logs** -- an activity log of who did what to the app (create, snapshot,
  rollback, domain, lifecycle) above a live tail of the app's own output.
- **Settings** -- the app's URLs (including any verified custom domain), SSH, API
  token, description and custom domains.

The top bar shows **live CPU / RAM / disk**, the app's address, and the lifecycle
actions (start/stop/restart, power on/off, reboot, fork, delete).

The sparkle button on the page hands you the same paste-into-your-own-agent
prompt, so the browser assistant and an external Claude Code are interchangeable.

## Snapshots, rollback and quotas

When the app-homes filesystem is **btrfs**, each app's home is a copy-on-write
subvolume, which unlocks two things:

- **Snapshots and rollback.** A snapshot is an instant, space-shared copy of the
  app's files. hostit takes one automatically before every deploy, before every
  assistant turn, and hourly; you (or an agent) can also take a labelled one on
  purpose. Rolling back restores the home to a snapshot -- and takes a safety
  snapshot of the current state first, so a rollback is itself undoable. All
  snapshots are thinned by a grandfather-father-son policy (the last 50, plus daily
  for a week, weekly for a month, monthly for a quarter) -- none is kept forever.

  ```sh
  hostit apps snapshot myapp "before the rewrite"   # save a restorable point
  hostit apps snapshots myapp                        # list them, newest first
  hostit apps rollback myapp <snapshot-id>           # restore (safety-snapshotted)
  hostit apps rmsnapshot myapp <snapshot-id>         # delete one by hand
  ```

  The built-in assistant has the same abilities (it snapshots before risky work and
  can roll back -- reversible, so it runs without a confirmation step), and so does
  the REST API (`GET`/`POST /api/apps/{app}/snapshots`,
  `POST .../snapshots/{id}/restore`, `DELETE .../snapshots/{id}`).

  Optionally quiesce a database around a snapshot with hooks in `hostit.yml`:

  ```yaml
  snapshot:
    pre:  "sqlite3 data/app.db \".backup data/app.snap.db\""   # flush before
    post: "rm -f data/app.snap.db"                              # clean up after
  ```

- **Fork.** Duplicate an app into a new one, seeding its home from a copy of the
  source's files -- either its current state or a specific snapshot. The fork gets its
  own subdomain, Unix user and container, and the two run independently from there.
  Reached from the snapshot menu on the app page (including a per-snapshot "Fork"),
  the CLI, or the REST API:

  ```sh
  hostit apps fork myapp myapp-copy                  # seed from myapp's current files
  hostit apps fork myapp myapp-copy <snapshot-id>    # seed from a specific snapshot
  ```

  (`POST /api/apps/{app}/fork` with `{"new_name": "...", "snapshot_id": "..."}`; the
  snapshot id is optional.)

- **Hard disk quotas.** The app's `disk_mb` limit is enforced by a btrfs qgroup, so
  a write past it fails immediately (EDQUOT) instead of the app being stopped later
  by a periodic sweep.

Setting this up is a one-off: a btrfs image on a loopback file, mounted at the
app-homes path -- see [Development](#development). It needs no extra block device.

## Custom domains

An app answers on its `<app>.<base-domain>` subdomain out of the box; attach the
owner's own hostname on top of it. Reached from the app page's Actions menu, the
CLI, or the REST API:

```sh
hostit apps domain add myapp blog.example.com   # prints the two DNS records to create
hostit apps domain list myapp                    # status: pending / active / error
hostit apps domain verify myapp blog.example.com # re-check DNS and (re)issue the cert
hostit apps domain rm myapp blog.example.com
```

The owner creates two DNS records at **their** provider (both plain CNAMEs, so any
provider works):

1. **Traffic** -- `blog.example.com` -> `myapp.<base-domain>` (or an A record to the
   server at a zone apex, where CNAME is not allowed).
2. **TLS challenge delegation** -- `_acme-challenge.blog.example.com` ->
   `_acme-challenge.acme.<base-domain>` (the same fixed target for every domain).

hostit then obtains a Let's Encrypt certificate over **DNS-01**, writing the
challenge TXT into the operator's own zone (the same Route53 setup that issues the
wildcard). Because validation is via public DNS, **this works even when the server
is not reachable from the internet** -- the CA never connects to the box. The owner
never shares DNS credentials; the delegation CNAME is also the proof of control.
(`GET`/`POST /api/apps/{app}/domains`, `POST .../domains/{domain}/verify`,
`DELETE .../domains/{domain}`.)

## Deploy an app

SSH in as the app user, upload files, describe the app in `hostit.yml`:

```yaml
# mode: static -- hostit serves public/. Nothing to install, nothing to run.
mode: static
```

```yaml
# mode: app -- your command in the workspace container;
# it MUST listen on 0.0.0.0:$PORT
mode: app
run: ./server --debug
env:
  FOO: bar
```

Then:

```sh
hostit deploy      # apply hostit.yml and (re)start; survives reboots
hostit status      # is it running?
hostit logs -f     # follow logs

# App verbs act on the run: command; the container keeps running:
hostit start       # start the run: command
hostit stop        # stop it (container stays up, SSH still works)
hostit restart     # restart it (fast; no container recreate)

# Power verbs act on the container itself:
hostit poweroff    # stop the container (stays off across reboots)
hostit poweron     # start it again
hostit reboot      # recreate/restart the container

hostit info        # name, URL, port
```

Changing `env:` recreates the container (which kicks active SSH sessions, like
docker); changing `mode:`, `prepare:` or `run:` only restarts the app inside it. Keys hostit does not know are an error, so a typo is reported rather than
quietly ignored.

## Notes

- App names are `[a-z][a-z0-9-]*` (max 32 chars), doubling as Unix usernames and
  DNS labels; a reserved-name list blocks `root`, `api`, `www`, etc.
- `tls: off` runs the proxy on plain HTTP (`listen-http`), for development or
  behind an existing TLS-terminating proxy.
- The app CLI talks to the daemon via `/run/hostit/hostit.sock`, authenticated by
  the kernel (SO_PEERCRED); app users can only ever act on their own app.
- The workspace image is built once for the whole host. The earlier rootless
  model forced a per-app copy (~40s and ~230 MB each), which is why the image is
  now shared across apps.
- On small hosts, give the machine swap: an `apt`-based image build inside a
  container gets OOM-killed on a 512 MB box.
- Scale-out to multiple runner hosts behind one proxy is on the roadmap (the
  registry already has a `host` column for it; see [TODO.md](TODO.md)), but only
  single-host is implemented today.

## Development

```sh
make test vet fmt
make web            # build the React app into server/site (embedded at compile time)

# End-to-end tests against a running server (creates and deletes e2e-* apps):
HOSTIT_HOST=https://hostit.apps.example.com HOSTIT_TOKEN=... make e2e
```

Layout follows the ntfy conventions: thin `main.go`, CLI wiring in `cmd/`, service
packages at the root (`server/`, `app/`, `agent/`, `assistant/`, `store/`, `user/`,
`appctl/`, `client/`, `config/`), and the web app in `web/` (Vite + React, no UI
framework). The `assistant/` package is the in-browser AI agent: a loop over the
Anthropic Messages API whose tools are scoped to one app.

### Releasing and environments

The web assets are embedded at compile time (`go:embed server/site`), so a release
is a single self-contained binary/`.deb`.

```sh
make release-snapshot   # local .deb in dist/ (for staging / a dev box)
git tag vX.Y.Z && GITHUB_TOKEN=$(gh auth token) make release   # tag + publish a GitHub release
```

**btrfs is required**: hostit refuses to start unless the app-homes path is on a
btrfs filesystem, because snapshots, rollback, fork and hard disk quotas are core.
The Ansible role sets it up behind `hostit_btrfs: true`: it creates a btrfs image
on a loopback file (75% of free space), mounts it at `/var/lib/hostit/apps` via a
systemd unit, and migrates existing homes into subvolumes once. No extra block
device is needed.

The reference deployment is driven by Ansible with two environments -- a **staging**
host and a **prod** host, each its own machine and base domain. Staging installs a
locally built snapshot `.deb`; prod pins a released version and pulls the `.deb`
from the GitHub release. The usual flow is snapshot -> deploy to staging -> verify
-> tag a release -> bump the prod version -> deploy to prod. (Per-app dev/stage
environments -- building a change on a staging copy of one app and promoting it to
that app's prod -- are on the roadmap; see [TODO.md](TODO.md).)

## Contributing

Contributions are welcome. To build and check locally:

```sh
make web            # build the React app into server/site (embedded at compile time)
make test vet fmt   # Go tests, go vet, and gofmt
cd web && npm test  # frontend unit tests (vitest)
```

Please run `make web` before committing any change under `web/`, since the built
assets in `server/site` are tracked and embedded at compile time. Keep to the
existing style (see the Go conventions in the package layout above; ASCII only,
comments explain *why*). Open an issue to discuss larger changes before a PR.

## License

Licensed under the [Apache License 2.0](LICENSE).
