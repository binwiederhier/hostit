# hostit

**hostit** is a tiny self-hosted mini-app platform, built to be driven by AI agents
(or humans) over SSH and a REST API. One binary. Each app gets:

- its own **container**: SSH sessions land INSIDE it (root in there, `apt install`
  away), the app runs in it, and other apps are invisible -- processes, files and
  loopback ports included (rootless podman + per-Unix-user isolation + nftables)
- **SSH access** (normal ssh/scp/sftp/rsync via the host's sshd; keys via the API)
- a **subdomain** (`myapp.apps.example.com`) with **automatic Let's Encrypt TLS**
- supervised execution: a `run:` command supervised by the hostit agent in the
  default workspace image, or your own `image:`/`build:` container, deployed with
  a single `hostit up`

The intended workflow: tell Claude "create an app on my host and deploy this" --
it calls the REST API to get an SSH login, pushes the code, writes `hostit.yml`,
runs `hostit up`, and the app is live with a cert.

## How it works

```
                      :80/:443
                   +-------------+
 blog.apps.x.com   |   hostit    |  /run/hostit/hostit.sock   hostit up/down/logs
 ----------------> |   serve     | <------------------------  (run by app users)
                   |  (as root)  |
 hostit.apps.x.com |  proxy+API  |  SQLite registry: /var/lib/hostit/hostit.db
 ----------------> +-------------+
                          |
                 useradd, loginctl, authorized_keys
                          v
        /srv/hostit/apps/blog/      <- app user "blog", home 0750
          hostit.yml README.txt     <- scaffolded on creation
          ...app files...           <- uploaded via scp/rsync/git
        systemd --user hostit-app   <- app listens on 127.0.0.1:$PORT
```

The daemon only manages users and proxies traffic; app workloads run unprivileged
as their own user (plain process or rootless podman). Apps listen on loopback
only; the proxy is the sole ingress. TLS certs are issued on demand per
subdomain (certmagic/ACME), restricted to registered apps.

## Install (server)

Requirements: Linux with systemd, sshd. For container mode: `podman` (rootless).

Via the .deb (recommended; ships binary, systemd unit and example config):

```sh
make release-snapshot                         # goreleaser; or: make deb (plain dpkg-deb)
sudo apt install ./dist/hostit_*linux_amd64.deb podman
sudo cp /etc/hostit/server.yml.example /etc/hostit/server.yml
sudo $EDITOR /etc/hostit/server.yml           # set base-domain + admin-token
sudo systemctl enable --now hostit
```

Or manually:

```sh
make build && sudo make install
sudo mkdir -p /etc/hostit
sudo cp server.yml.example /etc/hostit/server.yml
sudo $EDITOR /etc/hostit/server.yml           # set base-domain + admin-token
sudo cp hostit.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now hostit
```

There is also an Ansible role that does all of this (deb, podman, config,
firewall) in `~/Code/ansible`. Releases are built with goreleaser
(`.goreleaser.yml`); `make release` publishes to GitHub once the repo is
pushed there, `scripts/mkdeb.sh` remains as a git-free fallback.

DNS: point a wildcard at the host:

```
apps.example.com.    A  <host-ip>
*.apps.example.com.  A  <host-ip>
```

## Create an app

Via the REST API (`https://hostit.<base-domain>`, Bearer token) or the bundled
client:

```sh
export HOSTIT_HOST=https://hostit.apps.example.com
export HOSTIT_TOKEN=...

hostit admin add blog                       # generates an SSH key pair for you
hostit admin add blog -k ~/.ssh/id_ed25519.pub   # or bring your own key
hostit admin list
hostit admin remove blog                    # deletes user + ALL app data
```

Or with curl:

```sh
curl -s -H "Authorization: Bearer $HOSTIT_TOKEN" \
  -d '{"name": "blog", "ssh_keys": ["ssh-ed25519 AAAA... me@laptop"]}' \
  "$HOSTIT_HOST/v1/apps"
```

The response contains the URL, the SSH login and (if no key was supplied) a
one-time private key. Endpoints: `POST/GET /v1/apps`, `GET/DELETE /v1/apps/{name}`,
`PUT /v1/apps/{name}/keys`, `GET /v1/health`.

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
hostit up          # (re)deploy + start (systemd user unit, survives reboots)
hostit status      # service status
hostit logs -f     # follow logs
hostit restart     # restart
hostit down        # stop + disable
hostit info        # name, URL, port
```

## Notes

- App names are `[a-z][a-z0-9-]*` (max 32 chars), doubling as Unix usernames and
  DNS labels; a reserved-name list blocks `root`, `api`, `www`, etc.
- `tls: off` runs the proxy on plain HTTP (`listen-http`), for development or
  behind an existing TLS-terminating proxy.
- The app CLI talks to the daemon via `/run/hostit/hostit.sock`, authenticated by
  the kernel (SO_PEERCRED); app users can only ever see their own app.
- Scale-out to multiple runner hosts behind one proxy is sketched in
  `plans/260803-hostit.md` (the registry has a `host` column for this), but only
  single-host is implemented.

## Development

```sh
make test vet fmt
```

Layout follows the ntfy conventions: thin `main.go`, CLI wiring in `cmd/`, service
packages at the root (`server/`, `app/`, `store/`, `appctl/`, `client/`, `config/`).
