# Connections and credentials

## Description

An owner attaches an account or a secret **once**, names it, and then grants it
to individual apps. The app asks hostit for a usable credential per request over
its own unix socket; it never holds a refresh token, and never has a credential
baked into an environment variable that nothing can rotate.

Two words, deliberately, because they are two different things to a person:

- A **connection** is an OAuth account you connect (Google Calendar, Gmail,
  Slack, Discord, GitHub, Jira). hostit holds a refresh token -- or, for a
  provider whose token does not expire, that token -- and mints short-lived
  access tokens.
- A **credential** is a secret you paste (an IMAP mailbox, any API key). hostit
  stores it and hands it back unchanged.

Both are the same row and the same API. An app cannot tell which it was given,
which is the point: `GET /v1/connections/{slug}/token` answers the same shape
either way.

## Why it exists

hostit brokers the **credential**, not the API. It holds what has to be held and
hands the app something usable, so the app uses the vendor's own SDK and hostit
never grows an API translation layer per vendor. Adding a provider is a
credential type plus a refresh rule, not a proxy surface maintained forever.

That is safe here precisely because the credential is the app owner's own: the
app, the container and the account all belong to the same person, so there is no
third party to keep the token from. The companion feature is **private apps** --
an app holding your calendar must not be one URL guess away from being someone
else's calendar reader.

The research behind this choice, and the four patterns that were rejected, is in
`docs/slides/presentations/integrations.md`.

## Name and slug are different things

Each connection carries both, and conflating them is the mistake this shape
exists to prevent:

- **`label`** is the NAME a person reads: "Work calendar". Free text, changeable,
  and changing it affects nothing else.
- **`slug`** is the REFERENCE an app asks for: `work-calendar`. Lowercase, 3-32
  characters of letters, digits and dashes, unique per owner, and part of a URL
  an app builds by hand.

The UI derives the slug from the name (`slugify` in `web/src/connections.js`) so
nobody invents both, and stops deriving the moment the reference is edited --
because a rename must never silently move what apps ask for. Renaming a slug
breaks any app configured for the old one, and the dialog says so rather than
looking cosmetic.

## Slugs: why a connection has a reference

One owner can hold **several of the same provider** -- a work calendar and a
personal one -- so a grant names a connection, not a provider, and an app asks
for it by the name its owner gave it:

```
GET /v1/connections                    -> [{slug: "work-cal", provider: "google-calendar"}, ...]
GET /v1/connections/work-cal/token     -> {access_token: "...", expires_at: "..."}
GET /v1/connections/personal-cal/token -> a different account entirely
```

Slugs are lowercase, 3-32 characters of letters, digits and dashes, and unique
**per owner**. One person's `cal` never resolves for another: `tokenFor` looks
the slug up in the APP OWNER's namespace, so an app can only ever reach the
connections of whoever owns it -- holding a grant on someone else's connection
id reaches nothing.

## Who registers the OAuth clients

**The operator, per provider, per instance.** There is no shared hostit OAuth
client to inherit; registering one and getting it reviewed is the operator's own
relationship with the provider. `connections:` in `control.yml` holds a client id
and secret per provider, and a provider with no client is not offered at all.

Google Calendar and Gmail fall back to `google-client-id` (the login client)
when unlisted: it is the same Google Cloud client and scopes are requested per
authorization, so an instance that can already sign in with Google needs no
second registration to read a calendar.

**There is a way around all of this for mail.** The `imap` provider reaches a
Gmail mailbox at `imap.gmail.com:993` with a Google app password (2-Step
Verification required on the account), and `smtp` sends through
`smtp.gmail.com:465` the same way. No OAuth client, no verification, no CASA, no
user cap and no 7-day expiry. For a personal instance that is strictly the
better route to mail than the `gmail` OAuth provider. POP3 works too, but IMAP
is better: POP3 has no folders, no server-side flags, and removes as it reads.

The same applies to calendars: `caldav` against Fastmail, iCloud or Nextcloud
gets the two-calendar cross-reference with none of what follows.

**Gmail is the expensive one, and Calendar is not.** Google splits the two:

- `calendar.readonly` is *sensitive* -- verification only (a justification per
  scope and a demo video, 3-5 business days), **free**.
- `gmail.readonly` is *restricted* -- verification **and** a CASA Tier 2
  assessment by a Google-empanelled assessor, **renewed every 12 months**, from
  roughly $800/year at the cheaper assessors up to several thousand. Self-scan
  is no longer permitted for restricted APIs.

Personal use, Testing status (up to 100 test users, each added by hand) and
single-Workspace internal use are exempt from both.

**The cost of staying in Testing is not money, it is time.** Google expires
refresh tokens issued by a Testing-status app after **7 days**. That is invisible
for login, which spends its token once and drops it, and crippling for
connections: every connected account needs re-consenting weekly. `Reconnect`
exists partly for this -- it swaps the credential without touching the slug or
any grant.

**Enable the API too.** Verification and consent are separate from the API being
switched on in the Cloud project. A perfectly valid token still gets
`403 SERVICE_DISABLED` until Calendar (`calendar-json.googleapis.com`) or Gmail
(`gmail.googleapis.com`) is enabled for that project.

## Token models

Not every OAuth provider issues a refresh token, and treating them as if they do
refuses perfectly good connections:

| Provider | Stored | Refresh |
|---|---|---|
| Google Calendar, Gmail | refresh token | per request, ~1h access token |
| Discord, Jira | refresh token | per request |
| Slack (`xoxb-`), GitHub OAuth App | the access token itself | none -- handed back as-is |
| Discord | refresh token, **rotated every use** | per request, and the new one is stored |
| IMAP, generic | the pasted secret | none |

`Provider.LongLivedToken` marks the third row. Both paths return the same
`Token`, so the app socket behaves identically.

**Rotation is the trap.** Some providers issue a NEW refresh token on every
refresh and invalidate the old one -- Discord does. `Provider.Refresh` therefore
returns the rotated token as a second value and `tokenFor` stores it. Dropping it
makes the first request work and the second fail with `invalid_grant`, which
reads exactly like the owner revoking access and is not. Google does not rotate,
so a catalog of one provider would never have surfaced it.

Consent parameters are **per provider** (`Provider.AuthParams`) rather than
Google's copied everywhere: Google needs `access_type=offline` or it never issues
a refresh token, Atlassian needs `audience=api.atlassian.com` and
`offline_access` in its scopes, Slack and GitHub need none of it.

## Flows

```mermaid
sequenceDiagram
    actor O as Owner (browser)
    participant H as hostit-control
    participant P as Provider
    participant A as App (container)

    O->>H: POST /api/connections {provider, slug, label}
    H-->>O: {redirect_url}  (state carries provider:slug)
    O->>P: consent
    P->>O: 302 /auth/callback?code&state
    O->>H: callback
    Note over H: state cookie == state param<br/>slug exists? re-consent : create
    H->>P: POST /token (code)
    P-->>H: refresh token (or a long-lived one)
    Note over H: sealed with the instance key, then stored
    H-->>O: 302 /profile

    Note over O,A: later, the owner grants the app the connection
    A->>H: GET /v1/connections/work-cal/token (unix socket, SO_PEERCRED)
    H->>P: refresh
    P-->>H: access token
    H-->>A: {access_token, expires_at}
```

## Technical details

- `connections/` -- the vendor-facing half. `providers.go` is the catalog,
  `oauth.go` the consent URL, exchange and refresh, `crypto.go` the sealing.
- `store/connection.go` + migration 32 -- `connection` keyed on an id with a
  unique `(user_id, slug)`, and `app_connection` pointing at a connection id.
  **Migration slots 23, 30 and 31 are burned** (an abandoned PoC and the
  reverted redirect work both ran on stage); the connections migration drops the
  PoC's differently-shaped tables of the same name before creating its own.
- `control/connections.go` -- `connectionManager`, the only thing that ever
  holds a refresh token in the clear. `tokenFor` resolves in the app owner's
  namespace and checks the grant before opening anything.
- `web/src/pages/Connections.jsx` -- its own page, not a section of the profile:
  two cards (connections, credentials), one call-to-action menu each, and the
  add/edit dialogs. The catch-all `generic` provider is set below a divider in
  the menu rather than listed among the named ones, because it is the escape
  hatch for a service hostit does not know.
- `control/server_handler_connections.go` -- the owner API, the app-socket half,
  and `connectionFromState`, which handles the OAuth callback when the state says
  it belongs to a connection. Checked **before** the login guard, so an instance
  with Slack configured but no Google login still completes a consent.
- Encryption at rest: AES-256-GCM, key in `connections.key` (0600) beside the
  database. Real, and honestly limited -- anything that can read the database can
  usually read the file next to it. A passphrase or an OS keyring is the open
  question.

## Other notes

- Renaming a connection changes the name apps address it by, and breaks any app
  configured for the old one. That is the owner's call, and the UI says so.
- **Reconnect** re-consents in place: same slug, same grants, fresh credential.
  It exists so a revoked or expired account does not mean re-granting every app.
- Disconnecting deletes every grant naming it, so apps lose access at once rather
  than holding a grant pointing at nothing.
- A collaborator can manage an app but cannot grant it somebody else's
  credential: only the owner grants their own connections.
- 50 connections per owner, and the secret is never returned by any endpoint.
- A field may be `Multiline` (an SSH private key needs a textarea) and may carry
  a `Pattern` the value must match. The pattern exists for one class of mistake
  worth catching at paste time -- an SSH **public** key in the private key box --
  which otherwise validates as "some text" and fails much later inside an app.
- Access tokens are cached in memory per connection until two minutes before
  expiry, so a page making five calls is one round trip rather than five. The
  entry records the sealed credential it came from, so a reconnect invalidates it
  by construction. The grant is re-checked on every request BEFORE the cache is
  consulted, so revoking one is still immediate.
