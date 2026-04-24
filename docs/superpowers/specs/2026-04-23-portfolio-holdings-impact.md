# Portfolio Holdings-Impact — Design

**Date:** 2026-04-23

## Problem

Users want to know which holdings drove the portfolio's return. Today the API
exposes a portfolio-level equity curve (`/performance`), trailing-return
totals (`/trailing-returns`), and a current-day holdings snapshot
(`/holdings`), but nothing that answers the question "how much of my +237 %
since inception came from VTI vs. BND vs. the rest?".

The frontend wants a single endpoint it can call to populate a top-N
contribution table across canonical periods (YTD, 1Y, 3Y, 5Y, inception), with
arithmetic that balances: the per-ticker contributions plus a `rest` bucket
must sum exactly to the portfolio's cumulative return for that period, so the
UI can proportionally scale annualised deltas and click-to-remove numbers.

### Data gap

pvbt's snapshot schema today stores:

- `perf_data (date, metric, value)` — portfolio-level only (`PortfolioEquity`,
  `PortfolioBenchmark`).
- `holdings (asset_ticker, asset_figi, quantity, avg_cost, market_value)` —
  single point-in-time, the latest snapshot.
- `transactions (batch_id, date, type, ticker, figi, quantity, price, amount,
  qualified, justification)` — event log.
- `metrics (date, name, window, value)` — indicator values, not per-ticker
  positions.

There is no per-ticker daily market-value series, so contribution cannot be
computed from what the snapshot currently contains. pvbt already tracks
per-asset equity in its in-memory `perfData` dataframe (`Column(asset,
metric)`), but `writePerfData` in `pvbt/portfolio/sqlite.go` only serialises
rows keyed by `portfolioAsset`. The fix is to persist the per-ticker slice
too.

## Architecture

### End-to-end flow

```
pvbt run
  └─► Account.writePositionsDaily(tx)           ← new: per-ticker daily series
        writes positions_daily(date, ticker, figi, market_value, quantity)

pv-api GET /portfolios/{slug}/holdings-impact
  └─► Handler.HoldingsImpact()
        └─► snapshot.Reader.HoldingsImpact(ctx)
              1. Read perf_data → portfolio V(t) series.
              2. Read positions_daily → per-ticker MV(t) series.
              3. Read transactions → per-ticker net flows by date.
              4. For each period [t0,t1] within data range:
                   - V(t0), V(t1) → cumulative return
                   - For each ticker: P&L_i = (MV_i(t1) - MV_i(t0))
                                               - netPurchases_i(t0..t1]
                                               + dividends_i(t0..t1]
                     contribution_i = P&L_i / V(t0)
                   - Sort by |contribution_i| desc, take top N, sum rest.
                   - avgWeight_i = mean over days of MV_i(t)/V(t) (when V>0)
                   - holdingDays_i = count of days MV_i(t) != 0
              5. Drop periods whose startDate < inception.
```

All math runs in Go over rows streamed from the snapshot; no on-the-fly
price lookups at request time.

### Endpoint shape

```
GET /portfolios/{slug}/holdings-impact?top=10
```

Query params:

| param | type | default | meaning                                                     |
|-------|------|---------|-------------------------------------------------------------|
| `top` | int  | 10      | Max number of named holdings per period (1..50).            |

Response body:

```json
{
  "portfolioSlug": "core-global-balanced",
  "asOf": "2026-04-14",
  "currency": "USD",
  "periods": [
    {
      "period": "inception",
      "label": "Since inception",
      "startDate": "2015-01-02",
      "endDate": "2026-04-14",
      "years": 11.283,
      "cumulativeReturn": 2.37,
      "annualizedReturn": 0.1147,
      "items": [
        {
          "ticker": "VTI",
          "figi": "BBG000BDTBL9",
          "contribution": 0.45,
          "avgWeight": 0.28,
          "holdingDays": 4120
        }
      ],
      "rest": {
        "count": 14,
        "contribution": 0.14
      }
    }
  ]
}
```

Invariants the response MUST satisfy:

1. For every period: `sum(items[].contribution) + rest.contribution ==
   cumulativeReturn` to within a small rounding tolerance (target: absolute
   error < 1e-9, see §Rounding below).
2. `rest` is always present. When every ticker fits in `top`, emit
   `{"count": 0, "contribution": 0}`.
3. `years` is precise decimal years (`days / 365.25`), not rounded.
4. Periods shorter than their definition (e.g. a 2-year-old portfolio
   requesting 3Y) are omitted entirely; they don't appear as zero rows.
5. Periods appear in a stable, deterministic order: inception, 5y, 3y, 1y,
   ytd. Frontend defaults to `periods[0]`; inception is the safest default.

### Period definitions

| period     | startDate                                                 | endDate       |
|------------|-----------------------------------------------------------|---------------|
| inception  | first `perf_data` date                                    | last date     |
| 5y         | `last - 5 years` (calendar)                               | last date     |
| 3y         | `last - 3 years`                                          | last date     |
| 1y         | `last - 1 year`                                           | last date     |
| ytd        | Jan 1 of year(last)                                       | last date     |

`startDate` is snapped forward to the first trading day at-or-after the
requested date (the same "perfAsOf with dir=after" pattern already used by
`snapshot.Reader.TrailingReturns`). A period is emitted iff the snapped
start predates the end and inception is not later.

## Database / snapshot schema

### New table: `positions_daily`

Added by pvbt's schema bootstrap (`pvbt/portfolio/sqlite.go` —
`CREATE TABLE` statements) and mirrored in
`pv-api/snapshot/fixture.go` for tests.

```sql
CREATE TABLE positions_daily (
  date         TEXT NOT NULL,
  ticker       TEXT NOT NULL,
  figi         TEXT NOT NULL,
  market_value REAL NOT NULL,
  quantity     REAL NOT NULL,
  PRIMARY KEY (date, ticker, figi)
);
CREATE INDEX idx_positions_daily_ticker ON positions_daily (ticker, date);
```

Notes:

- `$CASH` is a regular row with `ticker='$CASH'`, `figi=''`. This gives
  cash-drag / cash-interest its own contribution line rather than
  hiding it in `rest`.
- Dividends paid by a ticker accrue into that ticker's `market_value` via
  pvbt's existing accounting (cash from dividends sits in $CASH but the
  dividend transaction itself is tagged to the paying ticker). Contribution
  math below credits dividends back to the source ticker via the transactions
  table, so cash doesn't absorb them.
- No backfill of pre-existing snapshots — old snapshots will 404 the new
  endpoint. This is acceptable: stats-refresher re-runs a fresh backtest
  daily, so the column populates organically within one tick of deployment.
  Snapshots that fail to rerun stay on their old path (existing endpoints
  keep working; only `/holdings-impact` returns 404).

### pvbt changes

New method `Account.writePositionsDaily(tx *sql.Tx)` invoked from the existing
snapshot writer alongside `writePerfData`. It iterates every asset tracked in
`a.perfData` (not just `portfolioAsset`) and emits one row per (date, asset)
where the asset's stored equity is non-NaN. Quantity is read from the
per-asset positions history pvbt already tracks for broker reconciliation;
if quantity series isn't preserved in perfData today, it's added alongside
the equity column under a new metric name `PositionQuantity`.

Scope impact on pvbt: one new writer + one new schema line + ensure
`PositionQuantity` is retained in `perfData`. No change to how pvbt computes
performance; only how much of what it already has in memory is persisted.

## Reader implementation (pv-api)

New file: `pv-api/snapshot/holdings_impact.go` (with `holdings_impact_test.go`).

```go
// HoldingsImpact returns per-ticker contribution to portfolio return across
// canonical periods. topN caps items per period; the remainder is summed
// into rest. Periods whose start predates inception are omitted.
func (r *Reader) HoldingsImpact(ctx context.Context, slug string, topN int) (*openapi.HoldingsImpactResponse, error)
```

Internal helpers:

```go
type dailyRow struct {
    date time.Time
    portfolioValue float64              // from perf_data
    positions map[string]tickerDay      // ticker -> {mv, qty, figi}
    flows     map[string]float64        // ticker -> signed cash flow
                                        //   buys positive, sells negative,
                                        //   dividends received negative
                                        //   (credits ticker's P&L)
}

type periodWindow struct {
    id, label string
    start, end time.Time
}

// loadTimeline builds a chronologically ordered []dailyRow in one pass over
// perf_data, positions_daily, and transactions. Streaming join on date.
func (r *Reader) loadTimeline(ctx context.Context) ([]dailyRow, error)
```

Math per period `[t0, t1]`, for each ticker `i`:

```
pnl_i = (mv_i(t1) - mv_i(t0)) - netFlows_i(t0, t1]
contribution_i = pnl_i / V(t0)

avgWeight_i   = mean over t in [t0, t1] of
                (mv_i(t) / V(t)) when V(t) > 0 else 0
holdingDays_i = count of t in [t0, t1] where mv_i(t) != 0
```

`netFlows_i(t0, t1]` excludes the transaction on `t0` (so it counts toward the
period opening MV, not period flows) and includes `t1`. Dividends are
recorded as negative flows — they add to the ticker's P&L because cash
received was previously excluded from its MV.

Identity check: `sum_i pnl_i == V(t1) - V(t0) - externalFlows` where external
flows are zero for a pure backtest (no deposits/withdrawals). We enforce
this and fail loudly (500) if the residual exceeds a tolerance; the residual
typically reflects a dropped fee transaction or missing positions_daily row.

### Top-N and rest

Sort items by `|contribution|` descending, break ties by `|avgWeight|`
descending, then by ticker ascending for stability. Take first `topN`; sum
the rest's `contribution` into `rest.contribution`, count into `rest.count`.
Tickers that never held a position during the period are skipped entirely
(not in `items` and not in `rest.count`).

### Rounding

All monetary math uses `float64` end-to-end (matches existing snapshot code).
Before serialisation, round contributions to 6 decimal places and
`cumulativeReturn`/`annualizedReturn` to 6 as well. The "sums must balance"
invariant is satisfied before rounding; post-rounding drift is bounded by
`(topN+1) * 5e-7`, well below what the UI's `(c / R) * annual` multiplication
needs.

## API handler wiring

New file: `pv-api/portfolio/holdings_impact.go`.

```go
func (h *Handler) HoldingsImpact(c fiber.Ctx) error {
    slug := c.Params("slug")
    topN := parseTopN(c.Query("top"), 10 /*default*/, 1, 50)
    // ... readSnapshot lambda, same pattern as Performance()
}
```

Route registration in `api/portfolios.go` (sibling line to
`/holdings/history`):

```go
r.Get("/portfolios/:slug/holdings-impact", h.HoldingsImpact)
```

`snapshot.Reader.HoldingsImpact` receives the slug and `topN`; it returns
`*openapi.HoldingsImpactResponse`. The handler writes it with
`c.JSON(resp)`.

### OpenAPI additions

Added to `openapi/openapi.yaml` under `#/components/schemas`:

```yaml
HoldingsImpactResponse:
  type: object
  required: [portfolioSlug, asOf, currency, periods]
  properties:
    portfolioSlug: { type: string }
    asOf:          { type: string, format: date }
    currency:      { type: string, example: USD }
    periods:
      type: array
      items: { $ref: '#/components/schemas/HoldingsImpactPeriod' }

HoldingsImpactPeriod:
  type: object
  required: [period, label, startDate, endDate, years,
             cumulativeReturn, annualizedReturn, items, rest]
  properties:
    period:
      type: string
      enum: [ytd, 1y, 3y, 5y, inception]
    label:            { type: string }
    startDate:        { type: string, format: date }
    endDate:          { type: string, format: date }
    years:            { type: number }
    cumulativeReturn: { type: number }
    annualizedReturn: { type: number }
    items:
      type: array
      items: { $ref: '#/components/schemas/HoldingsImpactItem' }
    rest:             { $ref: '#/components/schemas/HoldingsImpactRest' }

HoldingsImpactItem:
  type: object
  required: [ticker, contribution, avgWeight, holdingDays]
  properties:
    ticker:       { type: string }
    figi:         { type: string }
    contribution: { type: number }
    avgWeight:    { type: number }
    holdingDays:  { type: integer }

HoldingsImpactRest:
  type: object
  required: [count, contribution]
  properties:
    count:        { type: integer }
    contribution: { type: number }
```

Route:

```yaml
/portfolios/{slug}/holdings-impact:
  get:
    operationId: getPortfolioHoldingsImpact
    summary: Per-ticker contribution to portfolio return across canonical periods.
    parameters:
      - in: path
        name: slug
        required: true
        schema: { type: string }
      - in: query
        name: top
        schema: { type: integer, minimum: 1, maximum: 50, default: 10 }
    responses:
      '200':
        description: OK
        content:
          application/json:
            schema: { $ref: '#/components/schemas/HoldingsImpactResponse' }
      '404':
        description: Portfolio not found, or snapshot lacks positions_daily.
```

## Edge cases

| case                                       | handling                                                                                      |
|--------------------------------------------|-----------------------------------------------------------------------------------------------|
| Portfolio younger than 1y / 3y / 5y        | Omit that period object entirely.                                                             |
| Ticker bought mid-period                   | `mv_i(t0)=0`, P&L correct as `mv_i(t1) - netFlows`. `avgWeight` reflects partial-period hold. |
| Ticker closed mid-period                   | `mv_i(t1)=0`, P&L is realised gain captured by the negative sale flows. `holdingDays` < total.|
| Splits                                     | Quantity and market_value both adjusted on the ex-date (pvbt handles), so math is invariant.  |
| Dividends                                  | Recorded as negative flow against the paying ticker; cash shows only the interest drag.       |
| Cash ($CASH)                               | Normal row. Its contribution is cash-interest. Can rank into `items` or fall into `rest`.     |
| Period with zero V(t0) (newly funded day)  | Snap start forward one day; if still zero, omit the period.                                   |
| Residual mismatch > 1e-6 after summing     | Return 500 with a structured error; log the residual and period for debugging.                |
| Old snapshot missing positions_daily       | Return 404 with `error_code: "snapshot_missing_positions_daily"`. Stats refresher re-runs daily; endpoint becomes live after re-backtest. |
| `top` out of range                         | Silently clamped to [1, 50]. Matches sibling `/metrics` endpoint behavior.                    |

## Testing

- **Unit (`snapshot/holdings_impact_test.go`)**:
  - Two-ticker fixture (VTI, BND, no cash) over 30 days, hand-computed
    contributions — assert items sum to cumulative return exactly (pre-round)
    and within 1e-6 after serialisation.
  - Ticker bought on day 10, sold on day 20: verify P&L = realised gain,
    `holdingDays == 11`, `avgWeight < full-period weight`.
  - Dividend on day 15: verify contribution credits paying ticker, cash row
    unchanged beyond interest.
  - Portfolio with inception = 2024-06-01; assert 5Y and 3Y periods omitted.
  - `top=1` with 3 tickers: first ticker in `items`, other two in `rest`
    with `count==2`.
  - Residual-check path: inject an off-by-one in positions_daily fixture and
    assert the reader returns an error.
- **Unit (`portfolio/holdings_impact_test.go`)**:
  - Handler routes slug → reader, serialises payload, honours `top` query.
  - 404 when snapshot lacks the table (simulate via fixture builder variant).
  - 400 on `top=0`, `top=51`, `top=abc`.
- **Integration**: extend `portfolio/handler_test.go` happy-path coverage with
  a fresh run to assert the whole pipeline (backtest → snapshot → API) works.

## What is NOT in scope

- Custom user-defined periods (`from` / `to` query params). Shape allows
  adding later without breaking the contract.
- Benchmark contribution breakdown — only the portfolio itself.
- Per-sector / per-asset-class rollups. Item granularity is strictly ticker.
- Contribution-to-volatility or other risk-attribution metrics. Pure return
  attribution only.
- Persisting `avgWeight` and `holdingDays` precomputed in the snapshot.
  Derived at read time; snapshot stays append-only-simple.
