# Contributing to hostit

Thanks for your interest. hostit is a single Go binary plus a React/Vite web app;
[docs/architecture/](docs/architecture/) explains how the pieces
fit and where the code lives (start with
[overview.md](docs/architecture/overview.md)), and is the best first read
before a non-trivial change.

## Building and testing

```sh
make web              # build the React app into server/site (embedded at compile time)
make build            # build the hostit binary
make test vet fmt     # Go tests, go vet, gofmt check
cd web && npm test    # frontend unit tests (vitest)
```

Browser end-to-end tests run against a *running* instance (they create and delete
real apps, so point them at a test host, never production):

```sh
cd web && npm run test:e2e:install    # one-time: install the Chromium browser
HOSTIT_BASE_URL=https://apps.example.com HOSTIT_ADMIN_TOKEN=... npm run test:e2e
```

`make check` (run by the release target) is `test`, `fmt-check` and `vet` together.
Please run `make web` before committing anything under `web/`: the built assets in
`server/site` are embedded into the binary at compile time.

## Development workflow

- **Test-driven.** For a bug fix or feature, write the test first and watch it fail,
  then make it pass. A test that passed before your change never exercised it.
- **Keep the diff focused.** One logical change per pull request; separate mechanical
  refactors from behavior changes.
- **Open an issue first** for larger or architectural changes, so we can agree on the
  approach before you invest in a PR.

## Style

hostit follows the ntfy Go conventions. The essentials:

- **Formatting** is `gofmt`; imports are two groups (stdlib, then everything else),
  each alphabetized.
- **Packages** are organized by what they do. Service packages are scoped to one tool
  or API, with the primary type in `service.go`, side types in `types.go`, helpers in
  `util.go`. HTTP handlers live in `server/server_handler_<topic>.go` as thin
  orchestration over the service packages.
- **Naming**: single-letter receivers (never `this`/`self`); error variables are `err`;
  exported `PascalCase`, unexported `camelCase`; interfaces take the `-er` suffix.
- **Comments** explain *why*, not *what*, and are one or two lines unless a real
  invariant needs more. Identifiers carry the *what*.
- **Grouped declarations**: package-level `const`/`var` go in `const (...)` / `var (...)`
  blocks. Don't repeat a constant's value or an identifier string; hoist a `const`.
  Large text blobs (HTML, Containerfiles, templates) are `//go:embed`-ed, not inlined.
- **Errors**: `errors.Is` / `errors.As` for sentinels; wrap with `%w`.
- **ASCII only** in code, comments and docs (`--` not an em dash, `->` not an arrow).

## Commit messages and PRs

- Write a short imperative subject line and a body explaining *why*.
- Do not include AI-assistant attribution or `Co-authored-by` trailers.
- By contributing, you agree your contribution is licensed under the project's
  [Apache 2.0 license](LICENSE).

## Security

Please do not open public issues for security vulnerabilities. See
[SECURITY.md](SECURITY.md) for how to report them privately.
