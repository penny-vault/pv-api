# pvapi 3.0 design

**Status:** draft for review
**Date:** 2026-04-16
**Author:** Jeremy Fergason (with Claude)

## Summary

pvapi 3.0 is a ground-up rewrite of the Penny Vault HTTP API. It replaces the
Fiber v2 application that currently serves Plaid/account endpoints with a Fiber
v3 application that serves the portfolio surface defined in
`frontend-ng/api/openapi.yaml`. The new application integrates with
`github.com/penny-vault/pvbt` as a Go library and shells out to compiled
strategy binaries to execute backtests. Results are cached in Postgres and
persisted per-portfolio as pvbt SQLite snapshots.

The existing repository structure (`cmd/`, `api/`, `sql/`, `types/`,
`pkginfo/`) is preserved. All handlers, domain code, and migrations are
deleted and rewritten. The `main` branch becomes pvapi 3.0; the prior
codebase remains available on the `legacy` branch.

## Goals

- Serve the `frontend-ng` OpenAPI contract (copied into this repo as source of
  truth and extended for portfolio lifecycle endpoints).
- Drive all backtest computation through pvbt, via compiled strategy binaries
  sourced from GitHub repositories.
- Support three execution backends for backtests: host (direct exec),
  Docker, and Kubernetes, selectable at startup.
- Authenticate requests against Auth0 JWTs.
- Run as a single binary with an in-process scheduler for continuously
  updating portfolios.

## Non-goals

- User account management beyond what Auth0 provides. No users table.
- A UI. The UI lives in `frontend-ng`.
- Role-based authorization. Not needed by the OpenAPI contract yet.
- Multi-process / worker topology. One `pvapi` process.

## System architecture

### Process topology

A single `pvapi` binary. `pvapi server` boots:

- A Fiber v3 HTTP server bound to `server.port`.
- A Postgres connection pool (`pgxpool`) initialized on first use.
- An in-process scheduler goroutine for continuous portfolios.
- A strategy-registry sync goroutine.

Shutdown is coordinated through a root `context.Context` cancelled on SIGINT
or SIGTERM. The scheduler stops accepting new runs and waits for in-flight
backtests; the HTTP server stops accepting new connections; the pool closes
last.

### External dependencies

| Dependency | Role |
|---|---|
| **Postgres** (pgx v5) | Portfolio definitions, strategy registry, cached summaries, run history. |
| **Per-portfolio SQLite** | pvbt's snapshot format. Written by the runner, read by the measurements endpoint. Path stored on `portfolios.snapshot_path`. |
| **Auth0** | JWKS URL for JWT verification. No userinfo calls at request time. |
| **GitHub** | Strategy discovery and source clones via pvbt's `library` package. |
| **`github.com/penny-vault/pvbt`** | Imported as a Go library (`library/`, `tradecron/`). Compiled strategy binaries are invoked via the `Runner` interface. |

### High-level flow

```
HTTP request
  -> auth middleware (JWT -> sub)
  -> handler (api/)
  -> service (portfolio/, strategy/)
  -> sql/ (pgxpool) or SQLite read (portfolio/measurements)
  -> response (oapi-codegen types, application/json)

Scheduler tick
  -> SELECT due continuous portfolios (FOR UPDATE SKIP LOCKED)
  -> backtest.Run(ctx, portfolio)
       -> Runner.Run -> strategy binary (host | docker | kubernetes)
       -> atomic rename snapshot -> /var/lib/pvapi/snapshots/<id>.sqlite
       -> UPDATE portfolios (derived summary, status, next_run_at)
       -> close backtest_runs row
```

## Package layout

```
cmd/              Cobra root, `server` subcommand, viper wiring. Retained from 2.x.
api/              Fiber v3 app, middleware (auth, CORS, logger, request-id, timer),
                  route registration, error-to-problem+json mapping. Handlers
                  implement the oapi-codegen-generated server interface.
openapi/          openapi.yaml (copied from frontend-ng, extended) and the
                  generated server types.
portfolio/        Domain package: create, list, read, update, delete, measurements.
                  Contains db.go, service.go, handler.go, types.go.
strategy/         Registry, sync, install coordinator, describe-output validator.
                  Wraps pvbt/library.
backtest/         Runner interface and implementations (host, docker, kubernetes),
                  single `Run` entry point, snapshot reader.
scheduler/        Ticker-driven loop over due continuous portfolios.
sql/              pgxpool singleton, tx helper, embedded migrations. Auto-migrate
                  on first pool access (current behavior retained).
types/            Cross-package shared types.
pkginfo/          -ldflags-injected version/commit/date.
cache/            Removed. 2.x used tinylru for userinfo responses; pvapi 3.0
                  has no userinfo call and JWK caching comes from jwx.Cache.
```

Dependency direction: `api -> portfolio -> {backtest, strategy}`;
`scheduler -> backtest`; `backtest -> strategy, sql`;
`strategy -> sql, pvbt/library`. `sql`, `types`, `pkginfo` sit below everything.
No cycles.

## Data model

### Postgres schema (fresh `1_init` migration)

```sql
-- strategies: registry of all strategies pvapi knows about
CREATE TYPE artifact_kind AS ENUM ('binary', 'image');

CREATE TABLE strategies (
    short_code      TEXT PRIMARY KEY,
    repo_owner      TEXT NOT NULL,
    repo_name       TEXT NOT NULL,
    clone_url       TEXT NOT NULL,
    is_official     BOOLEAN NOT NULL DEFAULT FALSE,
    owner_sub       TEXT,              -- Auth0 sub for unofficial; NULL for official
    description     TEXT,
    categories      TEXT[],
    stars           INTEGER,
    installed_ver   TEXT,
    installed_at    TIMESTAMPTZ,
    artifact_kind   artifact_kind,
    artifact_ref    TEXT,              -- binary path or image ref, per runner mode
    describe_json   JSONB,             -- full `describe --json` output
    cagr            DOUBLE PRECISION,  -- computed stats, nullable until first run
    max_drawdown    DOUBLE PRECISION,
    sharpe          DOUBLE PRECISION,
    stats_as_of     TIMESTAMPTZ,
    discovered_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((is_official AND owner_sub IS NULL) OR (NOT is_official AND owner_sub IS NOT NULL))
);
CREATE INDEX idx_strategies_official ON strategies(is_official);
CREATE INDEX idx_strategies_owner ON strategies(owner_sub) WHERE owner_sub IS NOT NULL;

-- portfolios: configuration + cached derived summary
CREATE TYPE portfolio_mode AS ENUM ('one_shot', 'continuous');
CREATE TYPE portfolio_status AS ENUM ('pending', 'running', 'ready', 'failed');

CREATE TABLE portfolios (
    id              UUID PRIMARY KEY DEFAULT uuidv7(),
    owner_sub       TEXT NOT NULL,                        -- Auth0 `sub`
    slug            TEXT NOT NULL,                        -- e.g. adm-aggressive-gm59
    name            TEXT NOT NULL,                        -- user-provided display name
    strategy_code   TEXT NOT NULL REFERENCES strategies(short_code),
    strategy_ver    TEXT NOT NULL,                        -- pinned at create time
    parameters      JSONB NOT NULL,                       -- validated vs describe_json.Parameters
    preset_name     TEXT,                                 -- preset matched at create time, or NULL
    benchmark       TEXT NOT NULL DEFAULT 'SPY',
    mode            portfolio_mode NOT NULL,
    schedule        TEXT,                                 -- tradecron string; NULL for one_shot
    status          portfolio_status NOT NULL DEFAULT 'pending',
    inception_date  DATE,                                 -- set after first successful run
    snapshot_path   TEXT,                                 -- abs path to per-portfolio SQLite
    last_run_at     TIMESTAMPTZ,
    next_run_at     TIMESTAMPTZ,                          -- for continuous portfolios
    last_error      TEXT,
    -- derived summary (written by backtest.Run on success)
    current_value   DOUBLE PRECISION,
    ytd_return      DOUBLE PRECISION,
    max_drawdown    DOUBLE PRECISION,
    sharpe          DOUBLE PRECISION,
    cagr_since_inception DOUBLE PRECISION,
    summary_json    JSONB,                                -- OpenAPI PortfolioSummary
    drawdowns_json  JSONB,                                -- []Drawdown
    metrics_json    JSONB,                                -- []PortfolioMetric
    trailing_json   JSONB,                                -- []TrailingReturnRow
    allocation_json JSONB,                                -- []AllocationRow
    current_assets  TEXT[],
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (owner_sub, slug)
);
CREATE INDEX idx_portfolios_owner ON portfolios(owner_sub);
CREATE INDEX idx_portfolios_due ON portfolios(next_run_at)
    WHERE mode = 'continuous' AND status IN ('ready', 'failed');

-- backtest_runs: one row per Run invocation
CREATE TYPE run_status AS ENUM ('queued', 'running', 'success', 'failed');
CREATE TABLE backtest_runs (
    id              UUID PRIMARY KEY DEFAULT uuidv7(),
    portfolio_id    UUID NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
    status          run_status NOT NULL,
    started_at      TIMESTAMPTZ,
    finished_at     TIMESTAMPTZ,
    duration_ms     INTEGER,
    error           TEXT,
    snapshot_path   TEXT
);
CREATE INDEX idx_runs_portfolio ON backtest_runs(portfolio_id, started_at DESC);
```

Notes:

- Primary keys use Postgres 18's built-in `uuidv7()`; no `pgcrypto` extension.
- Two tables only: `strategies`, `portfolios`. Derived summary columns live on
  `portfolios` directly; strategy stats live on `strategies` directly. No
  separate `portfolio_summaries` / `strategy_stats` tables.
- Ownership is keyed by Auth0 `sub`. No users table.
- `portfolios.strategy_ver` is pinned at portfolio creation so old portfolios
  stay reproducible after a strategy publishes a new version.

### Per-portfolio SQLite

pvbt's existing snapshot format (`metadata`, `perf_data`, `transactions`,
`holdings`, `tax_lots`, `metrics`, `annotations`). Path layout:

```
/var/lib/pvapi/snapshots/<portfolio_id>.sqlite
```

The runner writes to `<path>.new`, fsyncs, and atomically renames into place
to avoid torn reads. pvapi opens snapshots **read-only** for the
`/portfolios/{slug}/measurements` endpoint.

### Migrations

Delete `1_pvui_v0_1_0` and `2_scf_2023`. Start fresh with `1_init.up.sql`
and `1_init.down.sql` covering the tables above. Auto-migrate on first
pool access is retained.

## API surface

pvapi 3.0 becomes the source of truth for the OpenAPI contract. Copy
`frontend-ng/api/openapi.yaml` into `openapi/openapi.yaml` and extend it with
the portfolio-lifecycle and strategy endpoints below. `frontend-ng` then
consumes `pv-api`'s spec.

All endpoints require `BearerAuth` (Auth0 JWT). Errors are
`application/problem+json` per RFC 7807.

### Portfolios

| Method | Path | Purpose |
|---|---|---|
| GET    | `/portfolios` | List the caller's portfolios (existing). |
| POST   | `/portfolios` | Create: strategy + parameters + mode + schedule. Returns 201 with slug. |
| GET    | `/portfolios/{slug}` | Full detail (existing; path was `/portfolios/{id}`). |
| PATCH  | `/portfolios/{slug}` | Update name, schedule, or parameters. Changing parameters or pinning a new strategy version triggers a re-run. |
| DELETE | `/portfolios/{slug}` | Delete portfolio row and its snapshot file. |
| GET    | `/portfolios/{slug}/measurements` | Time series (existing). |
| POST   | `/portfolios/{slug}/runs` | Trigger a one-shot backtest now. Returns 202 with the new `backtest_runs` row. |
| GET    | `/portfolios/{slug}/runs` | Run history. |
| GET    | `/portfolios/{slug}/runs/{runId}` | Single run detail. |

### Strategies

| Method | Path | Purpose |
|---|---|---|
| GET    | `/strategies` | List strategies in the registry; `?include=unofficial` to opt in. |
| GET    | `/strategies/{shortCode}` | Describe output (parameters, presets, schedule, benchmark) + computed stats. |
| POST   | `/strategies` | Register an unofficial strategy by GitHub clone URL. User-scoped visibility. |

### Slug generation

`<short_code>-<preset_or_custom>-<4char>`

- Preset segment is the matched preset name (kebab-case, sanitized) when
  parameters equal a preset's parameter set. Otherwise `custom`.
- 4-character suffix is a base32 short hash of
  `(params_json, mode, schedule, benchmark)`. Deterministic: identical
  configurations produce identical slugs. A duplicate create for the same
  user returns `409` pointing at the existing slug.

### Conventions

- IDs internally are UUID v7; externally the API uses slugs for portfolios
  and short codes for strategies.
- Problem+JSON on every error (`type`, `title`, `status`, `detail`,
  `instance`). Status codes: 400, 401, 403, 404, 409, 422, 500.
- Run progress is polled via `GET /portfolios/{slug}/runs/{runId}`. No SSE
  in 3.0.
- Request/response types generated by `oapi-codegen` in strict-server mode.
  Handlers implement the generated interface; no hand-rolled JSON structs.

### Create-portfolio request body

```
POST /portfolios
{
  "name":           "ADM aggressive",
  "strategyCode":   "adm",
  "strategyVer":    "v1.2.0",           // optional; defaults to latest installed
  "parameters":     { ... },
  "benchmark":      "SPY",              // optional; defaults to strategy describe
  "mode":           "continuous",       // "one_shot" | "continuous"
  "schedule":       "@monthend",        // required iff mode=continuous
  "runNow":         true                // optional; kick the first run immediately
}
```

- `mode=one_shot` implicitly runs immediately on create (`runNow` is ignored
  and treated as true).
- `mode=continuous` sets `next_run_at` to the next tradecron boundary; if
  `runNow=true`, also enqueues an immediate first run.
- `strategyVer` that is not installed triggers an install synchronously
  (bounded by a timeout) before the create returns. Official strategies
  already installed are a no-op. Unofficial strategies always install
  ephemerally per run — `strategyVer` is tracked but no pre-install happens.

## Auth

- Bearer token from `Authorization` header only. Cookie and query-parameter
  fallbacks from 2.x are dropped.
- `api/auth.go` uses `lestrrat-go/jwx/v3` with a `jwk.Cache`. JWKS URL,
  audience, and issuer come from `[auth0]` config.
- On each request: cache lookup, parse, validate `iss`/`aud`/`exp`; stash
  `sub` on `fiber.Ctx.Locals`. No userinfo call.
- Role middleware is dropped until we have an admin surface.

## Strategy lifecycle

### Registry sync

A background goroutine runs on `strategy.registry_sync_interval` (default
1 hour). It:

1. Wraps the pass in a `pg_try_advisory_lock` so concurrent pvapi instances
   do not race (future-proofing).
2. Calls `pvbt/library.Search` with `owner:penny-vault topic:pvbt-strategy`.
3. Reconciles into `strategies`: insert new rows, update volatile fields
   (`stars`, `description`, `categories`, `updated_at`), leave unseen rows
   alone.
4. For any official strategy not yet installed, enqueues an install.
5. For installed official strategies, `git ls-remote` for the latest tag;
   if a newer tag exists, builds the new version into a new directory and
   atomically flips `strategies.installed_ver`/`artifact_ref` after success.
   Old artifacts remain available; portfolios pin `strategy_ver`.

### Install per runner

| Runner | Official install | Unofficial install |
|---|---|---|
| Host | `git clone` + `go build` -> binary at `/var/lib/pvapi/strategies/official/<owner>/<repo>/<version>/bin`. `artifact_ref = <path>`, `artifact_kind = binary`. | `git clone` + `go build` into `/tmp/pvapi-strategies/<uuid>/bin`, removed after run. |
| Docker | `git clone` + build image from generated Dockerfile (go-builder stage + distroless runtime) -> `pvapi-strategy/<owner>/<repo>:<version>`. `artifact_ref = <image>`, `artifact_kind = image`. | Image tagged `pvapi-strategy/ephemeral/<uuid>:latest`; `docker image rm` after run. |
| Kubernetes | Build via the configured builder (BuildKit or Kaniko pod spec in config, or an external CI pipeline), push to `runner.registry`. `artifact_ref = <registry>/…:<version>`. | Same path as official; more expensive, user experience notes this. |

### Unofficial strategies

Users register unofficial strategies via `POST /strategies` with a GitHub
clone URL. They are stored in the registry with `is_official = FALSE` and
installed lazily per-run (ephemeral by design). Visibility is scoped to the
user who registered them — the `GET /strategies` endpoint filters by
ownership unless `?include=unofficial` is set.

## Backtest execution

### Runner interface

```go
package backtest

type Runner interface {
    Run(ctx context.Context, req RunRequest) (RunResult, error)
}

type RunRequest struct {
    Strategy  StrategyArtifact
    Params    map[string]any
    Benchmark string
    OutPath   string
    Timeout   time.Duration
}

type StrategyArtifact struct {
    Kind artifact_kind  // binary | image
    Ref  string         // path or image reference
}
```

### Implementations

- **`HostRunner`**: `exec.CommandContext(ctx, bin, "backtest", "--config",
  cfg, "--out", outPath, "--json")`. stdout captured as progress events
  logged at `info`; stderr captured and logged at `error` on failure.
- **`DockerRunner`**: uses `github.com/docker/docker/client`. Runs the image
  with `--rm`, a tmpfs working dir, and a bind-mount for `outPath`. Works
  when pvapi itself runs in Docker with `/var/run/docker.sock` mounted in,
  provided the host path for `/var/lib/pvapi/snapshots` is mounted into the
  pvapi container at the same path (so bind-mount strings pvapi passes to
  the daemon resolve correctly on the host). Deployment notes below.
- **`KubernetesRunner`**: uses `k8s.io/client-go` to create a
  `batch/v1 Job`. Pod writes the snapshot into a shared PVC (configured
  in `[runner.kubernetes]`); pvapi waits for completion, copies the file
  from the PVC to `outPath`, deletes the Job.

Selected at startup by `runner.mode = host | docker | kubernetes`.

### backtest.Run

```
1. Load strategy. If official and not installed, install first. If unofficial,
   clone + build ephemeral artifact.
2. Insert `backtest_runs` row (status = running).
3. Materialize pvbt config with portfolio parameters + benchmark into tmpdir.
4. Runner.Run(...).
5. On success: fsync + atomic rename tmpSqlite -> /var/lib/pvapi/snapshots/<id>.sqlite.
   Open snapshot, extract stats, update portfolios row (derived columns +
   JSONB blobs + status=ready + last_run_at + inception_date if unset +
   last_error=NULL). Close backtest_runs row (success).
6. On failure: leave snapshot in place, set portfolios.status=failed,
   populate last_error. Close backtest_runs row (failed) with error text.
7. Defer-release ephemeral artifacts and tmpdirs. Respect ctx cancellation.
```

## Scheduler

- Single ticker in `scheduler/`, default interval 60 seconds (config).
- Each tick:

  ```sql
  SELECT id, schedule
    FROM portfolios
   WHERE mode = 'continuous'
     AND status IN ('ready', 'failed')
     AND (next_run_at IS NULL OR next_run_at <= NOW())
   ORDER BY next_run_at NULLS FIRST
   LIMIT :batch_size
     FOR UPDATE SKIP LOCKED;
  ```

- For each due portfolio: set `status='running'`, compute
  `next_run_at = tradecron.Next(schedule, now)`, dispatch to
  `backtest.Run` on a bounded worker pool (default size = `runtime.NumCPU()`,
  configurable).
- `schedule` uses pvbt tradecron syntax (`@monthend`, `@daily`, etc.).
- Portfolio creation with `mode=continuous` bootstraps `next_run_at` to the
  next cron boundary, or immediately if the request sets `run_now=true`.

## Errors

- `api/errors.go` exports `WriteProblem(c fiber.Ctx, err error)`. It switches
  on sentinel errors using `errors.Is`:
  - `portfolio.ErrNotFound`, `strategy.ErrNotFound` -> 404
  - `*.ErrConflict` -> 409 (duplicate slug, etc.)
  - `*.ErrInvalidParams` -> 422 with `instance` pointing at the offending field
  - anything else -> 500 with a generic title; full error logged with request_id.
- Validation errors from `oapi-codegen` map to 422 at the boundary.

## Config

Cobra + Viper + TOML, unchanged. `pvapi.toml`, env `PVAPI_*`, flags, in
that precedence order.

```toml
[server]
port = 3001
allow_origins = "https://www.pennyvault.com"

[auth0]
jwks_url     = "https://<tenant>.us.auth0.com/.well-known/jwks.json"
audience     = "https://api.pennyvault.com"
issuer       = "https://<tenant>.us.auth0.com/"

[db]
url = "postgres://pvapi@db/pvapi"

[runner]
mode = "host"   # host | docker | kubernetes

  [runner.host]
  # nothing today

  [runner.docker]
  # socket path, network, resource limits

  [runner.kubernetes]
  namespace       = "pvapi"
  service_account = "pvapi-backtests"
  snapshot_pvc    = "pvapi-snapshots"
  image_registry  = "ghcr.io/penny-vault"
  builder         = "buildkit"   # or "kaniko" or "external"

[strategy]
registry_sync_interval = "1h"
official_dir           = "/var/lib/pvapi/strategies/official"
ephemeral_dir          = "/tmp/pvapi-strategies"
github_query           = "owner:penny-vault topic:pvbt-strategy"

[scheduler]
tick_interval = "60s"
worker_pool   = 0      # 0 = runtime.NumCPU()
batch_size    = 32

[log]
level = "info"
pretty = false
```

Retired sections: `[plaid]`, `[email]`, `[debug]`.

Subcommands: `pvapi server` (http + scheduler), `pvapi version`. Root
defaults to `server` for backward compat with the current entrypoint.

## Logging

Zerolog, retained. Fields attached to every log line:

- Global: `version`, `commit`, `runner_mode`.
- Per-request: `request_id`, `sub`, `method`, `path`, `status`,
  `duration_ms`.
- Per-backtest: `run_id`, `portfolio_id`, `short_code`, `strategy_ver`.

Strategy stdout captured line-by-line at `info` with `child=<short_code>`;
stderr at `error` on non-zero exit.

## Testing

- Ginkgo/Gomega per package, following the existing `*_suite_test.go`
  pattern.
- **Tests never touch a live database** and the project does not adopt a
  database-mocking library. `sql/` is covered only by compile-time
  checks and whatever end-to-end verification the runtime exercises (the
  pool is wired up at server start; auto-migrate runs on first access).
  Any SQL correctness work happens via code review, not unit tests.
- `strategy/` suite uses a checked-in fake strategy repository under
  `testdata/` to exercise clone/build/describe without hitting GitHub. Hit
  GitHub once per PR via a single smoke test that can be skipped locally.
- `backtest/` suite uses a stub `Runner` that copies a checked-in SQLite
  fixture to `OutPath`. A `//go:build integration` file exercises
  `HostRunner` against the fake strategy end-to-end.
- `api/` suite boots the Fiber app in-process (no database), mints test
  JWTs using the repo's existing `jwk-test-priv.json`/`jwk-test-pub.json`,
  and hits endpoints. `httpmock` for any outbound GitHub traffic. Handlers
  that need the pool receive it through an interface that tests stub with
  a no-op; DB-reachability failures are surfaced at server start, not at
  test time.
- No hard coverage threshold; every handler and service method has at
  least one happy-path test.

## Build and CI

- `Makefile`: `build`, `lint`, `test`, `clean` (as today); add `gen`
  (re-run oapi-codegen).
- `.github/workflows/ci-unit-tests.yml` is retained and extended with a
  job running the integration-tagged suites. Lint target kept.
- `-ldflags` stamping via `pkginfo/`, unchanged.

## Filesystem layout on the host

```
/var/lib/pvapi/
  strategies/
    official/<owner>/<repo>/<version>/bin       # host runner only
  snapshots/
    <portfolio_id>.sqlite                       # per-portfolio pvbt snapshot
/tmp/pvapi-strategies/
  <uuid>/bin                                    # ephemeral unofficial (host)
```

Docker and Kubernetes runners use image references and PVCs instead, per
`runner.kubernetes.snapshot_pvc`.

## Deployment considerations

- pvapi is expected to run in a Docker container in production.
- Host runner is intended for local development and VM deployments.
- Docker runner requires the Docker socket mounted into the pvapi container
  (`/var/run/docker.sock`). Understand the privilege implications. The host
  path for `snapshots/` must also be mounted into pvapi at the same
  in-container path, because bind-mount strings pvapi passes to the Docker
  daemon resolve against the host filesystem, not pvapi's own.
- Kubernetes runner requires a service account with permissions to create
  Jobs in `runner.kubernetes.namespace`, plus either in-cluster BuildKit /
  Kaniko or an external build pipeline.
- The `snapshots` volume must be writable by pvapi (host) or mounted into
  backtest containers (docker/kubernetes).

## Migration from 2.x

- `legacy` branch (already pushed) preserves the Plaid/account codebase at
  its last commit on main.
- `main` branch is reset to pvapi 3.0. Delete:
  - `account/` (Plaid and account domain code)
  - `sql/migrations/1_pvui_v0_1_0.*` and `sql/migrations/2_scf_2023.*`
  - All handlers in `api/` (rewrite)
  - `pvapi.toml` `[plaid]`, `[email]`, `[debug]` sections
- Retain:
  - `cmd/` scaffolding (Cobra/Viper/TOML wiring), with config struct rewritten
  - `sql/` pool/migrate scaffolding (migrations replaced)
  - `pkginfo/`, `types/`
  - CI workflow, Makefile, lint config, copyright header
  - Module path `github.com/penny-vault/pv-api` and binary name `pvapi`
  - JWK test keys `jwk-test-priv.json` / `jwk-test-pub.json` (used by tests)
- Remove:
  - `cache/` (tinylru; no remaining consumer)
- Rewrite:
  - `api/` — Fiber v3, auth middleware, new routes
  - `go.mod` — drop Fiber v2, Plaid SDK, goccy/go-json; add Fiber v3,
    oapi-codegen runtime, Docker client, k8s client-go, modernc sqlite
- `pvapi` 3.0 release will ship an initial `/var/lib/pvapi/` bootstrap on
  first run (creating directories if missing).

## Open items

These are known gaps to be pinned down during implementation, not during
brainstorming:

- **Kubernetes builder**: the spec allows BuildKit, Kaniko, or an external
  pipeline — the concrete choice (and how the build step is invoked) is
  deferred to the first k8s deployment.
- **Strategy-registry stats seeding**: per-strategy `cagr`/`sharpe`/
  `max_drawdown` need a reference backtest configuration. Either shipped
  in the strategy's own repository as a canonical preset, or recomputed by
  pvapi. Decision deferred; nullable columns for now.
- **SSE/WebSocket for run progress**: currently polling only. If the UI
  needs live progress, add later.
