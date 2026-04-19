# pvapi 3.0 Backtest Runner + Snapshot Slice Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make portfolios actually run. Implement the backtest runner end-to-end so creating a `one_shot` portfolio executes a strategy binary, writes a per-portfolio SQLite snapshot, and serves all eleven derived-data endpoints (`/summary`, `/drawdowns`, `/statistics`, `/trailing-returns`, `/holdings`, `/holdings/{date}`, `/holdings/history`, `/performance`, `/transactions`, `/runs`, `/runs/{runId}`) from that snapshot.

**Coordination note:** `/holdings/history` uses pvbt's batches-join query (`batches` table + `batch_id` on `transactions` / `annotations`). pvbt's schema change for this is in flight; pvapi is developing in parallel. The test fixture builder in this plan creates the batches schema locally so implementation does not block on the pvbt release.

**Architecture:**
- Two new packages: `backtest/` (Runner interface, HostRunner, Dispatcher worker pool, Run orchestration) and `snapshot/` (read-only typed accessors over the per-portfolio SQLite).
- State principle: the per-portfolio SQLite file is the single source of truth for backtest output. Postgres holds only orchestration state + scalar KPI cache columns for the list view.
- Dispatcher is a bounded worker-pool; `POST /runs` and portfolio-create auto-trigger both call `Submit`. Same dispatcher shared with Plan 6's scheduler later.
- Derived endpoints are near-identical skeletons that call a `SnapshotReader` interface injected into `portfolio.Handler`; tests use a fake opener/reader.
- All fixtures built programmatically — no checked-in binary SQLite files.

**Tech Stack:** Go 1.25+, Fiber v3, `github.com/jackc/pgx/v5/pgxpool`, `modernc.org/sqlite` (pure-Go SQLite, read-only), `github.com/bytedance/sonic`, Ginkgo/Gomega.

**Reference spec:** `docs/superpowers/specs/2026-04-18-pvapi-3-0-backtest-runner.md`

**Worktree:**

```bash
cd /Users/jdf/Developer/penny-vault/pv-api
git worktree add .worktrees/pvapi-3-backtest-runner -b pvapi-3-backtest-runner main
cd .worktrees/pvapi-3-backtest-runner
```

All subsequent commands assume you are in `.worktrees/pvapi-3-backtest-runner`.

---

## File overview

**Created**

```
sql/migrations/5_drop_dead_portfolio_cols.up.sql
sql/migrations/5_drop_dead_portfolio_cols.down.sql

backtest/
  doc.go
  config.go                 -- Config struct, ApplyDefaults, Validate
  config_test.go
  errors.go                 -- sentinel errors: ErrRunnerFailed, ErrTimedOut,
                               ErrAlreadyRunning, ErrQueueFull, ErrStrategyNotInstalled
  runner.go                 -- Runner interface, RunRequest
  host.go                   -- HostRunner (exec.CommandContext)
  host_test.go
  args.go                   -- BuildArgs (parameters -> CLI flags)
  args_test.go
  dispatcher.go             -- Dispatcher: bounded worker pool
  dispatcher_test.go
  run.go                    -- Run orchestration
  run_test.go
  sweep.go                  -- StartupSweep (stale .tmp + running->failed)
  sweep_test.go
  backtest_suite_test.go    -- ginkgo suite runner
  testdata/
    fakestrat/main.go       -- tiny Go program: reads FAKESTRAT_FIXTURE env and
                               copies that file to --output. Compiled by TestMain.

snapshot/
  doc.go
  reader.go                 -- Open(path), Close, *sql.DB (read-only)
  errors.go                 -- ErrNotFound (wraps sql.ErrNoRows for date reads)
  fixture.go                -- test helper (buildTestSnapshot(path)) - deliberately
                               in non-_test file so backtest/ tests can import it
  fixture_test.go
  summary.go                -- Summary(ctx)
  summary_test.go
  drawdowns.go              -- Drawdowns(ctx)
  drawdowns_test.go
  statistics.go             -- Statistics(ctx) []openapi.PortfolioStatistic
  statistics_test.go
  returns.go                -- TrailingReturns(ctx)
  returns_test.go
  holdings.go               -- CurrentHoldings(ctx), HoldingsAsOf(ctx, date)
  holdings_test.go
  performance.go            -- Performance(ctx, slug, from, to)
  performance_test.go
  transactions.go           -- Transactions(ctx, filter), TransactionFilter type
  transactions_test.go
  kpis.go                   -- Kpis(ctx) (Kpis, error) -- internal struct
  kpis_test.go
  snapshot_suite_test.go

portfolio/
  snapshot_iface.go         -- SnapshotOpener, SnapshotReader interfaces (new file)
  runs.go                   -- RunStore interface + Run domain type + PoolRunStore
  runs_test.go
```

**Modified**

```
openapi/openapi.yaml                -- rename /metrics -> /statistics,
                                       /measurements -> /performance; add /transactions
openapi/openapi.gen.go              -- regenerated via `make gen`
sql/migrate.go                       -- adds migration 5 (no code change, just new SQL files)
api/portfolios.go                    -- real handlers for the 10 derived endpoints;
                                        inject Dispatcher for POST /runs
api/server.go                        -- instantiate backtest.Dispatcher and pass into handlers
cmd/server.go                        -- build backtest.Config from viper; start dispatcher;
                                        pass to api.NewApp
go.mod / go.sum                      -- add modernc.org/sqlite
portfolio/handler.go                 -- add derived-endpoint handlers + POST /runs handler;
                                        accept SnapshotOpener + Dispatcher + RunStore
portfolio/handler_test.go            -- add derived-endpoint specs with FakeSnapshotOpener
portfolio/db.go                      -- no change (runs live in runs.go)
portfolio/store.go                   -- extend composite Store interface with RunStore methods
```

---

## Task 1: OpenAPI contract rename + `/transactions` endpoint + regen

**Files:**
- Modify: `openapi/openapi.yaml`
- Regen: `openapi/openapi.gen.go`

- [ ] **Step 1: Open `openapi/openapi.yaml` and rename the `/metrics` path block**

In `openapi/openapi.yaml`, find the `/portfolios/{slug}/metrics:` path entry. Change the path key to `/portfolios/{slug}/statistics:`, change `operationId: getPortfolioMetrics` to `operationId: getPortfolioStatistics`, change the `summary:` line to `Risk and style statistics`, and change the response schema `$ref: '#/components/schemas/PortfolioMetric'` to `$ref: '#/components/schemas/PortfolioStatistic'`.

- [ ] **Step 2: Rename the `/measurements` path block**

Find `/portfolios/{slug}/measurements:`. Change the path key to `/portfolios/{slug}/performance:`, operationId from `getPortfolioMeasurements` to `getPortfolioPerformance`, summary to `Equity-curve time series`, and the `$ref: '#/components/schemas/PortfolioMeasurements'` to `$ref: '#/components/schemas/PortfolioPerformance'`.

- [ ] **Step 3: Add the `/transactions` path block**

After the `/performance:` block, insert:

```yaml
  /portfolios/{slug}/transactions:
    get:
      tags: [Portfolios]
      operationId: getPortfolioTransactions
      summary: Backtest transactions (buys, sells, dividends, etc.)
      parameters:
        - $ref: '#/components/parameters/PortfolioSlug'
        - name: from
          in: query
          required: false
          description: Inclusive start date (YYYY-MM-DD).
          schema:
            type: string
            format: date
        - name: to
          in: query
          required: false
          description: Inclusive end date (YYYY-MM-DD).
          schema:
            type: string
            format: date
        - name: type
          in: query
          required: false
          description: Comma-separated list of transaction types to include.
          schema:
            type: string
      responses:
        '200':
          description: Transaction list
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/TransactionsResponse'
        '401':
          $ref: '#/components/responses/Unauthorized'
        '404':
          $ref: '#/components/responses/NotFound'
        '500':
          $ref: '#/components/responses/ServerError'
```

- [ ] **Step 4: Rename `components/schemas/PortfolioMetric` to `PortfolioStatistic`**

Find `    PortfolioMetric:` under `components: schemas:`. Rename the key to `PortfolioStatistic` (leave the object body unchanged — same `label`, `value`, `format` properties).

- [ ] **Step 5: Rename `PortfolioMeasurements` schema to `PortfolioPerformance` and `MeasurementPoint` to `PerformancePoint`**

Find `    PortfolioMeasurements:` — rename to `    PortfolioPerformance:`. Inside its body, change the `$ref: '#/components/schemas/MeasurementPoint'` to `$ref: '#/components/schemas/PerformancePoint'`. Then find `    MeasurementPoint:` and rename to `    PerformancePoint:`.

- [ ] **Step 6: Add `Transaction` and `TransactionsResponse` schemas**

After `PerformancePoint:`, insert:

```yaml
    Transaction:
      type: object
      required: [date, type]
      properties:
        date:
          type: string
          format: date
        type:
          type: string
          enum: [buy, sell, dividend, fee, deposit, withdrawal, split, interest, journal]
        ticker:
          type: string
          nullable: true
        figi:
          type: string
          nullable: true
        quantity:
          type: number
          format: double
        price:
          type: number
          format: double
        amount:
          type: number
          format: double
        qualified:
          type: boolean
          nullable: true
        justification:
          type: string
          nullable: true

    TransactionsResponse:
      type: object
      required: [items]
      properties:
        items:
          type: array
          items:
            $ref: '#/components/schemas/Transaction'
```

- [ ] **Step 6b: Add the `/holdings/history` path and its schemas**

In `openapi/openapi.yaml`, find the existing `/portfolios/{slug}/holdings/{date}:` path entry. Insert this block immediately after it (so `/history` sits next to the other holdings routes — oapi-codegen's route matching is order-insensitive, but this keeps the file readable):

```yaml
  /portfolios/{slug}/holdings/history:
    get:
      tags: [Portfolios]
      operationId: getPortfolioHoldingsHistory
      summary: Per-batch holdings history across the backtest
      parameters:
        - $ref: '#/components/parameters/PortfolioSlug'
        - name: from
          in: query
          required: false
          description: Inclusive lower bound on batch timestamp (YYYY-MM-DD).
          schema:
            type: string
            format: date
        - name: to
          in: query
          required: false
          description: Inclusive upper bound on batch timestamp (YYYY-MM-DD).
          schema:
            type: string
            format: date
      responses:
        '200':
          description: Per-batch holdings history
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/HoldingsHistoryResponse'
        '401':
          $ref: '#/components/responses/Unauthorized'
        '404':
          $ref: '#/components/responses/NotFound'
        '500':
          $ref: '#/components/responses/ServerError'
```

Then, inside `components: schemas:`, immediately after `TransactionsResponse:`, add:

```yaml
    HoldingsHistoryEntry:
      type: object
      required: [batchId, timestamp, items]
      properties:
        batchId:
          type: integer
          format: int64
        timestamp:
          type: string
          format: date-time
        items:
          type: array
          items:
            $ref: '#/components/schemas/Holding'
        annotations:
          type: object
          description: Optional strategy-written key/value labels for this batch.
          additionalProperties:
            type: string

    HoldingsHistoryResponse:
      type: object
      required: [items]
      properties:
        items:
          type: array
          items:
            $ref: '#/components/schemas/HoldingsHistoryEntry'
```

- [ ] **Step 7: Regenerate `openapi.gen.go`**

Run: `make gen`
Expected: exits 0; `openapi/openapi.gen.go` updated.

- [ ] **Step 8: Verify the generated file has the new types**

Run: `grep -c "PortfolioStatistic\|PortfolioPerformance\|PerformancePoint\|Transaction\b\|TransactionsResponse\|HoldingsHistoryEntry\|HoldingsHistoryResponse" openapi/openapi.gen.go`
Expected: nonzero count (should be at least 14).

Run: `grep -c "PortfolioMetric\|PortfolioMeasurements\|MeasurementPoint" openapi/openapi.gen.go`
Expected: `0` (all old names gone).

- [ ] **Step 9: Compile the tree**

Run: `go build ./...`
Expected: success. (No handler code references these types yet — all derived endpoints are 501 stubs — so no downstream breakage.)

- [ ] **Step 10: Commit**

```bash
git add openapi/openapi.yaml openapi/openapi.gen.go
git commit -m "rename OpenAPI /metrics->/statistics, /measurements->/performance, add /transactions and /holdings/history"
```

---

## Task 2: Migration 5 — drop dead portfolio columns

**Files:**
- Create: `sql/migrations/5_drop_dead_portfolio_cols.up.sql`
- Create: `sql/migrations/5_drop_dead_portfolio_cols.down.sql`

- [ ] **Step 1: Write the up migration**

Create `sql/migrations/5_drop_dead_portfolio_cols.up.sql`:

```sql
BEGIN;
ALTER TABLE portfolios
    DROP COLUMN summary_json,
    DROP COLUMN drawdowns_json,
    DROP COLUMN metrics_json,
    DROP COLUMN trailing_json,
    DROP COLUMN allocation_json,
    DROP COLUMN current_assets;
COMMIT;
```

- [ ] **Step 2: Write the down migration**

Create `sql/migrations/5_drop_dead_portfolio_cols.down.sql`:

```sql
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

- [ ] **Step 3: Apply the migration against the pvapi_smoke database**

Run:
```bash
PVAPI_DB_URL="pgx5://jdf@localhost:5432/pvapi_smoke" \
  go run ./cmd migrate up
```
Expected: `migrate: 5/u drop_dead_portfolio_cols (…ms)` printed, exits 0.

- [ ] **Step 4: Verify the columns are gone**

Run:
```bash
psql -d pvapi_smoke -c '\d portfolios' | grep -E "summary_json|drawdowns_json|metrics_json|trailing_json|allocation_json|current_assets"
```
Expected: no output (all six columns removed).

- [ ] **Step 5: Run the existing test suite to confirm nothing broke**

Run: `ginkgo run -race ./...`
Expected: all specs pass.

- [ ] **Step 6: Commit**

```bash
git add sql/migrations/5_drop_dead_portfolio_cols.up.sql sql/migrations/5_drop_dead_portfolio_cols.down.sql
git commit -m "add migration 5: drop dead derived-data columns from portfolios"
```

---

## Task 3: `backtest/` package scaffold — Config, errors, Runner interface

**Files:**
- Create: `backtest/doc.go`
- Create: `backtest/config.go`
- Create: `backtest/config_test.go`
- Create: `backtest/errors.go`
- Create: `backtest/runner.go`
- Create: `backtest/backtest_suite_test.go`

- [ ] **Step 1: Write `backtest/doc.go`**

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

// Package backtest runs strategy binaries that produce per-portfolio SQLite
// snapshots. It defines a pluggable Runner interface (HostRunner lands here;
// Docker/Kubernetes runners come in Plans 8/9), a bounded worker-pool
// Dispatcher, and the Run orchestration entry point that updates
// backtest_runs and portfolios rows around each invocation.
package backtest
```

- [ ] **Step 2: Write `backtest/errors.go`**

```go
// Copyright 2021-2026
// SPDX-License-Identifier: Apache-2.0
// (full header omitted for brevity — use the same header as doc.go)

package backtest

import "errors"

var (
	// ErrRunnerFailed is returned when the strategy binary exits non-zero
	// or cannot be launched. The wrapped error holds details.
	ErrRunnerFailed = errors.New("backtest: runner failed")

	// ErrTimedOut is returned when the runner exceeded its configured timeout.
	ErrTimedOut = errors.New("backtest: runner timed out")

	// ErrAlreadyRunning is returned by Run when the portfolio's status is
	// already 'running' at the time the worker picks the task up.
	ErrAlreadyRunning = errors.New("backtest: portfolio already running")

	// ErrQueueFull is returned by Dispatcher.Submit when the task channel
	// has reached its bounded capacity.
	ErrQueueFull = errors.New("backtest: dispatcher queue full")

	// ErrStrategyNotInstalled is returned when the resolved strategy has
	// no installed binary on disk.
	ErrStrategyNotInstalled = errors.New("backtest: strategy binary not installed")
)
```

- [ ] **Step 3: Write `backtest/runner.go`**

```go
// (copyright header)

package backtest

import (
	"context"
	"time"
)

// Runner executes a strategy binary and produces a SQLite snapshot at
// RunRequest.OutPath. Implementations: HostRunner (Plan 5), DockerRunner
// (Plan 8), KubernetesRunner (Plan 9).
type Runner interface {
	Run(ctx context.Context, req RunRequest) error
}

// RunRequest carries everything a Runner needs to produce one snapshot.
type RunRequest struct {
	Binary  string        // absolute path to the strategy binary
	Args    []string      // strategy-specific CLI flags (parameters + benchmark)
	OutPath string        // absolute path where the snapshot must be written
	Timeout time.Duration // 0 means use Config.Timeout default
}
```

- [ ] **Step 4: Write the failing Config test**

Create `backtest/config_test.go`:

```go
// (copyright header)

package backtest_test

import (
	"runtime"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/backtest"
)

var _ = Describe("Config", func() {
	Describe("ApplyDefaults", func() {
		It("sets MaxConcurrency to runtime.NumCPU when zero", func() {
			c := backtest.Config{}
			c.ApplyDefaults()
			Expect(c.MaxConcurrency).To(Equal(runtime.NumCPU()))
		})

		It("preserves a non-zero MaxConcurrency", func() {
			c := backtest.Config{MaxConcurrency: 3}
			c.ApplyDefaults()
			Expect(c.MaxConcurrency).To(Equal(3))
		})

		It("sets Timeout to 15 minutes when zero", func() {
			c := backtest.Config{}
			c.ApplyDefaults()
			Expect(c.Timeout).To(Equal(15 * time.Minute))
		})
	})

	Describe("Validate", func() {
		It("rejects empty SnapshotsDir", func() {
			c := backtest.Config{RunnerMode: "host"}
			Expect(c.Validate()).To(MatchError(ContainSubstring("snapshots_dir")))
		})

		It("rejects runner mode other than host in Plan 5", func() {
			c := backtest.Config{SnapshotsDir: "/tmp/snaps", RunnerMode: "docker"}
			Expect(c.Validate()).To(MatchError(ContainSubstring("runner.mode")))
		})

		It("accepts host mode with a snapshots dir", func() {
			c := backtest.Config{SnapshotsDir: "/tmp/snaps", RunnerMode: "host"}
			Expect(c.Validate()).To(Succeed())
		})

		It("rejects negative MaxConcurrency", func() {
			c := backtest.Config{SnapshotsDir: "/tmp/snaps", RunnerMode: "host", MaxConcurrency: -1}
			Expect(c.Validate()).To(MatchError(ContainSubstring("max_concurrency")))
		})
	})
})
```

- [ ] **Step 5: Write the ginkgo suite runner**

Create `backtest/backtest_suite_test.go`:

```go
// (copyright header)

package backtest_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestBacktest(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Backtest Suite")
}
```

- [ ] **Step 6: Run the failing config test**

Run: `ginkgo -r backtest`
Expected: compilation failure (`undefined: backtest.Config`).

- [ ] **Step 7: Write `backtest/config.go`**

```go
// (copyright header)

package backtest

import (
	"errors"
	"runtime"
	"time"
)

// Config drives the backtest subsystem. Populated at startup from viper.
type Config struct {
	SnapshotsDir   string        // absolute path; required
	MaxConcurrency int           // 0 -> runtime.NumCPU()
	Timeout        time.Duration // per-run timeout; 0 -> 15 minutes
	RunnerMode     string        // "host" only in Plan 5
}

// ApplyDefaults fills zero-valued fields with their defaults.
func (c *Config) ApplyDefaults() {
	if c.MaxConcurrency == 0 {
		c.MaxConcurrency = runtime.NumCPU()
	}
	if c.Timeout == 0 {
		c.Timeout = 15 * time.Minute
	}
}

// Validate returns an error if the config is not usable.
func (c Config) Validate() error {
	if c.SnapshotsDir == "" {
		return errors.New("backtest: snapshots_dir is required")
	}
	if c.MaxConcurrency < 0 {
		return errors.New("backtest: max_concurrency must be >= 0")
	}
	if c.RunnerMode != "host" {
		return errors.New("backtest: runner.mode must be \"host\" in Plan 5 (docker/kubernetes land in Plans 8/9)")
	}
	return nil
}
```

- [ ] **Step 8: Run the tests**

Run: `ginkgo -r backtest`
Expected: all specs pass.

- [ ] **Step 9: Commit**

```bash
git add backtest/
git commit -m "scaffold backtest package: config, errors, runner interface"
```

---

## Task 4: `backtest/host.go` HostRunner + fakestrat + tests

**Files:**
- Create: `backtest/testdata/fakestrat/main.go`
- Create: `backtest/host.go`
- Create: `backtest/host_test.go`

- [ ] **Step 1: Write the fake strategy binary**

Create `backtest/testdata/fakestrat/main.go`:

```go
// Copyright 2021-2026
// SPDX-License-Identifier: Apache-2.0
// Tiny test-only stand-in for a real strategy binary. Reads the
// FAKESTRAT_FIXTURE env variable as a source path and copies it to the
// --output flag. FAKESTRAT_BEHAVIOR=fail exits 1; FAKESTRAT_BEHAVIOR=sleep
// sleeps forever so context cancellation paths can be exercised.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

func main() {
	// First positional arg is the subcommand ("backtest"). Accept and
	// ignore — we only care about --output and env flags.
	if len(os.Args) < 2 || os.Args[1] != "backtest" {
		fmt.Fprintln(os.Stderr, "fakestrat: expected 'backtest' subcommand")
		os.Exit(2)
	}
	os.Args = append(os.Args[:1], os.Args[2:]...)

	output := flag.String("output", "", "output SQLite path")
	flag.Parse()

	switch os.Getenv("FAKESTRAT_BEHAVIOR") {
	case "fail":
		fmt.Fprintln(os.Stderr, "fakestrat: simulated failure")
		os.Exit(1)
	case "sleep":
		time.Sleep(1 * time.Hour)
	}

	if *output == "" {
		fmt.Fprintln(os.Stderr, "fakestrat: --output is required")
		os.Exit(2)
	}
	src := os.Getenv("FAKESTRAT_FIXTURE")
	if src == "" {
		fmt.Fprintln(os.Stderr, "fakestrat: FAKESTRAT_FIXTURE env is required")
		os.Exit(2)
	}

	in, err := os.Open(src)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakestrat: open fixture:", err)
		os.Exit(1)
	}
	defer in.Close()

	out, err := os.Create(*output)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakestrat: create output:", err)
		os.Exit(1)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		fmt.Fprintln(os.Stderr, "fakestrat: copy:", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 2: Write the failing HostRunner test**

Create `backtest/host_test.go`:

```go
// (copyright header)

package backtest_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/backtest"
)

var (
	fakeStratBin   string
	fakeStratSrc   string // a real file used as FAKESTRAT_FIXTURE
)

var _ = BeforeSuite(func() {
	dir := GinkgoT().TempDir()
	fakeStratBin = filepath.Join(dir, "fakestrat")
	cmd := exec.Command("go", "build", "-o", fakeStratBin, "./testdata/fakestrat")
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))

	fakeStratSrc = filepath.Join(dir, "fixture.bin")
	Expect(os.WriteFile(fakeStratSrc, []byte("this-is-a-fake-snapshot"), 0o644)).To(Succeed())
})

var _ = Describe("HostRunner", func() {
	var runner *backtest.HostRunner

	BeforeEach(func() {
		runner = &backtest.HostRunner{}
	})

	It("copies the fixture to OutPath on success", func() {
		out := filepath.Join(GinkgoT().TempDir(), "out.sqlite")
		Expect(os.Setenv("FAKESTRAT_FIXTURE", fakeStratSrc)).To(Succeed())
		DeferCleanup(func() { os.Unsetenv("FAKESTRAT_FIXTURE") })

		err := runner.Run(context.Background(), backtest.RunRequest{
			Binary:  fakeStratBin,
			Args:    []string{"--something", "1"},
			OutPath: out,
			Timeout: 5 * time.Second,
		})
		Expect(err).NotTo(HaveOccurred())

		data, rerr := os.ReadFile(out)
		Expect(rerr).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("this-is-a-fake-snapshot"))
	})

	It("wraps non-zero exit in ErrRunnerFailed with stderr attached", func() {
		out := filepath.Join(GinkgoT().TempDir(), "out.sqlite")
		Expect(os.Setenv("FAKESTRAT_BEHAVIOR", "fail")).To(Succeed())
		DeferCleanup(func() { os.Unsetenv("FAKESTRAT_BEHAVIOR") })
		Expect(os.Setenv("FAKESTRAT_FIXTURE", fakeStratSrc)).To(Succeed())
		DeferCleanup(func() { os.Unsetenv("FAKESTRAT_FIXTURE") })

		err := runner.Run(context.Background(), backtest.RunRequest{
			Binary:  fakeStratBin,
			OutPath: out,
			Timeout: 5 * time.Second,
		})
		Expect(err).To(MatchError(backtest.ErrRunnerFailed))
		Expect(err.Error()).To(ContainSubstring("simulated failure"))
	})

	It("returns ErrTimedOut when the timeout fires", func() {
		out := filepath.Join(GinkgoT().TempDir(), "out.sqlite")
		Expect(os.Setenv("FAKESTRAT_BEHAVIOR", "sleep")).To(Succeed())
		DeferCleanup(func() { os.Unsetenv("FAKESTRAT_BEHAVIOR") })

		err := runner.Run(context.Background(), backtest.RunRequest{
			Binary:  fakeStratBin,
			OutPath: out,
			Timeout: 150 * time.Millisecond,
		})
		Expect(err).To(MatchError(backtest.ErrTimedOut))
	})

	It("returns ErrTimedOut when the parent context is cancelled", func() {
		out := filepath.Join(GinkgoT().TempDir(), "out.sqlite")
		Expect(os.Setenv("FAKESTRAT_BEHAVIOR", "sleep")).To(Succeed())
		DeferCleanup(func() { os.Unsetenv("FAKESTRAT_BEHAVIOR") })

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(100 * time.Millisecond)
			cancel()
		}()

		err := runner.Run(ctx, backtest.RunRequest{
			Binary:  fakeStratBin,
			OutPath: out,
			Timeout: 5 * time.Second,
		})
		Expect(err).To(MatchError(backtest.ErrTimedOut))
	})
})
```

- [ ] **Step 3: Run the failing test**

Run: `ginkgo -r backtest`
Expected: compile error (`undefined: backtest.HostRunner`).

- [ ] **Step 4: Implement HostRunner**

Create `backtest/host.go`:

```go
// (copyright header)

package backtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/rs/zerolog/log"
)

// HostRunner runs the strategy binary directly as a host process.
type HostRunner struct{}

// Run implements Runner.
func (r *HostRunner) Run(ctx context.Context, req RunRequest) error {
	timeoutCtx := ctx
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		timeoutCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	args := append([]string{"backtest", "--output", req.OutPath}, req.Args...)
	cmd := exec.CommandContext(timeoutCtx, req.Binary, args...)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// stdout streams to debug log (strategy progress)
	stdout := newLogWriter("strategy-stdout")
	cmd.Stdout = stdout

	runErr := cmd.Run()

	if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("%w: %s", ErrTimedOut, firstNBytes(stderr.String(), 2048))
	}

	if runErr != nil {
		return fmt.Errorf("%w: %s: %s", ErrRunnerFailed, runErr.Error(), firstNBytes(stderr.String(), 2048))
	}

	return nil
}

// firstNBytes trims a string to at most n bytes.
func firstNBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// logWriter forwards each line written to it to zerolog at debug level.
type logWriter struct {
	scope string
	buf   bytes.Buffer
}

func newLogWriter(scope string) *logWriter { return &logWriter{scope: scope} }

func (w *logWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		idx := strings.IndexByte(w.buf.String(), '\n')
		if idx < 0 {
			break
		}
		line := w.buf.String()[:idx]
		log.Debug().Str("scope", w.scope).Msg(line)
		w.buf.Next(idx + 1)
	}
	return len(p), nil
}
```

- [ ] **Step 5: Run the tests**

Run: `ginkgo -r backtest`
Expected: all HostRunner specs pass. (The `timed out` test depends on the fake binary not dying first; if flaky on slow machines, bump the fake's sleep duration.)

- [ ] **Step 6: Commit**

```bash
git add backtest/host.go backtest/host_test.go backtest/testdata/
git commit -m "add HostRunner with fakestrat-based integration tests"
```

---

## Task 5: `backtest/args.go` BuildArgs parameter -> CLI flags

**Files:**
- Create: `backtest/args.go`
- Create: `backtest/args_test.go`

- [ ] **Step 1: Write the failing test**

Create `backtest/args_test.go`:

```go
// (copyright header)

package backtest_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/backtest"
)

var _ = Describe("BuildArgs", func() {
	It("emits --kebab-case flags for camelCase keys with --benchmark appended", func() {
		params := map[string]any{
			"momentumWindow": 90,
			"riskProfile":    "aggressive",
			"useTax":         true,
		}
		args := backtest.BuildArgs(params, "SPY")
		Expect(args).To(ContainElement("--momentum-window"))
		Expect(args).To(ContainElement("90"))
		Expect(args).To(ContainElement("--risk-profile"))
		Expect(args).To(ContainElement("aggressive"))
		Expect(args).To(ContainElement("--use-tax"))
		Expect(args).To(ContainElement("true"))
		Expect(args).To(ContainElement("--benchmark"))
		Expect(args).To(ContainElement("SPY"))
	})

	It("serializes arrays as comma-joined strings", func() {
		params := map[string]any{
			"tickers": []any{"VTI", "BND"},
		}
		args := backtest.BuildArgs(params, "")
		Expect(args).To(ContainElement("--tickers"))
		Expect(args).To(ContainElement("VTI,BND"))
	})

	It("omits --benchmark when blank", func() {
		args := backtest.BuildArgs(map[string]any{}, "")
		Expect(args).NotTo(ContainElement("--benchmark"))
	})

	It("produces deterministic order", func() {
		params := map[string]any{"z": 1, "a": 2}
		a := backtest.BuildArgs(params, "")
		b := backtest.BuildArgs(params, "")
		Expect(a).To(Equal(b))
	})
})
```

- [ ] **Step 2: Run the failing test**

Run: `ginkgo -r backtest`
Expected: compile error (`undefined: backtest.BuildArgs`).

- [ ] **Step 3: Implement BuildArgs**

Create `backtest/args.go`:

```go
// (copyright header)

package backtest

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// BuildArgs converts a portfolio's parameter map and benchmark into the
// strategy-binary CLI flags documented in the design spec
// ("Parameter mapping"). Returns a flat []string suitable for appending
// to the "backtest --output <path>" base command.
//
// Order is deterministic: parameter keys sorted ascending; --benchmark
// last (if non-empty).
func BuildArgs(params map[string]any, benchmark string) []string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]string, 0, 2*len(keys)+2)
	for _, k := range keys {
		out = append(out, "--"+toKebab(k), stringify(params[k]))
	}
	if benchmark != "" {
		out = append(out, "--benchmark", benchmark)
	}
	return out
}

// toKebab converts camelCase or snake_case to kebab-case.
func toKebab(s string) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r == '_':
			b.WriteRune('-')
		case unicode.IsUpper(r):
			if i > 0 {
				b.WriteRune('-')
			}
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func stringify(v any) string {
	switch vv := v.(type) {
	case string:
		return vv
	case []any:
		parts := make([]string, len(vv))
		for i, e := range vv {
			parts[i] = stringify(e)
		}
		return strings.Join(parts, ",")
	default:
		return fmt.Sprintf("%v", vv)
	}
}
```

- [ ] **Step 4: Run the tests**

Run: `ginkgo -r backtest`
Expected: all specs pass.

- [ ] **Step 5: Commit**

```bash
git add backtest/args.go backtest/args_test.go
git commit -m "add backtest.BuildArgs: parameters + benchmark -> CLI flags"
```

---

## Task 6: `snapshot/` package scaffold + fixture builder

**Files:**
- Add dep: `modernc.org/sqlite`
- Create: `snapshot/doc.go`, `snapshot/errors.go`, `snapshot/reader.go`
- Create: `snapshot/fixture.go` (not `_test` — so `backtest/` tests can use it)
- Create: `snapshot/fixture_test.go`
- Create: `snapshot/snapshot_suite_test.go`

- [ ] **Step 1: Add the sqlite driver**

Run: `go get modernc.org/sqlite`

Run: `go mod tidy`

- [ ] **Step 2: Write `snapshot/doc.go`**

```go
// (copyright header)

// Package snapshot reads per-portfolio SQLite files produced by strategy
// binaries. The per-portfolio file is the single source of truth for
// backtest output; every derived endpoint (summary, drawdowns, statistics,
// trailing-returns, holdings, performance, transactions) is served by
// opening a snapshot and calling the matching accessor on Reader.
//
// Only a read-only SQLite handle is opened — writes happen in the
// backtest package via atomic rename.
package snapshot
```

- [ ] **Step 3: Write `snapshot/errors.go`**

```go
// (copyright header)

package snapshot

import "errors"

// ErrNotFound is returned by date-parameterized readers (HoldingsAsOf)
// when the requested date falls outside the backtest window.
var ErrNotFound = errors.New("snapshot: not found")
```

- [ ] **Step 4: Write `snapshot/reader.go`**

```go
// (copyright header)

package snapshot

import (
	"database/sql"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite"
)

// Reader is a read-only handle onto a per-portfolio SQLite file.
type Reader struct {
	db *sql.DB
}

// Open connects to the SQLite file at path in read-only mode.
func Open(path string) (*Reader, error) {
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", "ro")
	q.Set("_pragma", "query_only(true)")
	u.RawQuery = q.Encode()

	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("snapshot open: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("snapshot ping: %w", err)
	}
	return &Reader{db: db}, nil
}

// Close releases the underlying SQLite handle.
func (r *Reader) Close() error { return r.db.Close() }
```

- [ ] **Step 5: Write `snapshot/fixture.go` (programmatic fixture builder)**

Create `snapshot/fixture.go`. This is the canonical known-values snapshot used by every snapshot test AND by backtest tests.

```go
// (copyright header)

package snapshot

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// BuildTestSnapshot creates a pvbt-shaped SQLite file at path with known
// values. Exported (non-_test) so backtest/ tests can use it as the
// fakestrat FAKESTRAT_FIXTURE source.
//
// The fixture represents a 5-day backtest with known equity curve and
// one BUY transaction.
func BuildTestSnapshot(path string) error {
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return fmt.Errorf("build fixture open: %w", err)
	}
	defer db.Close()

	stmts := []string{
		// schema (matches pvbt portfolio/sqlite.go, plus the in-flight
		// batches table and batch_id columns on transactions/annotations)
		`CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT)`,
		`CREATE TABLE perf_data (date TEXT NOT NULL, metric TEXT NOT NULL, value REAL NOT NULL)`,
		`CREATE TABLE batches (batch_id INTEGER PRIMARY KEY, timestamp TEXT NOT NULL)`,
		`CREATE TABLE transactions (batch_id INTEGER REFERENCES batches(batch_id), date TEXT, type TEXT, ticker TEXT, figi TEXT, quantity REAL, price REAL, amount REAL, qualified INTEGER, justification TEXT)`,
		`CREATE TABLE holdings (asset_ticker TEXT, asset_figi TEXT, quantity REAL, avg_cost REAL, market_value REAL)`,
		`CREATE TABLE tax_lots (asset_ticker TEXT, asset_figi TEXT, date TEXT, quantity REAL, price REAL, id TEXT DEFAULT '')`,
		`CREATE TABLE metrics (date TEXT, name TEXT, window TEXT, value REAL)`,
		`CREATE TABLE annotations (batch_id INTEGER REFERENCES batches(batch_id), timestamp INTEGER, key TEXT, value TEXT)`,

		// metadata
		`INSERT INTO metadata VALUES ('schema_version', '4')`,
		`INSERT INTO metadata VALUES ('start_date', '2024-01-02')`,
		`INSERT INTO metadata VALUES ('end_date', '2024-01-08')`,
		`INSERT INTO metadata VALUES ('benchmark', 'SPY')`,
		`INSERT INTO metadata VALUES ('perf_data_frequency', 'daily')`,

		// perf_data: portfolio and benchmark equity curve (5 trading days)
		// portfolio: 100000 -> 101000 -> 100500 -> 102000 -> 103000
		`INSERT INTO perf_data VALUES ('2024-01-02', 'portfolio_value', 100000)`,
		`INSERT INTO perf_data VALUES ('2024-01-03', 'portfolio_value', 101000)`,
		`INSERT INTO perf_data VALUES ('2024-01-04', 'portfolio_value', 100500)`,
		`INSERT INTO perf_data VALUES ('2024-01-05', 'portfolio_value', 102000)`,
		`INSERT INTO perf_data VALUES ('2024-01-08', 'portfolio_value', 103000)`,
		`INSERT INTO perf_data VALUES ('2024-01-02', 'benchmark_value', 100000)`,
		`INSERT INTO perf_data VALUES ('2024-01-03', 'benchmark_value', 100500)`,
		`INSERT INTO perf_data VALUES ('2024-01-04', 'benchmark_value', 100800)`,
		`INSERT INTO perf_data VALUES ('2024-01-05', 'benchmark_value', 101500)`,
		`INSERT INTO perf_data VALUES ('2024-01-08', 'benchmark_value', 102000)`,

		// batches: three rebalance points
		`INSERT INTO batches VALUES (1, '2024-01-02T14:30:00Z')`,
		`INSERT INTO batches VALUES (2, '2024-01-05T14:30:00Z')`,
		`INSERT INTO batches VALUES (3, '2024-01-08T14:30:00Z')`,

		// one BUY transaction in batch 1 + one DIVIDEND in batch 2
		`INSERT INTO transactions VALUES (1, '2024-01-02', 'buy', 'VTI', 'BBG000BDTBL9', 100, 100, 10000, 0, 'initial buy')`,
		`INSERT INTO transactions VALUES (2, '2024-01-05', 'dividend', 'VTI', 'BBG000BDTBL9', 0, 0, 25.50, 1, 'qualified div')`,

		// annotations: one per batch
		`INSERT INTO annotations VALUES (1, 1704205800, 'reason', 'initial allocation')`,
		`INSERT INTO annotations VALUES (2, 1704464200, 'reason', 'dividend payment')`,
		`INSERT INTO annotations VALUES (3, 1704722600, 'reason', 'final state')`,

		// current holdings
		`INSERT INTO holdings VALUES ('VTI', 'BBG000BDTBL9', 100, 100, 10300)`,
		`INSERT INTO holdings VALUES ('$CASH', '', 1, 93000, 93000)`,

		// metrics (full-window risk stats + max drawdown)
		`INSERT INTO metrics VALUES ('2024-01-08', 'sharpe_ratio', 'full', 1.23)`,
		`INSERT INTO metrics VALUES ('2024-01-08', 'sortino_ratio', 'full', 1.80)`,
		`INSERT INTO metrics VALUES ('2024-01-08', 'beta', 'full', 0.95)`,
		`INSERT INTO metrics VALUES ('2024-01-08', 'alpha', 'full', 0.02)`,
		`INSERT INTO metrics VALUES ('2024-01-08', 'std_dev', 'full', 0.11)`,
		`INSERT INTO metrics VALUES ('2024-01-08', 'ulcer_index', 'full', 0.50)`,
		`INSERT INTO metrics VALUES ('2024-01-08', 'tax_cost_ratio', 'full', 0.01)`,
		`INSERT INTO metrics VALUES ('2024-01-08', 'max_drawdown', 'full', -0.00495)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("build fixture: %w (SQL: %s)", err, stmt)
		}
	}
	return nil
}
```

- [ ] **Step 6: Write suite runner + fixture smoke test**

Create `snapshot/snapshot_suite_test.go`:

```go
// (copyright header)

package snapshot_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSnapshot(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Snapshot Suite")
}
```

Create `snapshot/fixture_test.go`:

```go
// (copyright header)

package snapshot_test

import (
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/snapshot"
)

var _ = Describe("Reader / fixture", func() {
	var fixturePath string

	BeforeEach(func() {
		fixturePath = filepath.Join(GinkgoT().TempDir(), "fixture.sqlite")
		Expect(snapshot.BuildTestSnapshot(fixturePath)).To(Succeed())
	})

	It("opens a built fixture and closes cleanly", func() {
		r, err := snapshot.Open(fixturePath)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Close()).To(Succeed())
	})

	It("returns an error for a nonexistent file", func() {
		_, err := snapshot.Open("/does/not/exist.sqlite")
		Expect(err).To(HaveOccurred())
	})
})
```

- [ ] **Step 7: Run the tests**

Run: `ginkgo -r snapshot`
Expected: both specs pass.

- [ ] **Step 8: Commit**

```bash
git add snapshot/ go.mod go.sum
git commit -m "scaffold snapshot package: Reader Open/Close + programmatic fixture"
```

---

## Task 7: `snapshot.Transactions`

**Files:**
- Create: `snapshot/transactions.go`
- Create: `snapshot/transactions_test.go`

- [ ] **Step 1: Write the failing test**

Create `snapshot/transactions_test.go`:

```go
// (copyright header)

package snapshot_test

import (
	"context"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/openapi"
	"github.com/penny-vault/pv-api/snapshot"
)

var _ = Describe("Transactions", func() {
	var reader *snapshot.Reader

	BeforeEach(func() {
		path := filepath.Join(GinkgoT().TempDir(), "f.sqlite")
		Expect(snapshot.BuildTestSnapshot(path)).To(Succeed())
		var err error
		reader, err = snapshot.Open(path)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(reader.Close)
	})

	It("returns all transactions when no filter is provided", func() {
		resp, err := reader.Transactions(context.Background(), snapshot.TransactionFilter{})
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.Items).To(HaveLen(2))
		Expect(resp.Items[0].Type).To(BeEquivalentTo(openapi.TransactionTypeBuy))
		Expect(resp.Items[0].BatchId).To(Equal(int64(1)))
		Expect(resp.Items[1].Type).To(BeEquivalentTo(openapi.TransactionTypeDividend))
		Expect(resp.Items[1].BatchId).To(Equal(int64(2)))
	})

	It("filters by date range inclusively", func() {
		from := mustDate("2024-01-04")
		to := mustDate("2024-01-08")
		resp, err := reader.Transactions(context.Background(), snapshot.TransactionFilter{From: &from, To: &to})
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.Items).To(HaveLen(1))
		Expect(resp.Items[0].Type).To(BeEquivalentTo(openapi.TransactionTypeDividend))
	})

	It("filters by type", func() {
		resp, err := reader.Transactions(context.Background(), snapshot.TransactionFilter{Types: []string{"buy"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.Items).To(HaveLen(1))
		Expect(resp.Items[0].Type).To(BeEquivalentTo(openapi.TransactionTypeBuy))
	})
})

func mustDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	Expect(err).NotTo(HaveOccurred())
	return t
}
```

- [ ] **Step 2: Run the failing test**

Run: `ginkgo -r snapshot`
Expected: compile error (undefined Transactions / TransactionFilter).

- [ ] **Step 3: Implement `snapshot/transactions.go`**

```go
// (copyright header)

package snapshot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/oapi-codegen/runtime/types"

	"github.com/penny-vault/pv-api/openapi"
)

// TransactionFilter scopes a Transactions read.
type TransactionFilter struct {
	From  *time.Time // inclusive
	To    *time.Time // inclusive
	Types []string   // e.g. []string{"buy","sell"}
}

// Transactions returns a filtered list of transactions from the snapshot.
func (r *Reader) Transactions(ctx context.Context, f TransactionFilter) (*openapi.TransactionsResponse, error) {
	var (
		where []string
		args  []any
	)
	if f.From != nil {
		where = append(where, "date >= ?")
		args = append(args, f.From.Format("2006-01-02"))
	}
	if f.To != nil {
		where = append(where, "date <= ?")
		args = append(args, f.To.Format("2006-01-02"))
	}
	if len(f.Types) > 0 {
		placeholders := strings.Repeat("?,", len(f.Types))
		placeholders = placeholders[:len(placeholders)-1]
		where = append(where, "type IN ("+placeholders+")")
		for _, t := range f.Types {
			args = append(args, t)
		}
	}

	q := `SELECT batch_id, date, type, ticker, figi, quantity, price, amount, qualified, justification
	        FROM transactions`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY batch_id, rowid"

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("transactions query: %w", err)
	}
	defer rows.Close()

	var out openapi.TransactionsResponse
	out.Items = []openapi.Transaction{}
	for rows.Next() {
		var (
			batchID                         int64
			dateStr                         string
			typeStr                         string
			ticker, figi, justification     *string
			quantity, price, amount         *float64
			qualified                       *int64
		)
		if err := rows.Scan(&batchID, &dateStr, &typeStr, &ticker, &figi, &quantity, &price, &amount, &qualified, &justification); err != nil {
			return nil, fmt.Errorf("transactions scan: %w", err)
		}
		d, perr := time.Parse("2006-01-02", dateStr)
		if perr != nil {
			return nil, fmt.Errorf("transactions parse date %q: %w", dateStr, perr)
		}
		t := openapi.Transaction{
			BatchId:       batchID,
			Date:          types.Date{Time: d},
			Type:          openapi.TransactionType(typeStr),
			Ticker:        ticker,
			Figi:          figi,
			Justification: justification,
			Quantity:      quantity,
			Price:         price,
			Amount:        amount,
		}
		if qualified != nil {
			v := *qualified != 0
			t.Qualified = &v
		}
		out.Items = append(out.Items, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("transactions iterate: %w", err)
	}
	return &out, nil
}
```

(If the generated `openapi.Transaction` fields differ — e.g., `Quantity` is not a pointer — adjust the scan targets accordingly. Check `openapi/openapi.gen.go` after Task 1 for exact field types.)

- [ ] **Step 4: Run the tests**

Run: `ginkgo -r snapshot`
Expected: all Transactions specs pass.

- [ ] **Step 5: Commit**

```bash
git add snapshot/transactions.go snapshot/transactions_test.go
git commit -m "add snapshot.Reader.Transactions with date and type filters"
```

---

## Task 8: `snapshot.CurrentHoldings` and `HoldingsAsOf`

**Files:**
- Create: `snapshot/holdings.go`
- Create: `snapshot/holdings_test.go`

- [ ] **Step 1: Write the failing tests**

Create `snapshot/holdings_test.go`:

```go
// (copyright header)

package snapshot_test

import (
	"context"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/snapshot"
)

var _ = Describe("Holdings", func() {
	var reader *snapshot.Reader

	BeforeEach(func() {
		path := filepath.Join(GinkgoT().TempDir(), "f.sqlite")
		Expect(snapshot.BuildTestSnapshot(path)).To(Succeed())
		var err error
		reader, err = snapshot.Open(path)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(reader.Close)
	})

	Describe("CurrentHoldings", func() {
		It("returns the holdings rows with totalMarketValue summed", func() {
			resp, err := reader.CurrentHoldings(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Items).To(HaveLen(2))
			Expect(resp.TotalMarketValue).To(BeNumerically("~", 103300, 0.01))
		})
	})

	Describe("HoldingsAsOf", func() {
		It("returns the replayed position on the buy date", func() {
			d := mustDate("2024-01-02")
			resp, err := reader.HoldingsAsOf(context.Background(), d)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Items).To(HaveLen(1))
			Expect(resp.Items[0].Ticker).To(Equal("VTI"))
			Expect(resp.Items[0].Quantity).To(BeNumerically("~", 100, 0.01))
		})

		It("returns ErrNotFound for a date outside the backtest window", func() {
			d := mustDate("2023-06-01")
			_, err := reader.HoldingsAsOf(context.Background(), d)
			Expect(err).To(MatchError(snapshot.ErrNotFound))
		})
	})

	Describe("HoldingsHistory", func() {
		It("emits one entry per batch with cumulative holdings and annotations", func() {
			resp, err := reader.HoldingsHistory(context.Background(), nil, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Items).To(HaveLen(3))

			// batch 1: one buy of 100 VTI
			Expect(resp.Items[0].BatchId).To(Equal(int64(1)))
			Expect(resp.Items[0].Items).To(HaveLen(1))
			Expect(resp.Items[0].Items[0].Ticker).To(Equal("VTI"))
			Expect(resp.Items[0].Items[0].Quantity).To(BeNumerically("~", 100, 0.01))
			Expect(*resp.Items[0].Annotations).To(HaveKeyWithValue("reason", "initial allocation"))

			// batch 2: dividend doesn't change quantity
			Expect(resp.Items[1].BatchId).To(Equal(int64(2)))
			Expect(resp.Items[1].Items).To(HaveLen(1))
			Expect(resp.Items[1].Items[0].Quantity).To(BeNumerically("~", 100, 0.01))

			// batch 3: empty batch (no transactions) still appears
			Expect(resp.Items[2].BatchId).To(Equal(int64(3)))
			Expect(resp.Items[2].Items).To(HaveLen(1))
			Expect(resp.Items[2].Items[0].Quantity).To(BeNumerically("~", 100, 0.01))
		})

		It("filters the batch range by from/to timestamps", func() {
			from := mustDate("2024-01-04")
			to := mustDate("2024-01-06")
			resp, err := reader.HoldingsHistory(context.Background(), &from, &to)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Items).To(HaveLen(1))
			Expect(resp.Items[0].BatchId).To(Equal(int64(2)))
		})
	})
})
```

- [ ] **Step 2: Run the failing tests**

Run: `ginkgo -r snapshot`
Expected: compile error.

- [ ] **Step 3: Implement `snapshot/holdings.go`**

```go
// (copyright header)

package snapshot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oapi-codegen/runtime/types"

	"github.com/penny-vault/pv-api/openapi"
)

const dateLayout = "2006-01-02"

// CurrentHoldings reads the holdings table as-is and sums totalMarketValue.
func (r *Reader) CurrentHoldings(ctx context.Context) (*openapi.HoldingsResponse, error) {
	endDate, err := r.readEndDate(ctx)
	if err != nil {
		return nil, err
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT asset_ticker, asset_figi, quantity, avg_cost, market_value FROM holdings`)
	if err != nil {
		return nil, fmt.Errorf("holdings query: %w", err)
	}
	defer rows.Close()

	var out openapi.HoldingsResponse
	out.Date = types.Date{Time: endDate}
	out.Items = []openapi.Holding{}

	var total float64
	for rows.Next() {
		var h openapi.Holding
		var figi string
		if err := rows.Scan(&h.Ticker, &figi, &h.Quantity, &h.AvgCost, &h.MarketValue); err != nil {
			return nil, fmt.Errorf("holdings scan: %w", err)
		}
		if figi != "" {
			h.Figi = &figi
		}
		total += h.MarketValue
		out.Items = append(out.Items, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("holdings iterate: %w", err)
	}
	out.TotalMarketValue = total
	return &out, nil
}

// HoldingsAsOf replays transactions up to (and including) date to reconstruct
// the per-ticker position. marketValue is approximated as quantity * last
// price observed for that ticker in the replayed transactions.
func (r *Reader) HoldingsAsOf(ctx context.Context, date time.Time) (*openapi.HoldingsResponse, error) {
	startDate, endDate, err := r.readDateWindow(ctx)
	if err != nil {
		return nil, err
	}
	if date.Before(startDate) || date.After(endDate) {
		return nil, ErrNotFound
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT ticker, figi, type, quantity, price FROM transactions
		  WHERE date <= ? ORDER BY date, rowid`,
		date.Format(dateLayout))
	if err != nil {
		return nil, fmt.Errorf("holdings asof query: %w", err)
	}
	defer rows.Close()

	type row struct {
		figi      string
		quantity  float64
		totalCost float64
		lastPrice float64
	}
	ledger := map[string]*row{}

	for rows.Next() {
		var (
			ticker, typeStr          string
			figi                     sql.NullString
			quantity, price          float64
		)
		if err := rows.Scan(&ticker, &figi, &typeStr, &quantity, &price); err != nil {
			return nil, fmt.Errorf("holdings asof scan: %w", err)
		}
		if ticker == "" {
			continue // e.g. cash journal
		}
		r := ledger[ticker]
		if r == nil {
			r = &row{figi: figi.String}
			ledger[ticker] = r
		}
		if price > 0 {
			r.lastPrice = price
		}
		switch typeStr {
		case "buy":
			r.totalCost += quantity * price
			r.quantity += quantity
		case "sell":
			if r.quantity > 0 {
				avg := r.totalCost / r.quantity
				r.totalCost -= avg * quantity
			}
			r.quantity -= quantity
		case "split":
			r.quantity *= price // split factor stored in price col per pvbt convention
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("holdings asof iterate: %w", err)
	}

	out := openapi.HoldingsResponse{
		Date:  types.Date{Time: date},
		Items: []openapi.Holding{},
	}
	for ticker, r := range ledger {
		if r.quantity <= 0 {
			continue
		}
		avg := 0.0
		if r.quantity > 0 {
			avg = r.totalCost / r.quantity
		}
		mv := r.quantity * r.lastPrice
		h := openapi.Holding{
			Ticker:      ticker,
			Quantity:    r.quantity,
			AvgCost:     avg,
			MarketValue: mv,
		}
		if r.figi != "" {
			figi := r.figi
			h.Figi = &figi
		}
		out.Items = append(out.Items, h)
		out.TotalMarketValue += mv
	}
	return &out, nil
}

// HoldingsHistory emits one entry per batch in the backtest. Uses the
// batches-join query recommended by the pvbt team:
//
//   SELECT b.batch_id, b.timestamp, t.ticker, t.figi,
//          SUM(CASE t.type WHEN 'buy' THEN t.quantity
//                          WHEN 'sell' THEN -t.quantity
//                          WHEN 'split' THEN t.quantity
//                          ELSE 0 END) AS quantity
//     FROM batches b
//     LEFT JOIN transactions t ON t.batch_id <= b.batch_id
//    GROUP BY b.batch_id, b.timestamp, t.ticker, t.figi
//   HAVING quantity != 0
//    ORDER BY b.batch_id, t.ticker;
//
// Annotations are fetched separately and zipped by batch_id.
func (r *Reader) HoldingsHistory(ctx context.Context, from, to *time.Time) (*openapi.HoldingsHistoryResponse, error) {
	// 1. select batches within range
	batchQ := `SELECT batch_id, timestamp FROM batches`
	var (
		where []string
		args  []any
	)
	if from != nil {
		where = append(where, "timestamp >= ?")
		args = append(args, from.Format("2006-01-02T15:04:05Z"))
	}
	if to != nil {
		// upper bound is end-of-day
		endOfTo := to.Add(24*time.Hour - time.Second)
		where = append(where, "timestamp <= ?")
		args = append(args, endOfTo.Format("2006-01-02T15:04:05Z"))
	}
	if len(where) > 0 {
		batchQ += " WHERE " + strings.Join(where, " AND ")
	}
	batchQ += " ORDER BY batch_id"

	rows, err := r.db.QueryContext(ctx, batchQ, args...)
	if err != nil {
		return nil, fmt.Errorf("holdings history batches: %w", err)
	}
	defer rows.Close()

	type batchKey struct {
		id int64
		ts time.Time
	}
	var batches []batchKey
	for rows.Next() {
		var b batchKey
		var tsStr string
		if err := rows.Scan(&b.id, &tsStr); err != nil {
			return nil, fmt.Errorf("holdings history scan: %w", err)
		}
		b.ts, _ = time.Parse(time.RFC3339, tsStr)
		batches = append(batches, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := &openapi.HoldingsHistoryResponse{Items: []openapi.HoldingsHistoryEntry{}}
	if len(batches) == 0 {
		return out, nil
	}

	// 2. for each batch, get cumulative holdings via SUM(CASE) over
	//    transactions up to and including that batch_id. Running two
	//    queries per batch is fine for typical strategies (dozens of
	//    batches); an aggregated single query is an optimization we can
	//    bring back if profiling demands it.
	for _, b := range batches {
		entry := openapi.HoldingsHistoryEntry{
			BatchId:   b.id,
			Timestamp: b.ts,
			Items:     []openapi.Holding{},
		}
		hRows, err := r.db.QueryContext(ctx, `
			SELECT ticker, figi,
			       SUM(CASE type
			           WHEN 'buy'   THEN  quantity
			           WHEN 'sell'  THEN -quantity
			           WHEN 'split' THEN  quantity
			           ELSE 0 END) AS q
			  FROM transactions
			 WHERE batch_id <= ?
			 GROUP BY ticker, figi
			HAVING q != 0
			 ORDER BY ticker
		`, b.id)
		if err != nil {
			return nil, fmt.Errorf("holdings history batch %d: %w", b.id, err)
		}
		for hRows.Next() {
			var ticker string
			var figi sql.NullString
			var q float64
			if err := hRows.Scan(&ticker, &figi, &q); err != nil {
				hRows.Close()
				return nil, fmt.Errorf("holdings history batch scan: %w", err)
			}
			h := openapi.Holding{Ticker: ticker, Quantity: q}
			if figi.Valid && figi.String != "" {
				s := figi.String
				h.Figi = &s
			}
			entry.Items = append(entry.Items, h)
		}
		hRows.Close()

		// 3. annotations for this batch
		aRows, err := r.db.QueryContext(ctx,
			`SELECT key, value FROM annotations WHERE batch_id = ? ORDER BY key`, b.id)
		if err != nil {
			return nil, fmt.Errorf("holdings history annotations %d: %w", b.id, err)
		}
		ann := map[string]string{}
		for aRows.Next() {
			var k, v string
			if err := aRows.Scan(&k, &v); err != nil {
				aRows.Close()
				return nil, err
			}
			ann[k] = v
		}
		aRows.Close()
		if len(ann) > 0 {
			entry.Annotations = &ann
		}
		out.Items = append(out.Items, entry)
	}
	return out, nil
}

func (r *Reader) readEndDate(ctx context.Context) (time.Time, error) {
	var s string
	err := r.db.QueryRowContext(ctx,
		`SELECT value FROM metadata WHERE key='end_date'`).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrNotFound
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("read end_date: %w", err)
	}
	return time.Parse(dateLayout, s)
}

func (r *Reader) readDateWindow(ctx context.Context) (time.Time, time.Time, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT key, value FROM metadata WHERE key IN ('start_date','end_date')`)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("read window: %w", err)
	}
	defer rows.Close()
	var start, end time.Time
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return time.Time{}, time.Time{}, err
		}
		t, perr := time.Parse(dateLayout, v)
		if perr != nil {
			return time.Time{}, time.Time{}, perr
		}
		if k == "start_date" {
			start = t
		} else {
			end = t
		}
	}
	if start.IsZero() || end.IsZero() {
		return time.Time{}, time.Time{}, ErrNotFound
	}
	return start, end, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `ginkgo -r snapshot`
Expected: all Holdings specs pass.

- [ ] **Step 5: Commit**

```bash
git add snapshot/holdings.go snapshot/holdings_test.go
git commit -m "add snapshot.Reader.CurrentHoldings + HoldingsAsOf + HoldingsHistory"
```

---

## Task 9: `snapshot.Summary` + `Statistics` + `Kpis`

**Files:**
- Create: `snapshot/summary.go`, `snapshot/summary_test.go`
- Create: `snapshot/statistics.go`, `snapshot/statistics_test.go`
- Create: `snapshot/kpis.go`, `snapshot/kpis_test.go`

- [ ] **Step 1: Write failing Summary test**

Create `snapshot/summary_test.go`:

```go
// (copyright header)

package snapshot_test

import (
	"context"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/snapshot"
)

var _ = Describe("Summary", func() {
	It("returns KPIs from metadata + metrics + perf_data", func() {
		path := filepath.Join(GinkgoT().TempDir(), "f.sqlite")
		Expect(snapshot.BuildTestSnapshot(path)).To(Succeed())
		r, err := snapshot.Open(path)
		Expect(err).NotTo(HaveOccurred())
		defer r.Close()

		s, err := r.Summary(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(s.CurrentValue).To(BeNumerically("~", 103000, 0.01))
		Expect(s.Sharpe).To(BeNumerically("~", 1.23, 0.001))
		Expect(s.Sortino).To(BeNumerically("~", 1.80, 0.001))
		Expect(s.Beta).To(BeNumerically("~", 0.95, 0.001))
		Expect(s.MaxDrawDown).To(BeNumerically("~", -0.00495, 0.001))
	})
})
```

- [ ] **Step 2: Run the failing test**

Run: `ginkgo -r snapshot`
Expected: compile error.

- [ ] **Step 3: Implement `snapshot/summary.go`**

```go
// (copyright header)

package snapshot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/penny-vault/pv-api/openapi"
)

// Summary returns top-line KPIs by reading metadata, the latest perf_data
// row, and the full-window metrics table.
func (r *Reader) Summary(ctx context.Context) (*openapi.PortfolioSummary, error) {
	k, err := r.Kpis(ctx)
	if err != nil {
		return nil, err
	}
	return &openapi.PortfolioSummary{
		CurrentValue:       k.CurrentValue,
		YtdReturn:          k.YtdReturn,
		OneYearReturn:      k.OneYearReturn,
		CagrSinceInception: k.Cagr,
		MaxDrawDown:        k.MaxDrawdown,
		Sharpe:             k.Sharpe,
		Sortino:            k.Sortino,
		Beta:               k.Beta,
		Alpha:              k.Alpha,
		StdDev:             k.StdDev,
		UlcerIndex:         &k.UlcerIndex,
		TaxCostRatio:       k.TaxCostRatio,
	}, nil
}

// readMetric returns the value of the full-window metric named name, or
// 0 if the row is absent. Any other error is returned as-is.
func (r *Reader) readMetric(ctx context.Context, name string) (float64, error) {
	var v float64
	err := r.db.QueryRowContext(ctx,
		`SELECT value FROM metrics WHERE name = ? AND window='full' LIMIT 1`, name).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read metric %s: %w", name, err)
	}
	return v, nil
}
```

- [ ] **Step 4: Implement `snapshot/kpis.go`**

```go
// (copyright header)

package snapshot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

// Kpis is the internal scalar-KPI struct written to portfolios columns by
// backtest.Run after a successful snapshot.
type Kpis struct {
	CurrentValue   float64
	YtdReturn      float64
	OneYearReturn  float64
	Cagr           float64
	MaxDrawdown    float64
	Sharpe         float64
	Sortino        float64
	Beta           float64
	Alpha          float64
	StdDev         float64
	UlcerIndex     float64
	TaxCostRatio   float64
	InceptionDate  time.Time
}

// Kpis computes the internal KPI struct from the snapshot.
func (r *Reader) Kpis(ctx context.Context) (Kpis, error) {
	start, _, err := r.readDateWindow(ctx)
	if err != nil {
		return Kpis{}, err
	}

	// latest portfolio_value -> currentValue + inception value
	var curVal, startVal float64
	if err := r.db.QueryRowContext(ctx,
		`SELECT value FROM perf_data WHERE metric='portfolio_value' ORDER BY date DESC LIMIT 1`).
		Scan(&curVal); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Kpis{}, fmt.Errorf("kpis current value: %w", err)
	}
	if err := r.db.QueryRowContext(ctx,
		`SELECT value FROM perf_data WHERE metric='portfolio_value' ORDER BY date ASC LIMIT 1`).
		Scan(&startVal); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Kpis{}, fmt.Errorf("kpis start value: %w", err)
	}

	// YTD: take the earliest portfolio_value >= YTD-start
	ytdStart := time.Date(time.Now().Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	var ytdBaseline float64
	err = r.db.QueryRowContext(ctx,
		`SELECT value FROM perf_data WHERE metric='portfolio_value' AND date >= ?
		  ORDER BY date ASC LIMIT 1`, ytdStart.Format(dateLayout)).Scan(&ytdBaseline)
	if errors.Is(err, sql.ErrNoRows) {
		ytdBaseline = startVal
	} else if err != nil {
		return Kpis{}, fmt.Errorf("kpis ytd baseline: %w", err)
	}
	ytdReturn := 0.0
	if ytdBaseline > 0 {
		ytdReturn = (curVal - ytdBaseline) / ytdBaseline
	}

	// One-year: take value ~365 days before end
	var oneYearBaseline float64
	cutoff := time.Now().AddDate(-1, 0, 0)
	err = r.db.QueryRowContext(ctx,
		`SELECT value FROM perf_data WHERE metric='portfolio_value' AND date <= ?
		  ORDER BY date DESC LIMIT 1`, cutoff.Format(dateLayout)).Scan(&oneYearBaseline)
	if errors.Is(err, sql.ErrNoRows) {
		oneYearBaseline = startVal
	} else if err != nil {
		return Kpis{}, fmt.Errorf("kpis 1y baseline: %w", err)
	}
	oneYearReturn := 0.0
	if oneYearBaseline > 0 {
		oneYearReturn = (curVal - oneYearBaseline) / oneYearBaseline
	}

	// CAGR since inception
	years := time.Since(start).Hours() / 24 / 365.25
	cagr := 0.0
	if startVal > 0 && years > 0 {
		cagr = math.Pow(curVal/startVal, 1/years) - 1
	}

	metrics := []struct {
		name string
		dst  *float64
	}{
		{"sharpe_ratio", new(float64)},
		{"sortino_ratio", new(float64)},
		{"beta", new(float64)},
		{"alpha", new(float64)},
		{"std_dev", new(float64)},
		{"ulcer_index", new(float64)},
		{"tax_cost_ratio", new(float64)},
		{"max_drawdown", new(float64)},
	}
	vals := map[string]float64{}
	for _, m := range metrics {
		v, err := r.readMetric(ctx, m.name)
		if err != nil {
			return Kpis{}, err
		}
		vals[m.name] = v
	}

	return Kpis{
		CurrentValue:  curVal,
		YtdReturn:     ytdReturn,
		OneYearReturn: oneYearReturn,
		Cagr:          cagr,
		MaxDrawdown:   vals["max_drawdown"],
		Sharpe:        vals["sharpe_ratio"],
		Sortino:       vals["sortino_ratio"],
		Beta:          vals["beta"],
		Alpha:         vals["alpha"],
		StdDev:        vals["std_dev"],
		UlcerIndex:    vals["ulcer_index"],
		TaxCostRatio:  vals["tax_cost_ratio"],
		InceptionDate: start,
	}, nil
}
```

- [ ] **Step 5: Write kpis + summary tests together**

Create `snapshot/kpis_test.go`:

```go
// (copyright header)

package snapshot_test

import (
	"context"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/snapshot"
)

var _ = Describe("Kpis", func() {
	It("computes current value, CAGR, and pulls risk metrics", func() {
		path := filepath.Join(GinkgoT().TempDir(), "f.sqlite")
		Expect(snapshot.BuildTestSnapshot(path)).To(Succeed())
		r, err := snapshot.Open(path)
		Expect(err).NotTo(HaveOccurred())
		defer r.Close()

		k, err := r.Kpis(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(k.CurrentValue).To(BeNumerically("~", 103000, 0.01))
		Expect(k.Sharpe).To(BeNumerically("~", 1.23, 0.001))
		Expect(k.MaxDrawdown).To(BeNumerically("~", -0.00495, 0.001))
		Expect(k.InceptionDate.Format("2006-01-02")).To(Equal("2024-01-02"))
	})
})
```

- [ ] **Step 6: Write failing Statistics test**

Create `snapshot/statistics_test.go`:

```go
// (copyright header)

package snapshot_test

import (
	"context"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/snapshot"
)

var _ = Describe("Statistics", func() {
	It("maps metrics rows to PortfolioStatistic with labels", func() {
		path := filepath.Join(GinkgoT().TempDir(), "f.sqlite")
		Expect(snapshot.BuildTestSnapshot(path)).To(Succeed())
		r, err := snapshot.Open(path)
		Expect(err).NotTo(HaveOccurred())
		defer r.Close()

		stats, err := r.Statistics(context.Background())
		Expect(err).NotTo(HaveOccurred())

		byLabel := map[string]float64{}
		for _, s := range stats {
			byLabel[s.Label] = s.Value
		}
		Expect(byLabel).To(HaveKeyWithValue("Sharpe Ratio", BeNumerically("~", 1.23, 0.001)))
		Expect(byLabel).To(HaveKeyWithValue("Beta", BeNumerically("~", 0.95, 0.001)))
	})
})
```

- [ ] **Step 7: Implement `snapshot/statistics.go`**

```go
// (copyright header)

package snapshot

import (
	"context"
	"fmt"

	"github.com/penny-vault/pv-api/openapi"
)

// statisticMeta describes a row in the metrics table that should be
// surfaced as a PortfolioStatistic. Keep the list + display labels in
// one place so renames don't drift.
var statisticMeta = []struct {
	Name   string
	Label  string
	Format openapi.MetricFormat
}{
	{"sharpe_ratio", "Sharpe Ratio", openapi.MetricFormatDecimal},
	{"sortino_ratio", "Sortino Ratio", openapi.MetricFormatDecimal},
	{"beta", "Beta", openapi.MetricFormatDecimal},
	{"alpha", "Alpha", openapi.MetricFormatPercent},
	{"std_dev", "Standard Deviation", openapi.MetricFormatPercent},
	{"ulcer_index", "Ulcer Index", openapi.MetricFormatDecimal},
	{"tax_cost_ratio", "Tax Cost Ratio", openapi.MetricFormatPercent},
	{"max_drawdown", "Max Drawdown", openapi.MetricFormatPercent},
}

// Statistics returns the risk/style statistic rows for the UI panel.
func (r *Reader) Statistics(ctx context.Context) ([]openapi.PortfolioStatistic, error) {
	out := make([]openapi.PortfolioStatistic, 0, len(statisticMeta))
	for _, m := range statisticMeta {
		v, err := r.readMetric(ctx, m.Name)
		if err != nil {
			return nil, fmt.Errorf("statistics %s: %w", m.Name, err)
		}
		out = append(out, openapi.PortfolioStatistic{
			Label:  m.Label,
			Value:  v,
			Format: m.Format,
		})
	}
	return out, nil
}
```

(Confirm the generated `openapi.MetricFormat` constants — `MetricFormatDecimal`, `MetricFormatPercent` — exist; if named differently, adjust.)

- [ ] **Step 8: Run the tests**

Run: `ginkgo -r snapshot`
Expected: all Kpis / Summary / Statistics specs pass.

- [ ] **Step 9: Commit**

```bash
git add snapshot/summary.go snapshot/summary_test.go snapshot/kpis.go snapshot/kpis_test.go snapshot/statistics.go snapshot/statistics_test.go
git commit -m "add snapshot.Reader.Summary + Kpis + Statistics"
```

---

## Task 10: `snapshot.Drawdowns` + `TrailingReturns` + `Performance`

**Files:**
- Create: `snapshot/drawdowns.go`, `snapshot/drawdowns_test.go`
- Create: `snapshot/returns.go`, `snapshot/returns_test.go`
- Create: `snapshot/performance.go`, `snapshot/performance_test.go`

- [ ] **Step 1: Write failing drawdowns test**

Create `snapshot/drawdowns_test.go`:

```go
// (copyright header)

package snapshot_test

import (
	"context"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/snapshot"
)

var _ = Describe("Drawdowns", func() {
	It("detects the single dip in the fixture (101000 -> 100500)", func() {
		path := filepath.Join(GinkgoT().TempDir(), "f.sqlite")
		Expect(snapshot.BuildTestSnapshot(path)).To(Succeed())
		r, err := snapshot.Open(path)
		Expect(err).NotTo(HaveOccurred())
		defer r.Close()

		dds, err := r.Drawdowns(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(dds).To(HaveLen(1))
		Expect(dds[0].Start.Format("2006-01-02")).To(Equal("2024-01-03"))
		Expect(dds[0].Trough.Format("2006-01-02")).To(Equal("2024-01-04"))
		Expect(dds[0].Depth).To(BeNumerically("~", -0.00495, 0.001))
	})
})
```

- [ ] **Step 2: Implement `snapshot/drawdowns.go`**

```go
// (copyright header)

package snapshot

import (
	"context"
	"fmt"
	"time"

	"github.com/oapi-codegen/runtime/types"

	"github.com/penny-vault/pv-api/openapi"
)

// Drawdowns streams the portfolio_value series and emits a Drawdown
// record for each peak-to-trough-to-recovery cycle, sorted by depth
// (deepest first).
func (r *Reader) Drawdowns(ctx context.Context) ([]openapi.Drawdown, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT date, value FROM perf_data
		  WHERE metric='portfolio_value' ORDER BY date ASC`)
	if err != nil {
		return nil, fmt.Errorf("drawdowns query: %w", err)
	}
	defer rows.Close()

	type point struct {
		d time.Time
		v float64
	}
	var series []point
	for rows.Next() {
		var s string
		var v float64
		if err := rows.Scan(&s, &v); err != nil {
			return nil, fmt.Errorf("drawdowns scan: %w", err)
		}
		t, _ := time.Parse(dateLayout, s)
		series = append(series, point{t, v})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var dds []openapi.Drawdown
	if len(series) == 0 {
		return dds, nil
	}
	peak := series[0]
	inDD := false
	var trough point
	var depth float64

	for i := 1; i < len(series); i++ {
		p := series[i]
		if p.v >= peak.v {
			// recovery or new peak
			if inDD {
				days := int32(i - indexOf(series, peak.d))
				dds = append(dds, openapi.Drawdown{
					Start:  types.Date{Time: peak.d},
					Trough: types.Date{Time: trough.d},
					Recovery: func() *types.Date {
						d := types.Date{Time: p.d}
						return &d
					}(),
					Depth: depth,
					Days:  &days,
				})
				inDD = false
			}
			peak = p
			continue
		}
		// drawdown
		if !inDD || p.v < trough.v {
			trough = p
			depth = (p.v - peak.v) / peak.v
		}
		inDD = true
	}
	if inDD {
		// unrecovered drawdown at end of series
		days := int32(len(series) - indexOf(series, peak.d))
		dds = append(dds, openapi.Drawdown{
			Start:  types.Date{Time: peak.d},
			Trough: types.Date{Time: trough.d},
			Depth:  depth,
			Days:   &days,
		})
	}

	// sort by depth ascending (more negative = deeper first)
	for i := 1; i < len(dds); i++ {
		for j := i; j > 0 && dds[j].Depth < dds[j-1].Depth; j-- {
			dds[j], dds[j-1] = dds[j-1], dds[j]
		}
	}
	return dds, nil
}

func indexOf(series []struct {
	d time.Time
	v float64
}, d time.Time) int {
	for i, p := range series {
		if p.d.Equal(d) {
			return i
		}
	}
	return 0
}
```

- [ ] **Step 3: Run drawdowns tests**

Run: `ginkgo -r snapshot`
Expected: drawdowns spec passes.

- [ ] **Step 4: Write failing trailing-returns test**

Create `snapshot/returns_test.go`:

```go
// (copyright header)

package snapshot_test

import (
	"context"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/openapi"
	"github.com/penny-vault/pv-api/snapshot"
)

var _ = Describe("TrailingReturns", func() {
	It("emits portfolio and benchmark rows with since-inception populated", func() {
		path := filepath.Join(GinkgoT().TempDir(), "f.sqlite")
		Expect(snapshot.BuildTestSnapshot(path)).To(Succeed())
		r, err := snapshot.Open(path)
		Expect(err).NotTo(HaveOccurred())
		defer r.Close()

		rows, err := r.TrailingReturns(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(2))

		var portfolioRow openapi.TrailingReturnRow
		for _, row := range rows {
			if row.Kind == openapi.ReturnRowKindPortfolio {
				portfolioRow = row
			}
		}
		Expect(portfolioRow.Title).NotTo(BeEmpty())
		Expect(portfolioRow.SinceInception).To(BeNumerically(">", 0))
	})
})
```

- [ ] **Step 5: Implement `snapshot/returns.go`**

```go
// (copyright header)

package snapshot

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/penny-vault/pv-api/openapi"
)

// TrailingReturns emits two rows — one portfolio, one benchmark — using
// the portfolio_value and benchmark_value series in perf_data.
func (r *Reader) TrailingReturns(ctx context.Context) ([]openapi.TrailingReturnRow, error) {
	portfolioRow, err := r.trailingRow(ctx, "portfolio_value", "Portfolio", openapi.ReturnRowKindPortfolio)
	if err != nil {
		return nil, err
	}
	benchRow, err := r.trailingRow(ctx, "benchmark_value", "Benchmark", openapi.ReturnRowKindBenchmark)
	if err != nil {
		return nil, err
	}
	return []openapi.TrailingReturnRow{portfolioRow, benchRow}, nil
}

func (r *Reader) trailingRow(ctx context.Context, metric, title string, kind openapi.ReturnRowKind) (openapi.TrailingReturnRow, error) {
	latestVal, err := r.latestPerf(ctx, metric)
	if err != nil {
		return openapi.TrailingReturnRow{}, err
	}

	ytdStart := time.Date(time.Now().Year(), 1, 1, 0, 0, 0, 0, time.UTC)

	ytdV, err := r.perfAsOf(ctx, metric, ytdStart, true)
	if err != nil {
		return openapi.TrailingReturnRow{}, err
	}
	oneYV, err := r.perfAsOf(ctx, metric, time.Now().AddDate(-1, 0, 0), false)
	if err != nil {
		return openapi.TrailingReturnRow{}, err
	}
	threeYV, err := r.perfAsOf(ctx, metric, time.Now().AddDate(-3, 0, 0), false)
	if err != nil {
		return openapi.TrailingReturnRow{}, err
	}
	fiveYV, err := r.perfAsOf(ctx, metric, time.Now().AddDate(-5, 0, 0), false)
	if err != nil {
		return openapi.TrailingReturnRow{}, err
	}
	tenYV, err := r.perfAsOf(ctx, metric, time.Now().AddDate(-10, 0, 0), false)
	if err != nil {
		return openapi.TrailingReturnRow{}, err
	}
	earliestV, err := r.earliestPerf(ctx, metric)
	if err != nil {
		return openapi.TrailingReturnRow{}, err
	}

	pct := func(baseline float64) float64 {
		if baseline <= 0 {
			return 0
		}
		return (latestVal - baseline) / baseline
	}

	return openapi.TrailingReturnRow{
		Title:          title,
		Kind:           kind,
		Ytd:            pct(ytdV),
		OneYear:        pct(oneYV),
		ThreeYear:      pct(threeYV),
		FiveYear:       pct(fiveYV),
		TenYear:        pct(tenYV),
		SinceInception: pct(earliestV),
	}, nil
}

func (r *Reader) latestPerf(ctx context.Context, metric string) (float64, error) {
	var v float64
	err := r.db.QueryRowContext(ctx,
		`SELECT value FROM perf_data WHERE metric=? ORDER BY date DESC LIMIT 1`, metric).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("latest %s: %w", metric, err)
	}
	return v, nil
}

func (r *Reader) earliestPerf(ctx context.Context, metric string) (float64, error) {
	var v float64
	err := r.db.QueryRowContext(ctx,
		`SELECT value FROM perf_data WHERE metric=? ORDER BY date ASC LIMIT 1`, metric).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("earliest %s: %w", metric, err)
	}
	return v, nil
}

func (r *Reader) perfAsOf(ctx context.Context, metric string, t time.Time, onOrAfter bool) (float64, error) {
	q := `SELECT value FROM perf_data WHERE metric=? AND date <= ? ORDER BY date DESC LIMIT 1`
	if onOrAfter {
		q = `SELECT value FROM perf_data WHERE metric=? AND date >= ? ORDER BY date ASC LIMIT 1`
	}
	var v float64
	err := r.db.QueryRowContext(ctx, q, metric, t.Format(dateLayout)).Scan(&v)
	if err == nil {
		return v, nil
	}
	if err == sql.ErrNoRows {
		// fall back to earliest value if cutoff predates the series
		return r.earliestPerf(ctx, metric)
	}
	return 0, err
}
```

- [ ] **Step 6: Write failing Performance test**

Create `snapshot/performance_test.go`:

```go
// (copyright header)

package snapshot_test

import (
	"context"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/snapshot"
)

var _ = Describe("Performance", func() {
	It("returns five daily points with both portfolioValue and benchmarkValue", func() {
		path := filepath.Join(GinkgoT().TempDir(), "f.sqlite")
		Expect(snapshot.BuildTestSnapshot(path)).To(Succeed())
		r, err := snapshot.Open(path)
		Expect(err).NotTo(HaveOccurred())
		defer r.Close()

		perf, err := r.Performance(context.Background(), "slug-123", nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(perf.PortfolioSlug).To(Equal("slug-123"))
		Expect(perf.Points).To(HaveLen(5))
		Expect(perf.Points[0].PortfolioValue).To(BeNumerically("~", 100000, 0.01))
		Expect(perf.Points[0].BenchmarkValue).To(BeNumerically("~", 100000, 0.01))
		Expect(perf.Points[4].PortfolioValue).To(BeNumerically("~", 103000, 0.01))
	})
})
```

- [ ] **Step 7: Implement `snapshot/performance.go`**

```go
// (copyright header)

package snapshot

import (
	"context"
	"fmt"
	"time"

	"github.com/oapi-codegen/runtime/types"

	"github.com/penny-vault/pv-api/openapi"
)

// Performance streams the portfolio + benchmark equity curves, optionally
// filtered by date range. Dates outside the stored series are clamped.
func (r *Reader) Performance(ctx context.Context, slug string, from, to *time.Time) (*openapi.PortfolioPerformance, error) {
	start, end, err := r.readDateWindow(ctx)
	if err != nil {
		return nil, err
	}
	if from == nil {
		from = &start
	}
	if to == nil {
		to = &end
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT date, metric, value FROM perf_data
		  WHERE metric IN ('portfolio_value','benchmark_value')
		    AND date >= ? AND date <= ?
		  ORDER BY date ASC`,
		from.Format(dateLayout), to.Format(dateLayout))
	if err != nil {
		return nil, fmt.Errorf("performance query: %w", err)
	}
	defer rows.Close()

	points := map[string]*openapi.PerformancePoint{}
	var order []string
	for rows.Next() {
		var ds, metric string
		var v float64
		if err := rows.Scan(&ds, &metric, &v); err != nil {
			return nil, fmt.Errorf("performance scan: %w", err)
		}
		p, ok := points[ds]
		if !ok {
			t, _ := time.Parse(dateLayout, ds)
			p = &openapi.PerformancePoint{Date: types.Date{Time: t}}
			points[ds] = p
			order = append(order, ds)
		}
		switch metric {
		case "portfolio_value":
			p.PortfolioValue = v
		case "benchmark_value":
			p.BenchmarkValue = v
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := openapi.PortfolioPerformance{
		PortfolioSlug: slug,
		From:          types.Date{Time: *from},
		To:            types.Date{Time: *to},
		Points:        make([]openapi.PerformancePoint, 0, len(order)),
	}
	for _, k := range order {
		out.Points = append(out.Points, *points[k])
	}
	return &out, nil
}
```

- [ ] **Step 8: Run the tests**

Run: `ginkgo -r snapshot`
Expected: all specs pass.

- [ ] **Step 9: Commit**

```bash
git add snapshot/drawdowns.go snapshot/drawdowns_test.go snapshot/returns.go snapshot/returns_test.go snapshot/performance.go snapshot/performance_test.go
git commit -m "add snapshot.Reader.Drawdowns + TrailingReturns + Performance"
```

---

## Task 11: `portfolio/runs.go` — RunStore + Run domain type

**Files:**
- Create: `portfolio/runs.go`
- Create: `portfolio/runs_test.go`
- Modify: `portfolio/store.go` (extend Store interface)

- [ ] **Step 1: Write `portfolio/runs.go`**

```go
// (copyright header)

package portfolio

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Run represents one row in the backtest_runs table.
type Run struct {
	ID          uuid.UUID
	PortfolioID uuid.UUID
	Status      string // queued | running | success | failed
	StartedAt   *time.Time
	FinishedAt  *time.Time
	DurationMs  *int32
	Error       *string
	SnapshotPath *string
}

// RunStore exposes the backtest_runs table. Ownership is enforced at
// the portfolio layer (we only expose runs the user owns via their
// portfolio).
type RunStore interface {
	CreateRun(ctx context.Context, portfolioID uuid.UUID, status string) (Run, error)
	UpdateRunRunning(ctx context.Context, runID uuid.UUID) error
	UpdateRunSuccess(ctx context.Context, runID uuid.UUID, snapshotPath string, durationMs int32) error
	UpdateRunFailed(ctx context.Context, runID uuid.UUID, errMsg string, durationMs int32) error
	ListRuns(ctx context.Context, portfolioID uuid.UUID) ([]Run, error)
	GetRun(ctx context.Context, portfolioID, runID uuid.UUID) (Run, error)
}

// PoolRunStore is the pgxpool-backed RunStore.
type PoolRunStore struct{ pool *pgxpool.Pool }

func NewPoolRunStore(pool *pgxpool.Pool) *PoolRunStore { return &PoolRunStore{pool: pool} }

func (s *PoolRunStore) CreateRun(ctx context.Context, portfolioID uuid.UUID, status string) (Run, error) {
	const q = `
		INSERT INTO backtest_runs (id, portfolio_id, status)
		VALUES (uuidv7(), $1, $2)
		RETURNING id, portfolio_id, status, started_at, finished_at, duration_ms, error, snapshot_path
	`
	return scanRun(s.pool.QueryRow(ctx, q, portfolioID, status))
}

func (s *PoolRunStore) UpdateRunRunning(ctx context.Context, runID uuid.UUID) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE backtest_runs SET status='running', started_at=NOW() WHERE id=$1`, runID)
	return err
}

func (s *PoolRunStore) UpdateRunSuccess(ctx context.Context, runID uuid.UUID, snapshotPath string, durationMs int32) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE backtest_runs SET status='success', finished_at=NOW(),
		                          snapshot_path=$2, duration_ms=$3
		  WHERE id=$1`, runID, snapshotPath, durationMs)
	return err
}

func (s *PoolRunStore) UpdateRunFailed(ctx context.Context, runID uuid.UUID, errMsg string, durationMs int32) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE backtest_runs SET status='failed', finished_at=NOW(),
		                          error=$2, duration_ms=$3
		  WHERE id=$1`, runID, errMsg, durationMs)
	return err
}

func (s *PoolRunStore) ListRuns(ctx context.Context, portfolioID uuid.UUID) ([]Run, error) {
	const q = `
		SELECT id, portfolio_id, status, started_at, finished_at, duration_ms, error, snapshot_path
		  FROM backtest_runs
		 WHERE portfolio_id=$1
		 ORDER BY COALESCE(started_at, '0001-01-01'::timestamptz) DESC
	`
	rows, err := s.pool.Query(ctx, q, portfolioID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *PoolRunStore) GetRun(ctx context.Context, portfolioID, runID uuid.UUID) (Run, error) {
	const q = `
		SELECT id, portfolio_id, status, started_at, finished_at, duration_ms, error, snapshot_path
		  FROM backtest_runs
		 WHERE id=$1 AND portfolio_id=$2
	`
	r, err := scanRun(s.pool.QueryRow(ctx, q, runID, portfolioID))
	if err != nil {
		return Run{}, ErrNotFound
	}
	return r, nil
}

// scanner is a tiny interface satisfied by pgx.Row and pgx.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanRun(s scanner) (Run, error) {
	var r Run
	err := s.Scan(&r.ID, &r.PortfolioID, &r.Status, &r.StartedAt, &r.FinishedAt, &r.DurationMs, &r.Error, &r.SnapshotPath)
	return r, err
}
```

- [ ] **Step 2: Extend `portfolio/store.go` Store interface**

Open `portfolio/store.go` and add `RunStore` methods onto the composite `Store` interface. Specifically, change the Store interface definition from:

```go
type Store interface {
    // ... existing portfolio methods
}
```

to include RunStore:

```go
type Store interface {
    RunStore
    // ... existing portfolio methods
}
```

And update `PoolStore` to embed a `*PoolRunStore`:

```go
type PoolStore struct {
    *pgxpool.Pool
    *PoolRunStore
}

func NewPoolStore(pool *pgxpool.Pool) *PoolStore {
    return &PoolStore{Pool: pool, PoolRunStore: NewPoolRunStore(pool)}
}
```

(Adjust to match the existing `PoolStore` layout precisely — look at `portfolio/store.go` before editing. The existing file pattern from Plan 4 has its own conventions.)

- [ ] **Step 3: Write a smoke test against the pvapi_smoke DB**

Create `portfolio/runs_test.go`:

```go
// (copyright header)

package portfolio_test

import (
	"context"
	"os"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/portfolio"
)

var _ = Describe("PoolRunStore", Ordered, func() {
	var (
		pool        *pgxpool.Pool
		store       *portfolio.PoolRunStore
		portfolioID uuid.UUID
	)

	BeforeAll(func() {
		dbURL := os.Getenv("PVAPI_SMOKE_DB_URL")
		if dbURL == "" {
			Skip("PVAPI_SMOKE_DB_URL not set; skipping run-store smoke test")
		}
		var err error
		pool, err = pgxpool.New(context.Background(), dbURL)
		Expect(err).NotTo(HaveOccurred())
		store = portfolio.NewPoolRunStore(pool)

		// Insert a throwaway portfolio row to foreign-key against.
		portfolioID = uuid.New()
		_, err = pool.Exec(context.Background(), `
			INSERT INTO portfolios (id, owner_sub, slug, name, strategy_code, strategy_ver, parameters, benchmark, mode, status)
			VALUES ($1, 'smoke|user', 'run-store-smoke-slug', 'smoke', 'x', 'v0.0.0', '{}'::jsonb, 'SPY', 'one_shot', 'pending')
		`, portfolioID)
		Expect(err).NotTo(HaveOccurred())

		DeferCleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM portfolios WHERE id=$1`, portfolioID)
			pool.Close()
		})
	})

	It("creates a queued run and advances it to success", func() {
		r, err := store.CreateRun(context.Background(), portfolioID, "queued")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Status).To(Equal("queued"))

		Expect(store.UpdateRunRunning(context.Background(), r.ID)).To(Succeed())
		Expect(store.UpdateRunSuccess(context.Background(), r.ID, "/tmp/x.sqlite", 1234)).To(Succeed())

		fetched, err := store.GetRun(context.Background(), portfolioID, r.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(fetched.Status).To(Equal("success"))
		Expect(*fetched.SnapshotPath).To(Equal("/tmp/x.sqlite"))
	})

	It("lists runs newest-first", func() {
		runs, err := store.ListRuns(context.Background(), portfolioID)
		Expect(err).NotTo(HaveOccurred())
		Expect(len(runs)).To(BeNumerically(">=", 1))
	})
})
```

- [ ] **Step 4: Run the test with the smoke DB**

Run:
```bash
PVAPI_SMOKE_DB_URL="pgx5://jdf@localhost:5432/pvapi_smoke" \
  ginkgo -r portfolio --focus "PoolRunStore"
```
Expected: specs pass.

Run without env:
```bash
ginkgo -r portfolio --focus "PoolRunStore"
```
Expected: specs SKIP with message about `PVAPI_SMOKE_DB_URL not set`.

- [ ] **Step 5: Commit**

```bash
git add portfolio/runs.go portfolio/runs_test.go portfolio/store.go
git commit -m "add portfolio.RunStore + PoolRunStore for backtest_runs"
```

---

## Task 12: `SnapshotOpener`/`SnapshotReader` interfaces in `portfolio`

**Files:**
- Create: `portfolio/snapshot_iface.go`

- [ ] **Step 1: Write the interface file**

Create `portfolio/snapshot_iface.go`:

```go
// (copyright header)

package portfolio

import (
	"context"
	"time"

	"github.com/penny-vault/pv-api/openapi"
)

// SnapshotReader is the subset of snapshot.Reader that portfolio handlers
// need. Redeclared here so handler tests can provide a fake without
// linking snapshot's modernc-sqlite dependency.
type SnapshotReader interface {
	Summary(ctx context.Context) (*openapi.PortfolioSummary, error)
	Drawdowns(ctx context.Context) ([]openapi.Drawdown, error)
	Statistics(ctx context.Context) ([]openapi.PortfolioStatistic, error)
	TrailingReturns(ctx context.Context) ([]openapi.TrailingReturnRow, error)
	CurrentHoldings(ctx context.Context) (*openapi.HoldingsResponse, error)
	HoldingsAsOf(ctx context.Context, date time.Time) (*openapi.HoldingsResponse, error)
	HoldingsHistory(ctx context.Context, from, to *time.Time) (*openapi.HoldingsHistoryResponse, error)
	Performance(ctx context.Context, slug string, from, to *time.Time) (*openapi.PortfolioPerformance, error)
	Transactions(ctx context.Context, filter SnapshotTxFilter) (*openapi.TransactionsResponse, error)
	Close() error
}

// SnapshotTxFilter mirrors snapshot.TransactionFilter for the portfolio
// layer (avoids importing snapshot from portfolio).
type SnapshotTxFilter struct {
	From  *time.Time
	To    *time.Time
	Types []string
}

// SnapshotOpener opens a SnapshotReader for a given snapshot file path.
// Production wires snapshot.Opener; tests wire a fake.
type SnapshotOpener interface {
	Open(path string) (SnapshotReader, error)
}
```

- [ ] **Step 2: Write a snapshot-package adapter**

Append to `snapshot/reader.go`:

```go
// Opener adapts Reader to the portfolio.SnapshotOpener interface.
type Opener struct{}

// Open satisfies portfolio.SnapshotOpener.
func (Opener) Open(path string) (*Reader, error) { return Open(path) }
```

Actually the return type mismatch matters — portfolio expects `(portfolio.SnapshotReader, error)`. Add an adapter file:

Create `snapshot/opener.go`:

```go
// (copyright header)

package snapshot

import (
	"context"
	"time"

	"github.com/penny-vault/pv-api/openapi"
	"github.com/penny-vault/pv-api/portfolio"
)

// readerAdapter wraps *Reader to present the portfolio.SnapshotReader
// interface (translating SnapshotTxFilter to TransactionFilter).
type readerAdapter struct{ *Reader }

func (a readerAdapter) Transactions(ctx context.Context, f portfolio.SnapshotTxFilter) (*openapi.TransactionsResponse, error) {
	return a.Reader.Transactions(ctx, TransactionFilter(f))
}

// Opener satisfies portfolio.SnapshotOpener.
type Opener struct{}

// Open opens a snapshot file and returns a portfolio.SnapshotReader.
func (Opener) Open(path string) (portfolio.SnapshotReader, error) {
	r, err := Open(path)
	if err != nil {
		return nil, err
	}
	return readerAdapter{Reader: r}, nil
}
```

Note the type-alias trick: `TransactionFilter(f)` works only if both structs have identical field sets, which they do (both `From`, `To`, `Types`). If that's not quite true in practice, convert field-by-field.

- [ ] **Step 3: Compile**

Run: `go build ./...`
Expected: success.

- [ ] **Step 4: Commit**

```bash
git add portfolio/snapshot_iface.go snapshot/opener.go
git commit -m "add portfolio.SnapshotOpener/SnapshotReader interfaces + snapshot.Opener adapter"
```

---

## Task 13: derived-data handlers + `/runs` / `/runs/{runId}` endpoints

**Files:**
- Modify: `portfolio/handler.go`
- Modify: `portfolio/handler_test.go`
- Modify: `api/portfolios.go`

- [ ] **Step 0: Extend the `Portfolio` domain struct with `SnapshotPath`**

Open `portfolio/types.go` and add to the `Portfolio` struct:

```go
SnapshotPath *string
```

Open `portfolio/db.go` and extend every `SELECT` list and every `Scan` call against `portfolios` to include the new column. Specifically:

- Every `SELECT … FROM portfolios …` string literal gets `, snapshot_path` appended to the column list.
- Every `Scan(...)` call gets `, &p.SnapshotPath` appended.

Run: `go build ./...`
Expected: success.

- [ ] **Step 1: Extend `portfolio.Handler` constructor**

Open `portfolio/handler.go`. Change the Handler struct and constructor:

```go
type Handler struct {
	store      Store
	strategies strategy.ReadStore
	opener     SnapshotOpener
	dispatcher Dispatcher  // added in Task 14; declare interface here now
}

// Dispatcher is the subset of backtest.Dispatcher the handler needs.
type Dispatcher interface {
	Submit(ctx context.Context, portfolioID uuid.UUID) (runID uuid.UUID, err error)
}

func NewHandler(store Store, strategies strategy.ReadStore, opener SnapshotOpener, dispatcher Dispatcher) *Handler {
	return &Handler{store: store, strategies: strategies, opener: opener, dispatcher: dispatcher}
}
```

(Previous constructor signature was `NewHandler(store, strategies)`. All call sites will fail to compile — we fix them in Task 15.)

- [ ] **Step 2: Add the Summary handler**

Append to `portfolio/handler.go`:

```go
// GET /portfolios/{slug}/summary
func (h *Handler) Summary(c fiber.Ctx) error {
	return h.readSnapshot(c, func(r SnapshotReader) (any, error) {
		return r.Summary(c.Context())
	})
}

// readSnapshot is the shared skeleton for all derived-data endpoints:
// resolve owner + slug, require status=ready + snapshot path, open reader,
// delegate.
func (h *Handler) readSnapshot(c fiber.Ctx, fn func(SnapshotReader) (any, error)) error {
	sub, err := subject(c)
	if err != nil {
		return api.WriteProblem(c, err)
	}
	slug := string([]byte(c.Params("slug"))) // copy off fiber buffer
	p, err := h.store.Get(c.Context(), sub, slug)
	if errors.Is(err, ErrNotFound) {
		return api.WriteProblem(c, api.ErrNotFound)
	}
	if err != nil {
		return api.WriteProblem(c, err)
	}
	if p.Status != StatusReady || p.SnapshotPath == nil || *p.SnapshotPath == "" {
		return api.WriteProblemWithDetail(c, api.ErrNotFound, "no successful run")
	}

	reader, err := h.opener.Open(*p.SnapshotPath)
	if err != nil {
		return api.WriteProblem(c, err)
	}
	defer reader.Close()

	out, err := fn(reader)
	if err != nil {
		return api.WriteProblem(c, err)
	}
	return c.JSON(out)
}
```

Add `api.WriteProblemWithDetail` if it doesn't already exist — open `api/errors.go` and add:

```go
// WriteProblemWithDetail writes a problem+json response overriding the
// detail field with the provided text. Title/status come from err.
func WriteProblemWithDetail(c fiber.Ctx, err error, detail string) error {
	// mirror WriteProblem's switch to pick status + title, then replace the
	// response's "detail" field with the override. See existing
	// WriteProblem implementation for the exact layout.
	// (If not already defined, copy WriteProblem and override the detail
	// before encoding.)
	return writeProblemImpl(c, err, detail)
}
```

Where `writeProblemImpl` is the internal helper. If `WriteProblem` is implemented inline, refactor it to call `writeProblemImpl(c, err, "")` so the override path works. Keep the change surgical.

- [ ] **Step 3: Add the remaining 7 derived-data handlers**

Append to `portfolio/handler.go`:

```go
// GET /portfolios/{slug}/drawdowns
func (h *Handler) Drawdowns(c fiber.Ctx) error {
	return h.readSnapshot(c, func(r SnapshotReader) (any, error) {
		return r.Drawdowns(c.Context())
	})
}

// GET /portfolios/{slug}/statistics
func (h *Handler) Statistics(c fiber.Ctx) error {
	return h.readSnapshot(c, func(r SnapshotReader) (any, error) {
		return r.Statistics(c.Context())
	})
}

// GET /portfolios/{slug}/trailing-returns
func (h *Handler) TrailingReturns(c fiber.Ctx) error {
	return h.readSnapshot(c, func(r SnapshotReader) (any, error) {
		return r.TrailingReturns(c.Context())
	})
}

// GET /portfolios/{slug}/holdings
func (h *Handler) Holdings(c fiber.Ctx) error {
	return h.readSnapshot(c, func(r SnapshotReader) (any, error) {
		return r.CurrentHoldings(c.Context())
	})
}

// GET /portfolios/{slug}/holdings/{date}
func (h *Handler) HoldingsAsOf(c fiber.Ctx) error {
	dateStr := string([]byte(c.Params("date")))
	d, perr := time.Parse("2006-01-02", dateStr)
	if perr != nil {
		return api.WriteProblem(c, fmt.Errorf("%w: date must be YYYY-MM-DD", api.ErrInvalidParams))
	}
	return h.readSnapshot(c, func(r SnapshotReader) (any, error) {
		resp, err := r.HoldingsAsOf(c.Context(), d)
		if errors.Is(err, snapshotNotFoundErr()) {
			return nil, api.ErrNotFound
		}
		return resp, err
	})
}

// snapshotNotFoundErr is a small indirection so this file needn't import
// snapshot directly — we want to preserve the package-boundary
// (portfolio doesn't import snapshot). Put it in a tiny file that can
// do the import.
func snapshotNotFoundErr() error { return snapshotPkgNotFound }
```

Create a tiny file that pulls in the snapshot.ErrNotFound sentinel (avoiding a module-wide import of snapshot into handler.go just for one error):

Create `portfolio/snapshot_errors.go`:

```go
// (copyright header)

package portfolio

import "github.com/penny-vault/pv-api/snapshot"

// snapshotPkgNotFound aliases snapshot.ErrNotFound so handlers can
// `errors.Is` against it without importing snapshot at the top of
// handler.go.
var snapshotPkgNotFound = snapshot.ErrNotFound
```

(Yes, this does pull snapshot into the portfolio package. The interface-first decoupling above was test-driven: handlers use the `SnapshotReader` interface, but this one sentinel is a small hardcoded dep. Acceptable.)

Continue in `portfolio/handler.go`:

```go
// GET /portfolios/{slug}/holdings/history
func (h *Handler) HoldingsHistory(c fiber.Ctx) error {
	var from, to *time.Time
	if s := string([]byte(c.Query("from"))); s != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return api.WriteProblem(c, fmt.Errorf("%w: from must be YYYY-MM-DD", api.ErrInvalidParams))
		}
		from = &t
	}
	if s := string([]byte(c.Query("to"))); s != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return api.WriteProblem(c, fmt.Errorf("%w: to must be YYYY-MM-DD", api.ErrInvalidParams))
		}
		to = &t
	}
	return h.readSnapshot(c, func(r SnapshotReader) (any, error) {
		return r.HoldingsHistory(c.Context(), from, to)
	})
}

// GET /portfolios/{slug}/performance
func (h *Handler) Performance(c fiber.Ctx) error {
	var from, to *time.Time
	if s := string([]byte(c.Query("from"))); s != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return api.WriteProblem(c, fmt.Errorf("%w: from must be YYYY-MM-DD", api.ErrInvalidParams))
		}
		from = &t
	}
	if s := string([]byte(c.Query("to"))); s != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return api.WriteProblem(c, fmt.Errorf("%w: to must be YYYY-MM-DD", api.ErrInvalidParams))
		}
		to = &t
	}
	slug := string([]byte(c.Params("slug")))
	return h.readSnapshot(c, func(r SnapshotReader) (any, error) {
		return r.Performance(c.Context(), slug, from, to)
	})
}

// GET /portfolios/{slug}/transactions
func (h *Handler) Transactions(c fiber.Ctx) error {
	var filter SnapshotTxFilter
	if s := string([]byte(c.Query("from"))); s != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return api.WriteProblem(c, fmt.Errorf("%w: from must be YYYY-MM-DD", api.ErrInvalidParams))
		}
		filter.From = &t
	}
	if s := string([]byte(c.Query("to"))); s != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return api.WriteProblem(c, fmt.Errorf("%w: to must be YYYY-MM-DD", api.ErrInvalidParams))
		}
		filter.To = &t
	}
	if s := string([]byte(c.Query("type"))); s != "" {
		filter.Types = strings.Split(s, ",")
	}
	return h.readSnapshot(c, func(r SnapshotReader) (any, error) {
		return r.Transactions(c.Context(), filter)
	})
}

// GET /portfolios/{slug}/runs
func (h *Handler) ListRuns(c fiber.Ctx) error {
	sub, err := subject(c)
	if err != nil {
		return api.WriteProblem(c, err)
	}
	slug := string([]byte(c.Params("slug")))
	p, err := h.store.Get(c.Context(), sub, slug)
	if errors.Is(err, ErrNotFound) {
		return api.WriteProblem(c, api.ErrNotFound)
	}
	if err != nil {
		return api.WriteProblem(c, err)
	}
	runs, err := h.store.ListRuns(c.Context(), p.ID)
	if err != nil {
		return api.WriteProblem(c, err)
	}
	out := make([]openapi.BacktestRun, 0, len(runs))
	for _, r := range runs {
		out = append(out, toAPIRun(r, slug))
	}
	return c.JSON(out)
}

// GET /portfolios/{slug}/runs/{runId}
func (h *Handler) GetRun(c fiber.Ctx) error {
	sub, err := subject(c)
	if err != nil {
		return api.WriteProblem(c, err)
	}
	slug := string([]byte(c.Params("slug")))
	p, err := h.store.Get(c.Context(), sub, slug)
	if errors.Is(err, ErrNotFound) {
		return api.WriteProblem(c, api.ErrNotFound)
	}
	if err != nil {
		return api.WriteProblem(c, err)
	}
	runIDStr := string([]byte(c.Params("runId")))
	runID, perr := uuid.Parse(runIDStr)
	if perr != nil {
		return api.WriteProblem(c, fmt.Errorf("%w: runId must be a uuid", api.ErrInvalidParams))
	}
	r, err := h.store.GetRun(c.Context(), p.ID, runID)
	if errors.Is(err, ErrNotFound) {
		return api.WriteProblem(c, api.ErrNotFound)
	}
	if err != nil {
		return api.WriteProblem(c, err)
	}
	return c.JSON(toAPIRun(r, slug))
}

// toAPIRun converts a domain Run to the OpenAPI shape.
func toAPIRun(r Run, slug string) openapi.BacktestRun {
	id, _ := uuid.Parse(r.ID.String())
	out := openapi.BacktestRun{
		Id:            id,
		PortfolioSlug: slug,
		Status:        openapi.RunStatus(r.Status),
	}
	if r.StartedAt != nil {
		out.StartedAt = r.StartedAt
	}
	if r.FinishedAt != nil {
		out.FinishedAt = r.FinishedAt
	}
	if r.DurationMs != nil {
		out.DurationMs = r.DurationMs
	}
	if r.Error != nil {
		out.Error = r.Error
	}
	return out
}
```

- [ ] **Step 4: Add tests for derived-data handlers**

Append to `portfolio/handler_test.go` (use the existing suite setup / fakes):

```go
// (added to existing handler_test.go)

// FakeSnapshotOpener / FakeSnapshotReader: minimal fakes that let us
// verify wiring without touching disk.
type fakeSnapshotOpener struct {
	readers map[string]portfolio.SnapshotReader
	err     error
}

func (f *fakeSnapshotOpener) Open(path string) (portfolio.SnapshotReader, error) {
	if f.err != nil {
		return nil, f.err
	}
	r, ok := f.readers[path]
	if !ok {
		return nil, errors.New("fake opener: unknown path " + path)
	}
	return r, nil
}

type fakeSnapshotReader struct {
	summary *openapi.PortfolioSummary
	// add fields on demand for additional tests
}

func (f *fakeSnapshotReader) Close() error { return nil }
func (f *fakeSnapshotReader) Summary(ctx context.Context) (*openapi.PortfolioSummary, error) {
	return f.summary, nil
}
func (f *fakeSnapshotReader) Drawdowns(ctx context.Context) ([]openapi.Drawdown, error)  { return nil, nil }
func (f *fakeSnapshotReader) Statistics(ctx context.Context) ([]openapi.PortfolioStatistic, error) { return nil, nil }
func (f *fakeSnapshotReader) TrailingReturns(ctx context.Context) ([]openapi.TrailingReturnRow, error) { return nil, nil }
func (f *fakeSnapshotReader) CurrentHoldings(ctx context.Context) (*openapi.HoldingsResponse, error) { return nil, nil }
func (f *fakeSnapshotReader) HoldingsAsOf(ctx context.Context, d time.Time) (*openapi.HoldingsResponse, error) { return nil, nil }
func (f *fakeSnapshotReader) HoldingsHistory(ctx context.Context, from, to *time.Time) (*openapi.HoldingsHistoryResponse, error) { return nil, nil }
func (f *fakeSnapshotReader) Performance(ctx context.Context, slug string, from, to *time.Time) (*openapi.PortfolioPerformance, error) { return nil, nil }
func (f *fakeSnapshotReader) Transactions(ctx context.Context, fi portfolio.SnapshotTxFilter) (*openapi.TransactionsResponse, error) { return nil, nil }

// Derived-data spec: Summary happy path + not-ready path.
//
// Reuses the existing `newHandlerHarness` / `fakeStore` / `fakeStrategyStore`
// setup pattern from handler_test.go. If those helpers are named
// differently in your tree, rename the calls — the shape of the spec
// is what matters.
var _ = Describe("Handler.Summary", func() {
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
		h := portfolio.NewHandler(store, &fakeStrategyStore{}, opener, nil /* dispatcher not used by Summary */)
		app.Get("/portfolios/:slug/summary", h.Summary)
	})

	It("returns 404 with 'no successful run' when status=pending", func() {
		pending := "path-not-set"
		store.rows = []portfolio.Portfolio{{
			ID: uuid.Must(uuid.NewV7()), OwnerSub: sub, Slug: "s1",
			Status: portfolio.StatusPending, SnapshotPath: &pending,
		}}
		store.rows[0].SnapshotPath = nil // simulate no snapshot yet

		req := httptest.NewRequest("GET", "/portfolios/s1/summary", nil)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusNotFound))

		body, _ := io.ReadAll(resp.Body)
		Expect(string(body)).To(ContainSubstring("no successful run"))
	})

	It("returns 200 with the summary payload when the snapshot opens", func() {
		path := "/fake/snap.sqlite"
		store.rows = []portfolio.Portfolio{{
			ID: uuid.Must(uuid.NewV7()), OwnerSub: sub, Slug: "s1",
			Status: portfolio.StatusReady, SnapshotPath: &path,
		}}
		wantSummary := &openapi.PortfolioSummary{
			CurrentValue:       103000,
			YtdReturn:          0.03,
			OneYearReturn:      0.05,
			CagrSinceInception: 0.12,
			MaxDrawDown:        -0.05,
			Sharpe:             1.23,
			Sortino:            1.80,
			Beta:               0.95,
			Alpha:              0.02,
			StdDev:             0.11,
			TaxCostRatio:       0.01,
		}
		opener.readers[path] = &fakeSnapshotReader{summary: wantSummary}

		req := httptest.NewRequest("GET", "/portfolios/s1/summary", nil)
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusOK))

		body, _ := io.ReadAll(resp.Body)
		var got openapi.PortfolioSummary
		Expect(sonic.Unmarshal(body, &got)).To(Succeed())
		Expect(got.CurrentValue).To(Equal(wantSummary.CurrentValue))
		Expect(got.Sharpe).To(Equal(wantSummary.Sharpe))
	})
})
```

**Critical:** the two `Skip`s above are reminders for the engineer. Replace each one with a real spec that follows the same pattern the Plan 4 handler_test.go uses for `Get`. Read `portfolio/handler_test.go` in the current tree and copy the fake-Store bootstrap verbatim; pass the fakeSnapshotOpener + a no-op Dispatcher (nil is fine for Summary tests) to the Handler constructor. When in doubt, ask.

- [ ] **Step 5: Fix compile errors in `api/portfolios.go`**

Open `api/portfolios.go`. **Delete the `api.PortfolioHandler` wrapper type and its `NewPortfolioHandler` constructor entirely** — they add an indirection over `portfolio.Handler` without carrying any additional responsibility. `RegisterPortfolioRoutesWith` instead takes `*portfolio.Handler` directly. Then update the single caller in `api/server.go` (around line 94) from `NewPortfolioHandler(portfolioStore, strategyStore)` to `portfolio.NewHandler(portfolioStore, strategyStore, cfg.SnapshotOpener, cfg.Dispatcher)`.

```go
// RegisterPortfolioRoutesWith mounts real portfolio handlers on r.
// Callers construct portfolio.Handler directly via portfolio.NewHandler.
func RegisterPortfolioRoutesWith(r fiber.Router, h *portfolio.Handler) {
	r.Get("/portfolios", h.List)
	r.Post("/portfolios", h.Create)
	r.Get("/portfolios/:slug", h.Get)
	r.Patch("/portfolios/:slug", h.Patch)
	r.Delete("/portfolios/:slug", h.Delete)
	r.Get("/portfolios/:slug/summary", h.Summary)
	r.Get("/portfolios/:slug/drawdowns", h.Drawdowns)
	r.Get("/portfolios/:slug/statistics", h.Statistics)
	r.Get("/portfolios/:slug/trailing-returns", h.TrailingReturns)
	r.Get("/portfolios/:slug/holdings", h.Holdings)
	r.Get("/portfolios/:slug/holdings/history", h.HoldingsHistory) // must precede :date
	r.Get("/portfolios/:slug/holdings/:date", h.HoldingsAsOf)
	r.Get("/portfolios/:slug/performance", h.Performance)
	r.Get("/portfolios/:slug/transactions", h.Transactions)
	r.Post("/portfolios/:slug/runs", h.CreateRun) // implemented in Task 14
	r.Get("/portfolios/:slug/runs", h.ListRuns)
	r.Get("/portfolios/:slug/runs/:runId", h.GetRun)
}
```

- [ ] **Step 6: Build**

Run: `go build ./...`
Expected: fails until Task 14 lands `CreateRun` on the handler. Comment out the `POST /runs` line temporarily (or stub `CreateRun` to return 501) so subsequent tests can compile:

```go
// temporary — filled in in Task 14
func (h *Handler) CreateRun(c fiber.Ctx) error {
	return api.WriteProblem(c, api.ErrNotImplemented)
}
```

Run: `go build ./...`
Expected: success.

- [ ] **Step 7: Run existing tests**

Run: `ginkgo -r`
Expected: all pre-existing specs pass. Any derived-endpoint specs you left `Skip`'d show up as `SKIP` rows — that's OK, they become real in the subagent's implementation pass.

- [ ] **Step 8: Commit**

```bash
git add portfolio/handler.go portfolio/handler_test.go portfolio/snapshot_errors.go api/portfolios.go api/errors.go
git commit -m "wire derived-data handlers + /runs list/get into portfolio.Handler"
```

---

## Task 14: `backtest/dispatcher.go` bounded worker pool

**Files:**
- Create: `backtest/dispatcher.go`
- Create: `backtest/dispatcher_test.go`

- [ ] **Step 1: Write the failing test**

Create `backtest/dispatcher_test.go`:

```go
// (copyright header)

package backtest_test

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/backtest"
)

// fakeRunner counts active/peak concurrency.
type fakeRunner struct {
	active int64
	peak   int64
	block  chan struct{}
}

func (f *fakeRunner) Run(ctx context.Context, req backtest.RunRequest) error {
	n := atomic.AddInt64(&f.active, 1)
	for {
		p := atomic.LoadInt64(&f.peak)
		if n > p {
			if atomic.CompareAndSwapInt64(&f.peak, p, n) {
				break
			}
			continue
		}
		break
	}
	defer atomic.AddInt64(&f.active, -1)
	select {
	case <-f.block:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// fakeRunStore records calls.
type fakeRunStore struct {
	mu       sync.Mutex
	created  []uuid.UUID
	running  map[uuid.UUID]bool
	finished map[uuid.UUID]string
}

func newFakeRunStore() *fakeRunStore {
	return &fakeRunStore{running: map[uuid.UUID]bool{}, finished: map[uuid.UUID]string{}}
}
func (f *fakeRunStore) CreateRun(ctx context.Context, pid uuid.UUID, status string) (backtest.RunRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	runID := uuid.New()
	f.created = append(f.created, runID)
	return backtest.RunRow{ID: runID, PortfolioID: pid, Status: status}, nil
}
// other methods omitted for brevity — test uses CreateRun only

var _ = Describe("Dispatcher", func() {
	It("caps concurrency at MaxConcurrency", func() {
		runner := &fakeRunner{block: make(chan struct{})}
		rs := newFakeRunStore()
		d := backtest.NewDispatcher(backtest.Config{
			SnapshotsDir: "/tmp", RunnerMode: "host", MaxConcurrency: 2,
		}, runner, rs, nil)
		d.Start(context.Background())
		DeferCleanup(func() { d.Shutdown(5 * time.Second) })

		for i := 0; i < 10; i++ {
			_, err := d.Submit(context.Background(), uuid.New())
			Expect(err).NotTo(HaveOccurred())
		}
		// let workers pick up 2 jobs, then release all
		Eventually(func() int64 { return atomic.LoadInt64(&runner.peak) }).Should(Equal(int64(2)))
		close(runner.block)
	})

	It("returns ErrQueueFull when the buffer is saturated", func() {
		runner := &fakeRunner{block: make(chan struct{})}
		defer close(runner.block)
		rs := newFakeRunStore()
		d := backtest.NewDispatcher(backtest.Config{
			SnapshotsDir: "/tmp", RunnerMode: "host", MaxConcurrency: 1,
		}, runner, rs, nil)
		d.Start(context.Background())
		DeferCleanup(func() { d.Shutdown(5 * time.Second) })

		// Fill the queue: 1 in-flight + 4 buffered == 5; 6th should fail.
		for i := 0; i < 5; i++ {
			_, _ = d.Submit(context.Background(), uuid.New())
		}
		_, err := d.Submit(context.Background(), uuid.New())
		Expect(err).To(MatchError(backtest.ErrQueueFull))
	})
})
```

- [ ] **Step 2: Implement `backtest/dispatcher.go`**

```go
// (copyright header)

package backtest

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// RunStore is the subset of portfolio.RunStore the dispatcher needs. Kept
// here (not imported from portfolio) to avoid a cycle backtest->portfolio.
type RunStore interface {
	CreateRun(ctx context.Context, portfolioID uuid.UUID, status string) (RunRow, error)
}

// RunRow mirrors portfolio.Run but lives here to avoid the import cycle.
type RunRow struct {
	ID          uuid.UUID
	PortfolioID uuid.UUID
	Status      string
}

// task is a queued backtest job.
type task struct {
	portfolioID uuid.UUID
	runID       uuid.UUID
}

// Dispatcher is a bounded worker-pool that funnels task submissions to
// backtest.Run invocations.
type Dispatcher struct {
	cfg     Config
	runner  Runner
	runs    RunStore
	runFn   func(ctx context.Context, portfolioID, runID uuid.UUID) error
	tasks   chan task
	wg      sync.WaitGroup
	ctx     context.Context
	cancel  context.CancelFunc
	started bool
}

// NewDispatcher builds a dispatcher. runFn is the orchestration callback;
// production passes backtest.Run (introduced in Task 15). Tests may pass
// a stub to exercise Submit / Shutdown semantics directly.
func NewDispatcher(cfg Config, runner Runner, runs RunStore, runFn func(ctx context.Context, portfolioID, runID uuid.UUID) error) *Dispatcher {
	cfg.ApplyDefaults()
	return &Dispatcher{
		cfg:    cfg,
		runner: runner,
		runs:   runs,
		runFn:  runFn,
		tasks:  make(chan task, cfg.MaxConcurrency*4),
	}
}

// Start launches worker goroutines.
func (d *Dispatcher) Start(parent context.Context) {
	if d.started {
		return
	}
	d.started = true
	d.ctx, d.cancel = context.WithCancel(parent)
	for i := 0; i < d.cfg.MaxConcurrency; i++ {
		d.wg.Add(1)
		go d.worker()
	}
	log.Info().Int("workers", d.cfg.MaxConcurrency).Msg("backtest dispatcher started")
}

// Submit enqueues a task for the given portfolio and returns the new run id.
func (d *Dispatcher) Submit(ctx context.Context, portfolioID uuid.UUID) (uuid.UUID, error) {
	run, err := d.runs.CreateRun(ctx, portfolioID, "queued")
	if err != nil {
		return uuid.Nil, err
	}
	select {
	case d.tasks <- task{portfolioID: portfolioID, runID: run.ID}:
		return run.ID, nil
	default:
		return uuid.Nil, ErrQueueFull
	}
}

// Shutdown stops accepting new tasks, waits up to grace for in-flight work
// to finish, then cancels worker contexts.
func (d *Dispatcher) Shutdown(grace time.Duration) error {
	if !d.started {
		return nil
	}
	close(d.tasks)
	done := make(chan struct{})
	go func() { d.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(grace):
	}
	d.cancel()
	return nil
}

func (d *Dispatcher) worker() {
	defer d.wg.Done()
	for t := range d.tasks {
		if d.runFn != nil {
			if err := d.runFn(d.ctx, t.portfolioID, t.runID); err != nil {
				log.Error().Err(err).Stringer("run_id", t.runID).Msg("backtest run failed")
			}
		} else {
			// no-op path used only by tests that pass nil runFn; the
			// runner is invoked directly with a bogus RunRequest just to
			// exercise concurrency counting.
			_ = d.runner.Run(d.ctx, RunRequest{})
		}
	}
}
```

- [ ] **Step 3: Run the tests**

Run: `ginkgo -r backtest`
Expected: both dispatcher specs pass.

- [ ] **Step 4: Commit**

```bash
git add backtest/dispatcher.go backtest/dispatcher_test.go
git commit -m "add backtest.Dispatcher with bounded worker pool"
```

---

## Task 15: `backtest/run.go` orchestration + POST /runs handler + auto-trigger

**Files:**
- Create: `backtest/run.go`
- Create: `backtest/run_test.go`
- Modify: `portfolio/handler.go` (replace the `CreateRun` stub)
- Modify: `portfolio/handler.go` (auto-trigger inside Create)

- [ ] **Step 1: Extend the RunStore interface in `backtest/dispatcher.go`**

Add methods needed by `Run` (they live in portfolio.RunStore but we mirror them in backtest.RunStore):

```go
type RunStore interface {
	CreateRun(ctx context.Context, portfolioID uuid.UUID, status string) (RunRow, error)
	UpdateRunRunning(ctx context.Context, runID uuid.UUID) error
	UpdateRunSuccess(ctx context.Context, runID uuid.UUID, snapshotPath string, durationMs int32) error
	UpdateRunFailed(ctx context.Context, runID uuid.UUID, errMsg string, durationMs int32) error
}
```

Also add a `PortfolioStore` interface for the backtest package:

```go
type PortfolioStore interface {
	GetByID(ctx context.Context, portfolioID uuid.UUID) (PortfolioRow, error)
	SetRunning(ctx context.Context, portfolioID uuid.UUID) error
	SetReady(ctx context.Context, portfolioID uuid.UUID, snapshotPath string, kpis SetKpis) error
	SetFailed(ctx context.Context, portfolioID uuid.UUID, errMsg string) error
}

type PortfolioRow struct {
	ID           uuid.UUID
	StrategyCode string
	StrategyVer  string
	Parameters   map[string]any
	Benchmark    string
	Status       string
	SnapshotPath *string
}

type SetKpis struct {
	CurrentValue       float64
	YtdReturn          float64
	MaxDrawdown        float64
	Sharpe             float64
	Cagr               float64
	InceptionDate      time.Time
}
```

Corresponding methods go onto `portfolio.PoolStore` (and the corresponding portfolio.PortfolioStore interface declared in portfolio package). These adapters are thin — the pool-backed versions are mirror-queries against the portfolios table. Add them in `portfolio/db.go` alongside the existing CRUD queries.

Specifically:

- `portfolio.PoolStore.GetByID(ctx, id)` → `SELECT * FROM portfolios WHERE id=$1` (no owner scoping — internal call).
- `portfolio.PoolStore.SetRunning(ctx, id)` → `UPDATE portfolios SET status='running', updated_at=NOW() WHERE id=$1`.
- `portfolio.PoolStore.SetReady(ctx, id, snapshotPath, kpis)` → the big UPDATE from the design spec, writing status, last_run_at, last_error=NULL, snapshot_path, current_value, ytd_return, max_drawdown, sharpe, cagr_since_inception, inception_date (COALESCE with existing).
- `portfolio.PoolStore.SetFailed(ctx, id, errMsg)` → `UPDATE portfolios SET status='failed', last_error=$2, updated_at=NOW() WHERE id=$1`.

- [ ] **Step 2: Write a failing Run orchestration test**

Create `backtest/run_test.go`:

```go
// (copyright header)

package backtest_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/backtest"
	"github.com/penny-vault/pv-api/snapshot"
)

// fakePortfolioStore + fakeRunStoreFull implement the interfaces backtest.Run
// uses; both capture their calls so we can assert the orchestration did what
// we expect.
type fakePortfolioStore struct {
	row         backtest.PortfolioRow
	setRunning  bool
	setReady    bool
	setFailed   string
	lastKpis    backtest.SetKpis
	snapshotOut string
}

func (f *fakePortfolioStore) GetByID(ctx context.Context, id uuid.UUID) (backtest.PortfolioRow, error) {
	return f.row, nil
}
func (f *fakePortfolioStore) SetRunning(ctx context.Context, id uuid.UUID) error {
	f.setRunning = true
	return nil
}
func (f *fakePortfolioStore) SetReady(ctx context.Context, id uuid.UUID, path string, k backtest.SetKpis) error {
	f.setReady = true
	f.snapshotOut = path
	f.lastKpis = k
	return nil
}
func (f *fakePortfolioStore) SetFailed(ctx context.Context, id uuid.UUID, errMsg string) error {
	f.setFailed = errMsg
	return nil
}

type fakeRunStoreFull struct {
	fakeRunStore
	updatedRunning  bool
	updatedSuccess  string
	updatedFailed   string
}

func (f *fakeRunStoreFull) UpdateRunRunning(ctx context.Context, id uuid.UUID) error { f.updatedRunning = true; return nil }
func (f *fakeRunStoreFull) UpdateRunSuccess(ctx context.Context, id uuid.UUID, path string, ms int32) error { f.updatedSuccess = path; return nil }
func (f *fakeRunStoreFull) UpdateRunFailed(ctx context.Context, id uuid.UUID, msg string, ms int32) error   { f.updatedFailed = msg; return nil }

var _ = Describe("Run orchestration", func() {
	It("writes a fresh snapshot, renames it, and updates the portfolio row", func() {
		snapsDir := GinkgoT().TempDir()

		// prime fakestrat to produce a fixture
		fixture := filepath.Join(GinkgoT().TempDir(), "fx.sqlite")
		Expect(snapshot.BuildTestSnapshot(fixture)).To(Succeed())
		Expect(os.Setenv("FAKESTRAT_FIXTURE", fixture)).To(Succeed())
		DeferCleanup(func() { os.Unsetenv("FAKESTRAT_FIXTURE") })

		ps := &fakePortfolioStore{row: backtest.PortfolioRow{
			ID: uuid.New(), StrategyCode: "fake", StrategyVer: "v0.0.0",
			Parameters: map[string]any{}, Benchmark: "SPY", Status: "queued",
		}}
		rs := &fakeRunStoreFull{}

		r := backtest.NewRunner(backtest.Config{SnapshotsDir: snapsDir, RunnerMode: "host"},
			&backtest.HostRunner{}, ps, rs,
			// strategy binary resolver — just returns fakeStratBin
			func(code, ver string) (string, error) { return fakeStratBin, nil })

		err := r.Run(context.Background(), ps.row.ID, uuid.New())
		Expect(err).NotTo(HaveOccurred())

		Expect(ps.setRunning).To(BeTrue())
		Expect(ps.setReady).To(BeTrue())
		Expect(ps.setFailed).To(BeEmpty())
		Expect(rs.updatedRunning).To(BeTrue())
		Expect(rs.updatedSuccess).NotTo(BeEmpty())
		Expect(ps.lastKpis.CurrentValue).To(BeNumerically("~", 103000, 0.01))

		// Final snapshot exists; tmp does not.
		Expect(ps.snapshotOut).To(Equal(filepath.Join(snapsDir, ps.row.ID.String()+".sqlite")))
		_, stErr := os.Stat(ps.snapshotOut)
		Expect(stErr).NotTo(HaveOccurred())
		_, stErr = os.Stat(ps.snapshotOut + ".tmp")
		Expect(os.IsNotExist(stErr)).To(BeTrue())
	})

	It("records a failure when the runner fails", func() {
		snapsDir := GinkgoT().TempDir()
		Expect(os.Setenv("FAKESTRAT_BEHAVIOR", "fail")).To(Succeed())
		DeferCleanup(func() { os.Unsetenv("FAKESTRAT_BEHAVIOR") })

		ps := &fakePortfolioStore{row: backtest.PortfolioRow{
			ID: uuid.New(), StrategyCode: "fake", StrategyVer: "v0.0.0",
			Parameters: map[string]any{}, Benchmark: "SPY", Status: "queued",
		}}
		rs := &fakeRunStoreFull{}

		r := backtest.NewRunner(backtest.Config{SnapshotsDir: snapsDir, RunnerMode: "host", Timeout: 5 * time.Second},
			&backtest.HostRunner{}, ps, rs,
			func(code, ver string) (string, error) { return fakeStratBin, nil })

		err := r.Run(context.Background(), ps.row.ID, uuid.New())
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, backtest.ErrRunnerFailed)).To(BeTrue())
		Expect(ps.setFailed).NotTo(BeEmpty())
		Expect(rs.updatedFailed).NotTo(BeEmpty())
	})
})
```

- [ ] **Step 3: Implement `backtest/run.go`**

```go
// (copyright header)

package backtest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/penny-vault/pv-api/snapshot"
)

// BinaryResolver returns the absolute path to an installed strategy
// binary for (code, ver).
type BinaryResolver func(code, ver string) (string, error)

// orchestrator owns Config + all stores + the runner + the resolver;
// its Run method is the canonical entry point.
type orchestrator struct {
	cfg      Config
	runner   Runner
	ps       PortfolioStore
	rs       RunStore
	resolve  BinaryResolver
}

// NewRunner builds the orchestration object. (Not named "NewRun" to avoid
// method/function name clash with the .Run method.)
func NewRunner(cfg Config, runner Runner, ps PortfolioStore, rs RunStore, resolve BinaryResolver) *orchestrator {
	cfg.ApplyDefaults()
	return &orchestrator{cfg: cfg, runner: runner, ps: ps, rs: rs, resolve: resolve}
}

// Run orchestrates a single backtest for the given portfolio + run id.
func (o *orchestrator) Run(ctx context.Context, portfolioID, runID uuid.UUID) error {
	started := time.Now()

	row, err := o.ps.GetByID(ctx, portfolioID)
	if err != nil {
		return o.fail(ctx, portfolioID, runID, started, fmt.Errorf("load portfolio: %w", err))
	}
	if row.Status == "running" {
		_ = o.rs.UpdateRunFailed(ctx, runID, "portfolio already running",
			int32(time.Since(started).Milliseconds()))
		return ErrAlreadyRunning
	}
	if err := o.ps.SetRunning(ctx, portfolioID); err != nil {
		return o.fail(ctx, portfolioID, runID, started, fmt.Errorf("set running: %w", err))
	}
	if err := o.rs.UpdateRunRunning(ctx, runID); err != nil {
		return o.fail(ctx, portfolioID, runID, started, fmt.Errorf("update run running: %w", err))
	}

	binary, err := o.resolve(row.StrategyCode, row.StrategyVer)
	if err != nil {
		return o.fail(ctx, portfolioID, runID, started, fmt.Errorf("%w: %v", ErrStrategyNotInstalled, err))
	}

	tmp := filepath.Join(o.cfg.SnapshotsDir, portfolioID.String()+".sqlite.tmp")
	final := filepath.Join(o.cfg.SnapshotsDir, portfolioID.String()+".sqlite")
	_ = os.Remove(tmp)

	if err := o.runner.Run(ctx, RunRequest{
		Binary: binary, Args: BuildArgs(row.Parameters, row.Benchmark),
		OutPath: tmp, Timeout: o.cfg.Timeout,
	}); err != nil {
		return o.fail(ctx, portfolioID, runID, started, err)
	}

	// fsync + atomic rename
	f, err := os.Open(tmp)
	if err != nil {
		return o.fail(ctx, portfolioID, runID, started, fmt.Errorf("open tmp: %w", err))
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return o.fail(ctx, portfolioID, runID, started, fmt.Errorf("fsync tmp: %w", err))
	}
	f.Close()

	if err := os.Rename(tmp, final); err != nil {
		return o.fail(ctx, portfolioID, runID, started, fmt.Errorf("rename: %w", err))
	}

	// read KPIs from the freshly-renamed file
	reader, err := snapshot.Open(final)
	if err != nil {
		return o.fail(ctx, portfolioID, runID, started, fmt.Errorf("open snapshot: %w", err))
	}
	kp, err := reader.Kpis(ctx)
	reader.Close()
	if err != nil {
		return o.fail(ctx, portfolioID, runID, started, fmt.Errorf("read kpis: %w", err))
	}

	setKpis := SetKpis{
		CurrentValue:  kp.CurrentValue,
		YtdReturn:     kp.YtdReturn,
		MaxDrawdown:   kp.MaxDrawdown,
		Sharpe:        kp.Sharpe,
		Cagr:          kp.Cagr,
		InceptionDate: kp.InceptionDate,
	}
	if err := o.ps.SetReady(ctx, portfolioID, final, setKpis); err != nil {
		return o.fail(ctx, portfolioID, runID, started, fmt.Errorf("set ready: %w", err))
	}
	if err := o.rs.UpdateRunSuccess(ctx, runID, final,
		int32(time.Since(started).Milliseconds())); err != nil {
		return fmt.Errorf("update run success: %w", err)
	}
	log.Info().Stringer("portfolio_id", portfolioID).Stringer("run_id", runID).Msg("backtest succeeded")
	return nil
}

func (o *orchestrator) fail(ctx context.Context, portfolioID, runID uuid.UUID, started time.Time, err error) error {
	msg := err.Error()
	if len(msg) > 2048 {
		msg = msg[:2048]
	}
	_ = o.ps.SetFailed(ctx, portfolioID, msg)
	_ = o.rs.UpdateRunFailed(ctx, runID, msg, int32(time.Since(started).Milliseconds()))
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %s", ErrTimedOut, msg)
	}
	return err
}
```

- [ ] **Step 4: Implement `CreateRun` handler in portfolio**

Replace the temporary `CreateRun` stub in `portfolio/handler.go`:

```go
// POST /portfolios/{slug}/runs
func (h *Handler) CreateRun(c fiber.Ctx) error {
	sub, err := subject(c)
	if err != nil {
		return api.WriteProblem(c, err)
	}
	slug := string([]byte(c.Params("slug")))
	p, err := h.store.Get(c.Context(), sub, slug)
	if errors.Is(err, ErrNotFound) {
		return api.WriteProblem(c, api.ErrNotFound)
	}
	if err != nil {
		return api.WriteProblem(c, err)
	}
	if p.Status == StatusRunning {
		return api.WriteProblem(c, api.ErrConflict)
	}
	runID, err := h.dispatcher.Submit(c.Context(), p.ID)
	if err != nil {
		return api.WriteProblem(c, err)
	}
	id, _ := uuid.Parse(runID.String())
	return c.Status(fiber.StatusAccepted).JSON(openapi.BacktestRun{
		Id:            id,
		PortfolioSlug: slug,
		Status:        openapi.RunStatusQueued,
	})
}
```

- [ ] **Step 5: Wire auto-trigger into the Create handler**

In `portfolio/handler.go`, locate the existing `Create` handler. After the INSERT succeeds but before `c.JSON`, add:

```go
// Auto-trigger first run per mode.
switch created.Mode {
case ModeOneShot:
	if _, subErr := h.dispatcher.Submit(c.Context(), created.ID); subErr != nil {
		log.Warn().Err(subErr).Msg("backtest dispatch failed at create time")
	}
case ModeContinuous:
	if req.RunNow != nil && *req.RunNow {
		if _, subErr := h.dispatcher.Submit(c.Context(), created.ID); subErr != nil {
			log.Warn().Err(subErr).Msg("backtest dispatch failed at create time")
		}
	}
}
```

(Adjust constant names — `ModeOneShot`, `ModeContinuous`, `StatusRunning` — to whatever the Plan 4 code actually exports. Look at `portfolio/types.go` before editing.)

- [ ] **Step 6: Run the tests**

Run: `ginkgo -r backtest`
Expected: all Run specs pass (success + failure paths).

Run: `ginkgo -r`
Expected: all specs either pass or skip.

- [ ] **Step 7: Commit**

```bash
git add backtest/run.go backtest/run_test.go portfolio/handler.go backtest/dispatcher.go portfolio/db.go
git commit -m "add backtest.Run orchestration + POST /runs handler + one_shot auto-trigger"
```

---

## Task 16: startup sweep + runner.mode validation

**Files:**
- Create: `backtest/sweep.go`
- Create: `backtest/sweep_test.go`

- [ ] **Step 1: Write the failing test**

Create `backtest/sweep_test.go`:

```go
// (copyright header)

package backtest_test

import (
	"context"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/backtest"
)

var _ = Describe("StartupSweep", func() {
	It("removes .tmp files older than 1h", func() {
		dir := GinkgoT().TempDir()
		old := filepath.Join(dir, "abc.sqlite.tmp")
		Expect(os.WriteFile(old, []byte("x"), 0o644)).To(Succeed())
		past := time.Now().Add(-2 * time.Hour)
		Expect(os.Chtimes(old, past, past)).To(Succeed())

		recent := filepath.Join(dir, "def.sqlite.tmp")
		Expect(os.WriteFile(recent, []byte("x"), 0o644)).To(Succeed())

		Expect(backtest.StartupSweep(context.Background(), dir, nil)).To(Succeed())
		_, oErr := os.Stat(old)
		Expect(os.IsNotExist(oErr)).To(BeTrue())
		_, rErr := os.Stat(recent)
		Expect(rErr).NotTo(HaveOccurred())
	})
})
```

- [ ] **Step 2: Implement `backtest/sweep.go`**

```go
// (copyright header)

package backtest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// PortfolioSweeper is the callback used by StartupSweep to mark any
// 'running' portfolios as 'failed' when the server restarts mid-run.
// Passed from cmd/server.go; nil for tests that only want .tmp cleanup.
type PortfolioSweeper interface {
	MarkAllRunningAsFailed(ctx context.Context, reason string) (int, error)
}

// StartupSweep removes stale .tmp files and flips any stuck-running
// portfolios to 'failed'. Logged at info.
func StartupSweep(ctx context.Context, snapshotsDir string, ps PortfolioSweeper) error {
	cutoff := time.Now().Add(-1 * time.Hour)
	entries, err := os.ReadDir(snapshotsDir)
	if err != nil {
		return err
	}
	removed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sqlite.tmp") {
			continue
		}
		full := filepath.Join(snapshotsDir, e.Name())
		info, ierr := os.Stat(full)
		if ierr != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(full); err == nil {
				removed++
			}
		}
	}
	log.Info().Int("stale_tmp_removed", removed).Msg("snapshots sweep")

	if ps != nil {
		n, err := ps.MarkAllRunningAsFailed(ctx, "server restarted mid-run")
		if err != nil {
			return err
		}
		log.Info().Int("running_to_failed", n).Msg("portfolios sweep")
	}
	return nil
}
```

- [ ] **Step 3: Add `MarkAllRunningAsFailed` to portfolio.PoolStore**

In `portfolio/db.go`, add:

```go
// MarkAllRunningAsFailed flips every portfolio whose status is 'running'
// to 'failed' and sets last_error. Called by backtest.StartupSweep.
func (s *PoolStore) MarkAllRunningAsFailed(ctx context.Context, reason string) (int, error) {
	tag, err := s.Pool.Exec(ctx,
		`UPDATE portfolios SET status='failed', last_error=$1, updated_at=NOW()
		  WHERE status='running'`, reason)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
```

- [ ] **Step 4: Run the tests**

Run: `ginkgo -r backtest`
Expected: sweep spec passes.

- [ ] **Step 5: Commit**

```bash
git add backtest/sweep.go backtest/sweep_test.go portfolio/db.go
git commit -m "add backtest.StartupSweep: stale .tmp cleanup + running->failed flip"
```

---

## Task 17: wire backtest + snapshot into `api.NewApp` and `cmd/server.go`

**Files:**
- Modify: `api/server.go`
- Modify: `cmd/server.go` (or wherever the server subcommand lives — check existing layout)

- [ ] **Step 1: Extend `api.Config` with a Dispatcher + SnapshotOpener**

Open `api/server.go`. Add fields to `api.Config`:

```go
type Config struct {
	// ... existing fields
	Dispatcher     portfolio.Dispatcher
	SnapshotOpener portfolio.SnapshotOpener
	RunStore       portfolio.RunStore
}
```

Inside `NewApp`, when `cfg.Pool != nil`, build the handler directly and register:

```go
portfolioHandler := portfolio.NewHandler(store, strategyStore, cfg.SnapshotOpener, cfg.Dispatcher)
RegisterPortfolioRoutesWith(app, portfolioHandler)
```

Where `store` now incorporates `cfg.RunStore` (or adjust `NewPoolStore` to build both — your call, follow Plan 4's existing pattern). Note the descriptive variable name `portfolioHandler`; avoid generic names like `h` or `inner`.

- [ ] **Step 2: Build backtest.Config + Dispatcher in `cmd/server.go`**

Open `cmd/server.go` (the server subcommand). After the pgxpool is built and before `api.NewApp`:

```go
btCfg := backtest.Config{
	SnapshotsDir:   viper.GetString("backtest.snapshots_dir"),
	MaxConcurrency: viper.GetInt("backtest.max_concurrency"),
	Timeout:        viper.GetDuration("backtest.timeout"),
	RunnerMode:     viper.GetString("runner.mode"),
}
btCfg.ApplyDefaults()
if err := btCfg.Validate(); err != nil {
	log.Fatal().Err(err).Msg("backtest config")
}
if err := os.MkdirAll(btCfg.SnapshotsDir, 0o750); err != nil {
	log.Fatal().Err(err).Msg("mkdir snapshots_dir")
}

runner := &backtest.HostRunner{}
runStore := portfolio.NewPoolRunStore(pool)
portfolioStore := portfolio.NewPoolStore(pool)   // assumes plan-4's ctor

resolve := func(code, ver string) (string, error) {
	// consult the strategy.Store for the installed binary path; plan 3
	// added an InstallDir column — the binary is at <InstallDir>/<code>
	s, err := strategyStore.GetByCode(ctx, code)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.InstallDir, code), nil
}

orch := backtest.NewRunner(btCfg, runner, portfolioStore, runStore, resolve)
dispatcher := backtest.NewDispatcher(btCfg, runner, runStore, orch.Run)
dispatcher.Start(ctx)

// run the startup sweep (fire-and-forget logging any failure)
if err := backtest.StartupSweep(ctx, btCfg.SnapshotsDir, portfolioStore); err != nil {
	log.Warn().Err(err).Msg("startup sweep")
}

apiCfg := api.Config{
	// ... existing
	Pool:            pool,
	Dispatcher:      dispatcher,
	SnapshotOpener:  snapshot.Opener{},
	RunStore:        runStore,
}

// graceful shutdown hook
go func() {
	<-ctx.Done()
	_ = dispatcher.Shutdown(30 * time.Second)
}()
```

Drop any TOML defaults the existing config already sets; this is just the new wiring.

- [ ] **Step 3: Add pvapi.toml defaults**

If `pvapi.toml` (or the viper config registration) has a defaults section, add:

```toml
[backtest]
snapshots_dir   = "/var/lib/pvapi/snapshots"
max_concurrency = 0
timeout         = "15m"

[runner]
mode = "host"
```

Check where other defaults are registered (likely `cmd/root.go`) and add `SetDefault` calls in matching style.

- [ ] **Step 4: Build + run all tests**

Run: `go build ./...`
Expected: success.

Run: `ginkgo -r`
Expected: all specs pass.

- [ ] **Step 5: Commit**

```bash
git add api/server.go cmd/server.go cmd/root.go pvapi.toml
git commit -m "wire backtest.Dispatcher + snapshot.Opener into server startup"
```

(Adjust the file list to what you actually changed — `pvapi.toml` / `cmd/root.go` exist only if the existing config pattern uses them.)

---

## Task 18: end-to-end smoke

**Files:** none new.

- [ ] **Step 1: Build the binary**

Run: `make build`
Expected: exits 0; produces `./pvapi`.

- [ ] **Step 2: Run the full ginkgo suite with race + coverage**

Run: `ginkgo run -race -cover ./...`
Expected: 100% pass. Any `Skip` rows should be the explicitly-skipped smoke tests that require `PVAPI_SMOKE_DB_URL`.

- [ ] **Step 3: Run lint**

Run: `make lint`
Expected: exits 0 (no new lint findings).

- [ ] **Step 4: Smoke-run the server against pvapi_smoke**

In one terminal:
```bash
PVAPI_DB_URL="pgx5://jdf@localhost:5432/pvapi_smoke" \
PVAPI_BACKTEST_SNAPSHOTS_DIR="$(pwd)/.smoke-snapshots" \
./pvapi server
```

In another terminal, without an Auth0 token (expect 401):
```bash
curl -si http://localhost:3000/portfolios | head -5
```
Expected: `HTTP/1.1 401 Unauthorized`.

With a forged Bearer token (the test harness in Plan 2 documents how to mint one), exercise the derived endpoints once a portfolio is created. Confirm:
- `POST /portfolios` with a one_shot body returns 201 and dispatches.
- The dispatcher log line fires; `GET /portfolios/{slug}/runs` shows a running-then-success row.
- `GET /portfolios/{slug}/summary` returns 200 once status=ready.

(This step is manual validation only — if any of it is painful, the subagent should stop and ask.)

- [ ] **Step 5: Tidy + commit (if anything changed)**

Run: `go mod tidy`
If it produced changes:

```bash
git add go.mod go.sum
git commit -m "go mod tidy after plan 5"
```

---

## Plan summary

Eighteen tasks cover the cross-cutting plumbing (contract, migration, packages, dispatcher, orchestration, wiring, sweep, smoke) and the eleven derived-data endpoints. Task 1 is the contract rename + new paths that must land first. All tasks use Ginkgo/Gomega per the repo convention, and only Task 11 and Task 18 Step 4 touch a live database — guarded behind `PVAPI_SMOKE_DB_URL` so hermetic runs (`ginkgo -r`) stay fast and self-contained. `/holdings/history` uses a local batches-schema fixture so pvapi implementation does not block on the in-flight pvbt release that adds that schema to the snapshot.
