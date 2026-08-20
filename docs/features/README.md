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
- [ssh-access.md](ssh-access.md) -- ssh / scp / sftp / rsync into an app's container
- [custom-domains.md](custom-domains.md) -- serve an app on your own hostname (DNS-01 certs)
- [snapshots-rollback.md](snapshots-rollback.md) -- automatic and manual snapshots, rollback
- [archiving.md](archiving.md) -- shelve an app instead of deleting it
- [quotas-limits.md](quotas-limits.md) -- disk (btrfs qgroup) and memory limits, app-count limits
- [logs.md](logs.md) -- the activity feed and live app output

### The workspace and AI
- [builtin-assistant.md](builtin-assistant.md) -- the in-browser AI chat that builds apps
- [bring-your-own-agent.md](bring-your-own-agent.md) -- drive an app with your own agent via a scoped token
- [browser-workspace.md](browser-workspace.md) -- the file editor and live preview
- [terminal.md](terminal.md) -- the in-browser terminal

### Platform
- [accounts-roles.md](accounts-roles.md) -- accounts, roles, invites, approval, admin controls
- [rest-api.md](rest-api.md) -- the REST API and account/app tokens
- [web-dashboard.md](web-dashboard.md) -- the dashboard and Google login
