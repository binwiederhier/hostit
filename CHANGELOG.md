# Changelog

Every released version, newest first. Dates are the tag's date.

Entries before v0.15.0 were reconstructed from the git history, so they say what
changed rather than what an operator had to do about it; from v0.15.0 on, each
release is written down as it is cut. Anything that changes a config file, a
default, or on-disk state is called out as **Breaking** or **Upgrade note**.

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
