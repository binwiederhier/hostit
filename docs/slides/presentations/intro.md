---
theme: seriph
title: hostit -- introduction
info: |
  hostit: a tiny self-hosted platform for small web apps, drivable by people or by AI
  agents. Who made it, what it is, why, and what it can do.
layout: cover
background: https://cover.sli.dev
class: text-center
transition: slide-left
mdc: true
---

# hostit

### Your own little app platform, self-hosted

<div class="mt-8 opacity-60">
What it is, why it exists, and everything it can do
</div>

<div @click="$slidev.nav.next" class="mt-10 py-1 inline-block px-3 rounded cursor-pointer" hover:bg="white op-10">
Press Space to start <carbon:arrow-right class="inline" />
</div>

<div class="abs-br m-6 text-sm opacity-40">
Philipp Heckel &middot; Head of Engineering at Slide
</div>

<style>
h1 {
  background-color: #10b981;
  background-image: linear-gradient(45deg, #34d399 20%, #0e7490 80%);
  background-size: 100%;
  -webkit-background-clip: text;
  -webkit-text-fill-color: transparent;
}
</style>

---
transition: fade-out
class: text-left
---

# Who made this

**Philipp Heckel** -- Head of Engineering at **Slide**, based in Connecticut (by way
of Germany).

<div class="grid grid-cols-2 gap-8 mt-6">
<div>

**The work**

- 3 years at **Deutsche Bank** (with Steffen)
- 8 years at **Datto**, building BCDR appliances
- Now 3 years at **Slide** -- BCDR again
- **Maintainer of [ntfy](https://ntfy.sh)**, founder of
  **ntfy.sh** (a mini-SaaS)

</div>
<div>

**The person**

- Linux nerd, open source fan
- Loves block devices; Go fanboy
- Dad of two kids
- F1 fan

</div>
</div>

<div class="mt-6 text-sm opacity-60">
github.com/binwiederhier &middot; linkedin.com/in/philippheckel &middot; hostit scratches
the same itch as ntfy: run your own thing, own your data, keep it small.
</div>

---

# What is hostit?

One binary you run on a Linux box. It turns that box into a tiny platform for small
web apps -- your own miniature Vercel/Heroku, that you own end to end.

<div class="grid grid-cols-2 gap-6 mt-8">
<div v-click class="p-4 rounded border border-gray-400 border-opacity-30">

**Every app gets**

- its own **container** (SSH lands inside it)
- a **subdomain** with automatic HTTPS
- a **loopback port**, proxied for you
- **snapshots**, logs, resource limits

</div>
<div v-click class="p-4 rounded border border-gray-400 border-opacity-30">

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
<div v-click class="p-4 rounded border border-gray-400 border-opacity-30">

**Self-hosted**

Your server, your data, your costs. No per-seat SaaS, no vendor lock-in.

</div>
<div v-click class="p-4 rounded border border-gray-400 border-opacity-30">

**Isolated**

Each app is its own Unix user, container and network namespace. Apps cannot see each
other.

</div>
<div v-click class="p-4 rounded border border-gray-400 border-opacity-30">

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
<div v-click>

- **Instant apps** -- create it, and it serves a live HTTPS URL immediately
- **Static or app mode** -- serve `public/`, or run any command that binds `$PORT`
- **In-browser AI assistant** -- build and change the app in plain English, with a
  live preview
- **File editor** -- edit files and deploy on save, no local checkout
- **A real shell** -- browser terminal into the container, plus `ssh`/`scp`/`rsync`
- **Snapshots** -- automatic (hourly + pre-deploy), one-click rollback, fork into a
  new app

</div>
<div v-click>

- **Bring your own agent** -- REST API + an app-scoped token; the URL is the permission
- **Custom domains** -- your own hostname on an app, TLS issued automatically
- **Logs and live status** -- streamed output, CPU/RAM/disk, a clear "crashed" state
- **Multi-tenant** -- Google sign-in, per-user app/memory/disk limits, an admin view
- **Isolated by design** -- separate users, containers and nftables port rules per app
- **One binary** -- no database service or queue to run alongside it

</div>
</div>

---
layout: fact
transition: slide-up
---

# 1 binary
No database server, no queue, no sidecars -- the whole platform is one Go binary on one Linux box

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
layout: statement
transition: fade-out
---

# One box. Your apps. Your rules.

<div class="mt-4 text-lg opacity-80">
Self-hosted, isolated, and agent-native -- from one Go binary.
</div>

<div class="mt-10 text-sm opacity-50">
Built by Philipp Heckel &middot; github.com/binwiederhier &middot; linkedin.com/in/philippheckel
</div>
