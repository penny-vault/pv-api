# Portfolio Metrics Endpoint

## Goal

Expose all pvbt-computed metrics via `GET /portfolios/{slug}/metrics` with support for window and metric filtering. The existing `/statistics` endpoint is unchanged.

## Endpoint

```
GET /portfolios/{slug}/metrics?window=since_inception,1yr&metric=Sharpe,UpsideCaptureRatio
```

**Query parameters:**
- `window` — comma-separated windows. Valid: `since_inception`, `5yr`, `3yr`, `1yr`, `ytd`, `mtd`, `wtd`. Default: `since_inception`.
- `metric` — comma-separated pvbt PascalCase metric names. Default: all metrics.

Unknown `window` or `metric` values are silently dropped. If all windows or all metrics resolve to nothing, return an empty `PortfolioMetrics` object.

## Response Shape

Column-oriented: `windows` is an ordered array; each metric's value array is position-aligned to it. `null` for a missing (metric, window) combination. Categories with no data are omitted.

```json
{
  "windows": ["since_inception", "1yr"],
  "summary": {
    "Sharpe":      [1.24, 0.98],
    "MaxDrawdown": [-0.18, null]
  },
  "risk": {
    "UpsideCaptureRatio":   [1.05, 1.02],
    "DownsideCaptureRatio": [0.82, 0.89]
  }
}
```

## OpenAPI Schema

Two new schemas in `openapi.yaml`:

```yaml
MetricGroup:
  type: object
  additionalProperties:
    type: array
    items:
      type: number
      nullable: true

PortfolioMetrics:
  type: object
  properties:
    windows:
      type: array
      items:
        type: string
    summary:    { $ref: '#/components/schemas/MetricGroup' }
    risk:       { $ref: '#/components/schemas/MetricGroup' }
    trade:      { $ref: '#/components/schemas/MetricGroup' }
    withdrawal: { $ref: '#/components/schemas/MetricGroup' }
    tax:        { $ref: '#/components/schemas/MetricGroup' }
    advanced:   { $ref: '#/components/schemas/MetricGroup' }
```

Add route to `openapi.yaml`:
```yaml
/portfolios/{slug}/metrics:
  get:
    operationId: getPortfolioMetrics
    parameters:
      - { name: slug, in: path, required: true, schema: { type: string } }
      - { name: window, in: query, schema: { type: string } }
      - { name: metric, in: query, schema: { type: string } }
    responses:
      '200': { content: { application/json: { schema: { $ref: '#/components/schemas/PortfolioMetrics' } } } }
      '404': { $ref: '#/components/responses/NotFound' }
```

## snapshot Layer

New file: `snapshot/metrics.go`

### Metric metadata

Static `metricMeta` slice — 71 entries, each with:
- `Name` — pvbt PascalCase name (used as the map key in the response and for DB lookup)
- `Category` — one of `summary`, `risk`, `trade`, `withdrawal`, `tax`, `advanced`
- `Label` — human-readable display name (client reference only, not in the response)
- `Format` — `number`, `percent`, or `currency` (client reference only)

Category assignments match pvbt's groupings:

| Category   | Metrics |
|------------|---------|
| summary    | TWRR, MWRR, Sharpe, Sortino, Calmar, KellerRatio, MaxDrawdown, StdDev |
| risk       | Beta, Alpha, TrackingError, DownsideDeviation, InformationRatio, Treynor, UlcerIndex, ExcessKurtosis, Skewness, RSquared, ValueAtRisk, UpsideCaptureRatio, DownsideCaptureRatio |
| trade      | WinRate, AverageWin, AverageLoss, ProfitFactor, AverageHoldingPeriod, Turnover, NPositivePeriods, TradeGainLossRatio, AverageMFE, AverageMAE, MedianMFE, MedianMAE, EdgeRatio, TradeCaptureRatio, LongWinRate, ShortWinRate, LongProfitFactor, ShortProfitFactor |
| withdrawal | SafeWithdrawalRate, PerpetualWithdrawalRate, DynamicWithdrawalRate |
| tax        | LTCG, STCG, UnrealizedLTCG, UnrealizedSTCG, QualifiedDividends, NonQualifiedIncome, TaxCostRatio, TaxDrag |
| advanced   | CAGR, ActiveReturn, SmartSharpe, SmartSortino, ProbabilisticSharpe, KRatio, KellyCriterion, OmegaRatio, GainToPainRatio, CVaR, TailRatio, RecoveryFactor, Exposure, ConsecutiveWins, ConsecutiveLosses, AvgDrawdown, AvgDrawdownDays, GainLossRatio, AvgUlcerIndex, P90UlcerIndex, MedianUlcerIndex |

### `Metrics` method

```go
func (r *Reader) Metrics(ctx context.Context, windows, metrics []string) (*openapi.PortfolioMetrics, error)
```

1. Filter `metricMeta` to entries matching the requested metric names (all if nil/empty). Preserve `metricMeta` order.
2. Filter windows to valid values; preserve request order; deduplicate.
3. Single batched query using a window function to get the latest value per `(name, window)` pair:
   ```sql
   SELECT name, window, value FROM (
     SELECT name, window, value,
            ROW_NUMBER() OVER (PARTITION BY name, window ORDER BY date DESC) AS rn
     FROM metrics WHERE name IN (?) AND window IN (?)
   ) WHERE rn = 1
   ```
4. Build response: for each matching metric entry, allocate `[]*float64` of length `len(windows)`, fill from query results, assign to the appropriate category map.
5. Omit category maps that remain empty.

## portfolio Layer

### `snapshot_iface.go`

Add to `SnapshotReader`:
```go
Metrics(ctx context.Context, windows, metrics []string) (*openapi.PortfolioMetrics, error)
```

### `handler.go`

New handler method:
```go
func (h *Handler) Metrics(c fiber.Ctx) error
```

- Parses `?window=` (comma-split, default `["since_inception"]`) and `?metric=` (comma-split, default nil).
- Delegates via `readSnapshot` to `r.Metrics(ctx, windows, metrics)`.

Route: `GET /portfolios/:slug/metrics`

## Route Registration

In `server.go` (or wherever portfolio routes are wired), add alongside existing snapshot routes:
```go
portfolios.Get("/:slug/metrics", h.Metrics)
```
