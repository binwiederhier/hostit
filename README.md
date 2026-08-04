# hostit

**hostit** is a tiny self-hosted mini-app platform, built to be driven by AI agents
(or humans) over SSH and a REST API. One binary. Each app gets:

- its own **container**: SSH sessions land INSIDE it (root in there, `apt install`
  away), the app runs in it, and other apps are invisible -- processes, files,
  networks and loopback ports included (podman with a per-app uid mapping, so
  container root is the app's own unprivileged user, plus nftables)
- **SSH access** (normal ssh/scp/sftp/rsync via the host's sshd; keys via the API)
- a **subdomain** (`myapp.apps.example.com`) with **automatic Let's Encrypt TLS**
- supervised execution: a `run:` command supervised by the hostit agent in the
  default workspace image, or your own `image:`/`build:` container, deployed with
  a single `hostit up`

Multi-user: people sign in with Google, an admin approves them from a small web
app, and each user gets their own apps within admin-adjustable limits (app count,
container memory, soft disk quota). Per-user API tokens make the same REST API and
CLI available to their agent, which is the point: `hostit admin add myapp` with a
user token is all an AI agent needs.

The intended workflow: tell your agent "create an app on my host and deploy this"
-- it calls the REST API to get an SSH login, pushes the code, writes `hostit.yml`,
runs `hostit up`, and the app is live with a cert.

## How it works

```
                        :80/:443 (TLS: wildcard, or per app on demand)
                     +--------------------+
  blog.apps.x.com -> |   hostit serve     | ->  proxies to 127.0.0.1:<port>
hostit.apps.x.com -> |   (root daemon)    |     web app + REST API
                     +--------------------+
                       |            ^
        useradd, keys, |            | /run/hostit/hostit.sock
        nftables, unit |            | (peercred: the kernel says who calls)
                       v            |
   app user "blog" (own uid) -------+
     home: /srv/hostit/apps/blog    <- 0750; scp/rsync/sftp write here
     systemd hostit-app@blog        -> podman start --attach hostit-app-blog
       +--------------- container "hostit-app-blog" ----------------------+
       |  uidmap 0:<blog's uid>:1  <- container root IS the app's user    |
       |  PID 1: hostit agent -> runs the `run:` command on 0.0.0.0:$PORT |
       |  /home/blog  <- the home above, bind-mounted                     |
       |  /usr/bin/hostit + /run/hostit/hostit.sock <- so the CLI works   |
       |  own network stack (slirp4netns): no peers, no host loopback     |
       +------------------------------------------------------------------+
```

SSH logins are handed to `/usr/bin/hostit-shell`, which execs the session into
the app's container, so users never get a host shell. The daemon creates and
runs the containers (it is the only thing that runs as root), mapping container
root to the app's own unprivileged uid: files in the bind-mounted home belong to
the app on both sides, and a workload escape lands on that uid, not on root.
Entering a container needs root podman, so app users reach it through
`/usr/bin/hostit-enter`, a root helper behind a sudoers grant scoped to the
`hostit-apps` group; it derives the target container from `SUDO_UID` and ignores
its arguments when choosing one, so you can only ever enter your own app.

Each app has its own network stack (slirp4netns), ports are published on
loopback only, and nftables restricts those ports to root and the owning uid, so
apps cannot reach each other. Because containers are created centrally, there is
one image store for the host: the workspace image is built once (~40s) instead of
per app, and an app's home holds only its own files.

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

## Users, roles and limits

- First Google login creates a **pending** account; an admin approves it (or the
  email is in `admin-emails`, which auto-creates an active admin).
- **Dashboard**: create/delete your apps, see usage vs limits, and copy the
  "use with your AI agent" snippet.
- **Profile**: SSH keys (they grant access to *all* your apps) and API tokens.
- **Admin**: approve/deny users, change roles, per-user limits, global defaults.

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

## Create an app

Via the REST API (`https://hostit.<base-domain>`, Bearer token: the global admin
token or a user's own token) or the bundled client:

```sh
export HOSTIT_HOST=https://hostit.apps.example.com
export HOSTIT_TOKEN=...

hostit admin add blog                            # generates an SSH key pair for you
hostit admin add blog -k ~/.ssh/id_ed25519.pub   # or bring your own key
hostit admin list
hostit admin remove blog                         # deletes user + ALL app data
```

Or with curl:

```sh
curl -s -H "Authorization: Bearer $HOSTIT_TOKEN" \
  -d '{"name": "blog", "ssh_keys": ["ssh-ed25519 AAAA... me@laptop"]}' \
  "$HOSTIT_HOST/v1/apps"
```

The response contains the URL, the SSH login and (if no key was supplied) a
one-time private key. New apps are scaffolded with a demo page and started right
away, so the URL serves something immediately.

API endpoints: `POST/GET /v1/apps`, `GET/DELETE /v1/apps/{name}`,
`PUT /v1/apps/{name}/keys`, `GET /v1/account` (+ `/keys`, `/tokens`),
`GET/PATCH /v1/users/{id}` and `/v1/settings` for admins, `GET /v1/health`.

## Deploy an app

SSH in as the app user, upload files, describe the app in `hostit.yml`:

```yaml
# Workspace mode: run a command in your workspace container;
# it MUST listen on 0.0.0.0:$PORT
run: ./server --debug
env:
  FOO: bar
```

```yaml
# Image mode: run your own image; hostit maps the port automatically
image: docker.io/library/nginx:alpine   # or "build: ." with a Dockerfile
container-port: 80
volumes:
  - ./data:/data
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

Changing `image:`, `build:`, `env:` or `volumes:` recreates the container (which
kicks active SSH sessions, like docker); changing `run:` only restarts the app
process inside it.

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
```

Layout follows the ntfy conventions: thin `main.go`, CLI wiring in `cmd/`, service
packages at the root (`server/`, `app/`, `agent/`, `store/`, `user/`, `appctl/`,
`client/`, `config/`), and the web app in `web/` (Vite + React, no UI framework).
