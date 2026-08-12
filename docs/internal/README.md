# hostit internal documentation

Engineering-facing documentation for hostit: how it works, why it is built the way
it is, and where the code lives. This is not the user documentation (that ships in
the app at `/docs` and in the top-level `README.md`); this is for people (and agents)
working ON hostit.

## Layout

```
docs/internal/
  architecture/   # the system: components, isolation, request/lifecycle flows, code map
  features/       # feature catalog: one file per user-facing feature, fixed structure
  subsystems/     # deep dives into non-obvious internals (assistant, identity, security, ...)
  slides/         # a sli.dev code-walkthrough presentation
  README.md       # this file
```

## Where to start

- New to the codebase? Read [`architecture/`](architecture/) first (the whole system,
  then the flows), then skim the [feature catalog](features/).
- Working on one feature? Its [`features/<name>.md`](features/) file links to the exact
  code and explains the intent before you touch it.
- Want the guided tour? See [`slides/`](slides/).

## Conventions

- Prose is ASCII only (no em dashes; use commas, parentheses or semicolons).
- Diagrams are [Mermaid](https://mermaid.js.org/) fenced code blocks so they render on
  GitHub and in the slides.
- Reference code as `path/file.go:symbol`, kept current with the code.
