# Subsystem deep dives

Long-form explanations of hostit's non-obvious internals: the parts where the
*why* is not evident from reading one file, and where a wrong mental model leads
to a wrong change. Each page reads the real code and cites it as `path/file.go:symbol`.

Start with [`../architecture/`](../architecture/) for the whole-system view; these
pages zoom in on one mechanism each.

## Pages

- [security-isolation.md](security-isolation.md) -- the full isolation model: the
  per-app contiguous uid block and uid map, per-app network namespace
  and nftables port rules keyed to the uid, `os.OpenRoot` containment for file
  operations, `hostit-shell` (SSH lands in the container, never a host shell),
  the sshd forwarding hardening, and the trust boundary (the root daemon is the
  control plane; an app is only ever an unprivileged uid).
- [app-identity.md](app-identity.md) -- the stable opaque app id. Every durable
  resource keys on the id, not the name, so a rename is `usermod -l` plus one DB
  update: no data move, no container recreate. How tokens and custom domains
  follow a rename.
- [assistant-internals.md](assistant-internals.md) -- the built-in assistant: the
  Anthropic Messages loop, tools as one app's REST surface, prompt caching, the
  SSE event stream, per-turn usage/cost accounting, and the Claude Max
  subscription sandbox (`claude -p` in a locked-down container, tools mediated
  MCP-over-peercred-socket). Why a credential's *presence* is the whole switch.
- [storage-btrfs.md](storage-btrfs.md) -- the btrfs storage model: one subvolume
  per app (the container runs it via `--rootfs`, not an image; the app's files
  live at `home/app` inside it; per-tag base subvolumes; installs survive
  recreates), whole-app read-only CoW snapshots, reflink fork,
  the per-app budget qgroup hard-capped on exclusive bytes (EDQUOT),
  the pure retention/GFS engine, and why btrfs is now mandatory.
- [release-and-preflight.md](release-and-preflight.md) -- how hostit is built,
  shipped and started: goreleaser and the `.deb`, the example Ansible role, the
  startup preflight, and why agents only pick up an upgrade on a restart.
- [seams-and-testing.md](seams-and-testing.md) -- the injection seams
  (`node.Services`, `run.Runner`, the service packages) that let the Manager and Machine be
  built and tested without root, and how that same node-local seam is what a
  future control/app-node split would remote.

## Conventions

- Prose is ASCII only (no em dashes; use commas, parentheses or semicolons;
  write `--`, not an em dash).
- Diagrams are [Mermaid](https://mermaid.js.org/) fenced blocks.
- Code is referenced as `path/file.go:symbol`, kept current with the code.
