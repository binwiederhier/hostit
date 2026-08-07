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

## Web app

The dashboard can create, manage and delete apps and drive them in the browser
(chat, terminal). These round out the in-browser experience.

- **File browser.** `GET /api/apps/{app}/files` already lists a directory and
  `/files/{path}` reads/writes one, so the API is there. A tree/list view with
  view-edit-upload-delete would make small changes (fix a line in `public/`,
  drop in a file) possible without SSH or an agent.
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

- **Dev/stage -> promote to prod (the "we work in prod" problem).** Right now the
  only copy of an app is the live one, so every edit (and every assistant change) is
  in production. Give each app an optional **staging** environment -- its own
  container + subdomain (e.g. `stage.<app>.<base>` or `<app>-stage`) sharing nothing
  live -- where changes and deploys land first, then a **Promote** action swaps it
  into prod atomically (blue/green: build/verify on stage, then flip the proxy /
  rename, keeping the old prod as instant rollback). Ties into the fork primitive
  (a stage is a fork that promotes back). Big feature; likely a `hostit promote`
  verb + a store notion of two environments per app.
- **Rename an app.** Let an owner rename an existing app. The name is the app's
  identity today -- subdomain, Unix user, home directory, container name, TLS cert,
  authorized_keys, the app-scoped token's `app_name`, the assistant session -- so a
  rename has to move or reissue all of them atomically (or refuse if the app is
  running). Simplest first cut: stop the app, rename user + home + container +
  store row + re-point DNS/cert on next request, restart. Consider keeping the old
  subdomain as a redirect for a while so links do not break.
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
