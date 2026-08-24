# What to connect: a candidate catalog

Companion to [connections.md](connections.md). That page is how connections work;
this one is what is worth connecting, and what each one costs to add.

**Friction is the only axis that matters.** Building a provider is a few lines --
endpoints, scopes, a refresh rule. What varies by two orders of magnitude is
whether somebody has to register a client, wait for a review, or pay for an
annual security assessment. So everything below is ranked by that, not by
popularity.

| Tier | What it means | Cost to add |
|---|---|---|
| **A** | API key or password. No OAuth at all -- the `generic` or `imap` credential already covers most of these | minutes, no registration |
| **B** | Self-serve OAuth client. Register, get id + secret, done. No human reviews anything | minutes, one console visit |
| **C** | OAuth + an approval step, quota request, or paid tier before it is useful | days to weeks |
| **D** | OAuth + security review or annual paid assessment | weeks, and money |

Tiers are from general knowledge of these APIs and are **worth re-checking before
building** -- vendors move things between tiers, usually in the wrong direction.

---

## Built in already

Nineteen providers ship, and **eleven need no OAuth client at all**:

| Static (paste and go) | OAuth (needs a client in `control.yml`) |
|---|---|
| `fastmail` (JMAP: mail + calendar + contacts) | `google-calendar`, `gmail` |
| `imap`, `smtp` | `slack`, `discord` |
| `caldav`, `carddav` | `github`, `jira` |
| `postgres`, `mysql`, `opensearch` | `hubspot`, `linear` |
| `s3`, `ntfy`, `home-assistant` | |
| `ssh-key`, `discord-bot` | |
| `generic` (anything with an API key) | |

**Fastmail is the one to reach for if you have it:** one scoped API token over
JMAP covers mail, calendars *and* contacts, instead of a CalDAV credential plus
an IMAP one plus an SMTP one for the same account.

**`discord` and `discord-bot` are both needed and do different things.** A user's
OAuth token lists the servers they are in and nothing inside them; channels and
messages need a bot invited to the server, which is a pasted token, not a consent
flow.

**Gmail without OAuth:** the `imap` provider reaches the same mailbox at
`imap.gmail.com:993` using a Google **app password** (needs 2-Step Verification
on the account). That sidesteps verification, CASA, the 100-test-user cap and
the 7-day refresh-token expiry entirely. `smtp.gmail.com:465` likewise for
sending. POP3 (`pop.gmail.com:995`) also works but IMAP is strictly better --
POP3 has no folders, no server-side flags and download-and-remove semantics.

Two shape decisions worth knowing: a **database is one pasted URL**, because
that is what every managed provider hands you and splitting it into
host/port/sslmode invites the app to reassemble it wrongly. **CalDAV splits**,
because its URL is not a credential and the app needs it beside the password.

## Tier A -- a pasted credential, works today

Nothing to register. Those not listed above work through `generic`; a few
deserve their own provider for the extra non-secret fields (a host, a base URL).

**Calendar, contacts and mail without Google**
- **CalDAV** -- Fastmail, iCloud, Nextcloud, Radicale, Zoho. App password.
  *This is the answer to Google Calendar's verification mess.*
- **CardDAV** -- contacts, same providers, same credential
- **IMAP / SMTP / JMAP** -- Fastmail, Migadu, Mailbox.org, any mailbox (built)

**Self-hosted and homelab**
- **Home Assistant** -- long-lived access token
- **ntfy** -- access token (dogfooding: an app that watches something and pushes)
- **Pixoo** -- local HTTP, no auth
- **Uptime Kuma**, **Healthchecks.io**, **Gotify**, **Miniflux**, **FreshRSS**
- **Jellyfin**, **Plex**, **Sonarr / Radarr / Prowlarr** -- API keys
- **Immich**, **Paperless-ngx**, **Vaultwarden**, **Nextcloud** (app password)
- **Grafana**, **Prometheus**, **InfluxDB** -- API tokens
- **Proxmox** -- API token (relevant: box11/box12)
- **Tailscale** -- API key
- **Syncthing**, **Portainer**, **Unifi**

**AI and models**
- **Anthropic**, **OpenAI**, **OpenRouter**, **Groq**, **DeepSeek**, **Mistral**
- **Replicate**, **Hugging Face**, **ElevenLabs**, **Deepgram**, **AssemblyAI**

**Money and life admin**
- **YNAB**, **Lunch Money**, **Actual Budget**, **Firefly III** -- personal
  finance, all token-based and all pleasant
- **Stripe** (restricted API keys), **Wise**

**Objects and storage**
- **S3 / R2 / B2 / MinIO / Wasabi** -- access key + secret
- **Cloudflare** (API token), **DigitalOcean**, **Hetzner**, **Vultr**, **Fly.io**

**Misc but genuinely useful**
- **Telegram Bot API** -- bot token, the easiest chat integration that exists
- **Matrix** -- access token (relevant: matrix.ntfy.sh)
- **Pushover**, **Readwise**, **Raindrop**, **Pinboard**, **Wallabag**
- **OpenWeather**, **WeatherAPI**, **AQICN**
- **Trakt**, **Last.fm**, **ListenBrainz**
- **Mastodon** -- per-instance access token, no app review
- **Bluesky** -- app password over AT Protocol

## Tier B -- self-serve OAuth, no review

Register a client, paste id and secret into `control.yml`, done. **This is where
to prove the OAuth path**, because none of them gate on a human.

**Built already, need only a client**
- **GitHub** (long-lived token), **Slack** (`xoxb-`), **Discord**, **Jira**

**Worth adding next**
- **Linear** -- cleanest API of the lot; the best first real OAuth demo
- **Notion** -- self-serve, good for "write results into a page"
- **GitLab**, **Gitea / Forgejo**, **Bitbucket**
- **Todoist**, **Trello**, **Height**, **Vikunja**
- **Strava**, **Oura**, **Withings**, **Fitbit** -- personal-dashboard shaped
- **Spotify**, **YouTube** (via Google, `youtube.readonly` is sensitive)
- **Sentry**, **PostHog**, **Plausible**, **Fathom**, **Umami**
- **Vercel**, **Netlify**, **Render**, **Railway**
- **Dropbox**, **Box**, **pCloud**
- **Zoom**, **Calendly**, **Cal.com**
- **Reddit**, **Twitch**
- **Airtable**, **Coda**, **Baserow**
- **Buttondown**, **Resend**, **Postmark**, **Loops**, **Mailgun** (many are
  also Tier A via plain API keys, which is easier -- prefer that)

## Tier C -- OAuth plus an approval, quota or paid tier

Buildable, but somebody has to say yes, or pay.

- **Google Calendar / Gmail / Drive-readonly** -- see connections.md. Sensitive
  or restricted scopes, verification, and for Gmail an annual CASA assessment
- **Microsoft Graph** (Outlook, OneDrive, Teams) -- self-serve to register, but
  admin consent for most useful tenant scopes; personal accounts are easier
- **HubSpot**, **Pipedrive**, **Attio**, **Intercom** -- developer app plus a
  workspace to test in
- **Shopify** -- app registration, partner account
- **Xero**, **QuickBooks** -- app review for production credentials
- **Plaid**, **Salesforce** -- sandbox is free, production is an application
- **Asana**, **ClickUp**, **Monday** -- generally fine, some scopes gated

## Tier D -- avoid unless there is a strong reason

- **X / Twitter** -- paid API tiers, hostile pricing
- **Meta (Instagram, Facebook, WhatsApp)** -- app review worse than Google's
- **LinkedIn** -- partner programme for anything beyond basic profile
- **Apple (iCloud, Health, Music)** -- no general third-party API; Health is
  device-local only
- **Amazon (Alexa, SP-API)** -- heavy onboarding

---

## The cheap-scope trick

Where a vendor tiers its scopes, the **write/create** scope is often far cheaper
than the **read-everything** one, because it grants access to less:

- Google Drive: `drive.readonly` is **restricted** (CASA, paid). `drive.file` --
  files your app created or the user explicitly picked -- is **non-sensitive**:
  no verification, no user cap, no 7-day token expiry. The Docs and Sheets APIs
  work on files created under it, so "generate a report into a Doc and keep it
  updated" is completely free where "read all my documents" is not.
- Same pattern in Microsoft Graph (`Files.ReadWrite.AppFolder`) and several
  others.

**Design the app around creating things, not reading everything**, and whole
tiers of cost disappear.

## What to write examples for

Two, at opposite ends, so the abstraction is visibly not OAuth-shaped:

1. **A CalDAV agenda** (Tier A). A pasted app password, no OAuth anywhere, and it
   delivers the two-calendar cross-reference that started this whole line of
   work -- without the Google console.
2. **A GitHub "assigned to me" board** (Tier B). The full OAuth round trip on a
   provider where nobody reviews anything, so the consent path is demonstrably
   working independently of Google's queue.

A third, if the dogfooding appeals: **an ntfy watcher** -- an app that polls
something and publishes to a topic, using a plain access token.
