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
  lives in the central registry. The app's stable **id** (already built; see Done
  below) is the natural registry key: name -> id -> (node, port).
- **SSH routing: single front door + ProxyJump.** One SSH endpoint on the proxy;
  `hostit-shell` looks up the app's node and jumps into the right container.
- **State and quota collection** become cross-node: the state cache and the disk
  quota walk currently assume everything is on this box; they move behind the
  agent's heartbeat/report path.

## Web app

The dashboard can create, manage and delete apps and drive them in the browser
(chat, terminal). These round out the in-browser experience.

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
  sets the initial app-detail tab -- host leans on details/deploy, build opens the
  split chat+preview workspace -- so each person lands in the surface that fits what
  they came to do.
- **Redesign the public error page and the placeholder app (both are weak).** The
  "nothing here" 404 page (`server/errorpage.go`, currently a card with a little
  jump game) and the placeholder page a new app serves (`cmd/placeholder.go`,
  currently a guestbook) both look poor and need a proper redesign, ideally sharing
  one visual language with the dashboard. Keep the 404's free-vs-stopped pages
  identical and keep the placeholder a real running backend (it proves an app can
  execute code).
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

## Assistant (AI chat) cost

The built-in assistant calls the Anthropic Messages API with the operator's API
key, so every turn is metered pay-per-token and adds up fast. Per-user/per-app
token and dollar usage is now tracked (admin user list), which is the visibility
these levers act on. Prompt caching is already in place: the system prompt, tool
definitions and the conversation prefix are cache-marked, so repeat turns pay the
~10x-cheaper cache-read rate. The remaining levers, roughly by impact:

- **Claude Max / subscription backend (biggest lever).** Add an option to drive the
  assistant through the Claude Agent SDK / Claude Code auth so an operator spends
  their own Claude Max subscription instead of API credit. An alternate `completer`
  backend (the loop already abstracts the model call behind an interface), an
  auth/token flow for the subscription, and config to pick the backend per install.
- **Model routing.** Default is `claude-sonnet-5` ($3/$15 per M in/out). Route
  simple turns (small edits, questions) to Haiku (~10x cheaper) and escalate to
  Sonnet only for hard work. Even a crude heuristic (cheap model unless the last
  turn used tools heavily / the ask is large) saves a lot.
- **Longer cache TTL for the stable prefix.** The default ephemeral cache is ~5 min;
  after a pause the next turn re-pays full input on the large, stable system prompt +
  tools. Use the 1-hour cache for that prefix so it stays warm across a real session.
- **Tune the thinking / effort budget.** Adaptive thinking bills at the output rate
  ($15/M). Dial effort down for routine turns; reserve high effort for hard ones.
- **Compact the transcript.** Context is capped at recent turns, but long sessions
  still grow the cached prefix (cache writes cost 1.25x). Summarize old turns into a
  short recap to keep the prefix small.
- **Spend caps.** Now that usage is tracked, add a per-user or per-app budget that
  warns (or soft-stops the built-in chat) at a threshold, turning the usage data
  into cost control, not just visibility.

## Smaller things

- **Terminal auto-reconnect.** The in-browser terminal's WebSocket drops on any
  blip (network change, laptop sleep, an app restart/redeploy recreating the
  container) and today just dies, leaving a dead pane. Reconnect automatically on
  an unexpected close with incremental backoff capped at 60s, show a countdown
  timer in the pane while waiting ("reconnecting in Ns..."), and add a manual
  "Reconnect" button in the terminal bar (top right, next to SSH/pop-out/fullscreen)
  to retry immediately. Applies to both the embedded terminal tab and the
  full-page pop-out.

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
- **Secrets.** `env:` values live in `hostit.yml`, which sits in the app's home
  and is served if someone points a web server at the wrong directory. A real
  secret store (or at least a separate file that is never in `public/`) would
  make it safe to put credentials in an app.
- **Log following.** `GET /api/apps/{app}/logs?lines=N` is a snapshot. An agent
  watching a slow start has to poll.
- **Long jobs.** `POST /api/apps/{app}/run` is bounded at five minutes, so a first
  `npm install` on a small box can outlast it. Anything longer has to become a
  `prepare:` step, which is fine but not obvious.
- **Persist apt-installed packages across a container recreate.** Packages a tenant
  `apt-get install`s live in the container's writable layer, so a deploy/reboot that
  recreates the container loses them. Options: a per-app overlay/commit, or a
  documented `prepare:` step that reinstalls them on every build. (The image-version
  half of this -- pinning each app to the image tag it was built with -- is done; see
  Done below.)

## Done (recent)

Kept briefly for context; prune when stale.

- **App modularization (refactor rounds 1 & 2).** `app/` split into single-purpose
  service packages -- `btrfs/`, `systemd/`, `container/`, `ssh/`, `unixuser/`, `run/`
  (shared Runner), `retention/` (tested GFS policy) -- composed by `app.Manager`.
  Over-quota shutdown removed (btrfs qgroups enforce it). Server handlers regrouped
  into `server/server_handler_<topic>.go` over the service packages; floating test
  files folded to mirror their source. CLI cleaned up: `hostit internal assistant`
  (was `assistant-poc`), internal commands hidden, regexes moved to their packages,
  scaffold + error page + workspace Containerfile moved to `go:embed`. Assistant
  package owns the Claude/Anthropic + subscription-sandbox integration. Web: app
  name in the title, fixed the stuck rename snackbar, always-fresh preview pane
  (proxy no-store on `?hostit_preview=`), and a friendlier "paused" step-limit note.

- **App id + cheap rename.** Every app has a stable opaque id; durable resources
  (home `apps/<id>`, snapshots, container `hostit-app-<id>`, unit, the per-app FK
  tables) key on it, so a rename is `usermod -l` + one DB update with no data move
  and no container recreate. One-off startup migration for existing apps. Custom
  domains and tokens follow the rename. `plans/260808-hostit-app-id-identity.md`.
- **Workspace image versioning + Node.js/PHP.** Node.js (npm) and PHP added to the
  image; each app is pinned to the image tag it was built with, so a Containerfile
  change only affects new apps and never recreates an existing one for that reason.
- **App-detail tabs + file browser.** The app page is tabbed: split chat+preview
  (assistant), a file browser/editor, an embedded terminal, a Logs tab (activity
  feed + live timestamped output), Snapshots, and Settings.
- **Assistant token/$ usage.** Every built-in-assistant turn's token usage is
  recorded per app (keyed by app id) and summed per user with a dollar cost in the
  admin user list. Only the built-in assistant, not a tenant's own agent.

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
