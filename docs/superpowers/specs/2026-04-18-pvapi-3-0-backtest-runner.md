# pvapi 3.0 — Plan 5: Backtest runner + snapshot-served slice

**Date:** 2026-04-18
**Parent spec:** [`2026-04-16-pvapi-3-0-design.md`](./2026-04-16-pvapi-3-0-design.md)
**Predecessors:** Plans 1–4 (all merged to `main`).

## Goal

Make portfolios actually run. Plan 4 left every derived-data endpoint stubbed to 501 and `runNow` as a no-op; Plan 5 wires the backtest runner end-to-end so that creating a `one_shot` portfolio executes a strategy binary, produces a per-portfolio SQLite snapshot, and serves all of the derived views (`/summary`, `/drawdowns`, `/statistics`, `/trailing-returns`, `/holdings`, `/holdings/{date}`, `/holdings/history`, `/performance`, `/transactions`, `/runs`, `/runs/{runId}`) from that snapshot.

## Guiding principle — state lives in SQLite

The per-portfolio SQLite snapshot is the single source of truth for everything a backtest produces: equity curve, holdings, transactions, scalar statistics. Postgres carries only orchestration state (`status`, `last_run_at`, `next_run_at`, `last_error`, `snapshot_path`) plus a handful of scalar KPI columns on `portfolios` so that the list endpoint (`GET /portfolios`) can render top-line numbers without opening N snapshots.

Every other derived endpoint reads the snapshot at request time. No JSONB caches.

## Scope

In scope for Plan 5:

- `backtest.Runner` interface + `HostRunner` implementation.
- `backtest.Run(ctx, portfolioID)` entry point (single-run orchestration).
- `backtest.Dispatcher` — bounded worker pool used by both `POST /runs` and the (future) scheduler.
- `snapshot` reader package — typed accessors over the per-portfolio SQLite.
- Real implementations of all eleven derived endpoints (paths listed above + `/holdings/history` — see below).
- `POST /portfolios/{slug}/runs` async dispatch (202 + background goroutine).
- Auto-trigger first run for `mode=one_shot` portfolios at create time, and for `mode=continuous` when `runNow=true`.
- OpenAPI contract updates: rename `/metrics` → `/statistics`, `/measurements` → `/performance`; add `/transactions`.
- Migration 5 — drop dead JSONB columns and `current_assets` from `portfolios`.
- Config wiring (`[backtest]` and `[runner]`).
- Snapshots directory layout and startup sweep of stale `.tmp` files.

Explicitly out of scope (lands later):

- Scheduler loop for `mode=continuous` portfolios — Plan 6.
- Unofficial strategies — Plan 7.
- Docker runner — Plan 8. (`runner.mode="docker"` returns a config error at startup in Plan 5.)
- Kubernetes runner — Plan 9. (Same.)
- Live trading — future project. `mode=live` still returns 422 at the API surface.

## Package layout

```
backtest/
  runner.go        # Runner interface, RunRequest, RunResult, errors
  host.go          # HostRunner implementation (exec.CommandContext)
  run.go           # backtest.Run entry point (orchestration)
  dispatcher.go    # bounded worker pool; Submit(portfolioID) + graceful shutdown
  errors.go        # sentinels: ErrAlreadyRunning, ErrRunnerFailed, ErrTimedOut, ...
  host_test.go
  dispatcher_test.go
  run_test.go
  testdata/
    fakestrat/main.go   # tiny Go program compiled by TestMain; copies fixture SQLite to --output

snapshot/
  reader.go        # Open(path) (*Reader, error); Close; read-only *sql.DB handle
  errors.go        # ErrNotFound (wraps sql.ErrNoRows for date-parameterized reads)
  summary.go       # Summary(ctx) (*api.PortfolioSummary, error)
  drawdowns.go     # Drawdowns(ctx) ([]api.Drawdown, error)
  statistics.go    # Statistics(ctx) ([]api.PortfolioStatistic, error)
  returns.go       # TrailingReturns(ctx) ([]api.TrailingReturnRow, error)
  holdings.go      # CurrentHoldings(ctx) (*api.HoldingsResponse, error)
                   # HoldingsAsOf(ctx, date) (*api.HoldingsResponse, error)  -- transaction replay
                   # HoldingsHistory(ctx, from, to) (*api.HoldingsHistoryResponse, error) -- per-batch
  performance.go   # Performance(ctx, slug, from, to) (*api.PortfolioPerformance, error)
  transactions.go  # Transactions(ctx, filter) ([]api.Transaction, error)
  kpis.go          # Kpis(ctx) (Kpis, error)  -- scalar values written to portfolios row
  reader_test.go   # covers all the above against testdata/sample.sqlite
  testdata/
    sample.sqlite  # checked-in fixture, ~20 trading days, one strategy, known values
```

Dependency direction: `api → portfolio → backtest → snapshot`; `snapshot` depends only on `database/sql` + `modernc.org/sqlite` + pvbt's metric helpers (`github.com/penny-vault/pvbt/portfolio`). No cycles; `api` never imports `snapshot` directly.

## Runner interface

```go
package backtest

type Runner interface {
    Run(ctx context.Context, req RunRequest) error
}

type RunRequest struct {
    Binary  string        // absolute path to strategy binary (from strategy.Store.InstallDir)
    Args    []string      // strategy-specific CLI flags derived from parameters + benchmark
    OutPath string        // absolute path where the snapshot must land (temporary name)
    Timeout time.Duration // 0 = use default from backtest config
}
```

### HostRunner

```
exec.CommandContext(ctx, req.Binary,
    append([]string{"backtest", "--output", req.OutPath}, req.Args...)...)
```

- stdout captured line-by-line and logged at `info` with a `strategy` log scope.
- stderr captured into a bounded buffer (first 8KiB); on non-zero exit or context-cancel, wrapped into `ErrRunnerFailed` with the first 2KiB attached.
- On context cancellation: `cmd.Process.Signal(syscall.SIGTERM)` then `SIGKILL` after 5s grace (Go standard pattern for `exec.CommandContext`).
- No stdin.

### Parameter mapping

Each key in `portfolio.parameters` becomes a CLI flag `--<kebab-case-name> <value>`. The strategy's `describe_json.Parameters` (persisted in Plan 3) declares the full parameter set, so `backtest.Run` knows every flag that needs to be passed. `--benchmark <ticker>` is always appended from `portfolio.benchmark`. No `--start`/`--end` — strategies decide their own windows based on their parameters.

Values are serialized with `fmt.Sprintf("%v", v)`; bool `true/false`, numbers stringify naturally, strings pass through, arrays are comma-joined. This matches pvbt CLI conventions.

## backtest.Run orchestration

```go
func Run(ctx context.Context, portfolioID, runID uuid.UUID) error
```

The `backtest_runs` row was already inserted (status=`queued`) by `Dispatcher.Submit`; `Run` receives its id so it can update that row as it progresses. `Run` is called from a worker goroutine; the worker count is the concurrency ceiling — no separate semaphore inside `Run`.

```
 1. SELECT * FROM portfolios WHERE id = $portfolioID.
 2. If row.status = 'running':
      - UPDATE backtest_runs SET status='failed',
            finished_at=NOW(), error='portfolio already running'
          WHERE id=$runID
      - return ErrAlreadyRunning.
 3. BEGIN tx:
      UPDATE portfolios SET status='running', updated_at=NOW()
        WHERE id=$portfolioID;
      UPDATE backtest_runs SET status='running', started_at=NOW()
        WHERE id=$runID;
    COMMIT.
 4. Resolve strategy artifact: path := strategy.Store.Binary(code, ver).
    If missing → fail fast with ErrStrategyNotInstalled.
 5. args := BuildArgs(portfolio.Parameters, portfolio.Benchmark,
                     strategy.describe_json.Parameters)
 6. tmp   := filepath.Join(cfg.SnapshotsDir, portfolioID+".sqlite.tmp")
    final := filepath.Join(cfg.SnapshotsDir, portfolioID+".sqlite")
    os.Remove(tmp)  // clear any stale partial from a prior crashed run
 7. runErr := runner.Run(ctx, RunRequest{Binary: path, Args: args,
                                          OutPath: tmp, Timeout: cfg.Timeout})
 8. On success (runErr == nil):
      - fd, _ := os.Open(tmp); fd.Sync(); fd.Close()
      - os.Rename(tmp, final)
      - reader, _ := snapshot.Open(final)
      - kpis, _ := reader.Kpis(ctx)
      - reader.Close()
      - BEGIN tx:
          UPDATE portfolios SET
              status='ready',
              last_run_at=NOW(),
              last_error=NULL,
              snapshot_path=$final,
              current_value=$kpis.CurrentValue,
              ytd_return=$kpis.YtdReturn,
              max_drawdown=$kpis.MaxDrawdown,
              sharpe=$kpis.Sharpe,
              cagr_since_inception=$kpis.Cagr,
              inception_date=COALESCE(inception_date, $kpis.InceptionDate),
              updated_at=NOW()
            WHERE id=$1;
          UPDATE backtest_runs SET
              status='success',
              finished_at=NOW(),
              duration_ms=...,
              snapshot_path=$final
            WHERE id=$runID;
        COMMIT.
 9. On failure (runErr != nil):
      - leave tmp in place (will be swept at next startup)
      - BEGIN tx:
          UPDATE portfolios SET
              status='failed',
              last_error=firstNBytes(runErr, 2048),
              updated_at=NOW()
            WHERE id=$1;
          UPDATE backtest_runs SET
              status='failed',
              finished_at=NOW(),
              duration_ms=...,
              error=firstNBytes(runErr, 2048)
            WHERE id=$runID;
        COMMIT.
10. Return runErr (nil on success). Worker goroutine loops back to pick up the next task.
```

Atomic rename guarantees that any concurrent reader of `snapshot_path` sees either the old snapshot or the new one, never a partial file. Postgres transactions ensure that `portfolios` and `backtest_runs` update together.

### Startup sweep

On server boot, `backtest` enumerates `cfg.SnapshotsDir`:

- Any `<id>.sqlite.tmp` older than 1 hour is removed.
- Any `portfolios.status='running'` row discovered at startup is marked `failed` with `last_error='server restarted mid-run'` — workers were killed, we can't recover their in-progress state. User can retry via `POST /runs`.

## Dispatcher

```go
type Dispatcher struct {
    cfg     Config
    runner  Runner
    db      *pgxpool.Pool
    tasks   chan task             // capacity = MaxConcurrency * 4
    wg      sync.WaitGroup
    ctx     context.Context       // cancelled on Shutdown
    cancel  context.CancelFunc
}

type task struct { portfolioID, runID uuid.UUID }

func NewDispatcher(cfg Config, runner Runner, db *pgxpool.Pool) *Dispatcher
func (d *Dispatcher) Start(parent context.Context)
func (d *Dispatcher) Submit(ctx context.Context, portfolioID uuid.UUID) (runID uuid.UUID, err error)
func (d *Dispatcher) Shutdown(gracePeriod time.Duration) error
```

- `Submit` inserts the `backtest_runs` row with `status='queued'` and pushes a task onto `tasks`. If the task queue is full, returns `ErrQueueFull` (handler maps to 503). Returns the run id on success.
- `Start` spins up `MaxConcurrency` worker goroutines; each loops reading from `tasks` and invoking `backtest.Run(d.ctx, task.portfolioID, task.runID)`. Worker count is the only concurrency limit — no separate semaphore.
- `Shutdown` closes `tasks`, waits up to `gracePeriod` for workers to drain remaining tasks, then calls `d.cancel()` so any in-flight `backtest.Run` sees its context cancelled. Cancelled runs take the failure path in `backtest.Run` and record `status='failed'` with `error='server shutting down'`.

Same dispatcher instance is shared with the scheduler in Plan 6. In Plan 5 only `POST /runs` and portfolio-create auto-trigger call `Submit`.

## Auto-trigger on portfolio create

Plan 4 accepted `runNow` as a no-op. Plan 5 wires it up inside `portfolio.Handler.Create`:

- After the successful INSERT, before returning 201:
  - `mode=one_shot` → always `dispatcher.Submit(portfolioID)` (ignore `runNow`).
  - `mode=continuous` and `runNow=true` → `dispatcher.Submit(portfolioID)`.
  - `mode=continuous` and `runNow=false` → leave pending; Plan 6 scheduler will pick it up.
  - `mode=live` → still returns 422 earlier in the handler; never reaches dispatch.
- `dispatcher.Submit` is non-blocking and does its own DB writes, so the Create handler still returns 201 promptly with `status='queued'`.

## Snapshot reader

```go
type Reader struct { db *sql.DB }

func Open(path string) (*Reader, error)   // modernc-sqlite, read-only (uri=...?mode=ro)
func (r *Reader) Close() error
```

All reader methods accept a context (cancellation propagates into SQLite queries via `QueryContext`). Each returns the matching OpenAPI shape directly — the `snapshot` package knows about `github.com/penny-vault/pv-api/api/oapi` types.

Computation strategy:

- `Summary` — compute top-line KPIs by selecting the last `perf_data` row(s) and pulling Sharpe/Sortino/Beta/Alpha/StdDev/UlcerIndex/TaxCostRatio/MaxDrawDown from the `metrics` table.
- `Drawdowns` — stream `perf_data` equity curve and use `pvbt/portfolio` drawdown helpers to detect peaks/troughs/recoveries.
- `Statistics` — select from `metrics` table filtered by `window='full'` (or equivalent), format each as `{label, value, format}` using a hardcoded label/format lookup per known metric name.
- `TrailingReturns` — compute YTD, 1Y, 3Y, 5Y, 10Y, since-inception from the `perf_data` series using pvbt helpers; emit the two OpenAPI rows (`portfolio` and `benchmark`).
- `CurrentHoldings` — `SELECT asset_ticker, asset_figi, quantity, avg_cost, market_value FROM holdings`; compute `totalMarketValue` as SUM.
- `HoldingsAsOf(date)` — 404 if `date` falls outside the backtest window recorded in `metadata` (`start_date` .. `end_date`). Otherwise replay every row of `transactions WHERE date <= $date` ordered by `(date, rowid)` to produce a per-ticker running ledger of `{quantity, avg_cost, last_price}`. `marketValue` is approximated as `quantity * last_price`, where `last_price` is the most recent `price` column seen for that ticker in the replayed transactions (the per-portfolio snapshot does not carry an EOD price table; this is the best signal available in-file). Zero-quantity tickers are omitted.
- `HoldingsHistory(from, to)` — emits one entry per batch (a strategy rebalance event) in the backtest. Uses the pvbt-recommended batches-join query: cumulative sum over `transactions JOIN batches ON t.batch_id <= b.batch_id`, grouped by `(batch_id, ticker, figi)`, filtering `HAVING quantity != 0`. Annotations for each batch are pulled with a second query and zipped by `batch_id`. Optional `from` / `to` filter the batch range by `batches.timestamp`. **Depends on pvbt shipping the `batches` table + adding `batch_id` to `transactions` and `annotations`; the fixture builder in this plan creates the schema locally so work can land in parallel with pvbt's change.**
- `Performance(slug, from, to)` — stream `perf_data WHERE metric IN ('portfolio_value','benchmark_value') AND date BETWEEN ...`; emit `PortfolioPerformance{points: [{date, portfolioValue, benchmarkValue}]}`.
- `Transactions(filter)` — parameterized WHERE over `transactions` table.
- `Kpis` — called by `backtest.Run` post-success; same data as `Summary` but returns an internal struct, not the API shape.

## OpenAPI contract changes

**Ordering note:** these contract changes must land as the first task of Plan 5, before any handler or reader work. oapi-codegen regenerates `api/oapi/` from the contract, and the renames (`PortfolioMetric` → `PortfolioStatistic`, `PortfolioMeasurements` → `PortfolioPerformance`, `MeasurementPoint` → `PerformancePoint`) plus the new `Transaction`/`TransactionsResponse` types are referenced by every downstream file. Doing the contract edit, regenerating types, and fixing the compile errors in one task keeps the rename blast radius contained to a single commit.

The `openapi/openapi.yaml` contract ships the following diff in Plan 5:

1. Path rename: `/portfolios/{slug}/metrics` → `/portfolios/{slug}/statistics`. Schema rename: `PortfolioMetric` → `PortfolioStatistic`. OperationId: `getPortfolioMetrics` → `getPortfolioStatistics`.
2. Path rename: `/portfolios/{slug}/measurements` → `/portfolios/{slug}/performance`. Schema renames: `PortfolioMeasurements` → `PortfolioPerformance`, `MeasurementPoint` → `PerformancePoint`. OperationId: `getPortfolioMeasurements` → `getPortfolioPerformance`.
3. New path: `GET /portfolios/{slug}/transactions` with query parameters `from`, `to`, `type`. New schema `Transaction`. OperationId: `getPortfolioTransactions`.
4. New path: `GET /portfolios/{slug}/holdings/history` with query parameters `from`, `to`. New schemas `HoldingsHistoryResponse`, `HoldingsHistoryEntry`. OperationId: `getPortfolioHoldingsHistory`.

`Transaction` schema:

```yaml
Transaction:
  type: object
  required: [date, type]
  properties:
    date:    { type: string, format: date }
    type:
      type: string
      enum: [buy, sell, dividend, fee, deposit, withdrawal, split, interest, journal]
    ticker:  { type: string, nullable: true }
    figi:    { type: string, nullable: true }
    quantity:       { type: number, format: double }
    price:          { type: number, format: double }
    amount:         { type: number, format: double }
    qualified:      { type: boolean, nullable: true }
    justification:  { type: string, nullable: true }

TransactionsResponse:
  type: object
  required: [items]
  properties:
    items:
      type: array
      items: { $ref: '#/components/schemas/Transaction' }

HoldingsHistoryEntry:
  type: object
  required: [batchId, timestamp, items]
  properties:
    batchId:    { type: integer, format: int64 }
    timestamp:  { type: string,  format: date-time }
    items:
      type: array
      items: { $ref: '#/components/schemas/Holding' }
    annotations:
      type: object
      additionalProperties: { type: string }
      description: Optional strategy-written key/value labels for this batch.

HoldingsHistoryResponse:
  type: object
  required: [items]
  properties:
    items:
      type: array
      items: { $ref: '#/components/schemas/HoldingsHistoryEntry' }
```

oapi-codegen regenerates `api/oapi/` types on this contract; downstream references in `api/portfolios.go` and `portfolio.Handler` update accordingly. Stub 501 routes for the renamed endpoints are replaced with real handlers.

## Schema — migration 5

```sql
-- up
BEGIN;
ALTER TABLE portfolios
    DROP COLUMN summary_json,
    DROP COLUMN drawdowns_json,
    DROP COLUMN metrics_json,
    DROP COLUMN trailing_json,
    DROP COLUMN allocation_json,
    DROP COLUMN current_assets;
COMMIT;

-- down
BEGIN;
ALTER TABLE portfolios
    ADD COLUMN summary_json     JSONB,
    ADD COLUMN drawdowns_json   JSONB,
    ADD COLUMN metrics_json     JSONB,
    ADD COLUMN trailing_json    JSONB,
    ADD COLUMN allocation_json  JSONB,
    ADD COLUMN current_assets   TEXT[];
COMMIT;
```

Nothing in the codebase reads these columns today (Plan 4 never wrote to them), so the drop is safe. The scalar KPI columns (`current_value`, `ytd_return`, `max_drawdown`, `sharpe`, `cagr_since_inception`, `inception_date`) stay — `backtest.Run` populates them and `GET /portfolios` reads them for the list view.

## Derived endpoint handlers

All ten follow the same shape inside `portfolio.Handler`:

```go
func (h *Handler) Summary(c fiber.Ctx) error {
    sub, err := subject(c)
    if err != nil { return api.WriteProblem(c, err) }

    slug := string([]byte(c.Params("slug")))  // fiber-buffer copy

    p, err := h.store.GetBySlug(ctx, sub, slug)
    if errors.Is(err, portfolio.ErrNotFound) { return api.WriteProblem(c, api.ErrNotFound) }
    if err != nil { return api.WriteProblem(c, err) }

    if p.Status != portfolio.StatusReady || p.SnapshotPath == "" {
        return api.WriteProblemWithDetail(c, api.ErrNotFound, "no successful run")
    }

    reader, err := snapshot.Open(p.SnapshotPath)
    if err != nil { return api.WriteProblem(c, err) }
    defer reader.Close()

    out, err := reader.Summary(c.Context())
    if err != nil { return api.WriteProblem(c, err) }

    return c.JSON(out)
}
```

One helper per endpoint; near-identical skeletons differ only in the reader call. `/holdings/{date}` additionally parses the date path parameter (ISO YYYY-MM-DD, 422 on parse error). `/transactions`, `/performance`, and `/holdings/history` parse `from` / `to` query params (and `type` for `/transactions`).

`/runs` and `/runs/{runId}` bypass the snapshot entirely — they query `backtest_runs` via the Store.

## Config

```toml
[backtest]
snapshots_dir   = "/var/lib/pvapi/snapshots"
max_concurrency = 0      # 0 means runtime.NumCPU()
timeout         = "15m"

[runner]
mode = "host"            # "docker"/"kubernetes" rejected at startup in Plan 5
```

Startup validates:

- `snapshots_dir` exists (created with `0750` if missing) and is writable by the process.
- `runner.mode == "host"`. Anything else returns a fatal config error with "docker/kubernetes runners not available until Plan 8/9".
- `max_concurrency >= 0`; `0` is replaced with `runtime.NumCPU()` internally.
- `timeout > 0`.

Effective values are logged at boot.

## Failure handling

| Mode | Detection | Action |
|------|-----------|--------|
| Strategy binary non-zero exit | `exec.ExitError` | `ErrRunnerFailed`; stderr captured in `last_error`. Old snapshot intact. |
| Strategy binary panic | stderr shows Go runtime panic | Same as non-zero exit. |
| Timeout | `ctx.Err() == context.DeadlineExceeded` | SIGTERM → SIGKILL after 5s; `ErrTimedOut` with "timed out after 15m". |
| Server shutdown mid-run | `Dispatcher.Shutdown` cancels worker contexts | In-flight runs flip to `failed` via `Run`'s cancel path; startup sweep also catches any that didn't record failure. |
| Rename tmp → final fails | `os.Rename` error | Treated as runner failure; partial `.tmp` stays for next startup sweep. |
| KPI compute fails post-rename | error from `reader.Kpis` | Run marked failed, but snapshot IS in place. `status='failed'` until user retries. Rare edge case — snapshot written but unreadable. |

No automatic retry. `backtest_runs` rows are append-only; users retry by hitting `POST /runs` again, which creates a new row.

## Testing

Per project conventions (Ginkgo/Gomega, no live DB, no DB mocking):

- `backtest/host_test.go`: `TestMain` compiles `testdata/fakestrat/main.go` to a temp binary. Tests cover success (copies fixture SQLite to `--output`), non-zero exit (fake binary exits 1 on some args), timeout (fake binary sleeps past timeout), stderr capture, context cancellation.
- `backtest/dispatcher_test.go`: in-memory fake Runner that blocks on channels; tests cover concurrency ceiling, queue backpressure, graceful shutdown draining in-flight and cancelling queued.
- `backtest/run_test.go`: uses fake Runner + real snapshot.Reader against a fixture + existing fake portfolio.Store and backtest.RunStore; asserts happy path updates all expected Postgres state, asserts failure path leaves status=failed with populated last_error.
- `snapshot/reader_test.go`: one `Describe` per reader method, against `testdata/sample.sqlite` with known values. The fixture is produced once by running `fakestrat` with a deterministic parameter set and is version-controlled.
- `portfolio/handler_test.go`: adds derived-endpoint specs using a `FakeSnapshotReader` that the handler accepts via constructor injection. This lets us unit-test each derived endpoint's happy path (200 with known shape), not-ready path (404 "no successful run"), and not-found path (404) without touching disk.

Interface injection for snapshot reader:

```go
// portfolio/handler.go
type SnapshotOpener interface {
    Open(path string) (SnapshotReader, error)
}

type SnapshotReader interface {
    Summary(ctx context.Context) (*api.PortfolioSummary, error)
    Drawdowns(ctx context.Context) ([]api.Drawdown, error)
    Statistics(ctx context.Context) ([]api.PortfolioStatistic, error)
    TrailingReturns(ctx context.Context) ([]api.TrailingReturnRow, error)
    CurrentHoldings(ctx context.Context) (*api.HoldingsResponse, error)
    HoldingsAsOf(ctx context.Context, date time.Time) (*api.HoldingsResponse, error)
    HoldingsHistory(ctx context.Context, from, to *time.Time) (*api.HoldingsHistoryResponse, error)
    Performance(ctx context.Context, slug string, from, to *time.Time) (*api.PortfolioPerformance, error)
    Transactions(ctx context.Context, filter TransactionFilter) (*api.TransactionsResponse, error)
    Close() error
}
```

Production wires `snapshot.Opener{}` which returns `*snapshot.Reader`. Tests pass a fake opener.

## Directories and permissions

```
/var/lib/pvapi/
  snapshots/
    <portfolio_id>.sqlite         # atomic final files
    <portfolio_id>.sqlite.tmp     # partial files during an active run (swept at startup)
```

- `snapshots_dir` created with `0750` if missing.
- Files written with `0640`.
- Process user owns both. Docker/K8s deployment notes are captured in Plans 8/9 — not Plan 5's concern.

## Open items

None blocking Plan 5. Deferred by design:

- Scheduler loop for `mode=continuous` (Plan 6).
- Unofficial strategy clone-at-runtime (Plan 7).
- Docker/K8s runners (Plans 8/9).
- Live trading (future).
- `/transactions` pagination — add `?limit`/`?cursor` once real backtests hit thousands of transactions and JSON payloads get unwieldy.
