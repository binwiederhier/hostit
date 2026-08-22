# Running hostit on a development machine

Building hostit needs nothing unusual (see [CONTRIBUTING.md](../CONTRIBUTING.md)).
*Running* it does: hostit creates Unix users, drives podman, systemd, nftables and
btrfs, and it runs as root. That is not something to point at your laptop, so this
page is about standing up a throwaway instance you can break.

## What the machine needs

A Linux box you have root on, with:

| | |
|---|---|
| **btrfs** | mandatory, not optional -- snapshots and per-app disk quotas are btrfs subvolumes and qgroups. A loopback image is fine (below). |
| **podman >= 4.3** | earlier versions lack the `--rootfs <path>:idmap` syntax every app container uses. |
| **crun >= 1.29** | Ubuntu 24.04 ships 1.14.1, which hard-fails on an idmapped rootfs. Drop in the static release binary. |
| **nftables, systemd** | `nft` and `systemctl` are shelled out to directly. |

macOS and WSL are out. Use a VM (multipass, libvirt, a cloud instance you destroy
afterwards) -- and prefer that to your workstation anyway, since hostit takes over
uid ranges, systemd units and nftables chains.

`hostit-control serve` checks all of this before it starts and names what is
missing, so the first run doubles as the check.

## A btrfs filesystem, without repartitioning

A loopback image is enough for development:

```sh
truncate -s 20G /var/lib/hostit.img
mkfs.btrfs /var/lib/hostit.img
mkdir -p /var/lib/hostit
mount -o loop /var/lib/hostit.img /var/lib/hostit
```

Add it to `/etc/fstab` if you want it back after a reboot. To start over, unmount
and re-`mkfs` -- everything hostit owns lives under that mount plus its systemd
units.

## Configuration

Copy the examples and edit them:

```sh
install -Dm600 control.yml.example /etc/hostit/control/control.yml
install -Dm600 node.yml.example    /etc/hostit/node/node.yml
install -Dm600 proxy.yml.example   /etc/hostit/proxy/proxy.yml
```

For a single-machine development instance, the settings that matter in
`control.yml`:

```yaml
base-domain: apps.example.test   # anything you can resolve; see DNS below
admin-token: <a long random string>
admin-emails: you@example.com    # your account becomes an admin on first login
tls: off                         # no ACME, no certificates, plain HTTP
listen-api: 127.0.0.1:2900       # the API on a plain port, no hostname routing
breakglass: true                 # sign in without Google, see below
```

`listen-api` matters for development specifically. The main listener routes by
hostname -- it decides between the web app and an app's container from the `Host`
header -- so `127.0.0.1:2586` matches nothing and answers "nothing deployed
here". `listen-api` serves the API directly with no hostname involved, which is
what tooling and the web dev server should talk to.

**Never set `breakglass: true` on anything reachable from the internet.** It is
gated behind the admin token, which already carries full admin rights, but there
is no reason to expose the endpoint on a real instance.

### DNS

Apps live at `<app>.<base-domain>`, so a development box needs a wildcard that
resolves. Easiest options, in order of laziness:

- Use a `.test` domain and add entries to `/etc/hosts` per app as you create them.
- Point `*.apps.example.test` at the VM in your own resolver (dnsmasq: `address=/apps.example.test/192.0.2.10`).
- Use a real wildcard DNS record for a domain you own, if the box is reachable.

Nothing about the code cares which you pick; without wildcard DNS you simply
cannot open an app by its hostname.

## Running it

The `.deb` is the path of least resistance, because it brings the systemd units,
the sudoers rule and the helper binaries with it:

```sh
make release-snapshot                          # builds packages into dist/
sudo dpkg -i --force-confold dist/hostit-control_*_amd64.deb \
                             dist/hostit-node_*_amd64.deb \
                             dist/hostit-proxy_*_amd64.deb
sudo systemctl start hostit-control hostit-node hostit-proxy
journalctl -fu hostit-control
```

`make build install` puts only the `hostit` CLI in `/usr/bin`, which is enough
for iterating on the CLI but not for running the daemons.

`hostit control status` should then show one control, one node and one proxy, and
`hostit control app list` an empty registry.

## Signing in without Google

A development instance has no Google OAuth credentials, so ordinary login is not
available. With `breakglass: true` and the admin token, mint a session directly:

```sh
curl -c cookies.txt -X POST \
  -H "Authorization: Bearer $HOSTIT_ADMIN_TOKEN" \
  "http://127.0.0.1:2900/auth/breakglass?email=you@example.com"
```

That sets the same session cookie a real login would. It will only sign in an
account that already exists, except for an address in `admin-emails`, which it
creates the way a first Google login would. Every browser end-to-end test uses
this same endpoint.

## The web app

The React app is embedded into the binary at build time, so the plain loop is
`make web` and restart control. For a fast loop with hot reload, run the dev
server and let it forward the API to your instance:

```sh
cd web
HOSTIT_DEV_TARGET=http://127.0.0.1:2900 npm run dev   # http://localhost:3000
```

`/api` and `/auth` are proxied (websockets included, so the browser terminal and
the assistant stream work). Remember `make web` before committing anything under
`web/`: the built assets in `control/site` are what ship in the binary.

## Tests

```sh
make test vet fmt          # Go
cd web && npm test         # frontend units (vitest)
```

The browser end-to-end tests drive a *running* instance and create and delete real
apps, so point them at your development box and never at anything you care about:

```sh
cd web
npm run test:e2e:install   # one-time, fetches Chromium
HOSTIT_BASE_URL=http://127.0.0.1:2900 HOSTIT_ADMIN_TOKEN=... npm run test:e2e
```

## Starting over

```sh
systemctl stop hostit-control hostit-node hostit-proxy
hostit control app list          # delete apps first if you want their users gone cleanly
rm -rf /var/lib/hostit/*         # registry, app homes, snapshots, certs
```

Deleting the data directory behind hostit's back leaves the per-app Unix users
and systemd units behind; removing the apps first is tidier. Re-`mkfs` the
loopback image for a guaranteed clean slate.
