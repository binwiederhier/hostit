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
- **Bug: assistant tool-group count flickers.** In the chat transcript, when a
  collapsed tool group grows (e.g. "2 actions" -> "3 actions" as the next tool
  streams in), the chip flickers. Likely the group re-renders/remounts as the
  streamed items are folded into a group in `renderTranscript`/`ToolGroup`
  (AppAssistant.jsx) -- stabilise the group's key/identity so the count updates
  in place instead of the chip briefly disappearing.
- **Rename "Dashboard" to "Apps", and make it an app switcher.** The nav link is
  really the app list, so call it "Apps". Turn it into a dropdown that lists the
  owner's apps (with status dots) so you can jump straight to any app -- especially
  handy from an app detail page -- instead of going back to the list first. The
  item still navigates to the list on click; the caret opens the switcher.
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
- **Defined behavior when no Anthropic key is set.** The built-in assistant needs
  an Anthropic API key in config; today it's assumed present. Decide and implement
  the no-key path: the assistant endpoints should report "not configured" cleanly
  (not a 500), and the UI should hide or disable the chat surface (and default the
  app-detail view away from the split/chat layout) so a server without a key is
  still fully usable for hosting.
- **Small screens: fold the app top bar into the nav.** On an app detail page,
  phones currently show two stacked bars -- the hostit logo nav, then the app's own
  back+name+controls bar -- which eats vertical space. On small screens put the
  back button and app name where the hostit logo sits (replacing it) so there is a
  single top bar; the avatar menu still gets you home.
- **Dark mode.** styles.css already defines dark tokens under
  `@media (prefers-color-scheme: dark)`, so the app half-follows the OS today.
  Finish it: audit every surface for contrast (the terminal/preview panes, the
  gradient avatar, the resource gauges, code/pre blocks), add an explicit
  light/dark/system toggle (in the profile menu) that persists and stamps
  `data-theme` on the root, and make sure published-artifact-style embeds and the
  iframe preview do not clash.
- **Semi-live app previews on the dashboard.** Thumbnails of each app in the list.
  Browser-side screenshotting is out (the app iframe is a different origin, so its
  pixels can't be read). Two workable options: (A) scaled-down live sandboxed
  iframes per card (`transform: scale`, `pointer-events: none`, lazy/visible-only)
  -- client-only, no deps, but every listed app loads, so guard it for many apps;
  (B) server-side headless-Chromium screenshots, cached and refreshed on deploy +
  a timer, served as `/api/apps/{name}/thumbnail` -- scales better but Chromium is
  a heavy dep that breaks the single-binary property, so it'd be an optional
  sidecar. Start with A.

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

- **Rollback: source tracking or snapshots.** After the assistant (or the owner)
  changes an app, there is no undo. Give people a way back, either:
  (A) **automated source tracking** -- keep the app's home under git and auto-commit
  on every deploy/assistant change (and maybe every write_file), so history is
  visible and any point is restorable (`git revert`/checkout); cheap, diffable, and
  natural for code, but not for large data/binaries; or
  (B) **snapshots/backups** -- periodic (and pre-deploy) snapshots of the app's home
  (btrfs/zfs subvolume snapshot, or a tarball to object storage) that can be
  restored wholesale; covers data too, but coarser and heavier. A good first cut is
  A for the source plus a pre-deploy snapshot as a safety net, surfaced in the UI as
  "restore to <time>".
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
