# meshcraft — development conventions

## Go rule #1: no `:=`

The short declaration operator `:=` is **forbidden** in all Go code in this
project. Always declare explicitly with `var`:

```go
// forbidden
u, err := store.GetUser(ctx, id)
for i := range items { ... }
if v := os.Getenv(k); v != "" { ... }

// correct
var u, err = store.GetUser(ctx, id)

var i int
for i = range items { ... }

var v = os.Getenv(k)
if v != "" { ... }
```

## Project context

- Full architecture: Obsidian vault under `docs/` (entry: `docs/fr/Meshcraft - Index.md` / `docs/en/`).
- Backend **and** proxy in Go, monorepo (`cmd/controller`, `cmd/proxy`, `cmd/mkoci`).
- React + TypeScript frontend (PWA), embedded in the controller binary via `go:embed`.
- **Everything builds and tests in Docker**: install nothing on the host beyond Docker.
  `make tidy` / `make test` / `make build` run through containers (see Makefile).
- Database: PostgreSQL (`db` service in docker-compose).
- Licence: AGPL-3.0-only.
