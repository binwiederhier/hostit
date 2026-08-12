---
theme: seriph
title: hostit -- introduction
info: |
  hostit: a tiny self-hosted platform for small web apps, drivable by people or by AI
  agents. Who made it, what it is, why, and what it can do.
class: text-center
transition: slide-left
mdc: true
---

# hostit

### Your own little app platform, self-hosted

<div class="mt-8 opacity-60">
What it is, why it exists, and everything it can do
</div>

<div class="abs-br m-6 text-sm opacity-40">
Philipp Heckel &middot; Head of Engineering at Slide
</div>

---
class: text-left
---

# Who made this

**Philipp Heckel** -- Head of Engineering at **Slide**, based in Connecticut (by way
of Germany).

- **Founder and maintainer of [ntfy](https://ntfy.sh)** -- the open-source pub-sub
  notification service, self-hosted or run on ntfy.sh
- **GitHub:** [github.com/binwiederhier](https://github.com/binwiederhier)
- **LinkedIn:** [linkedin.com/in/philippheckel](https://www.linkedin.com/in/philippheckel)

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

---

# Why hostit?

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

Each app is its own Unix user, container and network namespace. Apps cannot see each
other.

</div>
<div class="p-4 rounded border border-gray-400 border-opacity-30">

**Agent-native**

A REST API first; the web UI is just one client of it. Hand an agent a token and it
ships.

</div>
</div>

<div class="mt-8 text-base opacity-80">
Small on purpose: one binary, no external services to babysit.
</div>

---
zoom: 0.85
---

# Everything it can do

<div class="grid grid-cols-2 gap-x-10 gap-y-3 mt-4">
<div>

- **Instant apps** -- create it, and it serves a live HTTPS URL immediately
- **Static or app mode** -- serve `public/`, or run any command that binds `$PORT`
- **In-browser AI assistant** -- build and change the app in plain English, with a
  live preview
- **File editor** -- edit files and deploy on save, no local checkout
- **A real shell** -- browser terminal into the container, plus `ssh`/`scp`/`rsync`
- **Snapshots** -- automatic (hourly + pre-deploy), one-click rollback, fork into a
  new app

</div>
<div>

- **Bring your own agent** -- REST API + an app-scoped token; the URL is the permission
- **Custom domains** -- your own hostname on an app, TLS issued automatically
- **Logs and live status** -- streamed output, CPU/RAM/disk, a clear "crashed" state
- **Multi-tenant** -- Google sign-in, per-user app/memory/disk limits, an admin view
- **Isolated by design** -- separate users, containers and nftables port rules per app
- **One binary** -- no database service or queue to run alongside it

</div>
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
Built by Philipp Heckel &middot; github.com/binwiederhier &middot; linkedin.com/in/philippheckel
</div>
