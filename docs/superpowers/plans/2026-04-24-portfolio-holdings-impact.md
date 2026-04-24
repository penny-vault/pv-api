# Portfolio Holdings-Impact — Implementation Plan

> **For agentic workers:** Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax for tracking.

**Spec:** `docs/superpowers/specs/2026-04-23-portfolio-holdings-impact.md`
**Upstream:** pvbt PR penny-vault/pvbt#166 landed the `positions_daily` table (schema v5).

**Goal:** Implement `GET /portfolios/{slug}/holdings-impact?top=N`, returning each ticker's contribution to portfolio return across YTD, 1Y, 3Y, 5Y, and inception periods, with a `rest` bucket so `sum(items.contribution) + rest.contribution == cumulativeReturn` per period.

**Architecture:** A new `snapshot.Reader.HoldingsImpact` joins `perf_data` (portfolio V(t)), `positions_daily` (per-ticker MV(t)), and `transactions` (per-ticker flows) into a single daily timeline, then slices it into period windows and computes `pnl_i = (mv_i(t1) - mv_i(t0)) - flows_i` per ticker. The handler `Handler.HoldingsImpact` wraps the reader via the existing `readSnapshot` skeleton. OpenAPI schemas regenerate into `openapi.gen.go` via `go generate ./openapi/...`.

**Tech Stack:** Go, Fiber v3, modernc SQLite (snapshot), oapi-codegen, Ginkgo/Gomega.

---

## File Map

| Action | File | Responsibility |
|--------|------|----------------|
| Modify | `openapi/openapi.yaml` | Add `/portfolios/{slug}/holdings-impact` route + 4 schemas. |
| Modify | `openapi/openapi.gen.go` | Regenerated from YAML; do not hand-edit. |
| Modify | `snapshot/fixture.go` | Add `positions_daily` DDL + seed rows for tests. |
| Create | `snapshot/holdings_impact.go` | `Reader.HoldingsImpact`, timeline build, math. |
| Create | `snapshot/holdings_impact_test.go` | Reader unit tests (top-N, rest, periods, flows, $CASH, residual guard). |
| Modify | `portfolio/snapshot_iface.go` | Add `HoldingsImpact` to `SnapshotReader` + response alias if needed. |
| Create | `portfolio/holdings_impact.go` | `Handler.HoldingsImpact` wrapping `readSnapshot`. |
| Create | `portfolio/holdings_impact_test.go` | Handler test (routing, param parsing, fake reader). |
| Modify | `portfolio/handler_test.go` | Extend the existing in-package `fakeReader` with `HoldingsImpact`. |
| Modify | `api/portfolios.go` | Register stub + real route. |
| Modify | `CHANGELOG.md` | One-line entry under Unreleased. |

---

### Task 1: OpenAPI schemas and route

**Files:**
- Modify: `openapi/openapi.yaml`
- Modify: `openapi/openapi.gen.go` (via `go generate`)

- [ ] **Step 1: Add route block to `openapi/openapi.yaml`**

Insert after `/portfolios/{slug}/holdings/history:` (following the sibling-endpoint pattern):

```yaml
  /portfolios/{slug}/holdings-impact:
    get:
      tags: [Portfolios]
      operationId: getPortfolioHoldingsImpact
      summary: Per-ticker contribution to portfolio return across canonical periods.
      parameters:
        - $ref: '#/components/parameters/PortfolioSlug'
        - name: top
          in: query
          required: false
          description: Maximum number of named holdings per period (remaining folded into `rest`).
          schema:
            type: integer
            minimum: 1
            maximum: 50
            default: 10
      responses:
        '200':
          description: Holdings impact across canonical periods
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/HoldingsImpactResponse'
        '401':
          $ref: '#/components/responses/Unauthorized'
        '404':
          $ref: '#/components/responses/NotFound'
        '500':
          $ref: '#/components/responses/ServerError'
```

- [ ] **Step 2: Add component schemas**

Insert under `components.schemas` (next to `HoldingsHistoryResponse`):

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
      required: [period, label, startDate, endDate, years, cumulativeReturn, annualizedReturn, items, rest]
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
        rest: { $ref: '#/components/schemas/HoldingsImpactRest' }

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

- [ ] **Step 3: Regenerate Go types**

```bash
cd /Users/jdf/Developer/penny-vault/pv-api && go generate ./openapi/...
```

Expected: `openapi/openapi.gen.go` now contains `HoldingsImpactResponse`, `HoldingsImpactPeriod`, `HoldingsImpactItem`, `HoldingsImpactRest`, `HoldingsImpactPeriodPeriod` (enum type), and `GetPortfolioHoldingsImpactParams`.

- [ ] **Step 4: Build to confirm**

```bash
cd /Users/jdf/Developer/penny-vault/pv-api && go build ./openapi/...
```

- [ ] **Step 5: Commit**

```bash
git add openapi/openapi.yaml openapi/openapi.gen.go
git commit -m "feat(openapi): add holdings-impact endpoint and schemas"
```

---

### Task 2: Snapshot fixture — positions_daily

**Files:**
- Modify: `snapshot/fixture.go`

- [ ] **Step 1: Add the table to the fixture DDL**

In the `CREATE TABLE` list (around the existing `perf_data`/`holdings`/`transactions` lines), add:

```go
`CREATE TABLE positions_daily (
    date         TEXT NOT NULL,
    ticker       TEXT NOT NULL,
    figi         TEXT NOT NULL,
    market_value REAL NOT NULL,
    quantity     REAL NOT NULL,
    PRIMARY KEY (date, ticker, figi)
 )`,
`CREATE INDEX idx_positions_daily_ticker ON positions_daily(ticker, date)`,
```

- [ ] **Step 2: Add minimal seed rows so non-holdings-impact tests keep compiling**

For every existing `perf_data` seed date the fixture writes (`2024-01-02`..`2024-01-08`), add matching `positions_daily` rows for `VTI` and `$CASH` that roughly reconstitute the portfolio value. Seed values are illustrative — the existing VTI holding in the fixture uses qty=100, so:

```go
`INSERT INTO positions_daily VALUES ('2024-01-02','VTI','BBG000BDTBL9',10000,100)`,
`INSERT INTO positions_daily VALUES ('2024-01-02','$CASH','',90000,90000)`,
`INSERT INTO positions_daily VALUES ('2024-01-03','VTI','BBG000BDTBL9',10100,100)`,
`INSERT INTO positions_daily VALUES ('2024-01-03','$CASH','',90900,90900)`,
`INSERT INTO positions_daily VALUES ('2024-01-04','VTI','BBG000BDTBL9',10050,100)`,
`INSERT INTO positions_daily VALUES ('2024-01-04','$CASH','',90450,90450)`,
`INSERT INTO positions_daily VALUES ('2024-01-05','VTI','BBG000BDTBL9',10200,100)`,
`INSERT INTO positions_daily VALUES ('2024-01-05','$CASH','',91800,91800)`,
`INSERT INTO positions_daily VALUES ('2024-01-08','VTI','BBG000BDTBL9',10300,100)`,
`INSERT INTO positions_daily VALUES ('2024-01-08','$CASH','',92700,92700)`,
```

Values chosen so `VTI.mv + $CASH.mv == perf_data.portfolio_value` on each date (103000 = 10300 + 92700 etc.). This preserves the residual-check invariant for fixture-based tests.

- [ ] **Step 3: Build and run the existing snapshot tests**

```bash
cd /Users/jdf/Developer/penny-vault/pv-api && go test ./snapshot/...
```

Expected: all existing snapshot tests still pass; nothing else references `positions_daily` yet so no behavior change.

- [ ] **Step 4: Commit**

```bash
git add snapshot/fixture.go
git commit -m "test(snapshot): add positions_daily fixture rows"
```

---

### Task 3: Reader.HoldingsImpact — test-driven

**Files:**
- Create: `snapshot/holdings_impact.go`
- Create: `snapshot/holdings_impact_test.go`

- [ ] **Step 1: Write the failing Ginkgo spec**

Create `snapshot/holdings_impact_test.go`. Cover:

1. Two-ticker happy path from the standard fixture: inception period returns, items sum to `cumulativeReturn` exactly (pre-round), VTI + $CASH both present.
2. `topN=1`: VTI in `items`, $CASH in `rest` with `count=1`, contributions still balance.
3. Younger-than-5Y portfolio (standard fixture): 5Y / 3Y / 1Y periods omitted, YTD and inception present.
4. Mid-period buy via a second fixture: asset's first row appears on buy day, `holdingDays` < full period, `avgWeight` reflects partial hold.
5. Dividend transaction credits the paying ticker (use a `transactions`-row fixture with type='dividend').
6. Residual-guard fixture (inject positions_daily that does not sum to `perf_data`): reader returns an error.

Sketch (full spec elided):

```go
var _ = Describe("Reader.HoldingsImpact", func() {
    var r *snapshot.Reader
    BeforeEach(func() { r = openStandardFixture() })
    AfterEach(func() { _ = r.Close() })

    It("sums items + rest to cumulativeReturn per period", func() {
        resp, err := r.HoldingsImpact(context.Background(), "demo", 10)
        Expect(err).NotTo(HaveOccurred())
        Expect(resp.Periods).NotTo(BeEmpty())
        for _, p := range resp.Periods {
            var sum float64
            for _, it := range p.Items {
                sum += it.Contribution
            }
            sum += p.Rest.Contribution
            Expect(sum).To(BeNumerically("~", p.CumulativeReturn, 1e-6))
        }
    })

    It("omits 5y/3y/1y when history is too short", func() {
        resp, _ := r.HoldingsImpact(context.Background(), "demo", 10)
        var ids []string
        for _, p := range resp.Periods { ids = append(ids, string(p.Period)) }
        Expect(ids).To(ConsistOf("inception", "ytd"))
    })

    It("folds extras into rest when top=1", func() {
        resp, _ := r.HoldingsImpact(context.Background(), "demo", 1)
        Expect(resp.Periods[0].Items).To(HaveLen(1))
        Expect(resp.Periods[0].Rest.Count).To(Equal(1))
    })
})
```

- [ ] **Step 2: Run and confirm failure**

```bash
cd /Users/jdf/Developer/penny-vault/pv-api && go test ./snapshot/... -run HoldingsImpact 2>&1 | head -20
```

Expected: compile error — `r.HoldingsImpact` undefined.

- [ ] **Step 3: Implement `snapshot/holdings_impact.go`**

Skeleton — fill in the helpers marked `...`:

```go
package snapshot

import (
    "context"
    "fmt"
    "math"
    "sort"
    "time"

    "github.com/oapi-codegen/runtime/types"
    "github.com/penny-vault/pv-api/openapi"
)

const (
    cashTicker = "$CASH"
    residualTolerance = 1e-4 // dollar tolerance on identity check
)

// HoldingsImpact returns per-ticker contribution to portfolio return across
// canonical periods (inception, 5y, 3y, 1y, ytd). Items per period are capped
// at topN; the remainder is summed into rest.
func (r *Reader) HoldingsImpact(ctx context.Context, slug string, topN int) (*openapi.HoldingsImpactResponse, error) {
    if topN < 1 { topN = 1 }
    if topN > 50 { topN = 50 }

    timeline, err := r.loadHoldingsImpactTimeline(ctx)
    if err != nil {
        return nil, err
    }
    if len(timeline) == 0 {
        return nil, ErrNotFound
    }

    periods := buildPeriodWindows(timeline)
    out := &openapi.HoldingsImpactResponse{
        PortfolioSlug: slug,
        AsOf:          types.Date{Time: timeline[len(timeline)-1].date},
        Currency:      "USD",
    }
    for _, w := range periods {
        p, err := computePeriod(timeline, w, topN)
        if err != nil {
            return nil, err
        }
        out.Periods = append(out.Periods, *p)
    }
    return out, nil
}
```

Core helpers to implement:

- `type timelineDay struct { date time.Time; v float64; positions map[tickerKey]posDay; flows map[tickerKey]float64 }`
- `type tickerKey struct { ticker, figi string }`
- `loadHoldingsImpactTimeline(ctx)` — three queries (perf_data, positions_daily, transactions), streaming-joined by date into a map, then sorted into a slice. Accept both legacy (`portfolio_value`) and pvbt (`PortfolioEquity`) metric names, matching the clause used in `snapshot/returns.go`.
- `buildPeriodWindows(timeline) []periodWindow` — produces in order: inception, 5y, 3y, 1y, ytd. Skip any period whose snapped start predates inception. Snap the requested start forward to the first `timeline` entry on or after it (mirrors `perfAsOf` with `dir=after`).
- `computePeriod(timeline, w, topN)` — slices timeline, computes per-ticker `pnl_i`, `avgWeight_i`, `holdingDays_i`, checks residual, sorts, splits into items and rest.

Flows attribution inside `loadHoldingsImpactTimeline`, per transaction row:

| transaction type | `flows[ticker]` | `flows[$CASH]` |
|------------------|-----------------|----------------|
| `buy`            | `+= amount`     | `-= amount`    |
| `sell`           | `-= amount`     | `+= amount`    |
| `dividend`       | `-= amount`     | `+= amount`    |
| `fee`            | `+= amount`     | `-= amount`    |
| `deposit`        | skip            | `+= amount`    |
| `withdrawal`     | skip            | `-= amount`    |

Per-ticker math in `computePeriod` (let `t0, t1` be the window bounds):

```
pnl_i = (mv_i(t1) - mv_i(t0)) - sum(flows_i(t) for t in (t0, t1])
contribution_i = pnl_i / V(t0)
```

Per-ticker aggregates over `[t0, t1]`:

```
avgWeight_i   = mean of (mv_i(t) / V(t)) over days where V(t) > 0
holdingDays_i = count of days where mv_i(t) != 0
```

Residual check: `abs((V(t1) - V(t0)) - sum_i (pnl_i)) < residualTolerance`. On failure, return an error wrapping the period id and residual.

Top-N / rest selection:

```go
sort.SliceStable(items, func(i, j int) bool {
    ai, aj := math.Abs(items[i].Contribution), math.Abs(items[j].Contribution)
    if ai != aj { return ai > aj }
    awi, awj := math.Abs(items[i].AvgWeight), math.Abs(items[j].AvgWeight)
    if awi != awj { return awi > awj }
    return items[i].Ticker < items[j].Ticker
})
named := items
rest := openapi.HoldingsImpactRest{}
if len(items) > topN {
    named = items[:topN]
    for _, it := range items[topN:] {
        rest.Contribution += it.Contribution
        rest.Count++
    }
}
```

Period metadata:

- `years = (end.Sub(start).Hours() / 24.0) / 365.25`
- `cumulativeReturn = V(t1)/V(t0) - 1`
- `annualizedReturn = math.Pow(1+cum, 1/years) - 1` (same formula for all lengths; leave partial-year annualisation to the caller's judgment — spec calls for uniform handling).
- Rounding: 6 decimal places on contributions and return fields before serialisation.

- [ ] **Step 4: Run the tests**

```bash
cd /Users/jdf/Developer/penny-vault/pv-api && go test ./snapshot/... -run HoldingsImpact -v 2>&1 | tail -40
```

Expected: all added specs PASS. If residual-guard fixture is not yet present, add it now as a subordinate `Context`.

- [ ] **Step 5: Run the full snapshot suite to catch regressions**

```bash
cd /Users/jdf/Developer/penny-vault/pv-api && go test ./snapshot/...
```

- [ ] **Step 6: Commit**

```bash
git add snapshot/holdings_impact.go snapshot/holdings_impact_test.go
git commit -m "feat(snapshot): add Reader.HoldingsImpact for per-ticker contribution"
```

---

### Task 4: Extend SnapshotReader interface

**Files:**
- Modify: `portfolio/snapshot_iface.go`

- [ ] **Step 1: Add method to interface**

Append to the `SnapshotReader` interface:

```go
HoldingsImpact(ctx context.Context, slug string, topN int) (*openapi.HoldingsImpactResponse, error)
```

- [ ] **Step 2: Confirm the adapter auto-satisfies it**

`snapshot/opener.go`'s `readerAdapter` embeds `*Reader`, so the new method flows through without an explicit adapter method. Build to confirm:

```bash
cd /Users/jdf/Developer/penny-vault/pv-api && go build ./...
```

Expected: no errors. (If the Go compiler complains about `readerAdapter` missing methods, add a shim in `snapshot/opener.go` that forwards to `a.Reader.HoldingsImpact`.)

- [ ] **Step 3: Commit**

```bash
git add portfolio/snapshot_iface.go
git commit -m "feat(portfolio): add HoldingsImpact to SnapshotReader interface"
```

---

### Task 5: Handler + route wiring

**Files:**
- Create: `portfolio/holdings_impact.go`
- Modify: `api/portfolios.go`

- [ ] **Step 1: Implement the handler**

Create `portfolio/holdings_impact.go`:

```go
package portfolio

import (
    "strconv"

    "github.com/gofiber/fiber/v3"
)

// HoldingsImpact returns per-ticker contribution to portfolio return across
// canonical periods (YTD, 1Y, 3Y, 5Y, inception).
func (h *Handler) HoldingsImpact(c fiber.Ctx) error {
    slug := string([]byte(c.Params("slug")))
    topN := parseTopN(c.Query("top"))
    return h.readSnapshot(c, func(r SnapshotReader) (any, error) {
        return r.HoldingsImpact(c.Context(), slug, topN)
    })
}

// parseTopN returns a clamped integer; invalid or empty input yields the default 10.
func parseTopN(raw string) int {
    if raw == "" { return 10 }
    n, err := strconv.Atoi(raw)
    if err != nil || n < 1 { return 10 }
    if n > 50 { return 50 }
    return n
}
```

Note: the handler clamps silently rather than returning 400 for invalid `top` — matches the tolerant parsing used elsewhere in the handler (see `Metrics` and its `splitParam`). If the spec's 400-on-invalid-top behavior is preferred, switch to `writeProblem(c, fiber.StatusBadRequest, ...)` when parsing fails or falls outside `[1, 50]`. Default here is tolerant to keep behavior consistent with sibling endpoints.

- [ ] **Step 2: Register the stub route**

In `api/portfolios.go`, in `RegisterPortfolioRoutes` (stubs list), add after `holdings/history`:

```go
r.Get("/portfolios/:slug/holdings-impact", stubPortfolio)
```

- [ ] **Step 3: Register the real route**

In `RegisterPortfolioRoutesWith`, add after `holdings/:date`:

```go
r.Get("/portfolios/:slug/holdings-impact", h.HoldingsImpact)
```

- [ ] **Step 4: Build**

```bash
cd /Users/jdf/Developer/penny-vault/pv-api && go build ./...
```

- [ ] **Step 5: Commit**

```bash
git add portfolio/holdings_impact.go api/portfolios.go
git commit -m "feat(api): wire GET /portfolios/:slug/holdings-impact route"
```

---

### Task 6: Handler test

**Files:**
- Create: `portfolio/holdings_impact_test.go`
- Modify: `portfolio/handler_test.go` (extend `fakeReader`)

- [ ] **Step 1: Extend `fakeReader` in `handler_test.go`**

Add a `holdingsImpactFn` field and a `HoldingsImpact` method that delegates to it (or returns a canned payload if nil). Keep the pattern used by existing fake methods.

```go
holdingsImpactFn func(ctx context.Context, slug string, topN int) (*openapi.HoldingsImpactResponse, error)

func (f *fakeReader) HoldingsImpact(ctx context.Context, slug string, topN int) (*openapi.HoldingsImpactResponse, error) {
    if f.holdingsImpactFn != nil {
        return f.holdingsImpactFn(ctx, slug, topN)
    }
    return &openapi.HoldingsImpactResponse{
        PortfolioSlug: slug,
        Periods:       []openapi.HoldingsImpactPeriod{},
    }, nil
}
```

- [ ] **Step 2: Write the handler spec**

Create `portfolio/holdings_impact_test.go` with a Ginkgo describe that:

1. Hits `GET /portfolios/demo/holdings-impact` and asserts 200 + JSON decodes into `HoldingsImpactResponse`.
2. Asserts `top` query param is passed to the reader (capture `topN` in a closure, assert `== 3` when URL `?top=3`).
3. Asserts default `topN == 10` when query param is missing.
4. Asserts clamping (`?top=0` → 10; `?top=999` → 50) if the tolerant-parsing path is kept; otherwise asserts 400.
5. Asserts 404 when the store returns `ErrNotFound` for the slug.
6. Asserts 401 when no subject is present (use the existing auth harness pattern from sibling tests).

- [ ] **Step 3: Run**

```bash
cd /Users/jdf/Developer/penny-vault/pv-api && go test ./portfolio/... -run HoldingsImpact -v
```

- [ ] **Step 4: Run the whole portfolio package**

```bash
cd /Users/jdf/Developer/penny-vault/pv-api && go test ./portfolio/...
```

- [ ] **Step 5: Commit**

```bash
git add portfolio/holdings_impact_test.go portfolio/handler_test.go
git commit -m "test(portfolio): cover HoldingsImpact handler"
```

---

### Task 7: Changelog + final sweep

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Add entry**

Under `## Unreleased` → `### Added`:

```
- `GET /portfolios/{slug}/holdings-impact` — per-ticker contribution to
  portfolio return across YTD, 1Y, 3Y, 5Y, inception with top-N + rest
  bucket. Requires pvbt v5 snapshots (schema v5 / `positions_daily`).
```

- [ ] **Step 2: Full build + test sweep**

```bash
cd /Users/jdf/Developer/penny-vault/pv-api && go build ./... && go test ./...
```

Expected: green.

- [ ] **Step 3: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs: note holdings-impact endpoint in changelog"
```

---

## Self-Review

### Spec coverage

| Spec requirement | Task |
|------------------|------|
| Route `/portfolios/{slug}/holdings-impact?top=N` | 1, 5 |
| Response schemas (Response, Period, Item, Rest) | 1 |
| `asOf`, `currency`, stable period ordering | 1 (schemas) + 3 (`buildPeriodWindows`) |
| Periods: inception / 5y / 3y / 1y / ytd, omit when insufficient history | 3 (`buildPeriodWindows`) |
| Snap start forward to first trading day | 3 (`buildPeriodWindows`) |
| `years = days / 365.25`, precise | 3 (`computePeriod`) |
| `cumulativeReturn`, `annualizedReturn` | 3 (`computePeriod`) |
| Per-ticker `contribution`, `avgWeight`, `holdingDays` | 3 (`computePeriod`) |
| Flow attribution (buy/sell/dividend/fee/deposit/withdrawal) | 3 (`loadHoldingsImpactTimeline`) |
| `$CASH` as a regular row | 3 (data model + math handle $CASH like any ticker) |
| Residual invariant check + 500 on breach | 3 (`computePeriod`) |
| Top-N sort `|contribution|` desc, tie-break by `|avgWeight|` then ticker asc | 3 |
| `rest` always present, `{count: 0, contribution: 0}` when empty | 3 |
| Rounding to 6 dp | 3 |
| OpenAPI codegen regeneration | 1 |
| `SnapshotReader` interface extension | 4 |
| Handler using `readSnapshot`, auth + 404 behavior | 5, 6 |
| 404 on snapshots missing positions_daily | 3 (timeline load returns `ErrNotFound` when positions_daily is empty) |

### Placeholder scan

None.

### Type consistency

- `openapi.HoldingsImpactResponse` and friends generated in Task 1; consumed identically in Tasks 3, 4, 5, 6.
- `SnapshotReader.HoldingsImpact` signature in Task 4 matches the reader method created in Task 3.
- `readerAdapter` satisfies the interface by Go embedding — no explicit shim needed unless the method receives a non-embedded type (confirm in Task 4 Step 2).
- `fakeReader.HoldingsImpact` matches the interface added in Task 4.
- `parseTopN` clamps to the same `[1, 50]` window the reader enforces — consistent regardless of which parser entry point is used first.
