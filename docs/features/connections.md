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

## Slugs: why a connection has a name

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

**Gmail is the expensive one.** Read-only mail is a Google *restricted* scope: an
instance offering it to people who are not personally known to the operator needs
a CASA Tier 2 assessment, renewed every 12 months. Personal use, Testing status
(up to 100 test users) and single-Workspace internal use are exempt.

## Token models

Not every OAuth provider issues a refresh token, and treating them as if they do
refuses perfectly good connections:

| Provider | Stored | Refresh |
|---|---|---|
| Google Calendar, Gmail | refresh token | per request, ~1h access token |
| Discord, Jira | refresh token | per request |
| Slack (`xoxb-`), GitHub OAuth App | the access token itself | none -- handed back as-is |
| IMAP, generic | the pasted secret | none |

`Provider.LongLivedToken` marks the third row. Both paths return the same
`Token`, so the app socket behaves identically.

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
