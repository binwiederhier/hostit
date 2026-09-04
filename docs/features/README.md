# Feature catalog

One file per user-facing feature. Every file uses the **exact same structure** (see
[`_template.md`](_template.md)): `Description`, `Why it exists`, `User flows`,
`Technical details`, `Other notes` -- same headings, same order.

## Catalog

### Apps and deployment
- [apps-lifecycle.md](apps-lifecycle.md) -- create, list, open, rename and delete apps
- [deploy.md](deploy.md) -- `hostit.yml`, the `static` and `app` modes, deploying
- [fork.md](fork.md) -- duplicate an app from a snapshot of another
- [placeholder.md](placeholder.md) -- the page a brand-new app serves until it is built

### Access and data
- [private-apps.md](private-apps.md) -- public vs private apps, viewers and collaborators
- [app-gallery.md](app-gallery.md) -- the Explore gallery of publicly listed apps
- [ssh-access.md](ssh-access.md) -- ssh / scp / sftp / rsync into an app's container
- [connections.md](connections.md) -- accounts and credentials an owner attaches once and grants per app
- [connections-catalog.md](connections-catalog.md) -- what is worth connecting, ranked by how much friction each vendor imposes
- [mcp-servers.md](mcp-servers.md) -- MCP tool servers added by URL; hostit holds the token and makes the calls
- [custom-domains.md](custom-domains.md) -- serve an app on your own hostname (DNS-01 certs)
- [snapshots-rollback.md](snapshots-rollback.md) -- automatic and manual snapshots, rollback
- [export-download.md](export-download.md) -- download an app's workspace or one snapshot as a .zip / .tar.gz
- [archiving.md](archiving.md) -- shelve an app instead of deleting it
- [quotas-limits.md](quotas-limits.md) -- disk (btrfs qgroup) and memory limits, app-count limits
- [logs.md](logs.md) -- the activity feed and live app output

### The workspace and AI
- [builtin-assistant.md](builtin-assistant.md) -- the in-browser AI chat that builds apps
- [apps-that-think.md](apps-that-think.md) -- an app asking a model a question at runtime, with no API key of its own
- [bring-your-own-agent.md](bring-your-own-agent.md) -- drive an app with your own agent via a scoped token
- [browser-workspace.md](browser-workspace.md) -- the file editor and live preview
- [terminal.md](terminal.md) -- the in-browser terminal

### Platform
- [accounts-roles.md](accounts-roles.md) -- accounts, roles, invites, approval, admin controls
- [rest-api.md](rest-api.md) -- the REST API and account/app tokens
- [web-dashboard.md](web-dashboard.md) -- the dashboard and Google login
