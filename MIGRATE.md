# Rearchitecture — migration status

**What this is.** The migration of meshcraft from in-process device drivers to
**external containerised drivers speaking a REST contract (MDP v1)**, plus a
written-down **agent (proxy) contract**, a **git→image build pipeline** for both,
and a **UI contract** carried by ratchets and skills.

**Branch:** `rearch-phase0`, **merged into `main` on 2026-10-24** (fast-forward,
198 commits — `main` was a strict ancestor, so nothing was reconciled and
nothing was lost). `main` is now the line of record again.
**Second repo:** the drivers monorepo (`meshcraft-drivers`, checked out next to
this one; not yet pushed to github.com/akwafr).
This file is the running status of the migration (plan approved 2026-08-17).
**Last updated:** 2026-10-24.

> **What merging did NOT do.** It did not run the Phase 4 lab-parity gate,
> which is still blocked on the lab being reachable (`tools/labparity`). `main`
> therefore carries the nine external drivers with their conformance results
> but WITHOUT a side-by-side comparison against the in-tree connectors on real
> hardware. That comparison is the thing that would justify deleting the
> in-tree vendor code, and it is why that deletion has not happened. Merging
> was a decision to stop maintaining a 198-commit divergence, not a claim that
> P4 is finished.

---

## Status at a glance

| Phase | What | Status |
|---|---|---|
| **P0** | In-tree contract hardening (`Supports()`, `not_applied`) | ✅ done |
| **P1** | MDP v1 spec + SDK + `restdriver` adapter + 2 pilot drivers | ✅ code-complete |
| **P2** | Driver build/run pipeline + admin UI | ✅ done |
| **P3** | UI contracts + primitives + skills | ✅ done |
| **P4** | Migrate the 7 remaining drivers; delete in-tree vendor code | 🟨 code done — **lab parity + in-tree deletion pending** |
| **P5** | Agent contract + registry generalization | ✅ code-complete (2 heavy items were the last delivery) |

**Everything builds and tests in Docker.** Gates, all green as of the last commit:

```
make lint        # := ratchet (0) + web uilint (baselines only go DOWN)
make vet         # go vet + MDP/Agent schemas.json drift check
make test        # test-go (43 pkgs) + test-web (i18n parity 2168, frontend suite)
# drivers repo:
cd ../meshcraft-drivers && make lint && make test && make build DRIVER=<name>
```

---

## The two contracts (source of truth = Go types)

Both live under `contracts/<proto>/v<major>/` and follow the **three-file change
order** (see `CONTRIBUTING.md` → "Wire contracts"):

1. **Go types first** — `wire/mdpv1` (driver) / `wire/agentv1` (agent). The only
   packages a third party imports. `wire/metrics` carries the shared series
   vocabulary.
2. **`schemas.json` generated** — `go run ./tools/mdpschema` (a *two-contract*
   generator); `make vet` runs `-check` and fails on drift.
3. **`openapi.yaml` by hand** — paths + the rules a compiler can't state.

Served live at `GET /api/v1/contracts/{mdp,agent}` (and `{…}-schemas`), anonymous,
from the embedded `contracts` package. The drivers monorepo keeps a **vendored**
copy (`contract/`) guarded by its `make contract-sync` / `make lint`.

---

## Phase detail

### P0 — contract hardening ✅
`Connector.Supports()` replaces type-assertion-as-detection.
`ApplyResult.NotApplied` (`{Port, Kind: guardrail|platform|device, Text}`, JSON
`not_applied`) is the one refusal model; MDP exposes only `not_applied`.
Commits: `fce7ba9`, `739dc6f`.

### P1 — MDP v1 + SDK + adapter + pilots ✅ (code)
- `wire/mdpv1` — the wire envelope, session model, composite read, op payloads (`3d97ccb`, `739dc6f`).
- `internal/restdriver` — controller-side adapter: wraps a driver container as
  `driver.Connector`/`Admin`, registers it in the ordinary registry, mints a
  per-session SOCKS credential scoped to one host (`89e6135`).
- `contracts/mdp/v1/` — openapi by hand + schemas generated (`42fb2e3`).
- `internal/socks` Minter — per-session credentials, one host, lease-tied.
- Drivers monorepo: `sdk/mdpserver` + `sdk/socksdial` (`603c663`), pilots
  **hpe-procurve** (`a284ee5`) and **fs-s3400** (`19d4377`), conformance examiner.

### P2 — build/run pipeline + admin UI ✅
- `internal/imagebuild` — git URL → running image, **no host paths** (clone in a
  container, archive stream re-rooted, POST to `/build`) (`e4e3343`).
- `internal/driverhost` — declarative layer: `driver_repos`→builds→instances,
  reconciled at startup (`b3a16bf`).
- Drivers admin page `/config/drivers` — the reference screen (`fc1a879`).

### P3 — UI contract ✅
- `UI-GUIDE.md` §8 Keyboard + §9 Responsive; `web/src/ui/` primitives
  (`dismiss`, `Form`, `Modal` v2, `picker`, `Banner`, `PageHeader`,
  `ConfirmDialog`, `breakpoint`, `tokens`).
- Ratchets in `web/scripts/uilint.mjs` (`make lint`); i18n parity (`make test-web`).
- The normative UI contract is `web/src/UI-GUIDE.md`; the ratchets enforce it.
- `usePicker` behind every dropdown; `Banner` behind every state banner
  (`5a49cec`, `ed88b10`, `d93e754`).

### P4 — migrate the 7 + delete in-tree 🟨
**Code done** (`meshcraft-drivers aba0d18`, `meshcraft 3736a0c`+`b05ddbf`):
all 9 drivers now live in `meshcraft-drivers/drivers/<Info().Name>/`.
7 images built, **conformance 10/10 each**. Three out-of-contract deps found new
homes on the wire:
- `sdk/webfetch` — web transport (Instant On / RouterOS / SwOS). Controller keeps
  its own copy for discovery + the web-UI relay (not drivers).
- `wire/metrics` — series vocabulary; `internal/metricbuf` re-exports as aliases.
- `mdpv1.NormalizeAlg` — SNMPv3 algorithm spelling; `internal/snmpv3` delegates.

**NOT done — the two gates before in-tree deletion:**
1. **Lab parity, per driver.** Tool ready: `tools/labparity` (`37a9edb`) reads a
   device through the in-tree driver, then through the container (restdriver +
   minted SOCKS + composite read), and diffs. Empty diff = safe to delete.
   **BLOCKED:** the lab proxy (`proxy-lab`) tunnel is **down** (last seen
   2026-08-16) — no lab device is reachable. Bring the proxy back, then run
   `tools/labparity` per driver against a lab device (e.g. a Cisco 2960X at
   `192.0.2.64`), with credentials revealed through the API.
   Order (least → most risky): cisco-ios → mikrotik-swos/routeros →
   ubiquiti-edgeswitch/fs-s3900 → **aruba-instant-on-1930 LAST** (5-session
   budget, paperclip recovery).
2. **Then delete** the 9 vendor packages from `internal/driver`
   (interfaces/aliases/helpers stay), drop `Cfg.DriverEndpoints`/`DriverURLs`,
   make `readDeviceState` use `BulkReader` unconditionally, and run a full
   discovery sweep to confirm the inventory matches pre-migration.

### P5 — agent contract + registry generalization ✅ (code-complete)
- **Contract** (`a98b52d`): `wire/agentv1` (enroll/heartbeat/migrate + metrics
  envelope), imported by BOTH `internal/api` and `cmd/proxy` (the anonymous
  structs and retyped copies are gone). `contracts/agent/v1/`. Routes
  `/api/v1/agent/*` with `/api/proxy/*` kept as a permanent alias.
- **Registry multi-repository** (`e41b0fd`): proxy stays at `OCIDir` root, agents
  at `OCIDir/<repo>/`; wildcard route splits from the right; traversal refused.
  `mkoci --name/--tag` (defaults reproduce the proxy layout).
- **Build pipeline** (`853da02`): `imagebuild.BuildOCI` exports a build as an OCI
  layout (docker-save → gzip layers → OCI blobs; config/diff_ids preserved).
  `internal/agenthost` clones→builds→staging→atomic swap→records. Migration
  `0044` (`agent_repos`, `agent_builds`; no instances — an agent is pulled, not
  run). Admin API `/api/agent-repos` (global-admin). `StartAgentHost` gated on
  the Docker socket only.
- **Admin UI** (`9713353`): `web/src/pages/Agents.tsx`, route `/config/agents`.
  Shows the pullable ref `<host>/<agent_name>:<version>` + copy.
  **NOT verified: a live browser render** (needs the new controller build running
  behind auth; the dev instance runs the old build).

---

## Remaining work (prioritised)

1. **Lab parity → in-tree deletion (finishes P4).** Blocked on the lab proxy.
   The largest remaining risk-reduction: deletes ~130k lines of duplicated driver
   code once parity is proven.
2. **Live render check of the Agents page.** Rebuild the controller image, restart
   the stack, drive `/config/agents` in a headless browser (Chrome over CDP
   with puppeteer-core, in a container). Catches hook/blank-page bugs tsc misses.
3. **`latest`-tag aliasing for agents.** They serve by exact version today; a
   proxy pulling `:latest` gets a 404. Add an alias (a second index annotation or
   a `latest`→version symlink layout).
4. **Push the drivers monorepo to GitHub.** Then the `replace meshcraft/wire =>
   ../meshcraft/wire` in `go.mod` becomes a versioned require, and image builds
   no longer need the sibling checkout.
5. **P5 nice-to-haves not built:** `contracts/tunnel/` schemas for the yamux
   control plane (documented by reference in the agent openapi today).
6. **Vault (docs/).** No Obsidian note yet describes MDP / driver externalisation
   / the agent pipeline — the openapi headers are the de-facto design docs. A
   `01-Architecture` note (fr+en) would also feed the graph.

---

## Gotchas worth keeping in view

- **Go rule #1: no `:=`.** `make lint` scans test files too — run it, not just
  `make vet`, before committing (a P5 commit shipped 7 `:=` in tests because only
  vet+test were run).
- **The proxy stays on `/api/proxy/*` deliberately** — it may face an older
  controller. Only the request/response *bodies* became shared `agentv1` types.
- **`mkoci` defaults must stay byte-identical** to the proxy layout — the
  Dockerfile invokes it with no `--name/--tag`.
- **Never stamp a proxy/driver/agent version "dev"** — it sorts after digits and
  is never offered an update. Versions are UTC stamps `yyyymmdd.HHMMSS`.
