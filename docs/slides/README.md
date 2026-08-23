# Slides

The [Sli.dev](https://sli.dev/) presentations live in [`presentations/`](presentations/):

- **`tech-overview.md`** -- a **code overview** for developers: the package structure,
  how `app.Manager` composes the services, the test seam, and the main flows. A
  distilled tour of `docs/`; when those docs change, update the deck too.
- **`intro.md`** -- an **introduction** for a general audience: who made it, then a
  feature-by-feature tour of what hostit can do. Text only, no screenshots.
- **`multinode.md`** -- the **multi-node design**: proxy vs hosting-node
  responsibilities, the node registry schema, the key flows as sequence diagrams,
  and the internal NodeAgent RPC with its auth model. Companion to
  `plans/260807-hostit-multinode.md` and `plans/260815-hostit-nodeagent.md`.
- **`integrations.md`** -- **how other platforms do integrations**: the five patterns
  (credential vault, API proxy, unified API, embedded iPaaS, tool-calling/MCP), the four
  axes that decide between them, the OAuth-client-registration and Google CASA
  constraints that rule most of them out for a self-hosted platform, and where the
  `connections` proof of concept sits. Companion to `plans/260819-connections.md`.
- **`oauth-oidc.md`** -- **OAuth 2.0 and OIDC explained**, parameter by parameter with
  the attack each one blocks, through to the newer RFCs (8707, 9728, 9207, CIMD). Ends
  with exactly what `control/auth.go` does today and what it deliberately does not.
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
npx slidev presentations/integrations.md # how platforms do integrations
npx slidev presentations/oauth-oidc.md   # OAuth + OIDC, and hostit's login
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
- Sli.dev **clips** content that overflows a slide rather than scrolling it, and it does
  so silently. After editing, export to PNG (`npx slidev export <deck> --format png`) and
  look at the dense slides -- a tall Mermaid block needs `{scale: N}`, and a long table
  needs a per-slide `<style>` shrinking `table` font size.
- Presenter notes / speaker view: press `p` (or open `/presenter`).
- The slide **overview** (a grid/list of every slide, opened by the navbar button,
  the `o` or `` ` `` key, or right-click) is standard Slidev navigation, not part of
  these decks. Close it with `Esc`, a click, or the top-right `x`. It never appears in
  the exported PDF.
- `node_modules/` and build output here are throwaway; do not commit them (see the
  repo `.gitignore`).
