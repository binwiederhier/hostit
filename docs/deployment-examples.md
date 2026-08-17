# Deployment examples

Two shapes, end to end: everything on one machine, and a three-machine cluster
with two app nodes. Every key below exists in the config structs
(`controlconf`, `nodeconf`, `proxy`); commented-out lines show defaults.

The parts that are the same in both: apps are served at `<app>.<base-domain>`,
which needs a wildcard DNS record, and `admin-token` is a bearer token you
generate yourself (`openssl rand -hex 24`).

---

## 1. One droplet

All four components on one machine. This is the default shape, and the one
that needs the least configuration: control runs the machine half in-process,
and the proxy reads credentials control mints for it. Nothing is enrolled and
no certificates are copied anywhere.

```
                        one droplet
  :443/:80  ->  hostit-proxy  ->  hostit-control (:2910, in-process node)
                                       |
                                  app containers
```

**DNS**

```
*.apps.example.com.  A  <droplet-ip>
apps.example.com.    A  <droplet-ip>
```

**`/etc/hostit/control/control.yml`**

```yaml
base-domain: apps.example.com
admin-token: CHANGE-ME               # openssl rand -hex 24

# The proxy terminates TLS and forwards here over loopback. behind-proxy
# replaces the TLS listener entirely -- control still manages the certificates,
# and hands them to the proxy over the cluster link.
behind-proxy: true
listen-http: 127.0.0.1:2910

# Optional: web login. Without it hostit is token-only, with no web app.
# google-client-id: ...
# google-client-secret: ...
# admin-emails:
#   - you@example.com

# Defaults, shown for orientation:
# data-dir: /var/lib/hostit
# apps-dir: /var/lib/hostit/apps
# socket-file: /run/hostit/hostit.sock
# listen-node: ""                    # empty: the machine half runs in this
#                                    # process; the cluster listener still comes
#                                    # up on 127.0.0.1:2930 for the proxy
```

**`/etc/hostit/proxy/proxy.yml`**

```yaml
# Where control's HTTP listener is: the dashboard/API upstream and the
# unknown-host fallback.
control-url: http://127.0.0.1:2910

# Everything else is defaulted for a colocated proxy:
# proxy-id: local
# cluster-url: 127.0.0.1:2930                          # control's cluster listener
# proxy-cert-file: /var/lib/hostit/ipc/proxy-local.pem  # minted by control
# proxy-key-file: /var/lib/hostit/ipc/proxy-local.key
# cluster-ca-cert-file: /var/lib/hostit/ipc/ca.pem
# listen-https: ":443"
# listen-http: ":80"
# cache-dir: /var/lib/hostit-proxy
```

**No node config at all.** The `hostit-node` *daemon* does not run in this
shape -- control does the machine work in-process -- but the `hostit-node`
*package* is still required: it carries the `hostit` CLI that gets bind-mounted
into every container, the per-app systemd unit, the login shell and the sudoers
grant. Installing it does not enable the daemon (the postinst deliberately
leaves that to the operator), so all three packages go on this machine and only
`hostit-control` and `hostit-proxy` are enabled.

---

## 2. Three droplets (control + proxy, two nodes)

Control and the proxy share the first machine; two machines run nothing but
apps. Nodes dial control -- control never dials a node -- so the nodes need no
inbound ports except from the proxy, to the app port range.

```
                internet
                   |
             :443/:80
      +----------------------+
      |  droplet 1           |
      |  hostit-proxy        |
      |  hostit-control      |
      +----------------------+
         |                 |
         | 2930 (mTLS)     | 10000-19999 (app traffic)
         |                 |
  +--------------+   +--------------+
  |  droplet 2   |   |  droplet 3   |
  |  hostit-node |   |  hostit-node |
  +--------------+   +--------------+
```

Addresses used below: droplet 1 `10.0.0.1`, droplet 2 `10.0.0.2`, droplet 3
`10.0.0.3`, all on a private network. DNS still points at droplet 1 only.

### Droplet 1 -- control + proxy

**`/etc/hostit/control/control.yml`**

```yaml
base-domain: apps.example.com
admin-token: CHANGE-ME

behind-proxy: true
listen-http: 127.0.0.1:2910

# The cluster listener, where nodes AND proxies dial in. Setting it to a
# reachable address is what makes this a control-only daemon: no machine work
# happens here, and apps live on whichever node connects.
listen-node: 10.0.0.1:2930
```

**`/etc/hostit/proxy/proxy.yml`** -- same as the single-box one. The proxy is
colocated with control, so it still reads the credentials control mints:

```yaml
control-url: http://127.0.0.1:2910
# cluster-url: 127.0.0.1:2930
# proxy-cert-file: /var/lib/hostit/ipc/proxy-local.pem
# proxy-key-file: /var/lib/hostit/ipc/proxy-local.key
# cluster-ca-cert-file: /var/lib/hostit/ipc/ca.pem
```

### Enroll each node (run on droplet 1)

```bash
hostit-control node add --address 10.0.0.2 worker-1
hostit-control node add --address 10.0.0.3 worker-2
```

Each prints three PEM blocks -- the node's certificate, its key, and the
cluster CA. Save them on the node as `/etc/hostit/node/node.pem`, `node.key`
and `cluster-ca.pem` (mode 0600, owned by root). There is no join protocol and
no token: possession of the pair plus the registry row is membership, and
`hostit-control node remove <name>` revokes it.

### Droplets 2 and 3 -- nodes

**`/etc/hostit/node/node.yml`** (droplet 2; droplet 3 differs only in
`node-id` and `apps-bind-address`)

```yaml
node-id: worker-1
control-url: 10.0.0.1:2930

node-cert-file: /etc/hostit/node/node.pem
node-key-file: /etc/hostit/node/node.key
cluster-ca-cert-file: /etc/hostit/node/cluster-ca.pem

# Where this node publishes app ports. A remote node must publish on a real
# interface: the proxy is on another machine and cannot reach loopback here.
apps-bind-address: 10.0.0.2

# Who may reach those app ports. The proxies -- control never dials an app.
# The node's nftables rules accept these and drop everything else, which is
# the only thing protecting the app ports: proxy-to-app traffic is plain HTTP.
apps-allowed-addresses:
  - 10.0.0.1

# Defaults:
# data-dir: /var/lib/hostit
# apps-dir: /var/lib/hostit/apps
# socket-file: /run/hostit/hostit.sock
```

Droplet 3 is the same with `node-id: worker-2` and
`apps-bind-address: 10.0.0.3`.

### Firewall

The nodes must accept, from droplet 1 only:

- nothing on 2930 (nodes DIAL control, they do not listen)
- `10000-19999` for app traffic from the proxy

Droplet 1 must accept `2930` from the nodes, plus `:80`/`:443` from the world.

### A remote proxy

If you later move the proxy to its own machine, it enrolls exactly like a
node:

```bash
hostit-control proxy add edge-1      # on the control host
```

and its config names the credentials it printed:

```yaml
proxy-id: edge-1
control-url: http://10.0.0.1:2910
cluster-url: 10.0.0.1:2930
proxy-cert-file: /etc/hostit/proxy/proxy.pem
proxy-key-file: /etc/hostit/proxy/proxy.key
cluster-ca-cert-file: /etc/hostit/proxy/cluster-ca.pem
```

Note what this exposes: control's `listen-http` (2910) is plain HTTP, and so
is proxy-to-app traffic. Both are fine on a private network, and neither is
fine across the public internet.

---

## Which packages go where

| Machine | Packages |
|---|---|
| One droplet | `hostit-control`, `hostit-node`, `hostit-proxy` |
| Control + proxy | `hostit-control`, `hostit-proxy` |
| A node | `hostit-node` |

`hostit-node` carries the `hostit` CLI, the per-app systemd unit and the
sudoers rules, so it belongs on every machine that runs apps. The three
packages conflict with the pre-split `hostit` package and replace it.

## Verifying a cluster

```bash
hostit-control node list      # each node, with its address and last-seen
hostit-control proxy list     # each proxy, with its last-seen
```

A node or proxy that has connected shows a recent `last seen`. Control logs
`Node connected` / `Proxy connected` when a member dials in, and pushes each
its configuration immediately.
