# Slides

Two [Sli.dev](https://sli.dev/) presentations live here:

- **`slides.md`** -- a **code overview** for developers: the package structure, how
  `app.Manager` composes the services, the test seam, and the main flows. A distilled
  tour of `docs/internal/`; when those docs change, update `slides.md` too.
- **`intro.md`** -- an **introduction** for a general audience: what hostit is, its
  features (with UI screenshots), and who made it. Screenshots live in `public/` and
  are copies of `web/src/assets/docs/*.png`; re-copy them if the UI changes.

## Run it

```sh
cd docs/internal/slides
npm install
npm run dev                 # code overview -> http://localhost:3030
npx slidev intro.md         # intro deck    -> http://localhost:3030
```

Or without installing anything locally:

```sh
npx @slidev/cli docs/internal/slides/slides.md --open   # or intro.md
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
