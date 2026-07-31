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

- **Go**: never use `:=` — always `var` (see [CLAUDE.md](CLAUDE.md)).
- **Language**: all source code, comments, tests, and commit messages are in
  **English**. User-facing UI strings live in `web/src/i18n/locales/*.json`.
- **Do not rename** applied SQL migrations under `internal/store/migrations/`.
- Architecture notes: bilingual vault in [`docs/fr/`](docs/fr/) and
  [`docs/en/`](docs/en/).

## Pull requests

1. Keep changes focused; match existing style.
2. Run `make test` (and `make build` if you touch the frontend or Docker image).
3. Describe the *why* in the PR description.
