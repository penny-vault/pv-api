# pvapi 3.0 — Plan 6: Scheduler + continuous mode

**Status:** draft for review
**Date:** 2026-04-19
**Author:** Jeremy Fergason (with Claude)
**Supersedes scheduler section of:** `docs/superpowers/specs/2026-04-16-pvapi-3-0-design.md`

## Summary

Plan 6 adds an in-process scheduler goroutine that picks up due continuous
portfolios, advances `next_run_at` via `tradecron.Next`, and submits them to
the existing `backtest.Dispatcher`. The scheduler shares the bounded worker
pool with `POST /runs` — there is one pool per `pvapi` process, not two.

No schema change is required: the `next_run_at` column and the
`idx_portfolios_due` partial index were created by `1_init` in Plan 2.

Plan 6 also tightens `portfolio.Create` for continuous portfolios:

- `runNow` is ignored for `mode=continuous` (treated as true, matching the
  existing one_shot behavior). There is no valid use case for a continuous
  portfolio that skips its first run.
- The supplied `schedule` is validated via `tradecron.New(schedule,
  tradecron.RegularHours)` at create time. Invalid schedules return 422.
- `next_run_at` is bootstrapped on create to `tradecron.Next(now)` so the
  scheduler has a concrete boundary to fire against.

## Goals

- Continuous portfolios automatically re-run on their tradecron schedule.
- Scheduler and `POST /runs` share a single bounded worker pool
  (the `backtest.Dispatcher` from Plan 5).
- FOR UPDATE SKIP LOCKED claim semantics so the scheduler is future-proof
  against a multi-instance deployment, without requiring one today.
- Schedule syntax errors are caught at `POST /portfolios` time, not at the
  first cron boundary.

## Non-goals

- Multi-process scheduling topology. Single `pvapi` process remains the
  default; the SKIP LOCKED pattern just keeps the door open.
- SSE / WebSocket push for run progress. Polling via
  `GET /portfolios/{slug}/runs/{runId}` stays as-is.
- Per-portfolio market-hours configuration. All continuous portfolios use
  `tradecron.RegularHours`. A future plan can add per-portfolio overrides
  if a use case appears.
- Schedule edits. `PATCH /portfolios/{slug}` remains name-only (design
  spec rule). Changing the cron expression requires DELETE + re-create.

## Key decisions

| Decision | Choice | Rationale |
|---|---|---|
| `runNow` for continuous | Forced true, field ignored | No valid use case for continuous without an initial run (user directive, 2026-04-19). |
| Claim transaction shape | One short tx: SELECT FOR UPDATE SKIP LOCKED + UPDATE `next_run_at`, commit. `Submit` happens after commit. | Minimises row-lock duration. `Submit` is I/O and should not run inside a DB tx. |
| Queue-full at submit | Log warn and skip the firing. Do NOT rewind `next_run_at`. | Portfolio will fire again at the newly-advanced boundary. Prevents thundering-herd when the pool is saturated. |
| Invalid schedule at runtime | Log error, skip that row in the tick. Do not mark the portfolio `failed`. | Create-time validation makes this near-impossible. If it does happen, operator intervention is appropriate; we don't want the scheduler to quietly poison portfolio state. |
| Portfolio status transitions | Orchestrator owns them via existing `MarkRunningTx` / `MarkReadyTx` / `MarkFailedTx` methods. Scheduler touches only `next_run_at`. | Single source of truth for run lifecycle. Scheduler-only concern is the timer. |
| Market hours | Always `tradecron.RegularHours`. | No per-portfolio override use case today; hard-coding keeps the config surface flat. |
| Schedule parse cache | No cache. Parse on each tick. | Parse cost is negligible vs. the DB round trip. YAGNI; revisit if profiling says otherwise. |
| Immediate first tick on startup | Yes — `tickOnce` runs before the ticker starts. | Catches anything that became due while `pvapi` was down. |
| Scheduler errors | Logged; scheduler continues running. | A single bad tick must not kill the scheduler goroutine. |

## Architecture

### Package layout

**New:** `scheduler/`

| File | Contents |
|---|---|
| `scheduler/config.go` | `Config{TickInterval, BatchSize}`, `ApplyDefaults`, `Validate` |
| `scheduler/scheduler.go` | `Scheduler` struct, `New`, `Run(ctx)` blocking loop, `tickOnce`, `TradecronNext` helper |
| `scheduler/scheduler_test.go` | Ginkgo specs: happy path, queue-full, dispatcher error, ctx cancel, immediate first tick |
| `scheduler/scheduler_suite_test.go` | Ginkgo suite entry |

**Modified:**

| File | Change |
|---|---|
| `portfolio/db.go` | Add `PoolStore.ClaimDueContinuous` |
| `portfolio/store.go` | Embed claim method into `Store` interface |
| `portfolio/handler.go` | `Create`: validate `schedule` via `tradecron.New` for continuous; bootstrap `next_run_at`; always `Submit` on continuous (ignore request `runNow`) |
| `cmd/config.go` | Add `schedulerConf` to `Config` |
| `cmd/viper.go` | Defaults for `[scheduler]` section |
| `cmd/server.go` | Construct `Scheduler`, launch goroutine under root context |

No OpenAPI changes. `runNow` in the request body is retained; its value is
ignored for continuous (documented in OpenAPI description text only).

No migration. `1_init` already has `next_run_at` column and
`idx_portfolios_due` predicate index matching the scheduler query.

### Scheduler interfaces

```go
package scheduler

type Scheduler struct {
    store      PortfolioStore
    dispatcher Dispatcher
    cfg        Config
    nextRun    NextRunFunc
}

type PortfolioStore interface {
    ClaimDueContinuous(
        ctx context.Context,
        before time.Time,
        batchSize int,
        nextRun NextRunFunc,
    ) ([]Claim, error)
}

type Dispatcher interface {
    Submit(ctx context.Context, portfolioID uuid.UUID) (uuid.UUID, error)
}

type NextRunFunc func(schedule string, now time.Time) (time.Time, error)

type Claim struct {
    PortfolioID uuid.UUID
    Schedule    string
    NextRunAt   time.Time // newly-advanced value, for logging
}

func New(cfg Config, store PortfolioStore, dispatcher Dispatcher, nextRun NextRunFunc) *Scheduler
func (s *Scheduler) Run(ctx context.Context) error
```

`Dispatcher` is satisfied by the Plan 5 `*backtest.Dispatcher` via a thin
adapter in `cmd/server.go`. `PortfolioStore` is satisfied by
`*portfolio.PoolStore` via a thin adapter (needed because the scheduler's
`Claim` type is scheduler-owned; the portfolio package uses its own domain
types).

### Tick loop

```go
func (s *Scheduler) Run(ctx context.Context) error {
    s.tickOnce(ctx) // immediate first tick
    ticker := time.NewTicker(s.cfg.TickInterval)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-ticker.C:
            s.tickOnce(ctx)
        }
    }
}

func (s *Scheduler) tickOnce(ctx context.Context) {
    claims, err := s.store.ClaimDueContinuous(ctx, time.Now(), s.cfg.BatchSize, s.nextRun)
    if err != nil {
        log.Error().Err(err).Msg("scheduler: claim failed")
        return
    }
    for _, c := range claims {
        runID, err := s.dispatcher.Submit(ctx, c.PortfolioID)
        switch {
        case errors.Is(err, backtest.ErrQueueFull):
            log.Warn().
                Str("portfolio_id", c.PortfolioID.String()).
                Time("next_run_at", c.NextRunAt).
                Msg("scheduler: queue full, firing skipped until next boundary")
        case err != nil:
            log.Error().Err(err).
                Str("portfolio_id", c.PortfolioID.String()).
                Msg("scheduler: submit failed")
        default:
            log.Info().
                Str("portfolio_id", c.PortfolioID.String()).
                Str("run_id", runID.String()).
                Time("next_run_at", c.NextRunAt).
                Msg("scheduler: dispatched")
        }
    }
}
```

### Claim SQL (portfolio/db.go)

One transaction per tick. Parse errors from `nextRun` skip that single row
and do not abort the whole batch.

The method signature uses portfolio-local types only; the `scheduler` package
is not imported here. `cmd/server.go` adapts between `portfolio.DueContinuous`
and `scheduler.Claim` (same pattern as Plan 5's `backtestPortfolioStoreAdapter`).

```go
package portfolio

// DueContinuous is a portfolio row claimed for a scheduler tick, with its
// new next_run_at already committed.
type DueContinuous struct {
    PortfolioID uuid.UUID
    Schedule    string
    NextRunAt   time.Time
}

// NextRunFunc computes the next scheduled execution time for a tradecron
// schedule string. Returning an error causes that row to be skipped.
type NextRunFunc func(schedule string, now time.Time) (time.Time, error)

func (s *PoolStore) ClaimDueContinuous(
    ctx context.Context,
    before time.Time,
    batchSize int,
    nextRun NextRunFunc,
) ([]DueContinuous, error) {
    tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
    if err != nil {
        return nil, err
    }
    defer tx.Rollback(ctx)

    rows, err := tx.Query(ctx, `
        SELECT id, schedule
        FROM portfolios
        WHERE mode = 'continuous'
          AND status IN ('ready', 'failed')
          AND next_run_at IS NOT NULL
          AND next_run_at <= $1
        ORDER BY next_run_at
        LIMIT $2
        FOR UPDATE SKIP LOCKED
    `, before, batchSize)
    if err != nil {
        return nil, err
    }

    type pending struct {
        id       uuid.UUID
        schedule string
    }
    var pendings []pending
    for rows.Next() {
        var p pending
        if err := rows.Scan(&p.id, &p.schedule); err != nil {
            rows.Close()
            return nil, err
        }
        pendings = append(pendings, p)
    }
    rows.Close()

    var claims []DueContinuous
    for _, p := range pendings {
        nextAt, err := nextRun(p.schedule, before)
        if err != nil {
            log.Error().Err(err).
                Str("portfolio_id", p.id.String()).
                Str("schedule", p.schedule).
                Msg("scheduler: invalid schedule in DB, skipping row")
            continue
        }
        if _, err := tx.Exec(ctx,
            `UPDATE portfolios SET next_run_at = $1, updated_at = NOW() WHERE id = $2`,
            nextAt, p.id,
        ); err != nil {
            return nil, err
        }
        claims = append(claims, DueContinuous{
            PortfolioID: p.id,
            Schedule:    p.schedule,
            NextRunAt:   nextAt,
        })
    }

    if err := tx.Commit(ctx); err != nil {
        return nil, err
    }
    return claims, nil
}
```

**Dependency direction.** Unchanged from the design spec: `api -> portfolio
-> {backtest, strategy}`, `scheduler -> backtest`. No `portfolio -> scheduler`
edge. The `schedulerStoreAdapter` in `cmd/server.go` converts
`portfolio.DueContinuous` → `scheduler.Claim` and `scheduler.NextRunFunc`
→ `portfolio.NextRunFunc` at the seam.

### portfolio.Handler.Create changes

For `mode=continuous`:

1. **Validate schedule.** `_, err := tradecron.New(schedule,
   tradecron.RegularHours)`. On error, return 422 with
   `{title: "Invalid schedule", detail: err.Error(), instance: "#/schedule"}`.
2. **Compute `next_run_at`.** `nextAt := tc.Next(time.Now())`. Insert with
   `next_run_at = nextAt`.
3. **Force initial run.** After insert, call
   `dispatcher.Submit(ctx, portfolioID)` unconditionally (ignore the
   request body `runNow` field). Same treatment as one_shot.
4. **Submit failure handling.** `ErrQueueFull` → 503 (matches existing
   `POST /runs` pattern) with the portfolio row rolled back so the user
   can retry cleanly. Other Submit errors → 500, also with rollback.

The transactional boundary is: `INSERT portfolios` + `INSERT backtest_runs`
in a single tx, with `Submit`'s side-effects (channel send to dispatcher
workers) happening after commit. This matches Plan 5's existing handler
shape — no new pattern needed.

### Config

```toml
[scheduler]
tick_interval = "60s"
batch_size    = 32
enabled       = true   # opt-out for tests / maintenance
```

`schedulerConf` struct (cmd/config.go):

```go
type schedulerConf struct {
    TickInterval time.Duration `mapstructure:"tick_interval"`
    BatchSize    int           `mapstructure:"batch_size"`
    Enabled      bool          `mapstructure:"enabled"`
}
```

Viper defaults (cmd/viper.go):

- `scheduler.tick_interval = "60s"`
- `scheduler.batch_size = 32`
- `scheduler.enabled = true`

### cmd/server.go wiring

After the dispatcher is started and before Fiber begins serving:

```go
if conf.Scheduler.Enabled {
    sched := scheduler.New(
        scheduler.Config{
            TickInterval: conf.Scheduler.TickInterval,
            BatchSize:    conf.Scheduler.BatchSize,
        },
        &schedulerStoreAdapter{store: portStore},
        &schedulerDispatcherAdapter{dispatcher: dispatcher},
        scheduler.TradecronNext,
    )
    go func() {
        if err := sched.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
            log.Error().Err(err).Msg("scheduler exited with error")
        }
    }()
    log.Info().
        Dur("tick_interval", conf.Scheduler.TickInterval).
        Int("batch_size", conf.Scheduler.BatchSize).
        Msg("scheduler started")
} else {
    log.Info().Msg("scheduler disabled")
}
```

Shutdown: root ctx cancels on SIGINT/SIGTERM; `Run` returns
`context.Canceled` and the goroutine exits. `Dispatcher.Shutdown` is
already called in the existing shutdown sequence; a tick that commits
its claim tx just before shutdown is harmless — the advanced
`next_run_at` is durable and the Submit either succeeds (run executes)
or fails (next cron boundary picks it up).

### `TradecronNext` helper

```go
package scheduler

import "github.com/penny-vault/pvbt/tradecron"

func TradecronNext(schedule string, now time.Time) (time.Time, error) {
    tc, err := tradecron.New(schedule, tradecron.RegularHours)
    if err != nil {
        return time.Time{}, fmt.Errorf("tradecron.New(%q): %w", schedule, err)
    }
    return tc.Next(now), nil
}
```

Tests inject a stub `NextRunFunc` instead of calling `TradecronNext`.

## Testing

Following the project convention of "no live DB in tests":

### scheduler package (full unit coverage)

- Stub `PortfolioStore.ClaimDueContinuous` returning synthetic claims.
- Stub `Dispatcher.Submit` returning success, `ErrQueueFull`, or a
  generic error per test case.
- Cases:
  - Happy path: tick claims two portfolios, Submit called with each ID.
  - Queue-full: warn log emitted, loop continues, next portfolio still dispatched.
  - Submit error: error log emitted, loop continues.
  - ClaimDueContinuous error: top-level error log emitted, tick returns cleanly, next tick still fires.
  - Context cancel: `Run` returns `context.Canceled` promptly.
  - Immediate first tick: `tickOnce` invoked before the ticker fires.

### portfolio package

- `ClaimDueContinuous` is pure SQL; matches the project rule
  "SQL correctness via review, not unit tests" — no unit test.
- `Handler.Create` for continuous:
  - Invalid schedule returns 422 with `type`/`title`/`detail`/`instance`.
  - Valid schedule populates `next_run_at` on the inserted row.
  - Submit is called after successful insert.
  - `Submit` returning `ErrQueueFull` returns 503 and the portfolio row
    does not exist after the call (tx rolled back).

### cmd smoke

- Existing server-start smoke test verifies scheduler starts without
  error when `enabled=true` and stays silent when `enabled=false`.

## Migration

None. `portfolios.next_run_at TIMESTAMPTZ` and the partial index
`idx_portfolios_due ON portfolios(next_run_at) WHERE mode = 'continuous'
AND status IN ('ready', 'failed')` were added in `1_init` (Plan 2).

## Open items

- **Corrupted schedule string in DB.** Handled by log-and-skip in the
  scheduler. Requires operator intervention (DELETE + re-create) to
  resume. Not worth auto-failing inside the scheduler — that would
  propagate a transient scheduler bug into portfolio state.
- **Bulk backfill after extended downtime.** First tick claims up to
  `batch_size` and advances them to the next cron boundary; subsequent
  ticks pick up the rest. No special handling needed — the normal tick
  cadence absorbs it.
- **Clock skew between pvapi and Postgres.** Claim uses `time.Now()` from
  pvapi's clock for the `<= $1` comparison; `next_run_at` is then advanced
  via `tradecron.Next(now)` (also pvapi's clock). Both use the same
  reference, so skew is self-consistent. Postgres's `NOW()` is only used
  in `updated_at` which is advisory.
