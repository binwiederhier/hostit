# Changelog

Every released version, newest first. Dates are the tag's date.

Entries before v0.15.0 were reconstructed from the git history, so they say what
changed rather than what an operator had to do about it; from v0.15.0 on, each
release is written down as it is cut. Anything that changes a config file, a
default, or on-disk state is called out as **Breaking** or **Upgrade note**.

## v0.31.1 (2026-08-30)

- **Fix: dashboard previews no longer go stale after a restart.** Now that a
  preview screenshot runs on the app's node over the cluster link, control's very
  first sweep on startup raced the node's reconnect and failed every shot with
  "node not connected", leaving previews stale until the next six-hour sweep. The
  first sweep now waits three minutes for the node to connect. On-demand refreshes
  and later sweeps were never affected.

## v0.31.0 (2026-08-30)

- **The control plane runs no containers, and can run unprivileged.** App-preview
  screenshots and the Claude Max assistant sandbox both moved off hostit-control
  onto the node the app lives on (behind new NodeAgent verbs), so the container
  work -- the headless-chrome shot with its per-shot egress firewall, and the
  `claude -p` sandbox -- now runs where the app is, reaching its tools through the
  node's own app socket. The dashboard preview and the assistant behave exactly as
  before; a multi-node app is now shot and assisted on its own node rather than
  from control reaching across. With both podman users gone, **hostit-control needs
  no root** (it binds no privileged port -- hostit-proxy owns :443/:80 -- and does
  no machine work), and **hostit-proxy needs only `CAP_NET_BIND_SERVICE`**. Root
  concentrates in hostit-node, which is the only component that manipulates the
  host. **Upgrade note:** running unprivileged is opt-in and unchanged installs
  stay on root; to switch, run the two daemons as a dedicated user (systemd
  `User=`, plus `AmbientCapabilities=CAP_NET_BIND_SERVICE` for the proxy) and own
  their config and data dirs.

- **New `local-proxy-uid` node setting.** Each app's published port is reachable
  over loopback only by root and the app's own uid, so one app cannot reach
  another's port. A colocated hostit-proxy dials those ports too; when it runs
  unprivileged, set `local-proxy-uid` to its uid so the per-app firewall admits it.
  Default `0` (the proxy is root) leaves the rule unchanged.

## v0.30.0 (2026-08-28)

- **Viewer-only accounts.** A new `viewer` role for people who should only OPEN
  apps shared with them: they cannot create or manage apps of their own (the
  create endpoint refuses them, and their effective app limit is 0). Someone
  invited by email to view a private app becomes an active viewer on first
  sign-in, with no admin approval; their home is a "Shared with you" list. Admins
  assign or clear the role from the user list.

- **`outbound-allow-private-cidrs` replaces `outbound-allow-private`.** The SSRF
  guard on user-supplied URLs (MCP servers, custom OAuth issuers) was all-or-
  nothing -- a boolean that either blocked every private address or opened all of
  them, the cloud metadata service included. It is now a list of CIDRs, so a
  self-hoster can exempt just their LAN (e.g. `["192.168.1.0/24"]`) without
  exposing 169.254.169.254. **Breaking:** rename the key in `control.yml`; the old
  boolean is ignored. An empty list (the default) is the strict, block-everything
  mode. **Upgrade note:** if you relied on `outbound-allow-private: true`, list
  the specific ranges your MCP servers live on instead.

- **Internal: the login-shell binaries moved off `$PATH`.** `hostit-shell` and
  `hostit-enter` now live only under `/usr/lib/hostit/bin/`; the old `/usr/bin`
  copies and the one-time passwd-migration sweep are gone, every app user having
  been migrated in an earlier release. No operator action.

## v0.29.0 (2026-08-27)

- **GitHub and Linear connections refresh their tokens now.** Both were treated
  as long-lived (a non-expiring token, no refresh), but a GitHub App -- or a
  Linear workspace with token expiration enabled -- hands back an expiring
  access token AND a refresh token that hostit discarded, so the token died in
  hours and the connection needed reconnecting every morning while the status
  dot still read green. Both are hybrid now: at connect hostit records whether
  the credential is refreshable and refreshes the refreshable ones, while a
  classic OAuth App (permanent token, no refresh) is still probed. A probe that
  rejects a token now logs the provider's status and error, which was silent
  before. **Upgrade note:** reconnect existing GitHub/Linear connections once
  after upgrading so a refresh token is stored.

- **First-run onboarding.** A new account gets a welcome modal explaining what
  hostit is and asking one question -- how technical are you -- whose answer
  pre-fills the assistant prompt and which tabs an app opens with. Both are
  editable later from a new Preferences section on the profile.

- **Per-app tabs and a View menu.** Each app's header carries a View control
  that chooses which panes it opens with (assistant or files, plus logs,
  terminal, connections), overriding the profile default per app.

- **Invite viewers who have no account yet.** A restricted app can be shared
  with someone by email before they sign in; the grant activates on their first
  login.

- **Private apps get preview screenshots.** A private app's preview is captured
  through an app-bound, single-use grant, so its dashboard tile is no longer
  blank (screenshot preview mode only).

- **Admin: logs and an instance prompt.** Admins can read the control and node
  journals from the UI, and set an instance-wide system prompt that every app's
  assistant and the `/info` guide carry.

- **Assistant: an interrupted tool call no longer wedges a conversation.** The
  API loop persists the assistant's tool calls before their results, so a Stop,
  a timeout, or a restart in between left a tool_use with no tool_result on
  disk, which the Messages API rejected on every following turn. Such a
  transcript now self-heals on its next turn.

- **`/info` guide rewritten:** clearer apps-API vs container-API sections, the
  `data/` directory, expanded `hostit.yml` examples, and separate admin/user
  prompt notes.

- Assorted UI polish: colourful onboarding avatars, a dark-mode fix for the
  granted-connection chip, a steadier assistant typing hint, and the Apps
  switcher refreshing when opened.

- **Internal:** service packages are grouped under `system/`, `node/link`,
  `proxy/`, `control/config`, `http/outbound`, and `cmd/util` as the tree grew.

## v0.28.1 (2026-08-26)

- **Long-lived-token connections are now health-checked.** A long-lived-token
  provider (GitHub, Linear, Slack) issues a non-expiring token with no refresh,
  so nothing ever re-checked it: the refresh loop skipped it and `Verify()` just
  echoed the status written once at connect time. A token revoked or invalidated
  at the provider -- a rotated client id/secret, or a user revoking access --
  therefore stayed green forever. A provider can now carry a cheap authenticated
  probe, and BOTH the explicit Verify action AND the proactive sweep call it: a
  401/403 flips the connection to needs-reconnect (a network blip is treated as
  inconclusive and left alone). Wired for GitHub and Linear; Slack reports auth
  failure in the response body rather than the status code, so it has no probe
  yet and keeps trusting its stored status.

## v0.28.0 (2026-08-26)

- **Optional single-hostname SSH relay gateway (experimental, off by default).**
  On a multi-node deployment you can now offer one stable `ssh <app>@<base-domain>`
  that reaches an app on any node, routed by app name through the control host's
  own `sshd` -- no SSH daemon of hostit's own and no extra port. The app user's
  login shell resolves the node from a local file and, for a remote app, hands
  off to a small privileged helper (`hostit-relay`) that `ssh`es to the node with
  a control-held relay key; a colocated app is entered locally as before. `ssh`,
  `scp`, `sftp` and `rsync` all work; forwarding stays off. The per-node
  hostnames from v0.27.0 remain the default and work without this. Enable with
  `hostit_ssh_relay: true` on the control host (see docs/features/ssh-access.md).
  Trade-off, deliberately accepted: the control host holds a key trusted by every
  node, so a control-host compromise reaches every app. It stays OFF unless you
  turn it on.
- **Prometheus metrics.** Control, node and proxy each expose `/metrics` on an
  optional separate listener, set with `listen-metrics: <addr>` (empty = off) in
  the component's config -- Go runtime and process collectors plus hostit series:
  control request histograms by route, app/user/connected-node gauges and a
  deploy counter; per-node memory/disk/load gauges, app count and an exec
  counter; proxy request counters, latency and a routes gauge. Bind it to an
  internal interface: `/metrics` is unauthenticated.
- **Upgrade note.** Both features are opt-in and off by default, so an upgrade
  changes nothing until you set `hostit_ssh_relay` or a `listen-metrics` address.
- `hostit control app keys` now prints the correct flag order in its usage line
  (flags come before the app name, as with `app logs`).

## v0.27.0 (2026-08-26)

- **Multi-node SSH lands on the right node.** An app hosted on a remote (worker)
  node used to advertise `ssh <app>@<base-domain>`, which resolves to the control
  node, where the app user does not exist -- so SSH to any off-control app failed
  with `Permission denied (publickey)`. Each node now reports its own reachable
  SSH hostname to control (in the cluster heartbeat, like it already reports its
  app-bind address), control records it per node, and it advertises
  `ssh <app>@<that node's host>` for the app. Control is never in the SSH data
  path; the node's own sshd terminates the connection. A single-node deploy and
  the colocated control node are unaffected -- an unset SSH host falls back to the
  base domain, exactly as before.
- **Upgrade note.** For a REMOTE node to be reachable, set its SSH host: in the
  ansible inventory, `hostit_ssh_host: <node's public hostname or IP>` on that
  node (the role emits `ssh-host:` into its node.yml). Leave it unset on the
  colocated node. Without it, a remote node's apps keep advertising the base
  domain (the pre-existing behavior), so this is additive and safe to roll out.

## v0.26.0 (2026-08-25)

- **Security fixes (from a full audit).** A critical read-SSRF in the MCP client
  (a user-supplied server URL could rebind DNS past the outbound guard to reach
  cloud metadata / internal services) is closed. A privilege escalation is
  closed: an app-scoped token could reach its app's OWNER routes (transfer,
  delete, rename, collaborators, token rotation, connections) because it resolved
  to the owner -- it is now refused on account/owner routes (agent routes still
  work). Also fixed: a newline in an SSH key smuggled a persistent
  `authorized_keys` entry past revocation; the OAuth endpoint-discovery cache was
  keyed by provider name so two tenants with a same-named personal provider could
  poison each other's consent; a collaborator could read the owner's agent token;
  a node could attribute snapshots to another app; an unvalidated TLS SNI reached
  a cert file path; and the SSRF guard missed 6to4/NAT64 IPv6 encodings.

- **Connections stay alive on their own, and their health is visible.** Control
  now refreshes OAuth access tokens **proactively** in the background, before they
  expire, so a connection does not silently lapse and force a re-auth. Each
  connection carries a health status: a refresh the provider rejects flips it to
  **needs-reconnect** (persisted), and a successful refresh or reconnect clears
  it. The UI shows a per-connection badge and a bell by the profile icon when any
  connection needs re-authorizing; `POST /api/connections/{slug}/verify` checks
  one on demand; and the app-facing `GET /api/container/connections` list now
  carries each connection's status. Long-lived-token providers (Slack, GitHub)
  have nothing to refresh and are reported healthy unless a use fails.

- **Resource-exhaustion hardening.** Request bodies and app-controlled file reads
  are now bounded (readme, keys/tokens, cluster callbacks, the assistant's
  read-file and attachment count, config reads); per-app custom-domain and
  per-instance terminal-session caps; the memory/disk pool check is now atomic
  (closing a TOCTOU overcommit) and rejects overflowing values; the proxy's
  fallback-cert keygen no longer runs under the global cert mutex; and concurrent
  workspace exports are bounded so one tenant cannot pin a shared node's disk.

- **The agent `/info` guide documents the live-preview contract:** allow the
  hostit dashboard to frame the app, treat a request carrying `?hostit_preview=`
  as uncacheable, and know that connection tokens are auto-refreshed.

## v0.25.0 (2026-08-25)

- **Download an app's workspace, or one snapshot, as a `.zip` or `.tar.gz`.** The
  app detail header gets a download icon next to fork (on a narrow screen it
  folds into the actions kebab under "Fork app"), and every row on the Snapshots
  page gets one too. Downloading the **live workspace** takes a consistent
  read-only btrfs snapshot first, archives that, then drops it, so the archive is
  a point-in-time copy even while the app keeps writing; a **per-snapshot**
  download archives the existing snapshot subvolume directly, taking nothing new.

  Over the API this is `GET /api/apps/{app}/export` and
  `GET /api/apps/{app}/snapshots/{id}/export`, `?format=tar` for the gzipped tar
  (zip is the default), streamed as an attachment (`<app>.zip` /
  `<app>-<id>.tar.gz`) under the normal per-app authorization. Works on an
  archived app too, since its files stay readable.

- **A personal Slack connection that acts as you, alongside the shared bot.** The
  existing bot provider is renamed from `slack` to **`slack-bot`** (labelled
  **Slack (bot)**), symmetric with the new personal `slack-user`. Existing bot
  connections are migrated automatically; **operators must rename the `slack`
  OAuth client to `slack-bot`** under `connections:` in `control.yml` (a client
  under the old `slack` key is no longer offered). `slack-bot` is a shared bot
  that reads and posts only in channels it is invited to. The new `slack-user`
  provider, **Slack (personal)**, uses a Slack user token (`xoxp-`) and acts as
  the person who connected it: it reads the public and private channels they are
  already in and searches across them, with no bot to invite. The Add-account
  dialog lets the owner choose what it may read (public channels, private
  channels, search across channels, all on by default); there are no
  direct-message scopes. To offer it, add **User Token Scopes** to the Slack app
  (`search:read`, `channels:read`, `channels:history`, `groups:read`,
  `groups:history`, `users:read`) and a `slack-user` client under `connections:`
  in `control.yml` -- it can reuse the same Slack app as the bot or be a separate
  one. The personal token, like the bot token, does not expire and is stored
  as-is.

## v0.24.0 (2026-08-24)

- **The New app dialog can grant a chosen subset of connections.** The
  connections chooser was all-or-nothing; now it offers All (hovering it lists
  the connections in a tooltip), Selected (a popup where any number can be
  ticked), or None (the default). It only appears when there are connections to
  grant, and the private/public choice moved below the name and URL/SSH preview.

## v0.23.0 (2026-08-24)

- **An app chooses which model answers -- and it works on a Claude subscription,
  not just the metered API.** Until now `POST /api/container/assistant` only
  reached the Anthropic API, so an instance configured with only a Claude Max
  subscription could not answer an app at all. Now `GET
  /api/container/assistant/models` lists the models this instance actually has
  configured, and the ask routes on the id an app picks: a `claude-*` id runs on
  the operator's Claude subscription (a one-shot, tool-less `claude -p`), an
  `anthropic-*` id on the metered API. Omit the model for the default -- the same
  head-of-catalog default the chat UI takes. Still no tools on either backend,
  and still metered and rate-limited per app.

  **Note:** the subscription backend runs a sandbox container per call, so it is
  heavier than the API -- fine for a periodic log check, slower for a chat app
  making many calls. Point such an app at an `anthropic-*` model if the instance
  has an API key.

## v0.22.0 (2026-08-24)

- **Apps can ask a model a question.** `POST /api/container/assistant`, with
  `{"prompt": "..."}` or a `messages` conversation. The app holds **no API key**:
  hostit makes the call, meters it against that app, and rate-limits it against
  the owner -- the same budget an interactive turn spends.

  This is the opposite direction from the assistant you chat with. That one
  BUILDS an app; this lets the app itself think. An app can summarise its own
  logs and decide whether they are worth waking somebody for, or answer visitors
  in a particular voice, without an account anywhere or a secret in its
  environment that nothing can rotate.

  Inference only, deliberately: none of the assistant's tools are offered, since
  an app that could run them against itself is a self-modifying loop with nobody
  in the room. Two worked examples ship with it, `examples/pirate-chat` and
  `examples/log-watch`. The assistant knows the endpoint exists, so asking it for
  "an app that reads my logs and pings me if anything looks serious" builds on
  this rather than telling you to obtain an API key.

- **The container API is now reachable at a plain loopback URL**,
  `http://127.0.0.1:2586`, in addition to the unix socket -- so an app uses an
  ordinary HTTP client and URL instead of a hand-rolled unix-socket one. The
  in-container agent reverse-proxies it to the same socket, so identity
  (`SO_PEERCRED`) is unchanged. The socket stays available; nothing that used it
  needs to change.

- **The new-app dialog** leads with private (selected by default), blocks
  invalid names as you type, and lets you grant all or no connections at
  creation. And a **stale-bundle guard**: after a deploy, a browser holding an
  old page reloads once instead of erroring on a chunk that no longer exists.

## v0.21.0 (2026-08-24)

- **Security: cross-tenant file exposure closed.** Every app container mounted
  the node's whole `/run/hostit`, which also carried `apps-raw` -- an
  idmap-free view of every app's files -- and the operator sockets. Any tenant
  could read every other tenant's source, `hostit.yml` env values and
  `authorized_keys`. The node now serves the app socket from its own
  subdirectory and mounts only that into a container, so nothing else under
  `/run/hostit` is visible from inside. Isolation is by construction now, not by
  file permission.
- **Upgrade note.** The node's `socket-file` default moved to
  `/run/hostit/app/hostit.sock` (the ansible role sets it). Paths inside a
  container are unchanged (`/run/hostit/hostit.sock`), and containers pick up the
  scoped mount when they restart on upgrade.

## v0.20.0 (2026-08-24)

- **Connections.** Attach an account, a secret or a tool server once, then grant
  it to individual apps. An app asks hostit for a usable credential when it needs
  one, over its own socket -- so nothing is baked into a file, and revoking a
  grant takes effect on the next request rather than the next deploy.

  Three kinds, because they are three different things to a person, and
  "connections" is the umbrella rather than one of them. **Accounts** are
  services you sign in to: Google Calendar, Gmail, Slack, Discord,
  GitHub, Jira, HubSpot, Linear. **Credentials** are secrets you paste: Fastmail
  (one JMAP token for mail, calendar and contacts), IMAP, SMTP, CalDAV, CardDAV,
  Postgres, MySQL, OpenSearch, S3, ntfy, Home Assistant, an SSH key, a Discord
  bot token, or any API key at all. **MCP servers** are tool servers added by
  URL (below). Eleven of the nineteen need no OAuth client,
  no review and no console visit -- which is the point of brokering the
  credential rather than the API.

  Each one has a **name** you read and a **reference** an app asks for, derived
  from the name. That is what lets you attach the same service twice -- a work
  calendar and a personal one -- and have an app ask for the one it was granted.

  Credentials are sealed with AES-256-GCM and bound to the row they belong to, so
  ciphertext moved between rows does not decrypt. Access tokens are cached until
  shortly before they expire; the grant is re-checked on every request regardless.
  `hostit control connections rotate-key` re-seals everything under a fresh key.

  **Upgrade note:** an OAuth client per provider goes in `connections:` in
  `control.yml`; a provider without one is not offered. Google's two fall back to
  the login client. See the Connections setup page in the admin guide.

- **MCP servers.** Paste the URL of an MCP tool server and hostit works out the
  rest: whether it wants authorization, where to arrange it, and what tools it
  offers. Nothing to register in advance -- unlike the OAuth providers, an MCP
  server needs no client ID from an admin, because hostit identifies itself by a
  public metadata document at `/.well-known/oauth-client` (the Client ID Metadata
  Document that replaced dynamic registration in the MCP spec).

  Granted to an app the same way as anything else, but hostit does **not** hand
  over the token: an MCP token opens the whole server, so giving it to the app
  would make the grant decorative. The app sends a tool name and arguments to
  `/v1/mcp/<name>/call` and hostit makes the call -- so an app needs no MCP
  client, no OAuth of its own, and nothing to refresh. The same tools appear in
  the built-in assistant, so you can grant an app a server and just ask for what
  you want.

  **Note for operators:** `/.well-known/oauth-client` must be reachable from the
  internet, or an authorization server cannot fetch it and every consent fails.
  It is served unauthenticated on purpose.

- **Outbound fetches of user-supplied URLs are restricted.** Adding an MCP server
  means hostit fetches a URL a user chose, from inside its own network. hostit now
  refuses to connect to anything that is not publicly routable -- loopback, the
  private ranges, and the link-local range where cloud providers put their
  unauthenticated metadata service -- checked at connection time on the resolved
  address, so DNS rebinding does not get past it. Set
  `outbound-allow-private: true` in `control.yml` if your MCP servers really are
  on your own LAN.

- **Fixed: every app page opened a terminal connection nobody asked for.** The
  workspace mounts all its tabs, and the terminal connected its WebSocket on
  mount -- so opening an app to look at its logs opened a shell session too, and
  renaming or deleting the app left that socket dialling a name that no longer
  existed, logging a handshake error at whoever was looking. The terminal now
  mounts the first time its tab is opened, and stays mounted after, so switching
  away still does not kill the session.

- **An About box**, in the profile menu: what this is, which version is running,
  the author and a link to the source. The version is the first thing anybody is
  asked for when reporting a problem, and "check the deb version on the box" is
  not an answer for somebody who only has the web app.

- **Breaking: the web app is served at the base domain only.** A
  `hostit.<base-domain>` alias answered as well, left over from before the base
  domain took over. Two names for one thing is two to register with every OAuth
  provider, two to write in documentation, and one more to leak into a URL
  somebody bookmarks. That name is now an ordinary app subdomain.

  **Upgrade note:** if anyone reaches your instance at `hostit.<base-domain>`,
  tell them to use the base domain. If you registered that hostname as a redirect
  URI with an OAuth provider you can remove it -- hostit no longer sends it.

- **Providers, at three tiers.** Any OAuth 2.0 service hostit does not ship can
  now be added -- by the **operator** in `connections:` in `control.yml` or on the
  Admin page (everyone can then connect it), or by a **user** for themselves with
  no admin involved. A user registering their own OAuth app with a vendor and
  pasting the client in is an ordinary thing: nothing about OAuth requires the
  client to belong to the instance, and only the callback URL is hostit's, which
  is why the dialog shows it first.

  A definition is a label, a client, scopes, and either the two OAuth URLs or an
  `issuer` to discover them from. It behaves exactly like a built-in, because a
  catalog entry was always pure data. A user's own is visible only to them, and
  two users may each define `acme`; nobody can redefine a name hostit or the
  operator already uses, so `github` keeps meaning GitHub. Client secrets are
  encrypted like every other credential and never returned by the API.

  **Named MCP servers** live in the same places -- `mcp-servers:` in
  `control.yml`, the Admin page, or a user's own -- so adding one is a pick
  rather than a remembered URL. Pasting any URL still works.

  A malformed `control.yml` entry stops the server at start rather than
  vanishing from a menu.

- **`GET /api/connections?kind=` narrows the list** to `oauth`, `static` or
  `mcp`, filtering the offered providers with it so a one-kind view does not
  offer you the wrong thing to attach. An unknown kind is refused rather than
  ignored. The token and tools sub-resources now answer **404** for a member of
  the wrong kind rather than 400 -- the request was fine, the thing it asked for
  simply does not exist for that member.

- **The app-facing API answers at `/api/container` as well as `/v1`.** Same
  surface, same socket. The new spelling names who is asking -- code inside an
  app's container, authenticated by the socket it arrived on -- where `/v1` next
  to `/api` was always an odd seam. `/v1` keeps working and always will, since it
  is what the in-container CLI and every app written so far call.

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
