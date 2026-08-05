# TODO

Things worth doing, with enough context to pick up cold. Not a backlog of
everything imaginable -- if it is not written down here it is not planned.

## Multi-node: a proxy node and hosting nodes

Today one machine is everything: it terminates TLS, proxies, holds the registry,
and runs every app. The next step is separating the two roles, so apps can spread
across machines while keeping one front door and one dashboard.

- **Proxy node**: TLS, the web app, the REST API, the registry. Owns which app
  lives where, and proxies to the node hosting it instead of to loopback.
- **Hosting nodes**: create Unix users and containers, run apps, report state.
  No public listener of their own.

What has to exist before this works:

- **Node registry.** `store.App` already carries a `host` column, currently
  always `"local"` (`store.HostLocal`). It becomes a node identifier, and a
  `node` table records each node's address, capacity and health.
- **Node-to-node authentication.** The proxy calls hosting nodes over the
  network, so the unix-socket SO_PEERCRED trick does not carry: mutual TLS or a
  shared token, and every current "run this locally" path grows a remote variant.
- **Placement.** Which node gets a new app: free memory, free disk, app count.
  Simple to start (least-loaded), but it needs somewhere to live.
- **Lifecycle over the wire.** `app.Manager` runs podman and systemctl through a
  `Runner`. That interface is the seam: a remote implementation that speaks to a
  hosting node's agent would leave the rest of the manager untouched.
- **SSH routing.** `hostit-shell` execs into a local container. With apps
  elsewhere, an SSH session has to reach the right node -- either sshd on each
  hosting node with the proxy handing out the address, or a jump through the
  proxy.
- **State and quota collection** become cross-node: the state cache and the disk
  quota walk currently assume everything is on this box.

The plan predating this file is `~/Code/plans/260804-hostit-multiuser.md`.

## Smaller things

- **Secrets.** `env:` values live in `hostit.yml`, which sits in the app's home
  and is served if someone points a web server at the wrong directory. A real
  secret store (or at least a separate file that is never in `public/`) would
  make it safe to put credentials in an app.
- **Log following.** `GET /api/apps/{app}/logs?lines=N` is a snapshot. An agent
  watching a slow start has to poll.
- **Long jobs.** `POST /api/apps/{app}/run` is bounded at five minutes, so a first
  `npm install` on a small box can outlast it. Anything longer has to become a
  `prepare:` step, which is fine but not obvious.
