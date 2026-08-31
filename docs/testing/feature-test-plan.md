# hostit feature test plan

A manual, end-to-end test plan covering every user-facing action in hostit. Each
case pairs a **UI step** (drive the web app in a browser) with a **backend check**
(inspect the node/registry directly), because the two catch different failures: a
green UI over a broken backend, or a healthy backend the UI never reflects.

This is a living checklist. It is not a replacement for the automated Go suite in
[`e2e/`](../../e2e/); it is the human pass that exercises the actual browser and
the real serving path, and the reference we execute against a live environment.

## How to run

**Environment.** Run against a real deployment (prod `apps.heckel.io` or stage
`stageapps.heckel.io`). All app-level testing uses a **throwaway demo account**
and **throwaway apps** so a real tenant's data is never touched.

- **Sign in without Google:** breakglass. `POST /auth/breakglass?email=<demo>`
  with the admin token as `Authorization: Bearer <token>` mints a normal session
  cookie (the account must already exist; breakglass is always on, gated only by
  the admin token). Use a dedicated demo email (e.g. a non-tenant test account).
- **Backend access:** SSH to the node as root for the inspection commands below,
  and `hostit-control` (the on-box admin CLI) for registry state.
- **Admin token:** `grep '^admin-token:' /etc/hostit/control/control.yml`.

**Safety rules (read before executing on prod).**

- Create/rename/fork/delete/archive/transfer only on **throwaway apps you made**
  in this run. Never on an app you did not create.
- Do **not** run the admin-destructive cases (delete a real user, edit global
  settings/defaults, delete a system domain) against prod. They are marked
  `PROD: NO` below and are for a disposable stage instance only.
- Prefer small memory limits (64 MB, the floor) so the demo account's pool is not
  exhausted; clean up apps at the end of each area.

**Legend.** `[owner]` any account acting on its own app; `[admin]` requires the
admin token/role; `[destructive]` irreversible or data-changing; `PROD: YES/NO`
whether it is safe to execute against production.

## Backend verification toolkit

| Check | Command |
|---|---|
| Registry: app exists, port, owner | `hostit-control app list` (add `--json` to script it) |
| Cluster: nodes, proxies, totals | `hostit-control status` |
| Unix user + uid + home | `getent passwd <app>` (home is `.../apps-raw/<id>/home/app`) |
| Container running | `systemctl status hostit-app@<id>` (id is the app id from the registry) |
| Firewall: per-app port + egress rules | `nft list table inet hostit \| grep <uid>` |
| Serving + valid cert | `curl -sS -o /dev/null -w '%{http_code}' https://<app>.apps.heckel.io/` |
| Files/content in the container | `hostit-control app run <app> '<shell command>'` |
| Egress isolation still holds (v0.32.1) | `sudo -u '#<uid>' curl -m4 http://169.254.169.254/` must time out |
| Control/node health | `journalctl -u hostit-control -u hostit-node -u hostit-proxy --since '10 min ago'` |

---

## A. Authentication and session

- **A1 Sign in (breakglass)** `[owner] PROD: YES`
  - UI: hit `/auth/breakglass?email=<demo>` with the admin token; land on the
    dashboard as `<demo>`.
  - Backend: the session cookie is set; `GET /api/account` returns the demo email.
  - Expect: dashboard renders; nav shows Apps / Connections / Profile / Docs.
- **A2 First-run onboarding (WelcomeModal)** `[owner] PROD: YES`
  - UI: a brand-new account shows the "Welcome to hostit" modal
    (`web/src/pages/WelcomeModal.jsx`) with the technical-level cards (Not
    technical at all / Somewhat / Very). **Get started** saves level + tab/prompt
    presets; **Skip for now** just marks onboarded. This is the ONLY place the
    technical level is set (Profile has no tech-level control).
  - Backend: `PATCH /api/account {onboarded, tech_level, default_tabs,
    assistant_prompt}`; the modal no longer shows on reload.
  - Expect: the apps grid replaces the hero once onboarded.
- **A3 Sign out** `[owner] PROD: YES`
  - UI: sign out; the session ends and protected pages redirect to login.
  - Expect: `GET /api/account` returns 401 after logout.

## B. Account and profile

- **B1 Technical level** -- set in the first-run WelcomeModal, see **A2** (not on
  the Profile page). Changing it later requires re-triggering onboarding.
- **B2 Edit assistant instructions** `[owner] PROD: YES` -- Profile > Preferences >
  "Assistant instructions" box (only shown if the assistant is enabled); saves on
  blur (`PATCH /api/account {assistant_prompt}`); text is appended to every owned
  app's assistant prompt.
- **B3 Default app tabs** `[owner] PROD: YES` -- Profile > Preferences toggles
  (Assistant / Files / Terminal / Logs); saves instantly; open a new app and
  confirm the tab set matches.
- **B4 Add SSH key** `[owner] PROD: YES` -- Profile > SSH keys > Add. Backend: the
  key lands in each app's `authorized_keys`; `ssh <app>@<host>` works.
- **B5 Rename / delete SSH key** `[owner] PROD: YES` -- rename shows the new label;
  delete removes it from apps' `authorized_keys`.
- **B6 Create API token** `[owner] PROD: YES` -- Profile > API tokens > Create;
  the token is shown once. Backend: `hostit-control app list -H <base> -t <token>`
  authenticates.
- **B7 Delete API token** `[owner] PROD: YES` `[destructive]` -- deleting revokes
  it; a subsequent API call with it returns 401.

## C. Create and list apps

- **C1 Create app** `[owner] PROD: YES` -- Dashboard > New app; name validated
  against `^[a-z]([a-z0-9-]{0,30}[a-z0-9])?$`, reserved names rejected.
  - Backend (the full birth checklist): `hostit-control app list` shows it;
    `getent passwd <app>` shows a fresh uid + home; `systemctl status
    hostit-app@<id>` is active; `nft ... grep <uid>` has a loopback port rule and
    the egress-drop rule; `curl https://<app>.apps.heckel.io/` is 200 with a valid
    cert (placeholder page).
  - Cleanup: delete the app (C-cleanup / E4).
- **C2 Name collision / invalid name** `[owner] PROD: YES` -- creating a duplicate
  or an invalid/reserved name is rejected with a clear error; no partial app is
  left behind (`app list` unchanged).
- **C3 Quota exhaustion** `[owner] PROD: YES` -- create up to the app-count or
  memory-pool limit; the next create returns a "limit reached" error naming the
  spent pool. Verify no orphan app.
- **C4 Dashboard views** `[owner] PROD: YES` -- grid vs list toggle; each card
  shows status, resource bars, description, and a live preview thumbnail.

## D. App lifecycle (run state)

Do these on a throwaway app with a real `run:` command (e.g. a tiny server) so
start/stop/restart are observable; a `mode: static` app has no run command.

- **D1 Deploy** `[owner] PROD: YES` -- edit `hostit.yml`/files, Deploy; the served
  content changes. Backend: a pre-deploy snapshot is taken (K1); the container is
  (re)started.
- **D2 Start / Stop run** `[owner] PROD: YES` -- Stop halts the `run:` process,
  container stays up; Start resumes it. Backend: serving reflects the process
  being down/up while the container is still present.
- **D3 Restart run** `[owner] PROD: YES` -- fast reload, no container recreate.
- **D4 Power off** `[owner] PROD: YES` -- container stops and stays stopped across
  reboots. Backend: `systemctl status hostit-app@<id>` inactive; the subdomain
  serves a powered-off page/error, not the app.
- **D5 Power on** `[owner] PROD: YES` -- container starts; serving returns.
- **D6 Reboot** `[owner] PROD: YES` -- container restarts; brief downtime then 200.
- **D7 Archive** `[owner] PROD: YES` `[destructive]` -- more final than power-off:
  the app refuses to start until unarchived (`control/archive.go`, endpoint
  `POST /apps/{app}/archive`). Backend: the status dot reads grey/"archived"; the
  terminal websocket closes with code 4002; verbs that would run it return
  `ErrArchived`.
- **D8 Unarchive** `[owner] PROD: YES` -- the app becomes startable again; power on
  and confirm it serves.

## E. App management

- **E1 Rename app** `[owner] PROD: YES` -- rename via the app menu; the subdomain
  changes. Backend: `app list` shows the new name; new subdomain is 200 with a
  cert, old subdomain 404; the unix user/home is renamed or remapped consistently.
- **E2 Fork / duplicate** `[owner] PROD: YES` -- fork seeds a new app from the
  source's current state (or a chosen snapshot). Backend: a new app id + uid +
  subvolume; the fork serves the source's content; the two are independent
  (change one, the other is unaffected).
- **E3 Transfer ownership** `[owner] PROD: YES` `[destructive]` -- Actions >
  Transfer ownership, enter a second demo account's email. **You drop to
  collaborator** on the app. Source account no longer owns it, the target does
  (breakglass as the target to confirm). Backend: `OwnerID` changes; the app keeps
  serving throughout.
- **E4 Delete app** `[owner] PROD: YES` `[destructive]` -- Actions > Delete app,
  **type the exact app name to confirm** (`DeleteAppDialog`). Backend (full
  teardown): gone from `app list`; unix user removed (`getent passwd <app>`
  empty); subvolume and home removed; the systemd unit gone; the nft rules for its
  uid gone; the subdomain 404. The owner's assistant session for it is dropped.
- **E5 Use your own AI agent (app token)** `[owner/collab] PROD: YES` -- the
  sparkle button opens a dialog with the app's agent token + info URL + a ready
  prompt. Backend: the agent API (`/api/apps/<app>/...` with `requireApp`)
  authenticates with that token; rotating it (F6) invalidates the old one.
- **E6 Download workspace** `[owner/collab] PROD: YES` -- Download workspace >
  .zip / .tar.gz (`GET /api/apps/<app>/export[?format=tar]`); the archive contains
  the app's files.
- **E7 Connect via SSH** `[owner/collab] PROD: YES` -- the SSH dialog shows the
  `ssh <app>@<host>` command; with a profile SSH key added (B4), the command opens
  a shell in the container as the app user.

## F. App configuration

- **F1 Set description** `[owner] PROD: YES` -- shows on the card and in
  `README`/assistant context.
- **F2 Visibility public/private** `[owner] PROD: YES` -- toggle. Backend: a
  private app's subdomain returns 403 to an unauthenticated request and 200 to the
  owner's session (this is also the security boundary from finding #2 -- verify a
  non-owner cannot reach it).
- **F3 Default tabs (per app)** `[owner] PROD: YES` -- override which tabs this app
  opens with, from its View menu.
- **F4 Snapshot config** `[owner] PROD: YES` -- change the auto-snapshot schedule;
  confirm autos appear/stop accordingly (K).
- **F5 Update limits** `[owner] PROD: YES` -- change memory/disk (cpu is admin-
  only). Backend: floor is 64 MB memory / 256 MB disk / 100m cpu; pool accounting
  updates on the dashboard header; the cgroup limit is applied to the container.
- **F6 Rotate app token** `[owner] PROD: YES` `[destructive]` -- the old app-scoped
  token stops working, the new one works.
- **F7 Set app SSH keys** `[owner] PROD: YES` -- replace the per-app authorized
  keys; `ssh <app>@<host>` reflects the change.

## G. Files and editor

- **G1 Browse tree** `[owner] PROD: YES` -- the Files tab lists the app's home;
  folders expand.
- **G2 Edit and save** `[owner] PROD: YES` -- edit a file, Save; `hostit-control
  app run <app> 'cat <file>'` shows the new content.
- **G3 Save and deploy** `[owner] PROD: YES` -- Save & deploy applies and restarts;
  the served page changes (and a pre-deploy snapshot is taken).
- **G4 New file / New folder** `[owner/collab] PROD: YES` -- New file (`PUT
  .../files/<path>` empty) and New folder (`POST /api/apps/<app>/mkdir {path}`);
  both appear in the tree and on disk.
- **G5 Rename / move** `[owner/collab] PROD: YES` -- the per-row rename (pencil)
  and drag-between-folders both call `POST /api/apps/<app>/move {from,to}`; the
  path changes on disk.
- **G6 Delete file / folder** `[owner/collab] PROD: YES` `[destructive]` -- the
  per-row trash icon confirms ("permanently deleted") then `DELETE .../files/
  <path>`; the file is gone from disk.
- **G7 Upload** `[owner/collab] PROD: YES` -- drop OS files onto the tree (progress
  bar, cancellable); they land in the app home.
- **G8 Download** `[owner/collab] PROD: YES` -- download a (binary) file; the bytes
  match.
- **G9 Directory listing edge case** `[owner] PROD: YES` -- confirm a directory
  that lists empty is not actually empty: GET a known file path directly (a known
  files-API listing gap).

## H. Terminal

- **H1 Open terminal** `[owner] PROD: YES` -- the Terminal tab opens a shell in the
  container as the app; the welcome banner shows the app name + URL; `whoami` is
  the app user, `pwd` is `/home/app`.
- **H2 Run a command** `[owner] PROD: YES` -- commands execute; output streams.
- **H3 Powered-off / archived terminal** `[owner] PROD: YES` -- opening the
  terminal on a powered-off app closes with code 4001-style status; on an archived
  app, code 4002 (`web/src/reconnect.js`), and the UI does not pointlessly retry.

## I. Logs

- **I1 View logs** `[owner] PROD: YES` -- the Logs tab shows recent app output;
  matches `hostit-control app logs <app>`.

## J. Assistant

- **J1 Send a build message** `[owner] PROD: YES` -- ask the assistant to add a
  feature; it streams actions and edits files; the app updates. Backend: assistant
  token usage is recorded against the app.
- **J2 Stop mid-run** `[owner] PROD: YES` -- Stop halts an in-flight run cleanly.
- **J3 Upload an attachment** `[owner] PROD: YES` -- attach a file to a message;
  the assistant can reference it; delete the attachment.
- **J4 Transcript persists** `[owner] PROD: YES` -- reload; the conversation is
  still there (`GET /api/apps/<app>/assistant`).

## K. Snapshots and rollback

- **K1 Auto snapshot before deploy** `[owner] PROD: YES` -- deploy; a new AUTO
  snapshot appears at the top of the timeline.
- **K2 Take a manual snapshot** `[owner] PROD: YES` -- Take snapshot with a label;
  it appears; `hostit-control app snapshot ...` agrees.
- **K3 Roll back** `[owner] PROD: YES` `[destructive]` -- change a file, roll back
  to an earlier snapshot; the file reverts, and a safety snapshot of the pre-
  rollback state is taken first (reversible).
- **K4 Delete a snapshot** `[owner] PROD: YES` `[destructive]` -- remove one; it
  leaves the list; others remain restorable.
- **K5 Export a snapshot** `[owner] PROD: YES` -- export/download a snapshot's
  contents.

## L. Sharing and collaboration

- **L1 Viewers on a private app** `[owner] PROD: YES` -- add a viewer email; that
  account (breakglass) can reach the private app but cannot manage it; remove the
  viewer and confirm access is revoked.
- **L2 Collaborators** `[owner] PROD: YES` -- add a collaborator; they can act on
  the app (deploy/edit) per the collaborator scope; remove them.
- **L3 Non-member is blocked** `[owner] PROD: YES` -- a third account can neither
  view (private) nor manage the app.

## M. Custom domains

- **M1 Add a domain** `[owner] PROD: YES` -- add a custom domain; it shows pending
  verification with the required DNS record.
- **M2 Verify** `[owner] PROD: YES` -- once the DNS record exists, Verify succeeds;
  a cert is issued and the domain serves the app.
- **M3 Delete a domain** `[owner] PROD: YES` `[destructive]` -- removing it stops
  serving on that domain.

## N. Connections and providers

- **N1 Add a connection** `[owner] PROD: YES` -- connect an OAuth provider or an
  MCP server from the Connections page; OAuth completes the round trip.
- **N2 Reconnect / verify** `[owner] PROD: YES` -- reconnect refreshes; verify
  reports reachability; MCP tools list.
- **N3 Delete a connection** `[owner] PROD: YES` `[destructive]`.
- **N4 Per-app grant / revoke** `[owner] PROD: YES` -- grant a connection to a
  specific app, then revoke; the app's assistant gains/loses access.
- **N5 Custom provider add/update/delete** `[owner] PROD: YES` -- a user-supplied
  OAuth issuer resolves via discovery; a bad/private URL is blocked unless its CIDR
  is in `outbound-allow-private-cidrs`.

## O. Preview

- **O1 Auto preview** `[owner] PROD: YES` -- a new/changed app's card thumbnail
  updates to a real screenshot (the shot container runs under strict egress).
- **O2 Manual refresh** `[owner] PROD: YES` -- the card refresh button re-shoots.

## P. Admin (`pages/Admin.jsx`)

Read-only admin views are prod-safe; anything that changes accounts, global
config, or serving is `PROD: NO` (disposable instance only).

- **P1 Users table (read)** `[admin] PROD: YES` -- lists accounts with role,
  status, usage, limits. The page must not expose secrets or another tenant's
  sensitive data (the admin screenshot PII regression -- do not publish it).
- **P2 All-apps table (read)** `[admin] PROD: YES` -- `GET /api/apps?all=true`;
  Manage links to app detail, Open app opens the live app.
- **P3 Cluster status** `[admin] PROD: YES` -- members/health/totals; matches
  `hostit-control status`.
- **P4 Control / node logs** `[admin] PROD: YES` -- the Logs card streams the
  systemd journal (`/api/admin/logs/control`, `/api/admin/logs/node/<name>`).
- **P5 Approve / deny a pending user** `[admin] PROD: NO` -- `PATCH /api/users/:id
  {status}`; changes real access.
- **P6 Change a user's role** `[admin] PROD: NO` -- Make admin / user / viewer
  (Make admin has its own confirm).
- **P7 Edit a user's limits** `[admin] PROD: NO` -- app-limit / RAM pool / disk
  pool (`PATCH /api/users/:id`).
- **P8 Invite a user** `[admin] PROD: NO` -- `POST /api/users {email,role}` pre-
  approves an account.
- **P9 Delete a user** `[admin] PROD: NO` `[destructive]` -- the dialog forces a
  choice: give their apps to another user (`?apps=transfer&transfer_to=`) or delete
  the apps too (`?apps=delete`). Irreversible.
- **P10 Allowed sign-up domains** `[admin] PROD: NO` -- Allow domain / Stop auto-
  approving (`POST`/`DELETE /api/domains`); affects who can self-register.
- **P11 Instance providers / MCP servers** `[admin] PROD: NO` -- add/edit/remove
  instance-scoped providers (`/api/providers` scope=instance).
- **P12 Global defaults** `[admin] PROD: NO` -- default app-limit, per-app
  RAM/disk, RAM/disk pools (`PATCH /api/settings`) -- global.
- **P13 Instance assistant/info instructions** `[admin] PROD: NO` -- instance-wide
  prompt (`PATCH /api/settings {info_prompt}`).
- **P14 Rotate the connections key** `[admin] PROD: NO` `[destructive]` -- rotating
  invalidates stored credential encryption; disposable instance only.

## Q. Roles and access control (cross-cutting)

Verify the gating holds at both the UI (buttons disabled/hidden) and the API
(a forbidden call is rejected server-side, not just hidden).

- **Q1 Owner-only actions** `PROD: YES` -- as a collaborator, the owner-only items
  (Rename, Transfer, Change resources, Archive/Unarchive, Delete, Visibility,
  add/remove Collaborators, snapshot-interval config, View-tabs menu) are disabled
  with an "Only the owner can ..." tooltip. Confirm the matching API call returns
  403 for a non-owner, not just a hidden button.
- **Q2 Collaborator capabilities** `PROD: YES` -- a collaborator CAN deploy, edit
  files, use the terminal/assistant, take/restore snapshots, grant/revoke
  connections, edit the description, and SSH. Confirm each works for them.
- **Q3 Viewer role** `PROD: YES` -- a `viewer` account lands on "Shared with you"
  (`SharedApps`), can only open apps shared with it, and has no "+ New app"
  (`canCreate` is false). Confirm it cannot create or manage.
- **Q4 Non-member** `PROD: YES` -- an unrelated account cannot view a private app
  (403) nor see it listed, and every management API call is rejected.

---

## Coverage summary

Every user-facing action from the API surface (`control/api.go`,
`control/server_handler_self.go`, `control/server_handler_agent.go`) and the web
UI is represented above. The security-critical invariants -- egress isolation
(v0.32.1), private-app 403, uid-scoped loopback, delete teardown -- are folded
into the relevant cases rather than split out, so a passing run also re-confirms
the isolation model.

When executing, record per case: PASS / FAIL / SKIPPED (with reason), the UI
result, and the backend check result. A case is PASS only when **both** the UI
and the backend agree.
