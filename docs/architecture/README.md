# hostit architecture

How the pieces fit, what isolates what, and what happens during the flows that
matter. The top-level `README.md` covers *using* hostit; this covers *building on*
it. Read these in order:

- [`overview.md`](overview.md) -- the whole system on one page: one binary, running
  as root, is the entire control plane. The component diagram and what each listener
  serves.
- [`isolation.md`](isolation.md) -- what isolates what: the per-app Unix user and
  podman uid-block idmap, the network namespace, the nftables port rules, the
  `os.OpenRoot` file containment, the `hostit-shell` login shell, and the sshd
  forwarding hardening. Ends with "what stops an app escaping".
- [`flows.md`](flows.md) -- sequence diagrams: the startup preflight, creating an app,
  serving a request, logging in over SSH, and an agent deploying.
- [`code-map.md`](code-map.md) -- where the code lives: the package table, how
  `app.Manager` composes the service packages, the `go:embed` blobs, and the
  within-package file conventions.

For deeper dives into individual subsystems (the assistant, custom domains,
snapshots) see [`../subsystems/`](../subsystems/); for the per-feature catalog see
[`../features/`](../features/).

## Conventions

- Prose is ASCII only (no em dashes; use commas, parentheses or semicolons).
- Diagrams are [Mermaid](https://mermaid.js.org/) fenced code blocks so they render
  on GitHub and in the slides.
- Code is referenced as `path/file.go:symbol`, kept current with the code.
</invoke>
