# Everything runs in containers: install nothing on the host except Docker.
GO_IMAGE   := golang:1.24-alpine
NODE_IMAGE := node:22-alpine
DOCKER     := sudo docker
GOCACHE    := meshcraft_gocache
GOMODCACHE := meshcraft_gomodcache

RUN_GO = $(DOCKER) run --rm -v $(CURDIR):/src -w /src \
	-v $(GOMODCACHE):/go/pkg/mod -v $(GOCACHE):/root/.cache/go-build \
	$(GO_IMAGE)

.PHONY: tidy vet lint test test-go test-web build up down logs web-dev fmt

# Go rule #1 — the short declaration operator `:=` is forbidden — as a
# RATCHET. `go vet` says nothing about it, so the rule drifted; this number is
# how many are still in ./internal ./cmd ./tools, and `make lint` fails the
# moment there are more.
#
# LINT_BASELINE MAY ONLY EVER BE REDUCED. Lowering it is the point of the
# target: clean a package, re-run `make lint`, it tells you the new number, put
# it here. Raising it to make a build pass defeats the whole thing — rewrite the
# `:=` you just added with `var` instead.
#
# 143 when the ratchet was introduced on 2026-08-08; swept to 0 the same day.
# It is a ratchet rather than a plain `-baseline 0` on purpose: the number is
# what makes a regression legible ("you added 3") instead of just red.
LINT_BASELINE := 0

## go mod tidy (updates go.sum) — in container
tidy:
	$(RUN_GO) go mod tidy
	$(RUN_GO) sh -c 'cd wire && go mod tidy'

## Static analysis
vet:
	$(RUN_GO) go vet ./...
	$(RUN_GO) sh -c 'cd wire && go vet ./...'
	## The MDP schema document must match the wire types it is generated from.
	$(RUN_GO) go run ./tools/mdpschema -check

## Rule #1: no `:=`. Counts real occurrences off the SYNTAX TREE (go/parser), so
## comments and string literals quoting the forbidden form are not counted, and
## the guard of a type switch — the one `:=` with no `var` form — is exempt.
## Fails above the baseline, printing every file:line so you see what you added.
RUN_NODE = $(DOCKER) run --rm -v $(CURDIR)/web:/web -w /web $(NODE_IMAGE)

lint:
	$(RUN_GO) go run ./scripts/shortdecl -baseline $(LINT_BASELINE) \
		./internal ./cmd ./tools ./wire
	## Frontend ratchets (UI-GUIDE §8/§9): baselines only ever go down.
	$(RUN_NODE) node scripts/uilint.mjs

## All tests: Go suite + frontend unit tests, each in its container
test: test-go test-web

## Go tests
## The wire module (meshcraft/wire) is a SEPARATE Go module — `./...` from the
## repo root does not reach it, so it is tested explicitly.
test-go:
	$(RUN_GO) go test ./...
	$(RUN_GO) sh -c 'cd wire && go test ./...'

## Frontend unit tests (web/src/**/*.test.ts): plain node scripts run with
## --experimental-strip-types — no framework, no npm install needed.
test-web:
	$(RUN_NODE) sh -c 'node scripts/i18n_parity.mjs && npm test'

## gofmt (rewrites files)
fmt:
	$(RUN_GO) gofmt -w ./cmd ./internal

## Build the controller image (frontend + multi-arch proxies included).
## Build timestamp is computed here, in UTC: one value per build,
## later visible in the admin UI.
build:
	$(DOCKER) compose build \
	  --build-arg MESHCRAFT_BUILD=$$(date -u +%Y%m%d.%H%M%S)

## Start controller + database
up:
	$(DOCKER) compose up -d

down:
	$(DOCKER) compose down

logs:
	$(DOCKER) compose logs -f controller

## Frontend in dev mode (hot reload, API expected on :8080)
web-dev:
	$(DOCKER) run --rm -it -v $(CURDIR)/web:/web -w /web --network host \
		$(NODE_IMAGE) sh -c "npm install --no-audit --no-fund && npm run dev -- --host"
