---
theme: seriph
title: hostit -- introduction
info: |
  hostit: a tiny self-hosted platform for small web apps, drivable by people or by AI
  agents. Who made it, and a feature-by-feature tour of what it can do.
class: text-center
transition: slide-left
mdc: true
---

# hostit

### Your own little app platform, self-hosted

<div class="mt-8 opacity-60">
A feature tour: what it is, and everything it can do
</div>

<div class="abs-br m-6 text-sm opacity-40">
Phil Heckel &middot; heckel.io
</div>

---
class: text-left
---

# Who made this

**Phil Heckel** -- open-source developer, based in Connecticut (by way of Germany).

- Creator and maintainer of **[ntfy.sh](https://ntfy.sh)**, the open-source pub-sub
  notification service (`binwiederhier/ntfy`, tens of thousands of stars)
- A long line of self-hosting tools: `pcopy`, `pixoo`, and friends
- **GitHub:** [@binwiederhier](https://github.com/binwiederhier)

<div class="mt-8 text-base opacity-80">
hostit scratches the same itch as ntfy: run your own thing, own your data, keep it
small. This is that idea applied to <b>hosting little web apps</b>.
</div>

---

# What is hostit?

One binary you run on a Linux box. It turns that box into a tiny platform for small
web apps -- your own miniature Vercel/Heroku, that you own end to end.

<div class="grid grid-cols-2 gap-6 mt-8">
<div class="p-4 rounded border border-gray-400 border-opacity-30">

**Every app gets**

- its own **container** (SSH lands inside it)
- a **subdomain** with automatic HTTPS
- a **loopback port**, proxied for you
- **snapshots**, logs, resource limits

</div>
<div class="p-4 rounded border border-gray-400 border-opacity-30">

**Two ways to drive it**

- a **web UI**: build, edit, deploy in the browser
- a **REST API** + app-scoped token, so an
  **AI agent** can create and deploy apps on
  its own

</div>
</div>

<div class="mt-6 text-sm opacity-60">
The rest of this deck is one feature at a time.
</div>

---

# Feature: instant apps

Create an app and it is **live immediately** -- a real HTTPS URL serving a placeholder
page before you have built anything.

- One click in the UI, or `POST /api/apps {name}`
- The subdomain, the certificate, the container and the port are all wired up for you
- If the placeholder loads, the whole path works: routing, TLS, isolation, serving

<div class="mt-6 text-sm opacity-60">
No "provisioning" step to wait on. Creating and being reachable are the same moment.
</div>

---

# Feature: two ways to run

One small file, `hostit.yml`, describes how the app runs. Two modes cover what people
actually deploy.

<div class="grid grid-cols-2 gap-6 mt-6">
<div class="p-4 rounded border border-gray-400 border-opacity-30">

**`mode: static`**

Serve a `public/` directory. Plain HTML, or the built output of any frontend. Zero
configuration.

</div>
<div class="p-4 rounded border border-gray-400 border-opacity-30">

**`mode: app`**

Run your command. Any language or framework, as long as it binds `$PORT`. An optional
`prepare:` build step runs first, every deploy.

</div>
</div>

<div class="mt-6 text-sm opacity-60">
Deploy is one verb (<code>hostit deploy</code>); a config change reloads, only a
load-bearing change recreates the container.
</div>

---

# Feature: build it in the browser

An in-app **AI assistant** whose tools are that app's own operations -- so it is
confined to the one app.

- Ask in plain English: "add a leaderboard", "make the header dark"
- It reads and writes the app's files, runs commands in the container, and deploys
- A **live preview** sits right beside the chat
- Bring an API key, or connect a Claude Max subscription -- the credential's presence
  is the whole switch

<div class="mt-6 text-sm opacity-60">
It has no special powers: it uses the same REST surface an agent token does.
</div>

---

# Feature: edit files, deploy on save

A full **file editor** in the browser for when you want to touch things directly.

- Syntax highlighting, a file tree, create / rename / delete
- **Save & deploy** pushes the change and restarts the app
- No local checkout, no build machine, no CI to configure

<div class="mt-6 text-sm opacity-60">
Good for a quick fix or tweaking a config without dropping to a shell.
</div>

---

# Feature: a real shell in every app

A **terminal** straight into the app's container, in the browser.

- Root inside the container -- `apt install` whatever you need
- Your own filesystem, processes and ports; other apps are invisible
- Prefer your own machine? **`ssh` / `scp` / `sftp` / `rsync`** all work; keys are
  managed via the API

<div class="mt-6 text-sm opacity-60">
The container is the unit of isolation, and you get full run of it.
</div>

---

# Feature: snapshots, rollback, fork

Every app is snapshotted **automatically** -- on a schedule and before every deploy.

<div class="grid grid-cols-3 gap-4 mt-6 text-sm">
<div class="p-4 rounded border border-gray-400 border-opacity-30">

**Snapshot**

Instant, cheap (btrfs subvolumes). Take one yourself anytime.

</div>
<div class="p-4 rounded border border-gray-400 border-opacity-30">

**Rollback**

Restore any point. Reversible: the current state is snapshotted first.

</div>
<div class="p-4 rounded border border-gray-400 border-opacity-30">

**Fork**

Turn any snapshot into a brand-new app, near-instantly (reflink copy).

</div>
</div>

<div class="mt-8 text-sm opacity-60">
Retention is a grandfather-father-son policy, so history stays useful without growing
forever.
</div>

---

# Feature: bring your own agent

The whole platform is a **REST API** first; the web UI is just one client of it.

Every app carries an **app-scoped token** that only reaches `/api/apps/<app>/*` -- the
URL shape *is* the permission.

```bash
curl -H "Authorization: Bearer $TOKEN" https://host/api/apps/blog/info
curl -H "Authorization: Bearer $TOKEN" -T index.html \
     https://host/api/apps/blog/files/public/index.html
curl -H "Authorization: Bearer $TOKEN" -X POST https://host/api/apps/blog/deploy
```

<div class="mt-4 text-sm opacity-60">
Hand that token to any agent and it can create, edit and deploy -- no SSH, no console,
no access to any other app.
</div>

---

# Feature: custom domains

Point your own domain at an app, not just the default subdomain.

- Add the domain, hostit verifies it and issues a certificate automatically
- Wildcard (DNS-01) or on-demand per-domain TLS -- both handled for you
- The app is reachable at both its `app.example.com` subdomain and your domain

<div class="mt-6 text-sm opacity-60">
Same automatic HTTPS as everything else; nothing to renew by hand.
</div>

---

# Feature: logs and live status

Always know what an app is doing.

- **Logs tab** streams the app's output, rotated and timestamped
- Live **CPU / RAM / disk** per app, and a status dot: running, stopped, or **crashed**
- A crash-looping app backs off, then hostit gives up and marks it clearly rather than
  hammering the box

<div class="mt-6 text-sm opacity-60">
The signals you need to tell "it is fine" from "it needs me" at a glance.
</div>

---

# Feature: multi-tenant, with real limits

hostit is built for more than one person.

- **Google sign-in**, with an approval list or an allowed email-domain
- Per-user quotas: number of apps, memory, disk
- **Admin view**: every app, every user, and the assistant token spend
- Each app runs as its own Unix user, so tenants are isolated from each other

<div class="mt-6 text-sm opacity-60">
Run it just for yourself, or open it up to a team.
</div>

---

# What makes it safe

Isolation is not a setting; it is the architecture.

<div class="grid grid-cols-2 gap-6 mt-4 text-sm">
<div class="p-4 rounded border border-gray-400 border-opacity-30">

**App vs app**

Separate Unix users, containers and network namespaces; nftables pins each loopback
port to its owner's uid.

</div>
<div class="p-4 rounded border border-gray-400 border-opacity-30">

**App vs host**

SSH execs into the container -- no host shell. Container-root is an unprivileged host
uid. Every file op in a home goes through `os.OpenRoot`.

</div>
</div>

<div class="mt-6 text-sm opacity-60">
The daemon is the trusted control plane; tenant code and tenant files are treated as
untrusted throughout.
</div>

---

# Under the hood

<div class="grid grid-cols-2 gap-8 mt-6 text-base">
<div>

- **One Go binary** -- daemon, CLI and in-container agent in one
- **podman** -- a container per app, root mapped to an unprivileged uid
- **btrfs** -- subvolume per app: instant snapshots, reflink forks, disk quotas

</div>
<div>

- **systemd** -- one unit per app
- **nftables** -- each loopback port pinned to its app's uid
- **Let's Encrypt** -- wildcard or on-demand certificates, automatic

</div>
</div>

<div class="mt-8 text-sm opacity-60">
No database service, no message queue, no external dependencies to run. Just the box.
</div>

---
layout: center
class: text-center
---

# One box. Your apps. Your rules.

<div class="mt-4 text-lg opacity-80">
Self-hosted, isolated, and agent-native -- from one Go binary.
</div>

<div class="mt-10 text-sm opacity-50">
Built by Phil Heckel &middot; heckel.io &middot; github.com/binwiederhier
</div>
