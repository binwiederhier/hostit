# Connections demo

A personal dashboard that holds no credentials. It asks hostit for a token per
request over the app's own unix socket, uses it against the vendor's API, and
keeps nothing: no environment variable, no file, nothing in the image.

Proven on stage 2026-08-19 -- with a GitHub connection granted, it rendered
"Authenticated as binwiederhier (100 public repos), fetched live from
api.github.com with a credential this app never stored", and dropped straight
back to "granted: []" the moment the grant was revoked.

## Running it

1. Connect an account in your hostit profile (Connections).
2. Grant it to this app in the app's Settings.
3. Deploy this directory.

```
GET /v1/connections                     -> ["github"]
GET /v1/connections/{provider}/token    -> {"access_token": "...", "expires_at": "..."}
```

Both over `/run/hostit/hostit.sock`. See `plans/260819-connections.md`, and note
the known limitation: an app placed on a node that does not also run
hostit-control has no such socket yet (TODO.md, high priority).
