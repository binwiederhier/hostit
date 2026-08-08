# TODO

Things worth doing, with enough context to pick up cold. Not a backlog of
everything imaginable -- if it is not written down here it is not planned.

## Multi-node: a proxy node and hosting nodes

Today one machine is everything: it terminates TLS, proxies, holds the registry,
and runs every app. The next step is separating the two roles, so apps can spread
across machines while keeping one front door and one dashboard. The full design,
with flow and sequence diagrams and a 4-phase rollout, is in
`plans/260807-hostit-multinode.md`; this is the summary.

- **Proxy node**: TLS, the web app, the REST API, the registry, placement, and the
  assistant. Owns which app lives where, and proxies to the node hosting it
  instead of to loopback.
- **Hosting nodes**: run a `hostit-agent` that creates Unix users and containers,
  runs apps, and reports state. No public listener of their own.

The chosen shape and the decisions behind it:

- **`NodeAgent` interface, local + remote.** The node-local half of `app.Manager`
  (everything touching `Runner`, `SystemOps`, the `os.OpenRoot` file layer, and
  btrfs) becomes a `NodeAgent` interface with two implementations: `localNodeAgent`
  (today's in-process code) and `remoteNodeAgent` (RPC to another box). A single-box
  install is proxy + one `local` node in the same process -- zero network, no
  behavior change. This is why the split cannot just remote the `Runner`:
  `os.OpenRoot` containment, btrfs reflinks and podman all require the operation to
  happen where the app physically lives, so the whole node-local unit must move.
- **Transport: dedicated internal RPC** (HTTP+JSON behind the `NodeAgent`
  interface, per-node token or mTLS), NOT the public agent REST API -- the internal
  surface is a superset that includes root-level verbs (create a user, rewrite
  authorized_keys, delete a home) that must never be tenant-reachable.
- **Node registry.** `store.App.Host` (today always `store.HostLocal`) becomes a
  node identifier; a new `node` table records address, capacity and health via
  heartbeats. Placement is least-loaded (free memory, disk, app count).
- **Registry stays central; agents are stateless.** The proxy's SQLite store is the
  single source of truth; at create time the hosting node allocates its own local
  port/uid and returns them. Snapshot subvolumes are node-local but the metadata
  lives in the central registry. A stable app **id** (see "Rename an app" below) is
  the natural registry key: name -> id -> (node, port).
- **SSH routing: single front door + ProxyJump.** One SSH endpoint on the proxy;
  `hostit-shell` looks up the app's node and jumps into the right container.
- **State and quota collection** become cross-node: the state cache and the disk
  quota walk currently assume everything is on this box; they move behind the
  agent's heartbeat/report path.

## Web app

The dashboard can create, manage and delete apps and drive them in the browser
(chat, terminal). These round out the in-browser experience.

- **File browser.** `GET /api/apps/{app}/files` already lists a directory and
  `/files/{path}` reads/writes one, so the API is there. A tree/list view with
  view-edit-upload-delete would make small changes (fix a line in `public/`,
  drop in a file) possible without SSH or an agent.
- **Installable PWA.** Make the dashboard (`apps.example.com`) installable to the home
  screen / dock. Add `web/public/manifest.webmanifest` (name, `display: standalone`,
  `theme_color`, `start_url: "/"`, icons) linked from `index.html`; 192/512px + a
  maskable icon generated from a hostit glyph; and a minimal service worker that
  caches the app shell (JS/CSS) but is **network-first** for `/api/*` and app previews
  so it never serves stale data. HTTPS is already in place; everything drops into
  `web/public/` and is embedded into the Go binary. No new runtime deps.
- **Paste-to-upload in the chat.** Drag-and-drop and the "+" button now upload files
  into the app (images also go to the model as vision); add clipboard paste of an
  image as a natural extension.
- **Ask host-vs-build in the new-app modal.** When creating an app, let the owner
  pick their intent: "just host my existing app" or "build one here". The choice
  picks the default app-detail view (below) -- host leans on details/deploy, build
  opens the split chat+preview workspace -- so each person lands in the surface
  that fits what they came to do.
- **Multiple app-detail views, likely as tabs.** The page is one fixed layout
  today (the split chat + preview). Offer a few and let the owner switch: (1) the
  split view (now); (2) a file browser + embedded terminal (edit files, run
  commands, no chat); (3) a details-only view (address, SSH, token, resources);
  (4) a log view (tail of the app's output). Tabs across the top is the obvious
  shape; the new-app intent above sets the initial tab.
- **Use a Claude Max subscription instead of the API (cost).** The built-in
  assistant calls the Anthropic Messages API with the operator's API key, so every
  turn is metered pay-per-token -- which adds up fast. Add an option to drive it
  through the Claude Agent SDK / Claude Code auth so an operator can spend their
  own Claude Max subscription instead of API credit. Would mean an alternate
  `completer` backend (the loop already abstracts the model call behind an
  interface), an auth/token flow for the subscription, and config to pick which
  backend an install uses. Big lever on running cost.
- **Defined behavior when no Anthropic key is set.** The built-in assistant needs
  an Anthropic API key in config; today it's assumed present. Decide and implement
  the no-key path: the assistant endpoints should report "not configured" cleanly
  (not a 500), and the UI should hide or disable the chat surface (and default the
  app-detail view away from the split/chat layout) so a server without a key is
  still fully usable for hosting.
- **Semi-live app previews on the dashboard.** Thumbnails of each app in the list.
  Browser-side screenshotting is out (the app iframe is a different origin, so its
  pixels can't be read). Two workable options: (A) scaled-down live sandboxed
  iframes per card (`transform: scale`, `pointer-events: none`, lazy/visible-only)
  -- client-only, no deps, but every listed app loads, so guard it for many apps;
  (B) server-side headless-Chromium screenshots, cached and refreshed on deploy +
  a timer, served as `/api/apps/{name}/thumbnail` -- scales better but Chromium is
  a heavy dep that breaks the single-binary property, so it'd be an optional
  sidecar. Start with A.

## Smaller things

- **Does the hostit daemon actually need to run as root?** It drives podman,
  systemctl, useradd/usermod, nftables and btrfs -- audit which of those truly
  require root vs could run under a dedicated user with specific capabilities or a
  narrow sudoers grant, and whether the attack surface of a root daemon can be cut.

- **Dev/stage -> promote to prod (the "we work in prod" problem).** Right now the
  only copy of an app is the live one, so every edit (and every assistant change) is
  in production. Give each app an optional **staging** environment -- its own
  container + subdomain (e.g. `stage.<app>.<base>` or `<app>-stage`) sharing nothing
  live -- where changes and deploys land first, then a **Promote** action swaps it
  into prod atomically (blue/green: build/verify on stage, then flip the proxy /
  rename, keeping the old prod as instant rollback). Ties into the fork primitive
  (a stage is a fork that promotes back). Big feature; likely a `hostit promote`
  verb + a store notion of two environments per app.
- **Rename an app (via a stable app id).** Let an owner rename an existing app.
  The name is the app's identity in every layer today -- the `app` PK and five
  `app_name` FKs, the Unix user, home directory, container/unit, subdomain and SSH
  login -- so a name-based rename is a coordinated move across SQLite, the
  filesystem and the OS, with torn-state risk. The better fix is to give each app a
  stable opaque **id** (the uid/port is already a de-facto stable identity) and key
  the durable resources on it, keeping the Unix user named by the (renamable) name
  for SSH legibility. Rename then collapses to `usermod -l` + one DB UPDATE + a
  cache refresh; app-scoped tokens survive it. Costs a one-off per-app migration
  (like `MigrateToBlockUIDs`) and some home/container legibility (mitigated with a
  `by-name` symlink + GECOS/label). Composes with multi-node, where a
  node-independent id is the natural registry key. Full design, comparison and
  phased rollout: `plans/260808-hostit-app-id-identity.md`.
- **Secrets.** `env:` values live in `hostit.yml`, which sits in the app's home
  and is served if someone points a web server at the wrong directory. A real
  secret store (or at least a separate file that is never in `public/`) would
  make it safe to put credentials in an app.
- **Log following.** `GET /api/apps/{app}/logs?lines=N` is a snapshot. An agent
  watching a slow start has to poll.
- **Long jobs.** `POST /api/apps/{app}/run` is bounded at five minutes, so a first
  `npm install` on a small box can outlast it. Anything longer has to become a
  `prepare:` step, which is fine but not obvious.
- **Node.js in the workspace image + per-app image versioning.** Add `nodejs`
  (and npm) to the workspace image so JS apps work out of the box. Image size does
  NOT affect per-app start or reboot time: the image is built/pulled once and lives
  on disk; containers are overlay copy-on-write off it, so creating/starting one is
  a cheap layer, not a copy proportional to size. It only costs one-time build time
  and some shared disk. The catch is existing apps: today every app shares the one
  `hostit-workspace` image, so rebuilding it to add node would change the base under
  apps that have apt-installed their own things -- and those live in the container's
  writable layer, so a later recreate (deploy/reboot) already loses them regardless.
  So the real work is two-fold: (1) version the workspace image (tag by content/date)
  and pin each app to the image tag it was created with, so a new image only affects
  new apps; (2) if we want apt-installed packages to survive recreate at all, they
  need to persist (a per-app overlay/commit, or a documented `prepare:` step that
  reinstalls them on build). Minimum viable: add node to a new image tag, default new
  apps to it, leave existing apps pinned to their current tag.

## Done (recent)

Kept briefly for context; prune when stale.

- Btrfs storage model: per-app home subvolumes, snapshots (manual + auto), rollback
  (atomic, safety-snapshotted), hard qgroup disk quotas (EDQUOT), GFS retention,
  `hostit.yml` snapshot hooks, and **fork** (duplicate an app from a snapshot of its
  home). API + CLI + assistant tools + snapshot dialog.
- Web: in-browser terminal, built-in assistant, dark mode toggle, Apps switcher,
  mobile top-bar fold.
- Custom domains: attach an owner's hostname to an app, with DNS-01 (CNAME-delegated)
  certificates that work even for an internally-deployed server.
- Chat file uploads: drag-and-drop or "+" to save files into the app's uploads/, with
  images shown to the model as vision.
