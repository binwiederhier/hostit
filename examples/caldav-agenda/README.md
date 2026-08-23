# CalDAV agenda

Your next seven days, merged across **every** CalDAV calendar you grant it, with
each entry labelled by which calendar it came from. Cross-referencing a work and
a personal calendar is the case this exists for.

It exists to show two things:

1. **hostit's credential broker is not OAuth-shaped.** This uses a pasted app
   password. No OAuth client, no consent screen, no verification, no review, and
   no token that expires in seven days.
2. **An app reads its credential at the moment it needs it.** Nothing is stored
   in a file or an environment variable; every request asks the app's own unix
   socket, which is why revoking a grant takes effect immediately.

## Setup

1. Get an app password from your provider:
   - **Fastmail** -- Settings -> Privacy & Security -> App passwords
   - **iCloud** -- account.apple.com -> Sign-In and Security -> App-Specific Passwords
   - **Nextcloud** -- Settings -> Security -> Devices & sessions
2. In hostit, Profile -> **Credentials** -> Add CalDAV calendar:
   - Name: `work-cal` (this is what the app asks for; any name you like)
   - Server URL: `https://caldav.fastmail.com/dav/calendars`
   - Username: your address
   - Password: the app password
3. Repeat for a second calendar if you want them merged -- `personal-cal`, say.
4. On the app's Settings tab, **Grant** each one.

No deploy or restart is needed after granting: the app asks per request.

## Server URLs

| Provider | URL |
|---|---|
| Fastmail | `https://caldav.fastmail.com/dav/calendars` |
| iCloud | `https://caldav.icloud.com` |
| Nextcloud | `https://<host>/remote.php/dav` |
| Google (app password) | `https://apidata.googleusercontent.com/caldav/v2/<email>/events` |

## What it does not do

Read-only, and it never writes to a calendar. Recurring events are expanded for
the window shown; all-day events are treated as local dates. It fetches on every
page load rather than caching, which is fine at personal scale and keeps the
example honest about where the credential comes from.
