# Portfolio Metrics Endpoint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `GET /portfolios/{slug}/metrics?window=...&metric=...` that returns all 71 pvbt metrics grouped by category in a compact column-oriented shape.

**Architecture:** New `snapshot.Reader.Metrics()` method queries the SQLite `metrics` table for requested names and windows, returning an `openapi.PortfolioMetrics` value grouped by category. A new `portfolio.Handler.Metrics` method routes the query params through `readSnapshot` using the existing skeleton. The `/statistics` endpoint is left untouched.

**Tech Stack:** Go, Fiber v3, Ginkgo/Gomega, oapi-codegen v2.6.0, modernc SQLite.

---

## File Map

| File | Action | Purpose |
|------|--------|---------|
| `openapi/openapi.yaml` | Modify | Add `MetricGroup`, `PortfolioMetrics` schemas + route |
| `openapi/openapi.gen.go` | Regenerated | Contains `MetricGroup` and `PortfolioMetrics` Go types |
| `snapshot/fixture.go` | Modify | Add pvbt PascalCase metric rows for multiple windows |
| `snapshot/metrics.go` | Create | `metricMeta` slice + `Metrics()` method on `*Reader` |
| `snapshot/metrics_test.go` | Create | Ginkgo tests for `Metrics()` |
| `portfolio/snapshot_iface.go` | Modify | Add `Metrics()` to `SnapshotReader` interface |
| `portfolio/handler.go` | Modify | Add `Metrics()` handler method |
| `portfolio/handler_test.go` | Modify | Stub `Metrics()` on `fakeSnapshotReader` + handler test |
| `api/portfolios.go` | Modify | Register `/metrics` route in both stub and real functions |

---

### Task 1: Add OpenAPI schemas and regenerate types

**Files:**
- Modify: `openapi/openapi.yaml`
- Regenerated: `openapi/openapi.gen.go`

- [ ] **Step 1: Add `MetricGroup` and `PortfolioMetrics` schemas**

In `openapi/openapi.yaml`, find the `# ============ Enumerations ============` section (around line 770) and add the two new schemas immediately after `PortfolioStatistic` (around line 950):

```yaml
    MetricGroup:
      description: Map of pvbt metric name to array of values, one per requested window.
      type: object
      additionalProperties:
        type: array
        items:
          type: number
          nullable: true

    PortfolioMetrics:
      description: All pvbt metrics grouped by category, column-oriented against a shared windows list.
      type: object
      required: [windows]
      properties:
        windows:
          type: array
          items:
            type: string
          description: Ordered list of requested windows. Each metric value array is aligned to this list.
        summary:
          $ref: '#/components/schemas/MetricGroup'
        risk:
          $ref: '#/components/schemas/MetricGroup'
        trade:
          $ref: '#/components/schemas/MetricGroup'
        withdrawal:
          $ref: '#/components/schemas/MetricGroup'
        tax:
          $ref: '#/components/schemas/MetricGroup'
        advanced:
          $ref: '#/components/schemas/MetricGroup'
```

- [ ] **Step 2: Add the route**

In `openapi/openapi.yaml`, after the `/portfolios/{slug}/statistics` block (around line 398), add:

```yaml
  /portfolios/{slug}/metrics:
    get:
      tags: [Portfolios]
      operationId: getPortfolioMetrics
      summary: All pvbt metrics grouped by category, filterable by window and name
      parameters:
        - $ref: '#/components/parameters/PortfolioSlug'
        - name: window
          in: query
          description: Comma-separated windows (since_inception,5yr,3yr,1yr,ytd,mtd,wtd). Default is since_inception.
          schema:
            type: string
        - name: metric
          in: query
          description: Comma-separated pvbt PascalCase metric names. Default is all metrics.
          schema:
            type: string
      responses:
        '200':
          description: Metrics grouped by category
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/PortfolioMetrics'
        '401':
          $ref: '#/components/responses/Unauthorized'
        '404':
          $ref: '#/components/responses/NotFound'
        '500':
          $ref: '#/components/responses/ServerError'
```

- [ ] **Step 3: Regenerate types**

```bash
cd /Users/jdf/Developer/penny-vault/pv-api
go generate ./openapi/
```

Expected: `openapi/openapi.gen.go` is updated with no errors.

- [ ] **Step 4: Verify generated types**

```bash
grep -A5 "MetricGroup\|PortfolioMetrics" openapi/openapi.gen.go | head -30
```

Expected output (exact field names may vary; verify `Windows []string` is non-pointer):
```go
// MetricGroup defines model for MetricGroup.
type MetricGroup = map[string][]*float64

// PortfolioMetrics defines model for PortfolioMetrics.
type PortfolioMetrics struct {
    Advanced   *MetricGroup `json:"advanced,omitempty"`
    Risk       *MetricGroup `json:"risk,omitempty"`
    Summary    *MetricGroup `json:"summary,omitempty"`
    Tax        *MetricGroup `json:"tax,omitempty"`
    Trade      *MetricGroup `json:"trade,omitempty"`
    Withdrawal *MetricGroup `json:"withdrawal,omitempty"`
    Windows    []string     `json:"windows"`
}
```

If `MetricGroup` is generated as `map[string][]float64` (non-pointer items), update the `snapshot/metrics.go` implementation in Task 3 accordingly, using `float64` slice instead of `[]*float64`.

- [ ] **Step 5: Compile check**

```bash
go build ./...
```

Expected: compiles cleanly.

- [ ] **Step 6: Commit**

```bash
git add openapi/openapi.yaml openapi/openapi.gen.go
git commit -m "feat(openapi): add MetricGroup, PortfolioMetrics schemas and /metrics route"
```

---

### Task 2: Extend fixture with pvbt-style metric rows

**Files:**
- Modify: `snapshot/fixture.go`

The current fixture only has legacy snake_case metric names with `window='full'`. The new `Metrics()` method reads pvbt PascalCase names with windows like `since_inception` and `1yr`. Add them to the fixture.

- [ ] **Step 1: Add pvbt metric rows to BuildTestSnapshot**

In `snapshot/fixture.go`, find the metrics INSERT statements (around line 82). Add the following immediately after the existing metric inserts, before the closing `}` of the `stmts` slice:

```go
		// pvbt PascalCase metrics — used by the Metrics() method tests
		`INSERT INTO metrics VALUES ('2024-01-08', 'Sharpe', 'since_inception', 1.55)`,
		`INSERT INTO metrics VALUES ('2024-01-08', 'Sharpe', '1yr', 1.20)`,
		`INSERT INTO metrics VALUES ('2024-01-08', 'Sortino', 'since_inception', 1.82)`,
		`INSERT INTO metrics VALUES ('2024-01-08', 'UpsideCaptureRatio', 'since_inception', 1.05)`,
		`INSERT INTO metrics VALUES ('2024-01-08', 'DownsideCaptureRatio', 'since_inception', 0.82)`,
		`INSERT INTO metrics VALUES ('2024-01-08', 'Beta', 'since_inception', 0.90)`,
		`INSERT INTO metrics VALUES ('2024-01-08', 'Beta', '1yr', 0.88)`,
		`INSERT INTO metrics VALUES ('2024-01-08', 'WinRate', 'since_inception', 0.62)`,
		`INSERT INTO metrics VALUES ('2024-01-08', 'TaxCostRatio', 'since_inception', 0.015)`,
```

- [ ] **Step 2: Run existing snapshot tests to confirm fixture still works**

```bash
go test ./snapshot/... -v 2>&1 | tail -20
```

Expected: all existing tests pass (PASS lines, no FAIL).

- [ ] **Step 3: Commit**

```bash
git add snapshot/fixture.go
git commit -m "test(snapshot): add pvbt PascalCase metric rows to test fixture"
```

---

### Task 3: Implement snapshot.Reader.Metrics (TDD)

**Files:**
- Create: `snapshot/metrics_test.go`
- Create: `snapshot/metrics.go`

- [ ] **Step 1: Write failing tests**

Create `snapshot/metrics_test.go`:

```go
// Copyright 2021-2026
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package snapshot_test

import (
	"context"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/snapshot"
)

var _ = Describe("Metrics", func() {
	var (
		r   *snapshot.Reader
		ctx = context.Background()
	)

	BeforeEach(func() {
		path := filepath.Join(GinkgoT().TempDir(), "m.sqlite")
		Expect(snapshot.BuildTestSnapshot(path)).To(Succeed())
		var err error
		r, err = snapshot.Open(path)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(r.Close)
	})

	It("returns metrics for since_inception by default", func() {
		result, err := r.Metrics(ctx, []string{"since_inception"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Windows).To(Equal([]string{"since_inception"}))

		Expect(result.Summary).NotTo(BeNil())
		sharpe := (*result.Summary)["Sharpe"]
		Expect(sharpe).To(HaveLen(1))
		Expect(*sharpe[0]).To(BeNumerically("~", 1.55, 0.001))
	})

	It("returns values for multiple windows, nil where window is missing", func() {
		result, err := r.Metrics(ctx, []string{"since_inception", "1yr"}, []string{"Sharpe", "UpsideCaptureRatio"})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Windows).To(Equal([]string{"since_inception", "1yr"}))

		sharpe := (*result.Summary)["Sharpe"]
		Expect(sharpe).To(HaveLen(2))
		Expect(*sharpe[0]).To(BeNumerically("~", 1.55, 0.001)) // since_inception
		Expect(*sharpe[1]).To(BeNumerically("~", 1.20, 0.001)) // 1yr

		// UpsideCaptureRatio has since_inception but not 1yr
		upCapture := (*result.Risk)["UpsideCaptureRatio"]
		Expect(upCapture).To(HaveLen(2))
		Expect(*upCapture[0]).To(BeNumerically("~", 1.05, 0.001))
		Expect(upCapture[1]).To(BeNil())
	})

	It("filters to requested metric names", func() {
		result, err := r.Metrics(ctx, []string{"since_inception"}, []string{"WinRate"})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Summary).To(BeNil())
		Expect(result.Risk).To(BeNil())
		Expect(result.Trade).NotTo(BeNil())
		Expect(*result.Trade).To(HaveKey("WinRate"))
		Expect(result.Advanced).To(BeNil())
	})

	It("places metrics in correct categories", func() {
		result, err := r.Metrics(ctx, []string{"since_inception"}, []string{"Beta", "TaxCostRatio", "Sortino"})
		Expect(err).NotTo(HaveOccurred())
		Expect((*result.Risk)).To(HaveKey("Beta"))
		Expect((*result.Tax)).To(HaveKey("TaxCostRatio"))
		Expect((*result.Summary)).To(HaveKey("Sortino"))
	})

	It("silently drops unknown metric names", func() {
		result, err := r.Metrics(ctx, []string{"since_inception"}, []string{"Sharpe", "NotAMetric"})
		Expect(err).NotTo(HaveOccurred())
		Expect((*result.Summary)).To(HaveKey("Sharpe"))
		Expect((*result.Summary)).NotTo(HaveKey("NotAMetric"))
	})

	It("silently drops unknown window names", func() {
		result, err := r.Metrics(ctx, []string{"bogus", "since_inception"}, []string{"Sharpe"})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Windows).To(Equal([]string{"since_inception"}))
	})

	It("omits metrics that have no rows in the snapshot", func() {
		result, err := r.Metrics(ctx, []string{"since_inception"}, []string{"CVaR"})
		Expect(err).NotTo(HaveOccurred())
		// CVaR has no rows in fixture — advanced should be nil or empty
		if result.Advanced != nil {
			Expect(*result.Advanced).NotTo(HaveKey("CVaR"))
		}
	})
})
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
go test ./snapshot/... -run "Metrics" -v 2>&1 | tail -10
```

Expected: compilation error (`r.Metrics undefined`) or FAIL — not PASS.

- [ ] **Step 3: Create snapshot/metrics.go**

```go
// Copyright 2021-2026
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package snapshot

import (
	"context"
	"fmt"
	"strings"

	"github.com/penny-vault/pv-api/openapi"
)

type metricEntry struct {
	Name     string
	Category string
}

var metricMeta = []metricEntry{
	// summary
	{"TWRR", "summary"},
	{"MWRR", "summary"},
	{"Sharpe", "summary"},
	{"Sortino", "summary"},
	{"Calmar", "summary"},
	{"KellerRatio", "summary"},
	{"MaxDrawdown", "summary"},
	{"StdDev", "summary"},
	// risk
	{"Beta", "risk"},
	{"Alpha", "risk"},
	{"TrackingError", "risk"},
	{"DownsideDeviation", "risk"},
	{"InformationRatio", "risk"},
	{"Treynor", "risk"},
	{"UlcerIndex", "risk"},
	{"ExcessKurtosis", "risk"},
	{"Skewness", "risk"},
	{"RSquared", "risk"},
	{"ValueAtRisk", "risk"},
	{"UpsideCaptureRatio", "risk"},
	{"DownsideCaptureRatio", "risk"},
	// trade
	{"WinRate", "trade"},
	{"AverageWin", "trade"},
	{"AverageLoss", "trade"},
	{"ProfitFactor", "trade"},
	{"AverageHoldingPeriod", "trade"},
	{"Turnover", "trade"},
	{"NPositivePeriods", "trade"},
	{"TradeGainLossRatio", "trade"},
	{"AverageMFE", "trade"},
	{"AverageMAE", "trade"},
	{"MedianMFE", "trade"},
	{"MedianMAE", "trade"},
	{"EdgeRatio", "trade"},
	{"TradeCaptureRatio", "trade"},
	{"LongWinRate", "trade"},
	{"ShortWinRate", "trade"},
	{"LongProfitFactor", "trade"},
	{"ShortProfitFactor", "trade"},
	// withdrawal
	{"SafeWithdrawalRate", "withdrawal"},
	{"PerpetualWithdrawalRate", "withdrawal"},
	{"DynamicWithdrawalRate", "withdrawal"},
	// tax
	{"LTCG", "tax"},
	{"STCG", "tax"},
	{"UnrealizedLTCG", "tax"},
	{"UnrealizedSTCG", "tax"},
	{"QualifiedDividends", "tax"},
	{"NonQualifiedIncome", "tax"},
	{"TaxCostRatio", "tax"},
	{"TaxDrag", "tax"},
	// advanced
	{"CAGR", "advanced"},
	{"ActiveReturn", "advanced"},
	{"SmartSharpe", "advanced"},
	{"SmartSortino", "advanced"},
	{"ProbabilisticSharpe", "advanced"},
	{"KRatio", "advanced"},
	{"KellyCriterion", "advanced"},
	{"OmegaRatio", "advanced"},
	{"GainToPainRatio", "advanced"},
	{"CVaR", "advanced"},
	{"TailRatio", "advanced"},
	{"RecoveryFactor", "advanced"},
	{"Exposure", "advanced"},
	{"ConsecutiveWins", "advanced"},
	{"ConsecutiveLosses", "advanced"},
	{"AvgDrawdown", "advanced"},
	{"AvgDrawdownDays", "advanced"},
	{"GainLossRatio", "advanced"},
	{"AvgUlcerIndex", "advanced"},
	{"P90UlcerIndex", "advanced"},
	{"MedianUlcerIndex", "advanced"},
}

var validWindows = map[string]bool{
	"since_inception": true,
	"5yr":             true,
	"3yr":             true,
	"1yr":             true,
	"ytd":             true,
	"mtd":             true,
	"wtd":             true,
}

// Metrics returns all requested metrics grouped by pvbt category.
// windows is the ordered list of windows to include; defaults to ["since_inception"] if empty.
// metrics is the list of pvbt PascalCase metric names; returns all if empty.
// Unknown names and windows are silently dropped.
// Metrics absent from the snapshot are omitted from the response.
func (r *Reader) Metrics(ctx context.Context, windows, metrics []string) (*openapi.PortfolioMetrics, error) {
	resolvedWindows := filterWindows(windows)
	if len(resolvedWindows) == 0 {
		resolvedWindows = []string{"since_inception"}
	}
	resolvedMeta := filterMetrics(metrics)

	names := make([]string, len(resolvedMeta))
	for i, m := range resolvedMeta {
		names[i] = m.Name
	}

	dbRows, err := r.queryMetricRows(ctx, names, resolvedWindows)
	if err != nil {
		return nil, err
	}

	return buildPortfolioMetrics(resolvedWindows, resolvedMeta, dbRows), nil
}

func filterWindows(requested []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(requested))
	for _, w := range requested {
		if validWindows[w] && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

func filterMetrics(requested []string) []metricEntry {
	if len(requested) == 0 {
		return metricMeta
	}
	want := make(map[string]bool, len(requested))
	for _, n := range requested {
		want[n] = true
	}
	out := make([]metricEntry, 0, len(requested))
	for _, m := range metricMeta {
		if want[m.Name] {
			out = append(out, m)
		}
	}
	return out
}

// queryMetricRows returns map[name][window] = value for the latest date per (name, window) pair.
func (r *Reader) queryMetricRows(ctx context.Context, names, windows []string) (map[string]map[string]float64, error) {
	if len(names) == 0 || len(windows) == 0 {
		return map[string]map[string]float64{}, nil
	}

	args := make([]any, 0, len(names)+len(windows))
	namePH := make([]string, len(names))
	for i, n := range names {
		namePH[i] = "?"
		args = append(args, n)
	}
	windowPH := make([]string, len(windows))
	for i, w := range windows {
		windowPH[i] = "?"
		args = append(args, w)
	}

	query := fmt.Sprintf(`
		SELECT name, window, value FROM (
			SELECT name, window, value,
				   ROW_NUMBER() OVER (PARTITION BY name, window ORDER BY date DESC) AS rn
			FROM metrics WHERE name IN (%s) AND window IN (%s)
		) WHERE rn = 1`,
		strings.Join(namePH, ","),
		strings.Join(windowPH, ","),
	)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query metrics: %w", err)
	}
	defer rows.Close()

	result := make(map[string]map[string]float64)
	for rows.Next() {
		var name, window string
		var value float64
		if err := rows.Scan(&name, &window, &value); err != nil {
			return nil, fmt.Errorf("scan metric row: %w", err)
		}
		if result[name] == nil {
			result[name] = make(map[string]float64)
		}
		result[name][window] = value
	}
	return result, rows.Err()
}

func buildPortfolioMetrics(windows []string, entries []metricEntry, dbRows map[string]map[string]float64) *openapi.PortfolioMetrics {
	out := &openapi.PortfolioMetrics{Windows: windows}

	for _, m := range entries {
		windowVals, ok := dbRows[m.Name]
		if !ok {
			continue
		}
		vals := make([]*float64, len(windows))
		hasAny := false
		for i, w := range windows {
			if v, exists := windowVals[w]; exists {
				cp := v
				vals[i] = &cp
				hasAny = true
			}
		}
		if !hasAny {
			continue
		}
		setMetricValue(out, m.Category, m.Name, vals)
	}
	return out
}

func setMetricValue(out *openapi.PortfolioMetrics, category, name string, vals []*float64) {
	switch category {
	case "summary":
		if out.Summary == nil {
			g := make(openapi.MetricGroup)
			out.Summary = &g
		}
		(*out.Summary)[name] = vals
	case "risk":
		if out.Risk == nil {
			g := make(openapi.MetricGroup)
			out.Risk = &g
		}
		(*out.Risk)[name] = vals
	case "trade":
		if out.Trade == nil {
			g := make(openapi.MetricGroup)
			out.Trade = &g
		}
		(*out.Trade)[name] = vals
	case "withdrawal":
		if out.Withdrawal == nil {
			g := make(openapi.MetricGroup)
			out.Withdrawal = &g
		}
		(*out.Withdrawal)[name] = vals
	case "tax":
		if out.Tax == nil {
			g := make(openapi.MetricGroup)
			out.Tax = &g
		}
		(*out.Tax)[name] = vals
	case "advanced":
		if out.Advanced == nil {
			g := make(openapi.MetricGroup)
			out.Advanced = &g
		}
		(*out.Advanced)[name] = vals
	}
}
```

**Note on generated types:** If `openapi.MetricGroup` is `map[string][]float64` (non-pointer items) rather than `map[string][]*float64`, change `vals []*float64` to `vals []float64`, remove the pointer copy `cp := v; vals[i] = &cp`, and use `vals[i] = v` directly (using 0 for missing values) or filter out metrics with missing windows.

- [ ] **Step 4: Run tests**

```bash
go test ./snapshot/... -run "Metrics" -v 2>&1 | tail -20
```

Expected: all Metrics specs PASS.

- [ ] **Step 5: Run full snapshot suite**

```bash
go test ./snapshot/... -v 2>&1 | tail -10
```

Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add snapshot/metrics.go snapshot/metrics_test.go
git commit -m "feat(snapshot): add Metrics() method returning all pvbt metrics by category"
```

---

### Task 4: Add Metrics to SnapshotReader interface

**Files:**
- Modify: `portfolio/snapshot_iface.go`
- Modify: `portfolio/handler_test.go`

- [ ] **Step 1: Add method to SnapshotReader interface**

In `portfolio/snapshot_iface.go`, add to the `SnapshotReader` interface after the `Statistics` line:

```go
	Metrics(ctx context.Context, windows, metrics []string) (*openapi.PortfolioMetrics, error)
```

- [ ] **Step 2: Add stub to fakeSnapshotReader in handler_test.go**

In `portfolio/handler_test.go`, find `fakeSnapshotReader` (around line 445) and add a `metrics` field and the stub method. Add the field to the struct:

```go
type fakeSnapshotReader struct {
	summary *openapi.PortfolioSummary
	metrics *openapi.PortfolioMetrics
}
```

Add the stub method after the existing `Transactions` stub:

```go
func (f *fakeSnapshotReader) Metrics(_ context.Context, _, _ []string) (*openapi.PortfolioMetrics, error) {
	return f.metrics, nil
}
```

- [ ] **Step 3: Compile check**

```bash
go build ./...
```

Expected: compiles cleanly. (snapshot.Reader already has Metrics from Task 3 so the interface is satisfied.)

- [ ] **Step 4: Commit**

```bash
git add portfolio/snapshot_iface.go portfolio/handler_test.go
git commit -m "feat(portfolio): add Metrics to SnapshotReader interface"
```

---

### Task 5: Implement Handler.Metrics (TDD)

**Files:**
- Modify: `portfolio/handler_test.go`
- Modify: `portfolio/handler.go`

- [ ] **Step 1: Write failing handler test**

In `portfolio/handler_test.go`, add after the existing `Handler.Summary` describe block (after line 535):

```go
var _ = Describe("Handler.Metrics", func() {
	var (
		app    *fiber.App
		store  *fakeStore
		opener *fakeSnapshotOpener
		sub    = "auth0|owner"
	)

	BeforeEach(func() {
		store = &fakeStore{}
		opener = &fakeSnapshotOpener{readers: map[string]portfolio.SnapshotReader{}}
		app = fiber.New(fiber.Config{JSONEncoder: sonic.Marshal, JSONDecoder: sonic.Unmarshal})
		app.Use(func(c fiber.Ctx) error {
			c.Locals(types.AuthSubjectKey{}, sub)
			return c.Next()
		})
		h := portfolio.NewHandler(store, &fakeStrategyStore{}, opener, nil, nil, nil, strategy.EphemeralOptions{})
		app.Get("/portfolios/:slug/metrics", h.Metrics)
	})

	It("returns 404 when portfolio has no snapshot", func() {
		store.rows = []portfolio.Portfolio{{
			ID: uuid.Must(uuid.NewV7()), OwnerSub: sub, Slug: "s1",
			Status: portfolio.StatusPending, SnapshotPath: nil,
		}}
		req := httptest.NewRequest("GET", "/portfolios/s1/metrics", nil)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusNotFound))
	})

	It("returns 200 with metrics payload", func() {
		path := "/fake/snap.sqlite"
		store.rows = []portfolio.Portfolio{{
			ID: uuid.Must(uuid.NewV7()), OwnerSub: sub, Slug: "s1",
			Status: portfolio.StatusReady, SnapshotPath: &path,
		}}
		sharpeVal := 1.55
		g := openapi.MetricGroup{"Sharpe": []*float64{&sharpeVal}}
		want := &openapi.PortfolioMetrics{
			Windows: []string{"since_inception"},
			Summary: &g,
		}
		opener.readers[path] = &fakeSnapshotReader{metrics: want}

		req := httptest.NewRequest("GET", "/portfolios/s1/metrics", nil)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusOK))

		body, _ := io.ReadAll(resp.Body)
		var got openapi.PortfolioMetrics
		Expect(sonic.Unmarshal(body, &got)).To(Succeed())
		Expect(got.Windows).To(Equal([]string{"since_inception"}))
	})

	It("passes window and metric query params to reader", func() {
		path := "/fake/snap.sqlite"
		store.rows = []portfolio.Portfolio{{
			ID: uuid.Must(uuid.NewV7()), OwnerSub: sub, Slug: "s1",
			Status: portfolio.StatusReady, SnapshotPath: &path,
		}}

		var capturedWindows, capturedMetrics []string
		capturingReader := &capturingMetricsReader{
			fakeSnapshotReader: &fakeSnapshotReader{
				metrics: &openapi.PortfolioMetrics{Windows: []string{"since_inception", "1yr"}},
			},
			onMetrics: func(w, m []string) {
				capturedWindows = w
				capturedMetrics = m
			},
		}
		opener.readers[path] = capturingReader

		req := httptest.NewRequest("GET", "/portfolios/s1/metrics?window=since_inception,1yr&metric=Sharpe,Beta", nil)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusOK))
		Expect(capturedWindows).To(Equal([]string{"since_inception", "1yr"}))
		Expect(capturedMetrics).To(Equal([]string{"Sharpe", "Beta"}))
	})
})

// capturingMetricsReader wraps fakeSnapshotReader to capture Metrics() arguments.
type capturingMetricsReader struct {
	*fakeSnapshotReader
	onMetrics func(windows, metrics []string)
}

func (c *capturingMetricsReader) Metrics(ctx context.Context, windows, metrics []string) (*openapi.PortfolioMetrics, error) {
	c.onMetrics(windows, metrics)
	return c.fakeSnapshotReader.Metrics(ctx, windows, metrics)
}
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
go test ./portfolio/... -run "Handler.Metrics" -v 2>&1 | tail -10
```

Expected: compilation error (`h.Metrics undefined`) or FAIL — not PASS.

- [ ] **Step 3: Add Metrics handler method to handler.go**

In `portfolio/handler.go`, add after the `Statistics` handler method (after line 600):

```go
// GET /portfolios/{slug}/metrics
func (h *Handler) Metrics(c fiber.Ctx) error {
	windows := splitParam(string([]byte(c.Query("window"))), "since_inception")
	metrics := splitParam(string([]byte(c.Query("metric"))), "")
	return h.readSnapshot(c, func(r SnapshotReader) (any, error) {
		return r.Metrics(c.Context(), windows, metrics)
	})
}

// splitParam splits a comma-separated query param. Returns []string{defaultVal}
// if the param is empty and defaultVal is non-empty; returns nil if both are empty.
func splitParam(val, defaultVal string) []string {
	if val == "" {
		if defaultVal == "" {
			return nil
		}
		return []string{defaultVal}
	}
	return strings.Split(val, ",")
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./portfolio/... -run "Handler.Metrics" -v 2>&1 | tail -20
```

Expected: all three Handler.Metrics specs PASS.

- [ ] **Step 5: Run full portfolio suite**

```bash
go test ./portfolio/... -v 2>&1 | tail -10
```

Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add portfolio/handler.go portfolio/handler_test.go
git commit -m "feat(portfolio): add Handler.Metrics for GET /portfolios/{slug}/metrics"
```

---

### Task 6: Wire route in api/portfolios.go

**Files:**
- Modify: `api/portfolios.go`

- [ ] **Step 1: Add stub route**

In `api/portfolios.go`, find `RegisterPortfolioRoutes` and add the stub after the `statistics` line:

```go
	r.Get("/portfolios/:slug/metrics", stubPortfolio)
```

- [ ] **Step 2: Add real route**

In `RegisterPortfolioRoutesWith`, add after the `h.Statistics` line:

```go
	r.Get("/portfolios/:slug/metrics", h.Metrics)
```

- [ ] **Step 3: Run full test suite**

```bash
go test ./... 2>&1 | tail -20
```

Expected: all packages pass, no FAIL lines.

- [ ] **Step 4: Commit**

```bash
git add api/portfolios.go
git commit -m "feat(api): register GET /portfolios/:slug/metrics route"
```
