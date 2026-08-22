# hostit engineering documentation

Engineering-facing documentation for hostit: how it works, why it is built the way
it is, and where the code lives. This is not the user documentation (that ships in
the app at `/docs`, rendered from `web/src/pages/Docs.jsx`, and in the top-level
`README.md`); this is for people (and agents) working ON hostit.

## Layout

```
docs/
  architecture/   # the system: components, isolation, request/lifecycle flows, code map
  features/       # feature catalog: one file per user-facing feature, fixed structure
  subsystems/     # deep dives into non-obvious internals (assistant, identity, security, ...)
  slides/         # sli.dev presentations (tech overview + intro)
  development.md  # standing up an instance to develop against
  README.md       # this file
```

## Releasing

`make release` on a tag. It depends on `changelog-check`, which refuses to build
a tag `CHANGELOG.md` does not describe -- so the entry is written before the
artifacts exist, not remembered afterwards. `make release-snapshot` skips that
check: snapshots are untagged builds for stage, not releases.

Two things that used to bite: the built `control/site/index.html` is no longer
committed (only `.gitkeep` is), because a web build overwrote it and left the
tree dirty, which goreleaser refuses; and `GITHUB_TOKEN` must be set (`GITHUB_TOKEN=$(gh auth token) make release`).

## Where to start

- New to the codebase? Read [`architecture/`](architecture/) first (the whole system,
  then the flows), then skim the [feature catalog](features/).
- Working on one feature? Its [`features/<name>.md`](features/) file links to the exact
  code and explains the intent before you touch it.
- Want the guided tour? Run the slides:
  `npx @slidev/cli docs/slides/presentations/tech-overview.md --open` (or `cd slides && npm install
  && npm run dev`). See [`slides/`](slides/) for export and presenter notes.

## Conventions

- Prose is ASCII only (no em dashes; use commas, parentheses or semicolons).
- Diagrams are [Mermaid](https://mermaid.js.org/) fenced code blocks so they render on
  GitHub and in the slides.
- Reference code as `path/file.go:symbol`, kept current with the code.
