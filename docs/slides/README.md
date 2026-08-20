# Slides

Three [Sli.dev](https://sli.dev/) presentations live in [`presentations/`](presentations/):

- **`tech-overview.md`** -- a **code overview** for developers: the package structure,
  how `app.Manager` composes the services, the test seam, and the main flows. A
  distilled tour of `docs/`; when those docs change, update the deck too.
- **`intro.md`** -- an **introduction** for a general audience: who made it, then a
  feature-by-feature tour of what hostit can do. Text only, no screenshots.
- **`multinode.md`** -- the **multi-node design**: proxy vs hosting-node
  responsibilities, the node registry schema, the key flows as sequence diagrams,
  and the internal NodeAgent RPC with its auth model. Companion to
  `plans/260807-hostit-multinode.md` and `plans/260815-hostit-nodeagent.md`.
- **`storage.md`** -- the **storage redesign**: the disk-full incident, the validated
  hard-cap mechanisms, the decided phase-1 plan (btrfs storage driver + hierarchical
  qgroups) and the from-scratch target shape (per-app subtrees, podman as a pure
  runtime). Companion to `plans/260813-hostit-disk-hard-cap.md`.

## Run it

```sh
cd docs/slides
npm install
npm run dev                              # tech overview -> http://localhost:3030
npx slidev presentations/intro.md        # intro deck    -> http://localhost:3030
npx slidev presentations/app-api.md      # the app API and the binary split
```

Or without installing anything locally:

```sh
npx @slidev/cli docs/slides/presentations/tech-overview.md --open   # or intro.md
```

## Export

```sh
npm run export     # tech-overview.md -> PDF
npm run build      # static site into dist/
```

## Notes

- Slides are separated by `---`; the Mermaid diagrams and code fences render natively
  in Sli.dev.
- Presenter notes / speaker view: press `p` (or open `/presenter`).
- The slide **overview** (a grid/list of every slide, opened by the navbar button,
  the `o` or `` ` `` key, or right-click) is standard Slidev navigation, not part of
  these decks. Close it with `Esc`, a click, or the top-right `x`. It never appears in
  the exported PDF.
- `node_modules/` and build output here are throwaway; do not commit them (see the
  repo `.gitignore`).
