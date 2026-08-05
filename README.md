# hostit

**hostit** is a tiny self-hosted mini-app platform, built to be driven by AI agents
(or humans) over SSH and a REST API. One binary. Each app gets:

- its own **container**: SSH sessions land INSIDE it (root in there, `apt install`
  away), the app runs in it, and other apps are invisible -- processes, files,
  networks and loopback ports included (podman with a per-app uid mapping, so
  container root is the app's own unprivileged user, plus nftables)
- **SSH access** (normal ssh/scp/sftp/rsync via the host's sshd; keys via the API)
- a **subdomain** (`myapp.apps.example.com`) with **automatic Let's Encrypt TLS**
- two ways to run: `mode: static` (hostit serves `public/`) or `mode: app` (your
  command, supervised by the hostit agent) -- deployed with a single `hostit up`
- a workspace with **python3, node/npm, php-cli, go and sqlite3** preinstalled
  (and root, so `apt-get install` anything else)

Multi-user: people sign in with Google, an admin approves them from a small web
app, and each user gets their own apps within admin-adjustable limits (app count,
container memory, soft disk quota). Per-user API tokens make the same REST API and
CLI available to their agent, which is the point: `hostit apps add myapp` with a
user token is all an AI agent needs.

The intended workflow: tell your agent "create an app on my host and deploy this"
-- it calls the REST API to get an SSH login, pushes the code, writes `hostit.yml`,
runs `hostit up`, and the app is live with a cert.

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

Requirements: Linux with systemd, sshd, and `podman` (plus `uidmap`, `passt` or
`slirp4netns`, `dbus-user-session`, `nftables`).

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
git-free fallback. Configuration management is left to you -- everything the
daemon needs is one config file plus the package, so an Ansible role or similar
is a natural fit.

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
and belongs to the operator.

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
`apps` and without a token (`hostit up`, `hostit logs -f`, `hostit guide`), where
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

curl -X PUT -H "Authorization: Bearer $T" --data-binary @index.html \
     $H/apps/myapp/files/public/index.html                    # upload one file
curl -X PUT -H "Authorization: Bearer $T" --data-binary @myapp \
     "$H/apps/myapp/files/myapp?mode=755"                    # ...executable, for run: mode
tar cf - . | curl -X POST -H "Authorization: Bearer $T" \
     -H "Content-Type: application/x-tar" --data-binary @- $H/apps/myapp/files

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

Actions are POST-only (`start`, `stop`, `restart`, `deploy`); a GET answers 405
rather than doing anything. SSH still works for anyone who prefers scp/rsync:
profile keys are written into a `# BEGIN hostit-managed keys` block in each
app's `authorized_keys`, so keys added there by hand are never clobbered.

Tokens come in two shapes. An **account token** (Profile -> API tokens) manages
everything you own, including creating apps. An **app token** is created with
each app, shown on its page, and can only touch that one app -- that is the one
that goes into a chat window.

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
hostit up          # apply changes and (re)start; survives reboots
hostit status      # service status
hostit logs -f     # follow logs
hostit restart     # restart
hostit down        # stop + disable
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
  model forced a per-app copy (~40s and ~230 MB each); the analysis behind the
  change is in `plans/260804-hostit-image-sharing.md`.
- On small hosts, give the machine swap: an `apt`-based image build inside a
  container gets OOM-killed on a 512 MB box.
- Scale-out to multiple runner hosts behind one proxy is sketched in
  `plans/260803-hostit.md` (the registry has a `host` column for it), but only
  single-host is implemented.

## Development

```sh
make test vet fmt
make web            # build the React app into server/site (embedded at compile time)

# End-to-end tests against a running server (creates and deletes e2e-* apps):
HOSTIT_HOST=https://hostit.apps.example.com HOSTIT_TOKEN=... make e2e
```

Layout follows the ntfy conventions: thin `main.go`, CLI wiring in `cmd/`, service
packages at the root (`server/`, `app/`, `agent/`, `store/`, `user/`, `appctl/`,
`client/`, `config/`), and the web app in `web/` (Vite + React, no UI framework).
