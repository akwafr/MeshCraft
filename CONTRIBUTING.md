# Contributing to meshcraft

Thank you for contributing. This project is licensed under the
[GNU Affero General Public License v3.0 only](LICENSE) (AGPL-3.0-only).
By submitting a contribution, you agree that it is licensed under the same
terms. If you run a modified version as a network service, AGPL requires you
to offer the corresponding source to users of that service.

## Development setup

Prerequisites: Docker (+ Compose plugin). **Nothing else on the host.**

```bash
cp .env.example .env
make build
make up
make test
```

Useful targets: `make tidy`, `make vet`, `make fmt`, `make web-dev`.

## Conventions

- **Go rule #1: no `:=`.** The short declaration operator is forbidden in all
  Go code, tests included. Declare with `var`, and hoist loop and `if`
  variables:

  ```go
  var u, err = store.GetUser(ctx, id)

  var i int
  for i = range items { ... }

  var v = os.Getenv(k)
  if v != "" { ... }
  ```

  `make lint` enforces it (`scripts/shortdecl`, baseline 0) — run it, not just
  `make vet`, before committing.
- **Language**: all source code, comments, tests, and commit messages are in
  **English**. User-facing UI strings live in `web/src/i18n/locales/*.json`
  (English and French are updated together; `make test-web` checks parity).
- **Do not rename or edit** applied SQL migrations under
  `internal/store/migrations/`: they are history. Add a new one instead.
- **Wire contracts** (`contracts/<protocol>/v<major>/`) change in a fixed order,
  in one commit: the Go types in `wire/` first, then `go run ./tools/mdpschema`
  to regenerate `schemas.json` (never edit it by hand; `make vet` fails on
  drift), then the hand-written `openapi.yaml` with a bumped `info.version`.
  Anything a v1 driver would misread goes into a new major version directory.
- **Decisions are written down before the work.** A decision that is expensive
  to reverse (a data shape, a wire contract, a default that lands on existing
  installs), counter-intuitive, or a trade-off between two bad options gets an
  Architecture Decision Record in `docs/en/06-Decisions/` (and its French twin in
  `docs/fr/06-Décisions/`) *before* it is implemented. The
  [decision index](docs/en/06-Decisions/Decision%20index.md) states the format.
  An ADR number is drawn at random (ADR-74818 explains why), never taken as
  the next in sequence. An accepted ADR is never rewritten: supersede it with
  a new one. The commit implementing an ADR cites its number.
- **Commit messages** explain the *why*; the diff already carries the *what*.
- Architecture notes: bilingual vault in [`docs/en/`](docs/en/) (entry point:
  [Index](docs/en/Index.md)) and [`docs/fr/`](docs/fr/).

## Pull requests

1. Keep changes focused; match existing style.
2. Run `make test` (and `make build` if you touch the frontend or Docker image).
3. Describe the *why* in the PR description.
