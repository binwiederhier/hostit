---
theme: seriph
title: hostit -- introduction
info: |
  hostit: a tiny self-hosted platform for small web apps, drivable by people or by AI
  agents. What it is, what it does, and who made it.
class: text-center
transition: slide-left
mdc: true
---

# hostit

### Your own little app platform, self-hosted

<div class="mt-8 opacity-60">
Spin up a small web app on your own server -- container, subdomain, HTTPS, SSH and an
API -- in one call. Built for the apps you (or your AI agent) throw together.
</div>

<div class="abs-br m-6 text-sm opacity-40">
Phil Heckel &middot; heckel.io
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
"Create an app on my server and deploy this" is one API call plus a token.
</div>

---

# Why it exists

AI agents are great at producing small apps. The awkward part is everything after
"here is the code": where does it run, on whose box, behind what URL, isolated from
what else?

<div class="grid grid-cols-3 gap-4 mt-8 text-sm">
<div class="p-4 rounded border border-gray-400 border-opacity-30">

**Self-hosted**

Your server, your data, your costs. No per-seat SaaS, no vendor lock-in.

</div>
<div class="p-4 rounded border border-gray-400 border-opacity-30">

**Isolated**

Each app is a separate Unix user + container + network namespace. Apps cannot see
each other.

</div>
<div class="p-4 rounded border border-gray-400 border-opacity-30">

**Agent-native**

The whole thing is a REST API first. The web UI is just one client of it.

</div>
</div>

<div class="mt-8 text-base opacity-80">
It is small on purpose: one binary, no external services to babysit.
</div>

---

# Your apps, at a glance

Create an app and it is live immediately -- a real URL serving a placeholder before
you have built anything. Status, CPU, RAM and disk update live.

<img src="/dashboard.png" class="rounded-lg shadow-xl mt-4 border border-gray-200 max-h-[62%] mx-auto" />

---

# Build it in the browser

An in-app **AI assistant** whose tools are that app's own operations. Ask in plain
English; it reads and writes the files, runs commands in the container, and deploys --
with a live preview beside the chat.

<img src="/workspace.png" class="rounded-lg shadow-xl mt-4 border border-gray-200 max-h-[60%] mx-auto" />

---

# Edit files, deploy on save

A full file editor with syntax highlighting and preview. "Save & deploy" pushes the
change and restarts the app. No local checkout, no build machine.

<img src="/editor.png" class="rounded-lg shadow-xl mt-4 border border-gray-200 max-h-[62%] mx-auto" />

---

# A real shell in every app

A terminal straight into the app's container -- root in there, `apt install` away. Or
use `ssh` / `scp` / `sftp` / `rsync` from your own machine; keys are managed via the
API.

<img src="/terminal.png" class="rounded-lg shadow-xl mt-4 border border-gray-200 max-h-[60%] mx-auto" />

---

# Snapshots and one-click rollback

hostit snapshots each app automatically -- on a schedule and before every deploy.
Roll back to any point (reversible: the current state is snapshotted first), or fork a
snapshot into a brand-new app.

<img src="/snapshots.png" class="rounded-lg shadow-xl mt-4 border border-gray-200 max-h-[58%] mx-auto" />

---

# Or let your own agent drive it

Every app comes with an **app-scoped token** that only reaches `/api/apps/<app>/*`.
Hand it to an agent and the URL shape *is* the permission.

```bash
# The agent reads the app, writes files, and deploys -- no SSH, no console.
curl -H "Authorization: Bearer $TOKEN" https://host/api/apps/blog/info
curl -H "Authorization: Bearer $TOKEN" -T index.html \
     https://host/api/apps/blog/files/public/index.html
curl -H "Authorization: Bearer $TOKEN" -X POST https://host/api/apps/blog/deploy
```

<div class="mt-4 text-sm opacity-60">
The built-in assistant uses this exact surface -- it has no special powers a token
holder does not.
</div>

---

# Multi-tenant, with real limits

Invite people (Google sign-in, or an approved-domain allow-list). Each user gets app,
memory and disk quotas; admins see every app and the assistant token spend.

<img src="/admin.png" class="rounded-lg shadow-xl mt-2 border border-gray-200 max-h-[60%] mx-auto" />

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
layout: image-right
image: /workspace.png
class: text-left
---

# Who made this

**Phil Heckel** -- open-source developer, based in Connecticut (by way of Germany).

- Creator and maintainer of **[ntfy.sh](https://ntfy.sh)**, the open-source
  pub-sub notification service (`binwiederhier/ntfy`)
- A long line of self-hosting tools: `pcopy`, `pixoo`, and friends
- **GitHub:** [@binwiederhier](https://github.com/binwiederhier)

<div class="mt-6 text-sm opacity-60">
hostit is a personal project, scratching the same itch as ntfy: run your own thing,
own your data, keep it small.
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
