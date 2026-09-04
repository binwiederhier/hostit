# Web dashboard

## Description

The dashboard is a single-page React app served by the hostit binary at the web
hostname. After signing in with Google, an owner sees their apps -- as a grid of
cards (live status, CPU/RAM/disk bars, description, a link to open each) or as a
dense list, switched by a toggle beside the New app button and remembered per
device. Archived apps are hidden behind a second toggle that appears only when
there are any. An owner can create a new app from a dialog, and can open any app
into its **workspace** -- a full-page view
with tabs for the built-in Assistant, a file editor with live preview, a terminal,
snapshots, logs, and a settings/overview area (URLs, SSH command, API token,
custom domains, rename, delete). There is a Profile page for the owner's SSH keys
and account API tokens, and, for admins, an Admin page to manage users, sign-up
approval, and global limits.

Login is Google OAuth. For testing and recovery there is also a "breakglass"
login that mints a normal session using the operator's admin token, with no
Google round-trip.

## Why it exists

hostit is usable entirely over SSH and the API, but the dashboard is what makes it
approachable: it turns "read the guide, craft curl calls" into a click, and it is
where the built-in assistant, the live preview, and the at-a-glance fleet view
live. It is a personal view -- each owner sees their own apps and their own usage
next to their limits -- which is why even an admin's app list is not silently
widened with other people's apps.

The SPA is deliberately thin: it holds no privileged logic, only calls the same
`/api` surface everything else uses, and authenticates with a session cookie
guarded against cross-site use (apps are subdomains of the web app, so the cookie
carries the `__Host-` prefix and writes require a same-origin signal). Breakglass
exists because driving the UI as a real user is invaluable for e2e testing and
recovery, and it grants nothing new -- the admin token already has full API
rights -- so it is safe to gate behind that same token and an opt-in flag.

## User flows

Login and gating (`web/src/App.jsx`):

```mermaid
flowchart TD
    A[Load SPA] --> B[GET /api/account]
    B -->|401| C[Login: Sign in with Google]
    C --> D[/auth/google -> Google consent/]
    D --> E[/auth/callback: verify, issue session cookie/]
    E --> B
    B -->|status pending| F[Waiting for approval screen]
    B -->|status denied| G[Access denied screen]
    B -->|status active| H[Dashboard + routes]
```

Everyday use:
1. Owner lands on the Dashboard (`/`), sees the apps grid and a usage counter
   ("N of M apps"), and clicks "New app" -> a dialog that previews the app's URL
   and SSH command, then `POST /api/apps`, then navigates to the new app.
2. Opening an app (`/app/:name`) shows the workspace: the assistant/editor/
   terminal/snapshots/logs tabs plus an overview with the app's URLs, SSH
   command, agent token, and custom-domain management.
3. Profile (`/profile`) manages SSH keys (which open all the owner's apps) and
   account-wide API tokens (shown once).
4. Admin (`/admin`, admins only) lists users with role/status/limits/assistant
   spend, invites users, manages approval domains, and sets global default
   limits.

## Technical details

Frontend (`web/src/`, embedded and served by `control/web.go`):

- `web/src/App.jsx`: top-level gate. `refreshAccount` calls `GET /api/account`;
  `account` is `undefined` (loading) / `null` (401 -> `Login`) / an object.
  `status` routes to `Pending` / `Denied` / the authed `BrowserRouter`. Routes:
  `/` -> `Dashboard`, `/app/:name` -> `AppDetail`, `/profile` -> `Profile`,
  `/admin` -> `Admin`. Two chrome-less paths render before the gate: `/docs`
  (shareable instance docs, `Docs.jsx`) and the popped-out terminal
  `/app/:name/terminal`.
- `Login` is a card with a "Sign in with Google" link to `/auth/google`.
- Card previews are stored PNGs served per app; how they are taken is below.
- Page width is per route: `App.jsx:roomyPath` gives the dashboard, profile and
  admin views `container-roomy` (1240px), the docs match it through their own
  `.docs-container` (they render their own `<main>`, so the width is repeated
  rather than shared), and the app page stays full-bleed. `RoutedMain` exists
  because `App` renders the Router, so only a child of it can read the location.
- `web/src/pages/Dashboard.jsx`: `AppCard` (status pill, CPU/RAM/disk bars,
  prefers a verified custom domain for the link), `AppRow`/`AppList` (the dense
  view; the whole row opens the app, with clicks on a link or button left
  alone), `ViewToggle`, `ArchivedToggle`, and the `NewAppDialog`; polls
  `GET /api/apps` on an interval and on reconnect. Both view choices live in
  localStorage (`hostit.appview`, `hostit.showarchived`) -- per-device viewing
  preferences, not account state worth a column and a round trip. An account
  whose apps are ALL archived gets a line saying so rather than the blank page
  an account with no apps would see.
- `web/src/pages/AppDetail.jsx`: the workspace. Tabs (`ws-viewtabs`): Assistant
  (only if `app.assistant_enabled`), Files, Terminal, Snapshots (only if
  `app.snapshots_enabled`), Logs, plus an overview/settings area with editable
  description, the agent API token, the SSH command, and custom-domain add/verify/
  remove (`/api/apps/{name}/domains...`). Tab code chunks are lazy-loaded and
  prefetched (`AppAssistant.jsx`, `AppEditor.jsx`, `AppTerminal.jsx`,
  `AppLogs.jsx`). The header's `DownloadMenu` (and each snapshot row's) offers
  the workspace / snapshot as a `.zip` or `.tar.gz`; on a narrow screen it folds
  into the actions kebab as a `MenuDownloadSub` row -- see `export-download.md`.
- `web/src/pages/Profile.jsx`: `SshKeys` (`/api/account/keys`) and `Tokens`
  (`/api/account/tokens`, new token shown once). Notes that each app also has its
  own token on its page.
- `web/src/pages/Admin.jsx`: users table (role/status/limits, assistant tokens
  and cost), invite, `AllowedDomains` (`/api/domains`), and global defaults
  (`/api/settings`). Renders "Not authorized" for non-admins.
- `web/src/api.js` is the fetch wrapper (`ApiError`); the SPA never holds
  privileged logic, it just calls `/api`.

Card previews (`control/preview/service.go`, `node/screenshot/`): control
schedules the shots (a sweep every 6h, plus a debounced, rate-limited shot after
assistant activity) and stores one PNG per app id under `previews/`; the node the
app lives on runs a headless chrome in a locked-down podman container and hands
back the bytes. What makes the picture reliable is that the shot **asks chrome
when the page is ready** rather than inferring it from pixels:

- **Lifecycle events are the signal** (`node/screenshot/cdp.go:waitReady`).
  `Page.setLifecycleEventsEnabled` turns them on; the shot waits for
  `firstContentfulPaint` (content really is on screen) and
  `networkIdle`/`networkAlmostIdle`, capped by `paintSignalWait` (20s). A page
  that paints at all does so in the first seconds, so the cap costs nothing and
  leaves the fallback its own budget. An animating page -- a canvas, a game --
  fires these in about a second, where the old pixel-diff settle burned the whole
  budget waiting for two identical frames it would never get.
- **A painted page then gets a flat `settleGrace` (5s), and the LATER frame is
  stored** (`cdp.go:afterGrace`). Two matching frames only prove the page held
  still for one poll: a late font, a lazy image or a chart that draws once its
  data lands all read as "settled" and get photographed half-rendered. A few
  seconds on a shot taken every few hours is cheap insurance.
- **A painted page is trusted -- no blank check.** Chrome said it painted, so a
  pale-but-real page keeps its preview instead of being refused into having none.
- **No paint signal falls back to frame polling**: capture every `settlePoll`
  (1.5s) and return the first stable non-blank frame, ceiling `settleDelay`
  (45s). That covers a chrome too old for lifecycle events and a page that never
  reports one. If the budget runs out and the frame is STILL blank the shot
  **fails** rather than returning a white card, and control keeps the last good
  preview.
- **"Blank" now means nothing was painted** (`cdp.go:isMostlyBlank`): sample every
  2px and call it blank only at <= 0.02% ink. The previous coarse 8px grid plus a
  ">= 99.5% white" rule slipped between thin text strokes, so a heading and two
  lines of prose -- which most small apps are -- read as empty and lost their
  shot.
- **Chrome must not background itself.** `--disable-renderer-backgrounding`,
  `--disable-backgrounding-occluded-windows`,
  `--disable-features=CalculateNativeWinOcclusion` and a `Page.bringToFront`
  call, because a backgrounded renderer commits no frames: no paint event and a
  blank capture however long the shot waits.
  `--run-all-compositor-stages-before-draw` stays, against capturing mid-paint.
- **One retry** on a shot that produced nothing (`node/screenshot/service.go:Engine.Shoot`),
  on the same browser: a renderer that never committed a frame is usually
  transient, and retrying here beats waiting hours for the next sweep.
- **Metrics** (`control/metrics.go`, recorded in
  `control/appaccess.go:Server.Screenshot`): `hostit_control_screenshot_duration_seconds`
  (histogram) and `hostit_control_screenshots_total{outcome="ok|failed"}`. Since a
  page that never paints now fails instead of storing a white card, `failed` is
  the signal that previews are missing, and the histogram says whether the settle
  budget is what is running out. The node logs each shot's `load_wait`, `settle`,
  `polls` and `blank`.

Login backend (`control/server_handler_auth.go`, `control/auth.go`,
`control/session.go`):

- `handleGoogleLogin` sets a state cookie and redirects to Google's consent
  screen (`googleAuthURL`, scopes `openid email profile`).
- `handleGoogleCallback` verifies the state, exchanges the code
  (`exchangeGoogleCodeLive` -> token + userinfo), requires a verified email,
  calls `user.Manager.Login` (creates or finds the account), and issues a signed
  session cookie.
- `handleBreakglass` (`POST /auth/breakglass`): gated on the admin token;
  requires the admin token; signs in an existing account by `?email=`, and will
  create one only for a configured admin email (the way a first Google login
  would). It grants nothing beyond what the admin token already has.
- `handleLogout` clears the session cookie.
- `session.go`: stateless HMAC-signed cookies (`<userID>|<expiry>|<hmac>`),
  30-day TTL, not individually revocable before expiry. `cookie` /
  `cookieName` add `HttpOnly`, `Secure` (when TLS is on), `SameSite=Lax`, and the
  `__Host-` prefix; `checkSameOrigin` blocks cross-site cookie writes.

Routing to the SPA: `control/proxy.go:newProxyHandler` sends requests on the web
hostname to `s.api`, whose fallthrough handler (`control/api.go`) serves the
embedded SPA (`control/web.go:webHandler`) for any non-`/api` path.

## Other notes

- The Google login handlers return 501 when web login is not configured
  (`config.WebEnabled`), so an API-only instance still runs.
- The dashboard is a *personal* view: `listedApps` returns the caller's own apps
  unless `?all=true` and the caller is an admin
  (`control/server_handler_apps.go`), so another user's app appearing on the
  dashboard would be a bug, not a privilege.
- Pending/denied accounts can still call `GET /api/account` (it is
  authenticated-only, not active-only), which is how the SPA shows the "waiting
  for approval" and "denied" screens.
- Breakglass is the documented way to drive the UI in e2e tests without Google
  OAuth; always available, gated by the admin token.
- Sessions cannot be revoked before their 30-day expiry (stateless by design); a
  deleted/denied user is still stopped because API calls re-check status per
  request via the token/user lookup.
- Related features: `accounts-roles.md` (roles, approval, invites),
  `rest-api.md` (everything the SPA calls), `app-gallery.md` (the Explore page
  and the card images it reuses), `builtin-assistant.md`,
  `browser-workspace.md`, `terminal.md`, `logs.md`, `snapshots-rollback.md`,
  `export-download.md`, `custom-domains.md`, `ssh-access.md`.
