# TODO

Things worth doing, with enough context to pick up cold. Not a backlog of
everything imaginable -- if it is not written down here it is not planned.

## Bugs

### App-process dot flickers, and "Stop app" does not stop the app

Reported after the app-process-state work (v0.2.9). Two problems, likely
related:

- **Dot flickers on action.** Clicking a menu item makes the dot jump
  orange -> green -> orange. The optimistic flip in `lifecycle()` sets
  `app_running` immediately, then `load()` reconciles against the state cache
  (`CachedStates`), which is still serving the pre-action value for up to a
  TTL, so the dot bounces back before the settle-poll (`scheduleCatchUp`)
  catches up. Need to either not reconcile from a known-stale cache right after
  an action, or hold the optimistic value until the settle poll confirms.
- **"Stop app" does not actually stop the app.** The dot ends orange (so the
  agent wrote `stopped` to `log/state`) but the app keeps serving. That points
  at the breadcrumb and the real stop being decoupled: `Pause()` writes
  `stopped` but the `run:` child is not being killed in prod. Suspect the
  signal path -- daemon `StopApp` -> `podman kill --signal USR1` -> agent PID 1
  -> `Pause()` -> kill child -- is not delivering or not killing (the unit test
  `TestAgentPauseAndResume` passes, so it is environment-specific). Also "some
  actions don't work" -- check whether the conditional menu is sending the verb
  the daemon expects for each state.

Repro: open an app, Stop app, watch the dot bounce, then confirm the app URL
still responds.

### Terminal pop-out flashes the app chrome on a white page

The popped-out terminal (`/app/:name/terminal`, `TerminalRoute` rendering
`AppTerminal` with `fullPage`) briefly shows the normal top menu bar on a white
background before the terminal mounts. It should be a bare dark page from the
first paint: no nav bar, dark background. Likely the app shell / router layout
(or the Suspense fallback for the lazy `AppTerminal`) renders before the
full-page terminal route takes over. Give the pop-out route its own chrome-less,
dark layout and a matching Suspense fallback so there is nothing to flash.

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

## Web app

The dashboard can create, manage and delete apps, but working *inside* an app
still means SSH or an external agent. These bring that into the browser.

- **Terminal in the browser.** The app page shows an `ssh <app>@host` command;
  a real terminal (xterm.js talking to a WebSocket the daemon bridges to
  `podman exec -it` in the app's container) would give the owner a shell without
  leaving the dashboard. Same isolation as `hostit-shell`: exec into the app's
  own container as its uid, cookie-auth on our origin, scoped to an owned app.
- **File browser.** `GET /api/apps/{app}/files` already lists a directory and
  `/files/{path}` reads/writes one, so the API is there. A tree/list view with
  view-edit-upload-delete would make small changes (fix a line in `public/`,
  drop in a file) possible without SSH or an agent.
- **Optional built-in agent (claude-code-style).** A chat panel on the app page
  that drives the app's own REST API with its app-scoped token -- the pasteable
  prompt, but hosted -- so a non-technical owner can say "make me a landing page"
  without wiring up an external agent. Needs an LLM backend + config; the app
  token already confines it to one app, which is most of the scoping done.

## Hard disk quotas

Today the quota is soft: a background walk measures each app's home
periodically and stops the container once it is over. So an app can write
several GB and only later get shut down -- the writes are not actually
prevented, and the box can fill in between checks. It should refuse the write
at the limit (EDQUOT) instead.

- **ext4 project quotas** are the fit (the box is ext4): enable `prjquota`,
  give each app home a project id and a block limit, and writes past it fail
  immediately. Container root is the app's host uid, and writes land as that
  uid in the bind-mounted home, so the quota follows the files correctly.
- Alternatives if project quotas are awkward: a per-app fixed-size loopback
  ext4 image mounted at the home (hard cap, heavier), or moving app data onto
  XFS with project quotas.
- Either way `disk_mb` becomes the enforced limit rather than the shut-down
  threshold; the periodic walk stays only for *reporting* usage, and the
  "stop when over quota" behaviour goes away.

## Smaller things

- **Custom domains for apps.** Let an app answer on the owner's own hostname
  (e.g. `blog.example.com`) as well as its `<app>.<base-domain>` subdomain. Needs
  a `domain` per app in the store, the proxy matching it (`appNameFromHost` only
  knows the base domain today), on-demand TLS allowing it (`allowTLSHost`), and a
  DNS/verification step so someone cannot claim a domain they do not control.
- **Secrets.** `env:` values live in `hostit.yml`, which sits in the app's home
  and is served if someone points a web server at the wrong directory. A real
  secret store (or at least a separate file that is never in `public/`) would
  make it safe to put credentials in an app.
- **Log following.** `GET /api/apps/{app}/logs?lines=N` is a snapshot. An agent
  watching a slow start has to poll.
- **Long jobs.** `POST /api/apps/{app}/run` is bounded at five minutes, so a first
  `npm install` on a small box can outlast it. Anything longer has to become a
  `prepare:` step, which is fine but not obvious.
