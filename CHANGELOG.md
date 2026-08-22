# Changelog

Every released version, newest first. Dates are the tag's date.

Entries before v0.15.0 were reconstructed from the git history, so they say what
changed rather than what an operator had to do about it; from v0.15.0 on, each
release is written down as it is cut. Anything that changes a config file, a
default, or on-disk state is called out as **Breaking** or **Upgrade note**.

## v0.19.1 (2026-08-22)

- **Fixed: a node's memory, disk and load readings never changed.** They were
  recorded only by the connect handshake, so a node that stayed connected
  reported whatever its machine looked like when it dialled in -- while
  `last_seen` ticked along beside them, so the row looked alive. A deploy is
  the worst moment to take that snapshot: on apps.heckel.io it froze at load
  15.92, captured while seventeen containers were restarting, on a box that was
  actually sitting at 1.52. The state poll now refreshes them every 30 seconds,
  the same cadence proxies already used. Display only -- nothing routes or
  schedules on these numbers.

## v0.19.0 (2026-08-22)

- **Private apps.** An app can now be reachable only by its owner, its
  collaborators, the people they give access to, and admins -- chosen when the
  app is created or changed later under Settings -> Visibility. It holds on
  every hostname the app answers to, custom domains included, and takes effect
  within about a second. Public stays the default, and every existing app is
  public, so nothing already deployed changes meaning.

  Access comes in two grants now. A **collaborator** can deploy, edit files, use
  the terminal and SSH in -- and so, obviously, can open the app. Someone given
  **access** can only open its URL: no files, no terminal, no deploys, and the
  app does not appear on their dashboard. An app with people on that list reads
  as *Restricted* rather than *Private*; that is a label for "private, plus
  somebody", not a third setting.

  A visitor without a grant is sent to sign in and returned to the app
  afterwards. One who is signed in but not on the list is told the app is
  private and to ask its owner, along with which account they are using -- being
  signed in as the wrong one is the usual cause. A hostname nobody has deployed
  still says nothing at all.

  Private apps are served by hostit-proxy, not routed through control, so they
  keep working while control is down -- the same property public apps have
  always had. Control resolves who may open each private app ahead of time and
  pushes that with the routing table, along with the Ed25519 PUBLIC key the
  proxy checks visitors' credentials with. The proxy can verify a grant and can
  never issue one. Minting still needs control, so during an outage everyone
  holding a grant keeps working and nobody new gets in.

  No screenshots are taken of a private app (the shot container has no
  credentials and would photograph the refusal page), so its card shows a
  placeholder. Scripts and webhooks are unaffected: an API token reaches a
  private app with no redirect.

- **Upgrade note:** two migrations run at startup. `28` adds `app.private`,
  defaulting every existing app to public; `29` adds the `app_viewer` table,
  empty. No action needed and nothing changes for an app you do not touch, but
  they do rewrite the `app` table, so take the usual backup first.

- **Security fixes.** The login's "come back here afterwards" parameter rejected
  a leading `//` but not `/\` or an embedded tab, both of which browsers read
  as leaving the site -- an open redirect on the platform's own origin, after a
  genuine sign-in. Responses served through hostit-proxy now carry the same
  `Strict-Transport-Security`, `X-Content-Type-Options` and `Referrer-Policy`
  headers control puts on app traffic; in any deployment with a proxy in front,
  which is the normal one, apps were getting none of them.

- **Running hostit for development is documented** at
  [docs/development.md](docs/development.md): what the machine needs (root,
  btrfs, podman, crun), a loopback btrfs filesystem, signing in without Google,
  and resetting. `npm run dev` now proxies `/api` and `/auth` to a local
  instance, so the web app can be developed with hot reload.

- Every browser end-to-end spec now fails on an uncaught JavaScript error,
  which is the only thing that catches a component that renders fine and throws
  when you click it.

- Explanatory text ("hint" and empty-state lines) reads at body size, and the
  live resource stats in the app header are hidden on small screens again --
  they had been covering the tab strip since the header usage row was restyled.

- **Cluster health at a glance.** Every member -- control included -- now
  reports its machine's memory, disk and load in its heartbeat, and the admin
  page lists them in one table with the same amber-at-75%, red-at-90% colouring
  the app usage readings use. `hostit control status` shows the same numbers.
- **Fixed: intermittent white screenshot cards.** The shot ran chrome in
  one-shot mode with a VIRTUAL time budget, which fast-forwards timers -- so an
  animated page (a canvas, a game, a spinner) could burn a 60-second budget in a
  fraction of a real second and be captured before its images, fonts or data
  arrived. Chrome is now driven over the DevTools protocol: navigate, wait for
  the load event, settle for ten REAL seconds, capture. A preflight also skips
  the shot entirely when the app is not answering yet, instead of storing a
  photograph of a connection error.
- **Per-app sizes and per-user pools are independent settings.** A pool no
  longer derives from `app limit x per-app default`; Global defaults is five
  plain rows (apps per user, RAM/disk per new app, RAM/disk pool per user).
  A stored zero for a default pool now falls back to the built-in rather than
  silently disabling pool enforcement instance-wide.
- The admin app list shows which node an app runs on instead of its creation
  date, and the users list shows Max RAM/Max Disk as sizes.
- The Profile page's SSH keys and API tokens are proper tables with add
  dialogs, each linking to the manual section that explains the feature.
- Five new screenshots in the manual (the assistant, custom domains, the
  resources dialog, the cluster table, the profile page), and CPU caps read
  "1 core" rather than "1 cores".

## v0.18.0 (2026-08-21)

- **New apps default to 128 MB RAM, 256 MB disk and 0.5 CPU cores.** The old
  512/2048 defaults were generous for what a fresh app usually is; owners
  raise limits within their pool when an app needs more. **Existing apps do
  not change**: a migration pins every pre-existing app at its old effective
  limits as per-app overrides (CPU stays uncapped for them), and every user
  who already owns apps keeps their old derived budget as an explicit pool.
  Only new apps and new users see the small defaults.
- **A consistent resource language across the UI.** Every RAM/disk reading is
  the same icon + used/total pair (GB from 1 GB up), coloured yellow at 75%
  and red at 90% of the limit; the dashboard header shows the pool at a
  glance; the resources dialog picks from preset dropdowns and grays out
  choices the pool no longer allows; the users list shows Max RAM/Max Disk
  with editing in a dialog behind the row menu; number fields lose their spin
  arrows. The app page's Actions menu gains Rename, Transfer ownership and
  Change resources.
- **Agents learn their budget**: the per-app `/info` gains a `limits` object
  (effective RAM/disk/CPU) and notes describing what each cap does -- and that
  an app token cannot change them. The user manual gains a "Resource limits
  and pools" section.
- The wordmark is one mark everywhere now -- the error page drops its `>_`
  badge for the same blinking-cursor wordmark, and the blink survives
  reduced-motion (it is identity, not motion).
- **Fixed: the web terminal could freeze silently after an app reboot** (a
  dead pty behind a live socket). A keepalive now surfaces it and reconnects,
  and a reload button is always in the title bar.
- **Fixed: a reboot fired during provisioning raced the container create**
  ("name already in use") -- found by the new limits e2e; the reboot now holds
  the app's lifecycle lock.

- **Per-app resource limits, bounded by per-user pools.** Owners (and
  admins) edit one app's RAM and disk via `PATCH /api/apps/{name}/limits` or
  the pencil on the Settings page's Resources card; empty fields inherit the
  account defaults. Every user has a memory and disk POOL that the sum of
  their apps' limits must fit -- unset pools derive `app limit x per-app
  default`, so nobody's budget changed -- and the pool binds admins too: to
  give more, raise the pool (admin Users page). Creating an app reserves its
  default allocation, so a spent pool also refuses new apps. CPU is a new
  admin-set cap (cores, via the container's `--cpus`); an app's own agent
  token cannot edit limits at all. Disk applies live; RAM and CPU at the
  next reboot or deploy (no auto-restart on save). Overrides survive
  restarts and reach a disconnected node on its next reconcile.

- **Fixed: image uploads now reach the assistant on the subscription
  backend.** The sandboxed `claude -p` turn received only prompt text, so an
  attached image was invisible and the assistant said it could not do
  anything with it. With images the sandbox now feeds stdin as one
  stream-json user message carrying real image blocks (`--input-format
  stream-json`), so the model sees the image on either backend; attachment
  paths are also named in the prompt, so non-image uploads are discoverable
  through the MCP tools. Text-only turns are byte-for-byte unchanged.

- **`hostit control apps` is now `hostit control app`**, singular like `node`
  and `proxy`. The plural keeps working as an alias; `hostit apps` still prints
  its deprecation note and forwards.
- **New: `hostit node status`** shows the node's own view: identity, whether
  its control link is up, and the apps control has placed on this host. Works
  on a worker host with no control anywhere near it. Served over a new
  root-only socket (`/run/hostit/node.sock`, key `node-socket-file`).
- **New: `hostit proxy status` and `hostit proxy route list`** show what the
  proxy is actually serving from its cache -- link state, table sequence, route
  count, and the routes themselves. Served over a new root-only socket
  (`/run/hostit/proxy.sock`, key `proxy-socket-file`). Both answer while
  control is down, which is the whole point of the cache.
- **Tab completion** for `hostit`, `hostit-control`, `hostit-node` and
  `hostit-proxy`, bash and zsh, shipped in each package. The front door
  completes its siblings' subcommands too (`hostit control app <TAB>`).
- **Readable tables.** Every CLI list (`status`, `app list`, `node list`,
  `proxy list`, snapshots, domains, routes) now prints a bordered table with
  headers; piped output degrades to plain text.
- **Breaking: `admin-token` must be at least 16 characters.** A short
  hand-typed token is brute-forceable over HTTPS at line rate; the compare is
  constant-time, so the token space is the only defense. Generated tokens
  (`openssl rand -hex 24`) are unaffected; hostit-control refuses to start on
  a shorter one and says so.
- The node now tells its link layer when the control connection drops, so
  callbacks between redials are dropped cleanly instead of posted into a dead
  session (and `hostit node status` reports the link honestly).
- **Fixed: a node hosting no apps froze at "LAST SEEN ... ago"** in
  `hostit control status`. The state poll doubles as the liveness heartbeat
  but skipped empty nodes, so a node's clock stopped the moment its last app
  left -- and a freshly added node looked dead before its first app arrived.
  Empty nodes now answer a heartbeat instead.
- The e2e suites now sweep stale `e2e-*` apps (older than an hour) at startup,
  so a crashed run's leftovers cannot eat the app limit or linger in the
  registry.
- Removed `scripts/mkdeb.sh` and the `make deb` targets: goreleaser builds the
  packages, and the script predated the binary split (it built the pre-split
  single package and referenced files that no longer exist).
- **Upgrade note:** this release runs four additive schema migrations (per-app
  limit columns, a deliberately burned no-op slot, per-user pool columns, and
  the pinning pass that freezes existing apps and owners at their pre-release
  budgets). Nothing already deployed changes size, and no config changes are
  required. Existing containers are recreated on upgrade as usual.

## v0.17.0 (2026-08-20)

- **An app on a secondary node has a working socket.** hostit-node serves the
  app socket on every host and relays to control over the cluster link, so SSH,
  the in-container CLI and the MCP bridge work wherever an
  app is placed. Control keeps every guard: the relay changes who carries the
  request, not who decides.
- **The browser terminal works for apps on any node.** Control used to ask the
  node for the shell command and then run it on its own host, so a terminal to
  a remote-node app died with "runuser: user does not exist". The pty now runs
  on the app's node and streams over the cluster link. An archived app also
  refuses a terminal now, like everything else.
- An unknown workspace view in the URL (`/app/<name>/<typo>`) shows a
  "No such view" page instead of silently rendering the remembered tab.
- Browser e2e suite grown to 14 specs (terminal, dashboard views, archiving,
  the model picker, snapshot settings, the editor's save-and-deploy).
- **The binary split.** `hostit-app` is the container binary (mounted in as
  `/usr/bin/hostit`; tenants type nothing new), `hostit` is the operator's
  front door (`hostit control ...`), and the apps commands live on
  `hostit-control` -- `hostit apps` survives as a deprecated alias.
- **Upgrade notes:** control listens on `/run/hostit/control.sock` now (new
  `control-socket-file` key); the login shell moved to
  `/usr/lib/hostit/bin/hostit-shell` with an automatic usermod sweep, and the
  old path keeps working for this release. Containers are recreated on upgrade
  as usual and pick up the new binary.

## v0.16.0 (2026-08-19)

- **Snapshot cadence is per app, and staggered.** Every app used to be
  snapshotted hourly on the same tick, which spiked the pool and the cleaner
  together. The default is now every three hours, apps are spread across that
  window by a hash of their name, and an app can set
  `snapshot.interval` in its own hostit.yml (`0` opts out; pre-deploy snapshots
  still happen). Settings gained a Snapshots section for the interval and the
  pre/post hooks, which had never been in the UI.
- **Archive and unarchive an app.** A shelved app powers off and refuses to run
  -- no power-on, no deploy, no starting from an SSH login -- and stops taking
  new snapshots. Its history thins to monthly rollups for a year, never to
  nothing, so an app archived today is still recoverable next year.
- **A dashboard list view**, toggled beside the New app button and remembered
  per device: dense rows for a fleet that cards make you scroll through.
- **A changelog**, with `make release` refusing to build a tag it does not
  describe.
- The dashboard header loses its three stat boxes, the app count moves beside
  the heading, and app rows are clickable. The dashboard, profile, admin and
  docs pages all widen to 1240px.
- **Upgrade note:** this runs one schema migration (an additive `archived`
  column on `app`). Automatic snapshots change from hourly to every three
  hours on upgrade -- per-app if an app sets `snapshot.interval`.

## v0.15.0 (2026-08-18)

The assistant's model picker is derived from what is configured, rather than
listed by hand in YAML.

- The picker offers exactly what the configured credentials can serve. An option
  is a (backend, model) pair with a backend-prefixed id -- `claude-opus-5` and
  `anthropic-opus-5` are different choices, because the same model reached two
  ways bills differently. Adding a provider is one new file.
- Replies name the backend that produced them ("Claude Opus 5"). This also fixed
  a real bug: the metered API loop recorded the raw provider model string, so
  those replies resolved to nothing in the UI.
- The default is the head of the catalog (the subscription first, strongest
  model first) rather than a configured model.
- The built `index.html` is no longer committed, so a web build stops dirtying
  the tree and a tagged release works again.
- **Breaking:** `assistant-models` and `assistant-model` are gone from
  `control.yml`, along with the per-user model allowlist and the admin
  "Assistant access" table. Any active user may pick any mode the instance can
  run; spend is still bounded by the per-user AI budget. Leaving the retired
  keys in the file is harmless -- they are ignored.

## v0.14.0 (2026-08-18)

The cluster link stops being two different things.

- A member sharing control's host reaches it over a unix socket, where the
  kernel identifies the caller, instead of mTLS on loopback. That removes the
  bootstrap gap where a proxy could come up before its certificates existed.
- `listen-node` became `listen-cluster`, and the socket has its own setting.
  `behind-proxy` is gone -- the proxy is always in front.
- The fused daemon is finished off: control holds no machinery of its own.
- **Upgrade note:** `listen-node` is still read as a fallback, so an old config
  keeps working, but rename it.

## v0.13.1 (2026-08-17)

- Control is ready before anything depends on it, and a fused control registers
  itself as the node it is.

## v0.13.0 (2026-08-17)

hostit splits into control, node and proxy.

- One member-dialed connection carries the cluster: the proxy is a member like a
  node, so the routing table and TLS keys stop travelling over a separate
  unauthenticated surface.
- `control-addresses` became `apps-allowed-addresses` (it names proxies, not
  control), and the public docs gained worked examples for a single box and a
  three-machine cluster.
- Fixes: a powered-off app stays off through a reconcile, a mirror push no
  longer deletes a snapshot control has not heard of yet, and the proxy does not
  panic when its link to control drops.
- **Breaking:** this is a multi-package deployment. See the admin guide.

## v0.12.0 (2026-08-15)

- Screenshot previews for apps that cannot be framed live, in a podman sandbox
  with strict egress isolation and a memory cap, debounced behind assistant
  turns.
- btrfs: stale qgroups are swept so quota rescans stay fast, and commands get
  two minutes rather than thirty seconds.

## v0.11.0 (2026-08-14)

- App collaborators, and ownership transfer.
- Delete answers immediately and tears down in the background; an immediate
  same-name recreate waits for the userdel rather than failing.

## v0.10.1 (2026-08-14)

- The preview always refreshes after the assistant changes something; editor
  preview controls match the assistant view.

## v0.10.0 (2026-08-14)

- Instant app creation via idmapped rootfs mounts. Daemon file I/O goes through
  a private raw bind of the apps dir, rebuilt at every start.

## v0.9.1 (2026-08-13)

- A missing app subvolume is never conjured; missing files 404.

## v0.9.0 (2026-08-13)

- One subvolume per app, with a one-time migration. Snapshots therefore cover
  installed software, not just the app's own files.
- The e2e suite becomes a full journey: lifecycle, fork, rename, power cycle,
  snapshots and an assistant build.
- A user limit change reaches their running apps immediately.

## v0.8.8 (2026-08-13)

- Power-off sticks: an SSH login no longer powers an app back on.

## v0.8.7 (2026-08-12)

- The CLI talks to the daemon over the unix socket locally, with no token.
- App units are `Type=notify`, so a deploy never races a starting container.
- Mini live previews on the dashboard cards.

## v0.8.6 (2026-08-12)

- A crashed app shows as a distinct red state rather than as running.
- Sidebar docs (user and admin guides) in the web app.

## v0.8.5 (2026-08-12)

- App-home file I/O moves behind a `homefs` service; handler tests for apps
  CRUD and scoping, custom domains, admin and settings.

## v0.8.4 (2026-08-12)

- btrfs is mandatory, with a startup preflight; the one-off migrations are gone.

## v0.8.3 (2026-08-12)

- Static placeholder and 404 pages, always-fresh preview, CI, and the community
  files for open-sourcing.

## v0.8.2 (2026-08-12)

- Operator and admin guide, updated architecture docs, example Ansible role.

## v0.8.1 (2026-08-12)

- Server handlers restructured; run and retention extracted; large blobs
  embedded rather than held as string literals.

## v0.8.0 (2026-08-11)

- An optional Claude subscription backend for the assistant, run as `claude -p`
  in a locked-down podman sandbox, with per-turn model selection.
- `app/` is modularized into tool-scoped service packages.

## v0.7.0 (2026-08-10)

- App id and rename (everything durable keys on the id), image pinning, and
  assistant usage accounting.

## v0.6.1 (2026-08-09)

- Dashboard card polish, lazy tabs, remembered open files; mobile
  keyboard-aware height and file-tree drawer.

## v0.6.0 (2026-08-09)

- A file-tree editor view with a preview pane, drag-and-drop upload, a terminal
  tab and syntax highlighting.
- Overview, Settings and Snapshots tabs; the dashboard becomes a card grid.

## v0.5.6 (2026-08-08)

- Orphaned app-home directories on delete are fixed.

## v0.5.5 (2026-08-07)

- Automatic snapshots are labelled, and an agent snapshot must give a reason.
- An external agent can consume the built-in assistant session.

## v0.5.4 (2026-08-07)

- Fixes a Rules-of-Hooks crash that white-screened the SPA.

## v0.5.3 (2026-08-07)

- Chat file uploads (drag-and-drop and a button), editable app description,
  snapshot row actions with confirm modals.

## v0.5.2 (2026-08-07)

- Custom domains: attach an owner's hostname to an app, with DNS-01 TLS.

## v0.5.1 (2026-08-07)

- Fork from a snapshot; disk quota applied at create and fork.

## v0.5.0 (2026-08-07)

- btrfs snapshots with hard qgroup quotas, snapshot hooks, restic-style
  retention (last/daily/weekly/monthly), one-click rollback, and fork.

## v0.4.1 (2026-08-07)

- Workspace polish: grouped Actions menu, compact resource row, stoppable
  assistant turns, dark mode.

## v0.4.0 (2026-08-07)

- App-side lifecycle verbs (distinct from container power), live CPU stat, and
  the app-detail workspace redesign.

## v0.3.2 (2026-08-06)

- Assistant hardening from a security review.

## v0.3.1 (2026-08-06)

- Server-owned assistant sessions with broadcast streaming, so a turn started on
  one device streams to all of them.

## v0.3.0 (2026-08-06)

- The in-browser coding assistant: a Go agent loop over the Messages API, with
  persisted conversations, a deploy tool, and thinking shown but not echoed
  back.

## v0.2.13 - v0.2.7 (2026-08-06)

- Lifecycle actions are watched to completion instead of guessed (v0.2.11);
  admin-token breakglass login for recovery and e2e (v0.2.12); app-process state
  shown distinctly from container state (v0.2.9); the app is separated from its
  container, giving power and app-process verbs (v0.2.7). v0.2.8, v0.2.10 and
  v0.2.13 are fixes.

## v0.2.6 - v0.2.3 (2026-08-05)

- The browser terminal becomes a real SSH session, with fullscreen and pop-out
  (v0.2.5, v0.2.6); instant idmapped container creation (v0.2.3); an Apache-2.0
  license (v0.2.4).

## v0.2.2 (2026-08-05)

- One API where the path says what the token may do; apps live under
  `/var/lib/hostit` and their homes are their own; `POST /api/{app}/run` with
  the limits that make it safe to offer.

## v0.2.1 (2026-08-04)

- A hardening round: every file operation the daemon does as root refuses
  symlinks, closing the tenant-to-host paths.

## v0.2.0 (2026-08-04)

- Admin approval flow, account tokens, an app overview for admins, and the web
  app served on the base domain.

## v0.1.0 (2026-08-03)

- Initial release: a self-hosted mini-app platform.
