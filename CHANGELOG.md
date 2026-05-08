# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Live progress (`pct`, `step`, `eta`) on `GET /portfolios/{slug}/runs`
  and `GET /portfolios/{slug}/runs/{runId}`, so polling clients can show
  progress without opening the SSE stream.
- `GET /portfolios/{slug}/statistics` now includes CAGR, Calmar, Information
  Ratio, Upside/Downside Capture, Win Rate, and Profit Factor.
- `GET /portfolios/{slug}/trailing-returns` now also returns `portfolio-tax`
  and `benchmark-tax` rows.
- `GET /portfolios/{slug}/metrics` accepts `window=10yr` and exposes
  pvbt's benchmark and after-tax metrics (`BenchmarkTWRR`, `BenchmarkCAGR`,
  `AfterTaxTWRR`, `AfterTaxCAGR`, and the rest of the `Benchmark*` family).

### Changed
- `PortfolioStatistic.value` is nullable. Metrics absent from the snapshot
  return `null` instead of `0`, so clients can distinguish missing from zero.
- `GET /portfolios/{slug}/summary` is now a strict passthrough of pvbt's
  metrics table. Every metric field (`ytdReturn`, `oneYearReturn`,
  `cagrSinceInception`, `sharpe`, `sortino`, `beta`, `alpha`, `stdDev`,
  `maxDrawDown`, `taxCostRatio`, `ulcerIndex`) is nullable, returning
  `null` when pvbt did not emit the value for the snapshot.
- `GET /portfolios/{slug}/trailing-returns` benchmark and after-tax rows
  are now sourced from pvbt rather than recomputed locally. The buy-and-hold
  15% LTCG approximation has been replaced by pvbt's lot-level tax model.
- Default CORS allowlist now includes `https://pennyvault.com` and
  `https://www.pennyvault.com` alongside the localhost dev origins.

### Removed
- `PortfolioSummary.benchmarkYtdReturn`. Use
  `GET /portfolios/{slug}/trailing-returns` for benchmark windowed returns.

### Fixed
- Snapshot reads no longer loop in `recalculating` forever. Each backtest
  run now writes its own snapshot file under
  `<snapshots_dir>/<portfolio_id>/<run_id>.sqlite`, so pruning older runs
  no longer deletes the active snapshot.
- Auto-recompute on a `failed` portfolio now stops after one consecutive
  failed retry (previously every read enqueued a new doomed run); the
  response is `503` with `last_error` in the body. Use `POST
  /portfolios/{slug}/runs` to retry explicitly.
- Snapshot subdirectory is removed when a portfolio is deleted.
- Server restart no longer leaves portfolios stuck recalculating: orphaned
  `queued`/`running` backtest runs are flipped to `failed` at startup, and
  a queue-full `Submit` no longer leaves a phantom `queued` row behind.
- Alert emails render correctly on mobile: the returns grid no longer
  attaches labels to the wrong values when columns stack, and trades-table
  headers and dollar amounts no longer wrap mid-word or mid-number.
- Alert email dates and the "since" label now use US/Eastern, so the run
  date no longer reads as tomorrow for a US reader after late-evening UTC
  rollover and "since Thursday" no longer appears on a Thursday.

### Added
- Periodic orphan-snapshot sweep removes any `<snapshots_dir>/<portfolio_id>/<run_id>.sqlite`
  that no DB row references, plus per-portfolio dirs whose portfolio has
  been deleted. Runs at startup and every `backtest.orphan_gc_interval`
  (default 7d; <0 disables the periodic sweep).

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
