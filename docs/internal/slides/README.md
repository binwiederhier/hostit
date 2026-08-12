# Code-walkthrough slides

A [Sli.dev](https://sli.dev/) presentation that walks through hostit: what it is, the
isolation model, the main flows, the code map, and the key subsystems. It is a
distilled tour of `docs/internal/` -- when those docs change, update `slides.md` too.

## Run it

```sh
cd docs/internal/slides
npm install
npm run dev        # opens http://localhost:3030
```

Or without installing anything locally:

```sh
npx @slidev/cli docs/internal/slides/slides.md --open
```

## Export

```sh
npm run export     # slides.md -> slides-export.pdf
npm run build      # static site into dist/
```

## Notes

- The single source is [`slides.md`](slides.md). Slides are separated by `---`; the
  Mermaid diagrams and code fences render natively in Sli.dev.
- Presenter notes / speaker view: press `p` (or open `/presenter`).
- The slide **overview** (a grid/list of every slide, opened by the navbar button,
  the `o` or `` ` `` key, or right-click) is standard Slidev navigation, not part of
  this deck. Close it with `Esc`, a click, or the top-right `x`. It never appears in
  the exported PDF. Slide 2 is the actual agenda.
- `node_modules/` and build output here are throwaway; do not commit them (see the
  repo `.gitignore`).
