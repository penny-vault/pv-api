# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [3.0.0] - 2026-05-03

A complete rewrite of pv-api on Fiber v3, structured around a strategy
registry, portfolio CRUD, and an asynchronous backtest dispatcher. Almost
nothing from 0.1.0 carries forward; treat this as a new product.

### Added

#### Foundation & infrastructure
- Fiber v3 server scaffold with a Cobra `server` subcommand, zerolog
  structured logging, and `/healthz`.
- Auth0-issued JWT authentication middleware (RS256, JWKS).
- OpenAPI 3.0.3 contract checked into the repo (`openapi/openapi.yaml`);
  Go request/response types generated via `oapi-codegen`.
- Postgres migration harness (`golang-migrate`); schema versioned per
  migration file.
- Multi-arch (amd64 + arm64) Docker image published to Docker Hub on
  every `main` commit.
- Embedded Redoc (`/openapi`), Scalar (`/openapi/scalar`), and Swagger
  UI (`/openapi/swagger`) for API exploration.

#### Strategy registry
- `GET /strategies` and `GET /strategies/{shortCode}` backed by a sync
  goroutine that mirrors `pvbt/library` results into the local registry.
- Two artifact-production modes: host-mode `Install` (clone + `go build`
  → on-disk binary) and Docker-mode `InstallDocker` (clone + `ImageBuild`
  → tagged image, via a narrow `dockercli.Client` wrapper around the
  engine SDK).
- Version pinning via `git ls-remote --tags`; failed installs retry only
  on upstream change (`last_attempted_ver` + `install_error`).
- Daily stats refresher: each installed strategy is backtested and the
  resulting KPIs (`cagr`, `max_drawdown`, `sharpe`, `sortino`,
  `ulcer_index`, `beta`, `alpha`, `std_dev`, `tax_cost_ratio`,
  `one_year_return`, `ytd_return`, `benchmark_ytd_return`) are surfaced
  on the `/strategies` views.

#### Portfolio CRUD + lifecycle
- `POST/GET/PATCH/DELETE /portfolios` and `GET /portfolios/{slug}`
  (config-only).
- Slug generation (FNV-1a + preset-name match); ownership scoping via
  Auth0 sub.
- Optional `startDate`/`endDate` per portfolio; configurable
  `runRetention` (default 2).
- KPI columns surfaced on list/detail (`currentValue`, `ytdReturn`,
  `maxDrawdown`, `sharpe`, `cagrSinceInception`).
- `POST /portfolios/{slug}/upgrade`: bump to a newer strategy version;
  auto-merges parameter changes when compatible, 409s with a diff body
  when not, accepts an explicit `parameters` body for resubmit.
- **Auto-recompute on read:** snapshot-data endpoints return 202 with
  `RecalculatingResponse` when the run database is missing or
  unreadable; the handler opportunistically reuses any in-flight run.
  A partial unique index on `backtest_runs(portfolio_id) WHERE status
  IN ('queued','running')` enforces at-most-one-in-flight at the
  database layer, closing the race window between concurrent reads.

#### Backtests + snapshots
- `backtest.Runner` interface with `HostRunner` and `DockerRunner`
  implementations.
- `POST /portfolios/{slug}/run` async dispatch via a bounded worker
  pool (`Dispatcher`).
- Atomic snapshot writes (tmp + fsync + rename) to
  `<snapshots-dir>/<portfolio-id>.sqlite`.
- Eleven derived-data endpoints: `/summary`, `/drawdowns`,
  `/statistics`, `/metrics`, `/trailing-returns`, `/holdings`,
  `/holdings/{date}`, `/holdings/history`, `/holdings-impact`,
  `/performance`, `/transactions`.
- Per-ticker contribution analysis at `/holdings-impact` across YTD,
  1Y, 3Y, 5Y, and inception with top-N + rest bucket.
- SSE progress stream at `/portfolios/{slug}/runs/{runId}/progress`.
- Run history at `/portfolios/{slug}/runs` and
  `/portfolios/{slug}/runs/{runId}`.
- Automatic prune of old `backtest_runs` rows and their snapshot files
  honoring per-portfolio `run_retention`.

#### Unofficial strategies
- `GET /strategies/describe?cloneUrl=...` clones, builds, and runs
  `describe` against an arbitrary HTTPS GitHub repository without
  persisting anything.
- `POST /portfolios` accepts `strategyCloneUrl` (mutually exclusive
  with `strategyCode`); unofficial portfolios re-clone HEAD on every
  backtest via `EphemeralBuild` / `EphemeralImageBuild`.
- Strict URL allowlist (`^https://github\.com/...$`); bounded
  build/clone via `[strategy].ephemeral_install_timeout`.

#### Scheduler
- In-process scheduler ticks every 60 s, claims due portfolios via
  `SELECT FOR UPDATE SKIP LOCKED`, and dispatches through the same
  worker pool that serves `POST /run`.

#### Alerts + email
- Per-portfolio alert configuration
  (`POST/GET/PATCH/DELETE /portfolios/{slug}/alerts`).
- Mailgun delivery with MJML-compiled HTML templates (success and
  failure variants), benchmark delta, returns grid
  (Day / WTD / MTD / YTD / 1Y), holdings, and trades sourced from the
  transactions table.
- HMAC-SHA256 unsubscribe tokens with a one-click
  `/api/alerts/unsubscribe?token=...` endpoint.
- On-demand summary email at `/portfolios/{slug}/email-summary`.

### Changed
- Upgraded the HTTP framework from Fiber v2 to Fiber v3.
- Schema reset to v1; every 0.1.0 migration was replaced.
- OpenAPI document pinned to 3.0.3 (down from 3.1.0) for client-tooling
  compatibility.

### Removed
- Plaid link-token endpoint (an experimental 0.1.0 feature; not part of
  3.0).
- Legacy mode-based scheduling (`continuous` / `one_shot`); replaced by
  `start_date` / `end_date` columns on portfolios.

## [0.1.0] - 2024-12-14
### Added
- Tests for postgresql schema and functions

[Unreleased]: https://github.com/penny-vault/pv-api/compare/v3.0.0...HEAD
[3.0.0]: https://github.com/penny-vault/pv-api/compare/v0.1.0...v3.0.0
[0.1.0]: https://github.com/penny-vault/pv-api/releases/tag/v0.1.0
