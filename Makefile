# Everything runs in containers: install nothing on the host except Docker.
GO_IMAGE   := golang:1.24-alpine
NODE_IMAGE := node:22-alpine
DOCKER     := sudo docker
GOCACHE    := meshcraft_gocache
GOMODCACHE := meshcraft_gomodcache

RUN_GO = $(DOCKER) run --rm -v $(CURDIR):/src -w /src \
	-v $(GOMODCACHE):/go/pkg/mod -v $(GOCACHE):/root/.cache/go-build \
	$(GO_IMAGE)

.PHONY: tidy vet test build up down logs web-dev fmt

## go mod tidy (updates go.sum) — in container
tidy:
	$(RUN_GO) go mod tidy

## Static analysis
vet:
	$(RUN_GO) go vet ./...

## Go tests
test:
	$(RUN_GO) go test ./...

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
