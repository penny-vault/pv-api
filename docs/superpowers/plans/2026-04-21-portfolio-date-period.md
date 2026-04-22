# Portfolio Date Period Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add optional `startDate`/`endDate` to portfolios; pass them as `--start`/`--end` to pvbt; remove `mode`, `schedule`, `runNow`; simplify scheduler to daily cadence; rename `POST /portfolios/{slug}/runs` to `/run` (singular).

**Architecture:** Dates live on the `portfolios` table. The backtest orchestrator reads them from `PortfolioRow` and appends `--start`/`--end` to pvbt's CLI args via `BuildArgs`. The scheduler drops tradecron; it now claims any portfolio whose `end_date IS NULL` and hasn't run today. All portfolio creation triggers an immediate run — no `runNow` flag.

**Tech Stack:** Go, PostgreSQL (pgx v5), Fiber v3, Ginkgo/Gomega tests.

**Spec:** `docs/superpowers/specs/2026-04-21-portfolio-date-period.md`

---

## File Map

| File | Change |
|------|--------|
| `sql/migrations/9_date_period.up.sql` | New — drops mode/schedule/next_run_at, adds start_date/end_date |
| `sql/migrations/9_date_period.down.sql` | New — reverse |
| `backtest/args.go` | `BuildArgs` gains `startDate, endDate *time.Time` |
| `backtest/args_test.go` | Tests for --start/--end |
| `backtest/dispatcher.go` | `PortfolioRow` gains `StartDate, EndDate *time.Time` |
| `backtest/run.go` | Pass dates from `PortfolioRow` to `BuildArgs` |
| `portfolio/types.go` | Remove `Mode`/`Schedule`/`NextRunAt`; add `StartDate`/`EndDate` |
| `portfolio/validate.go` | Remove mode/schedule validation; add date validation |
| `portfolio/validate_test.go` | Rewrite to cover date validation |
| `portfolio/db.go` | Update columns, scan, Insert; replace `ClaimDueContinuous` with `ClaimDue` |
| `portfolio/store.go` | Update `Store` interface |
| `portfolio/handler.go` | Update createBody, view, buildPortfolio, PATCH, autoTrigger |
| `portfolio/handler_test.go` | Remove mode/schedule from fixtures; replace continuous suite |
| `api/portfolios.go` | `POST /runs` → `POST /run` |
| `scheduler/scheduler.go` | Remove tradecron types; simplify to `ClaimDue` |
| `scheduler/scheduler_test.go` | Rewrite stubs and tests |
| `cmd/server.go` | Update adapters; remove tradecron import |

---

## Task 1: Migration 9

**Files:**
- Create: `sql/migrations/9_date_period.up.sql`
- Create: `sql/migrations/9_date_period.down.sql`

- [ ] **Step 1: Write the up migration**

```sql
-- sql/migrations/9_date_period.up.sql

-- Drop index that references mode before altering the column.
DROP INDEX IF EXISTS idx_portfolios_due;

-- Remove mode, schedule, next_run_at columns.
ALTER TABLE portfolios
    DROP COLUMN mode,
    DROP COLUMN schedule,
    DROP COLUMN next_run_at;

-- Drop the now-unused enum type.
DROP TYPE IF EXISTS portfolio_mode;

-- Add date-period columns.
ALTER TABLE portfolios
    ADD COLUMN start_date DATE,
    ADD COLUMN end_date   DATE;

-- New scheduler index: open-ended portfolios not yet run today.
CREATE INDEX idx_portfolios_due ON portfolios (last_run_at NULLS FIRST)
    WHERE end_date IS NULL AND status IN ('ready', 'failed');
```

- [ ] **Step 2: Write the down migration**

```sql
-- sql/migrations/9_date_period.down.sql

DROP INDEX IF EXISTS idx_portfolios_due;

ALTER TABLE portfolios
    DROP COLUMN IF EXISTS start_date,
    DROP COLUMN IF EXISTS end_date;

CREATE TYPE portfolio_mode AS ENUM ('one_shot', 'continuous');

ALTER TABLE portfolios
    ADD COLUMN mode        portfolio_mode NOT NULL DEFAULT 'one_shot',
    ADD COLUMN schedule    TEXT,
    ADD COLUMN next_run_at TIMESTAMPTZ;

CREATE INDEX idx_portfolios_due ON portfolios (next_run_at)
    WHERE mode = 'continuous' AND status IN ('ready', 'failed');
```

- [ ] **Step 3: Commit**

```bash
git add sql/migrations/9_date_period.up.sql sql/migrations/9_date_period.down.sql
git commit -m "migration: add start_date/end_date; drop mode/schedule/next_run_at"
```

---

## Task 2: backtest/args.go — add --start/--end

**Files:**
- Modify: `backtest/args.go`
- Modify: `backtest/args_test.go`

- [ ] **Step 1: Write failing tests**

In `backtest/args_test.go`, add inside the existing `Describe("BuildArgs", ...)` block:

```go
It("appends --start and --end when both are provided", func() {
    start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
    end := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)
    args := backtest.BuildArgs(map[string]any{}, "", &start, &end)
    Expect(args).To(ContainElements("--start", "2020-01-01", "--end", "2024-12-31"))
})

It("omits --start and --end when nil", func() {
    args := backtest.BuildArgs(map[string]any{}, "", nil, nil)
    Expect(args).NotTo(ContainElement("--start"))
    Expect(args).NotTo(ContainElement("--end"))
})

It("appends --start before --benchmark", func() {
    start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
    args := backtest.BuildArgs(map[string]any{}, "SPY", &start, nil)
    startIdx := -1
    benchIdx := -1
    for i, a := range args {
        if a == "--start" { startIdx = i }
        if a == "--benchmark" { benchIdx = i }
    }
    Expect(startIdx).To(BeNumerically("<", benchIdx))
})
```

Also update ALL existing test calls to `BuildArgs` to include the two new nil parameters:
- `backtest.BuildArgs(params, "SPY")` → `backtest.BuildArgs(params, "SPY", nil, nil)`
- `backtest.BuildArgs(params, "")` → `backtest.BuildArgs(params, "", nil, nil)`
- `backtest.BuildArgs(map[string]any{}, "")` → `backtest.BuildArgs(map[string]any{}, "", nil, nil)`

Add `"time"` to the test file imports.

- [ ] **Step 2: Run tests to confirm they fail**

```bash
cd /Users/jdf/Developer/penny-vault/pv-api && go test ./backtest/... 2>&1 | head -20
```

Expected: compile error — `BuildArgs` has wrong number of arguments.

- [ ] **Step 3: Update BuildArgs implementation**

Replace `backtest/args.go` content:

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

package backtest

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// BuildArgs converts a portfolio's parameter map, benchmark, and optional date
// window into the strategy-binary CLI flags. Returns a flat []string suitable
// for appending to the "backtest --output <path>" base command.
//
// Order: parameter keys sorted ascending; --start (if set); --end (if set);
// --benchmark last (if non-empty).
func BuildArgs(params map[string]any, benchmark string, startDate, endDate *time.Time) []string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]string, 0, 2*len(keys)+6)
	for _, k := range keys {
		out = append(out, "--"+toKebab(k), stringify(params[k]))
	}
	if startDate != nil {
		out = append(out, "--start", startDate.Format("2006-01-02"))
	}
	if endDate != nil {
		out = append(out, "--end", endDate.Format("2006-01-02"))
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

- [ ] **Step 4: Run tests to confirm they pass**

```bash
cd /Users/jdf/Developer/penny-vault/pv-api && go test ./backtest/... 2>&1 | tail -5
```

Expected: `ok  	github.com/penny-vault/pv-api/backtest`

- [ ] **Step 5: Commit**

```bash
git add backtest/args.go backtest/args_test.go
git commit -m "backtest: BuildArgs accepts optional --start/--end date args"
```

---

## Task 3: backtest/dispatcher.go and run.go — thread dates

**Files:**
- Modify: `backtest/dispatcher.go`
- Modify: `backtest/run.go`

- [ ] **Step 1: Add StartDate/EndDate to PortfolioRow**

In `backtest/dispatcher.go`, update `PortfolioRow`:

```go
// PortfolioRow carries the fields the orchestrator reads from a portfolio.
type PortfolioRow struct {
	ID               uuid.UUID
	StrategyCode     string
	StrategyVer      string
	StrategyCloneURL string
	Parameters       map[string]any
	Benchmark        string
	Status           string
	SnapshotPath     *string
	StartDate        *time.Time
	EndDate          *time.Time
}
```

`time` is already imported in `backtest/dispatcher.go`.

- [ ] **Step 2: Pass dates to BuildArgs in run.go**

In `backtest/run.go`, find the `o.runner.Run(ctx, RunRequest{...})` call (around line 96) and update the `Args` field:

```go
Args: BuildArgs(row.Parameters, row.Benchmark, row.StartDate, row.EndDate),
```

- [ ] **Step 3: Verify it compiles**

```bash
cd /Users/jdf/Developer/penny-vault/pv-api && go build ./backtest/... 2>&1
```

Expected: no output (clean build).

- [ ] **Step 4: Commit**

```bash
git add backtest/dispatcher.go backtest/run.go
git commit -m "backtest: thread StartDate/EndDate from PortfolioRow into BuildArgs"
```

---

## Task 4: portfolio package — types, validate, db, store, handler

This task touches all `portfolio/` files. Steps are sequential; compile only at the end. Each step modifies one file.

**Files:**
- Modify: `portfolio/types.go`
- Modify: `portfolio/validate.go`
- Modify: `portfolio/validate_test.go`
- Modify: `portfolio/db.go`
- Modify: `portfolio/store.go`
- Modify: `portfolio/handler.go`
- Modify: `portfolio/handler_test.go`

### Step 1: Replace portfolio/types.go

- [ ] **Write new types.go**

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

package portfolio

import (
	"time"

	"github.com/google/uuid"
)

// Status mirrors the portfolio_status enum.
type Status string

const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusReady   Status = "ready"
	StatusFailed  Status = "failed"
)

// Portfolio is the internal representation of a portfolios row.
type Portfolio struct {
	ID                   uuid.UUID
	OwnerSub             string
	Slug                 string
	Name                 string
	StrategyCode         string
	StrategyVer          *string
	StrategyCloneURL     string
	StrategyDescribeJSON []byte
	Parameters           map[string]any
	PresetName           *string
	Benchmark            string
	StartDate            *time.Time
	EndDate              *time.Time
	Status               Status
	LastRunAt            *time.Time
	LastError            *string
	SnapshotPath         *string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// CreateRequest is what the POST /portfolios handler passes to the domain layer.
type CreateRequest struct {
	Name             string
	StrategyCode     string
	StrategyVer      string
	StrategyCloneURL string
	Parameters       map[string]any
	Benchmark        string
	StartDate        *time.Time
	EndDate          *time.Time
}

// UpdateRequest is what PATCH /portfolios/{slug} passes to the domain layer.
type UpdateRequest struct {
	Name      string
	StartDate *time.Time
	EndDate   *time.Time
}
```

### Step 2: Replace portfolio/validate.go

- [ ] **Write new validate.go**

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

package portfolio

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/penny-vault/pv-api/strategy"
)

var (
	ErrStrategyNotReady        = errors.New("strategy not installed")
	ErrStrategyVersionMismatch = errors.New("strategy version not installed")
	ErrUnknownParameter        = errors.New("unknown parameter")
	ErrMissingParameter        = errors.New("missing required parameter")
	ErrInvalidStrategyDescribe = errors.New("strategy describe JSON is malformed")
	ErrInvalidDate             = errors.New("invalid date")
	ErrEndBeforeStart          = errors.New("endDate must be on or after startDate")
)

// ValidateCreate validates and normalises an official-strategy create request.
func ValidateCreate(req CreateRequest, s strategy.Strategy) (CreateRequest, error) {
	norm := req

	if s.InstalledVer == nil || len(s.DescribeJSON) == 0 {
		return norm, fmt.Errorf("%w: %s is still installing — try again shortly", ErrStrategyNotReady, s.ShortCode)
	}
	if norm.StrategyVer != "" && norm.StrategyVer != *s.InstalledVer {
		return norm, fmt.Errorf("%w: want %s, installed is %s",
			ErrStrategyVersionMismatch, norm.StrategyVer, *s.InstalledVer)
	}
	norm.StrategyVer = *s.InstalledVer

	var d strategy.Describe
	if err := json.Unmarshal(s.DescribeJSON, &d); err != nil {
		return norm, fmt.Errorf("%w: %w", ErrInvalidStrategyDescribe, err)
	}
	if err := validateParameters(norm.Parameters, d); err != nil {
		return norm, err
	}
	if norm.Benchmark == "" {
		norm.Benchmark = d.Benchmark
	}
	if err := validateDates(norm.StartDate, norm.EndDate); err != nil {
		return norm, err
	}
	return norm, nil
}

// ValidateCreateUnofficial validates an unofficial (clone-URL) strategy create
// request. Skips the install-lifecycle checks.
func ValidateCreateUnofficial(req CreateRequest, d strategy.Describe) (CreateRequest, error) {
	norm := req
	if err := validateParameters(norm.Parameters, d); err != nil {
		return norm, err
	}
	if norm.Benchmark == "" {
		norm.Benchmark = d.Benchmark
	}
	if err := validateDates(norm.StartDate, norm.EndDate); err != nil {
		return norm, err
	}
	return norm, nil
}

// validateDates checks that endDate is not before startDate.
func validateDates(start, end *time.Time) error {
	if start != nil && end != nil && end.Before(*start) {
		return fmt.Errorf("%w", ErrEndBeforeStart)
	}
	return nil
}

// validateParameters enforces that every declared parameter is present and no
// unknown parameters are supplied.
func validateParameters(params map[string]any, d strategy.Describe) error {
	declared := make(map[string]struct{}, len(d.Parameters))
	for _, p := range d.Parameters {
		declared[p.Name] = struct{}{}
	}
	for k := range params {
		if _, ok := declared[k]; !ok {
			return fmt.Errorf("%w: %s", ErrUnknownParameter, k)
		}
	}
	for _, p := range d.Parameters {
		if _, present := params[p.Name]; !present {
			return fmt.Errorf("%w: %s", ErrMissingParameter, p.Name)
		}
	}
	return nil
}
```

### Step 3: Replace portfolio/validate_test.go

- [ ] **Write new validate_test.go**

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

package portfolio_test

import (
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/portfolio"
	"github.com/penny-vault/pv-api/strategy"
)

var _ = Describe("ValidateCreate", func() {
	installed := "v1.0.0"
	admDescribe := []byte(`{"shortCode":"adm","name":"ADM","parameters":[{"name":"riskOn","type":"universe"}],"schedule":"@monthend","benchmark":"SPY"}`)

	makeStrategy := func() strategy.Strategy {
		return strategy.Strategy{
			ShortCode:    "adm",
			RepoOwner:    "penny-vault",
			RepoName:     "adm",
			CloneURL:     "https://github.com/penny-vault/adm.git",
			IsOfficial:   true,
			InstalledVer: &installed,
			DescribeJSON: admDescribe,
		}
	}

	It("accepts a valid request and normalises StrategyVer and Benchmark", func() {
		req := portfolio.CreateRequest{
			Name:         "foo",
			StrategyCode: "adm",
			Parameters:   map[string]any{"riskOn": "SPY"},
		}
		norm, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(err).NotTo(HaveOccurred())
		Expect(norm.StrategyVer).To(Equal("v1.0.0"))
		Expect(norm.Benchmark).To(Equal("SPY"))
	})

	It("defaults benchmark to strategy describe benchmark when blank", func() {
		req := portfolio.CreateRequest{
			Name: "foo", StrategyCode: "adm",
			Parameters: map[string]any{"riskOn": "SPY"},
		}
		norm, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(err).NotTo(HaveOccurred())
		Expect(norm.Benchmark).To(Equal("SPY"))
	})

	It("rejects a strategy version that does not match installed_ver", func() {
		req := portfolio.CreateRequest{
			Name: "foo", StrategyCode: "adm",
			StrategyVer: "v9.9.9",
			Parameters:  map[string]any{"riskOn": "SPY"},
		}
		_, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(errors.Is(err, portfolio.ErrStrategyVersionMismatch)).To(BeTrue())
	})

	It("rejects when the strategy is not installed", func() {
		s := makeStrategy()
		s.InstalledVer = nil
		s.DescribeJSON = nil
		req := portfolio.CreateRequest{
			Name: "foo", StrategyCode: "adm",
			Parameters: map[string]any{"riskOn": "SPY"},
		}
		_, err := portfolio.ValidateCreate(req, s)
		Expect(errors.Is(err, portfolio.ErrStrategyNotReady)).To(BeTrue())
	})

	It("rejects unknown parameter keys", func() {
		req := portfolio.CreateRequest{
			Name: "foo", StrategyCode: "adm",
			Parameters: map[string]any{"riskOn": "SPY", "bogus": 42},
		}
		_, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(errors.Is(err, portfolio.ErrUnknownParameter)).To(BeTrue())
		Expect(err.Error()).To(ContainSubstring("bogus"))
	})

	It("rejects missing required parameters", func() {
		req := portfolio.CreateRequest{
			Name: "foo", StrategyCode: "adm",
			Parameters: map[string]any{},
		}
		_, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(errors.Is(err, portfolio.ErrMissingParameter)).To(BeTrue())
		Expect(err.Error()).To(ContainSubstring("riskOn"))
	})

	It("accepts valid startDate and endDate", func() {
		start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		end := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)
		req := portfolio.CreateRequest{
			Name:       "foo",
			StrategyCode: "adm",
			Parameters: map[string]any{"riskOn": "SPY"},
			StartDate:  &start,
			EndDate:    &end,
		}
		_, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects endDate before startDate with ErrEndBeforeStart", func() {
		start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		end := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		req := portfolio.CreateRequest{
			Name:       "foo",
			StrategyCode: "adm",
			Parameters: map[string]any{"riskOn": "SPY"},
			StartDate:  &start,
			EndDate:    &end,
		}
		_, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(errors.Is(err, portfolio.ErrEndBeforeStart)).To(BeTrue())
	})

	It("accepts equal startDate and endDate", func() {
		d := time.Date(2022, 6, 1, 0, 0, 0, 0, time.UTC)
		req := portfolio.CreateRequest{
			Name:       "foo",
			StrategyCode: "adm",
			Parameters: map[string]any{"riskOn": "SPY"},
			StartDate:  &d,
			EndDate:    &d,
		}
		_, err := portfolio.ValidateCreate(req, makeStrategy())
		Expect(err).NotTo(HaveOccurred())
	})
})

var _ = Describe("ValidateCreateUnofficial", func() {
	d := strategy.Describe{
		ShortCode: "fake",
		Name:      "Fake",
		Parameters: []strategy.DescribeParameter{
			{Name: "riskOn", Type: "universe"},
		},
		Schedule:  "@monthend",
		Benchmark: "SPY",
	}

	It("accepts a well-formed request", func() {
		req := portfolio.CreateRequest{
			Name:       "p",
			Parameters: map[string]any{"riskOn": "SPY"},
		}
		norm, err := portfolio.ValidateCreateUnofficial(req, d)
		Expect(err).NotTo(HaveOccurred())
		Expect(norm.Benchmark).To(Equal("SPY"))
	})

	It("rejects unknown parameter", func() {
		req := portfolio.CreateRequest{
			Name:       "p",
			Parameters: map[string]any{"riskOn": "SPY", "nope": 1},
		}
		_, err := portfolio.ValidateCreateUnofficial(req, d)
		Expect(errors.Is(err, portfolio.ErrUnknownParameter)).To(BeTrue())
	})

	It("rejects missing parameter", func() {
		req := portfolio.CreateRequest{
			Name:       "p",
			Parameters: map[string]any{},
		}
		_, err := portfolio.ValidateCreateUnofficial(req, d)
		Expect(errors.Is(err, portfolio.ErrMissingParameter)).To(BeTrue())
	})

	It("rejects endDate before startDate", func() {
		start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		end := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
		req := portfolio.CreateRequest{
			Name:       "p",
			Parameters: map[string]any{"riskOn": "SPY"},
			StartDate:  &start,
			EndDate:    &end,
		}
		_, err := portfolio.ValidateCreateUnofficial(req, d)
		Expect(errors.Is(err, portfolio.ErrEndBeforeStart)).To(BeTrue())
	})
})
```

### Step 4: Update portfolio/db.go

- [ ] **Update portfolioColumns constant**

Find the `portfolioColumns` constant (line 39) and replace it:

```go
const portfolioColumns = `
	id, owner_sub, slug, name, strategy_code, strategy_ver, strategy_clone_url,
	strategy_describe_json, parameters,
	preset_name, benchmark, start_date, end_date, status, last_run_at,
	last_error, snapshot_path, created_at, updated_at
`
```

- [ ] **Update the scan function**

Find the `scan` function (around line 377) and replace it:

```go
func scan(r scanner) (Portfolio, error) {
	var (
		p          Portfolio
		statusStr  string
		paramsJSON []byte
	)
	err := r.Scan(
		&p.ID, &p.OwnerSub, &p.Slug, &p.Name, &p.StrategyCode, &p.StrategyVer,
		&p.StrategyCloneURL, &p.StrategyDescribeJSON, &paramsJSON,
		&p.PresetName, &p.Benchmark, &p.StartDate, &p.EndDate,
		&statusStr, &p.LastRunAt, &p.LastError, &p.SnapshotPath,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return Portfolio{}, err
	}
	p.Status = Status(statusStr)
	if len(paramsJSON) > 0 {
		if err := json.Unmarshal(paramsJSON, &p.Parameters); err != nil {
			return Portfolio{}, fmt.Errorf("unmarshaling parameters: %w", err)
		}
	}
	return p, nil
}
```

- [ ] **Update the Insert function**

Find the `Insert` function (around line 85) and replace it:

```go
func Insert(ctx context.Context, pool *pgxpool.Pool, p Portfolio) error {
	paramsJSON, err := json.Marshal(p.Parameters)
	if err != nil {
		return fmt.Errorf("marshaling parameters: %w", err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO portfolios (
			owner_sub, slug, name, strategy_code, strategy_ver,
			strategy_clone_url, strategy_describe_json, parameters,
			preset_name, benchmark, start_date, end_date, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`, p.OwnerSub, p.Slug, p.Name, p.StrategyCode, p.StrategyVer,
		p.StrategyCloneURL, p.StrategyDescribeJSON, paramsJSON,
		p.PresetName, p.Benchmark, p.StartDate, p.EndDate,
		string(p.Status))
	if err != nil {
		if uniqueViolation(err) {
			return ErrDuplicateSlug
		}
		return fmt.Errorf("inserting portfolio: %w", err)
	}
	return nil
}
```

- [ ] **Replace ClaimDueContinuous with ClaimDue; remove DueContinuous and NextRunFunc**

Delete the `DueContinuous` struct, `NextRunFunc` type, and `ClaimDueContinuous` function (from around line 283 to end of the function). Replace with:

```go
// ClaimDue returns up to batchSize portfolio IDs that are open-ended
// (end_date IS NULL) and have not yet run today. Concurrent safety is
// provided by the caller's dispatcher (ErrAlreadyRunning guard in the
// orchestrator prevents double-execution).
func ClaimDue(ctx context.Context, pool *pgxpool.Pool, batchSize int) ([]uuid.UUID, error) {
	rows, err := pool.Query(ctx, `
		SELECT id
		  FROM portfolios
		 WHERE end_date IS NULL
		   AND status IN ('ready', 'failed')
		   AND (last_run_at IS NULL OR last_run_at::date < CURRENT_DATE)
		 ORDER BY last_run_at NULLS FIRST
		 LIMIT $1
	`, batchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
```

Also remove the import of `"github.com/rs/zerolog/log"` from `portfolio/db.go` if it was only used by `ClaimDueContinuous`. Check and remove if unused.

### Step 5: Update portfolio/store.go

- [ ] **Update Store interface and PoolStore**

In `portfolio/store.go`, update the `Store` interface — replace `ClaimDueContinuous` with `ClaimDue`:

```go
// Store is the subset of db operations the handler needs.
type Store interface {
	RunStore
	List(ctx context.Context, ownerSub string) ([]Portfolio, error)
	Get(ctx context.Context, ownerSub, slug string) (Portfolio, error)
	Insert(ctx context.Context, p Portfolio) error
	UpdateName(ctx context.Context, ownerSub, slug, name string) error
	Delete(ctx context.Context, ownerSub, slug string) error
	ClaimDue(ctx context.Context, batchSize int) ([]uuid.UUID, error)
}
```

Remove the `time` import from `store.go` if it was only used for `ClaimDueContinuous`'s `before time.Time` parameter. Add `uuid` import if not present (it's already there for `GetByID`).

Replace the `PoolStore.ClaimDueContinuous` method with `PoolStore.ClaimDue`:

```go
// ClaimDue returns open-ended portfolio IDs not yet run today.
func (p PoolStore) ClaimDue(ctx context.Context, batchSize int) ([]uuid.UUID, error) {
	return ClaimDue(ctx, p.Pool, batchSize)
}
```

Remove the `DueContinuous` and `NextRunFunc` re-export if they existed in store.go (they don't — they're only in db.go).

Also remove the `time` import from `store.go` if it's now unused (check SetReady, MarkReadyTx signatures — they still use `time.Time`).

### Step 6: Update portfolio/handler.go

- [ ] **Update createBody and toRequest**

Find `type createBody struct` (around line 376) and replace the entire struct plus toRequest:

```go
// createBody mirrors the OpenAPI PortfolioCreateRequest shape.
type createBody struct {
	Name             string         `json:"name"`
	StrategyCode     string         `json:"strategyCode,omitempty"`
	StrategyCloneURL string         `json:"strategyCloneUrl,omitempty"`
	StrategyVer      string         `json:"strategyVer,omitempty"`
	Parameters       map[string]any `json:"parameters"`
	Benchmark        string         `json:"benchmark,omitempty"`
	StartDate        string         `json:"startDate,omitempty"`
	EndDate          string         `json:"endDate,omitempty"`
}

func (b createBody) toRequest(startDate, endDate *time.Time) CreateRequest {
	return CreateRequest{
		Name:             b.Name,
		StrategyCode:     b.StrategyCode,
		StrategyCloneURL: b.StrategyCloneURL,
		StrategyVer:      b.StrategyVer,
		Parameters:       b.Parameters,
		Benchmark:        b.Benchmark,
		StartDate:        startDate,
		EndDate:          endDate,
	}
}
```

- [ ] **Add parseDate helper**

Add after `toRequest`:

```go
// parseDate parses an optional YYYY-MM-DD string into a *time.Time.
// Returns nil, nil for empty strings.
func parseDate(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil, fmt.Errorf("invalid date %q: must be YYYY-MM-DD", s)
	}
	return &t, nil
}
```

- [ ] **Update portfolioView and toView**

Find `type portfolioView struct` (around line 402) and replace it plus `toView`:

```go
// portfolioView mirrors the OpenAPI Portfolio schema (config only).
type portfolioView struct {
	Slug             string         `json:"slug"`
	Name             string         `json:"name"`
	StrategyCode     string         `json:"strategyCode"`
	StrategyVer      *string        `json:"strategyVer"`
	StrategyCloneURL string         `json:"strategyCloneUrl"`
	Parameters       map[string]any `json:"parameters"`
	PresetName       *string        `json:"presetName"`
	Benchmark        string         `json:"benchmark"`
	StartDate        *string        `json:"startDate,omitempty"`
	EndDate          *string        `json:"endDate,omitempty"`
	Status           string         `json:"status"`
	CreatedAt        string         `json:"createdAt"`
	UpdatedAt        string         `json:"updatedAt"`
	LastRunAt        *string        `json:"lastRunAt"`
	LastError        *string        `json:"lastError"`
}

func toView(p Portfolio) portfolioView {
	v := portfolioView{
		Slug:             p.Slug,
		Name:             p.Name,
		StrategyCode:     p.StrategyCode,
		StrategyVer:      p.StrategyVer,
		StrategyCloneURL: p.StrategyCloneURL,
		Parameters:       p.Parameters,
		PresetName:       p.PresetName,
		Benchmark:        p.Benchmark,
		Status:           string(p.Status),
		CreatedAt:        p.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		UpdatedAt:        p.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		LastError:        p.LastError,
	}
	if p.StartDate != nil {
		d := p.StartDate.Format("2006-01-02")
		v.StartDate = &d
	}
	if p.EndDate != nil {
		d := p.EndDate.Format("2006-01-02")
		v.EndDate = &d
	}
	if p.LastRunAt != nil {
		t := p.LastRunAt.UTC().Format("2006-01-02T15:04:05Z")
		v.LastRunAt = &t
	}
	return v
}
```

- [ ] **Update the Create handler to parse dates**

In the `Create` function (around line 121), after parsing the body, add date parsing before calling `toRequest`. Replace:

```go
req := body.toRequest()
```

With:

```go
startDate, err := parseDate(body.StartDate)
if err != nil {
    return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity", err.Error())
}
endDate, err := parseDate(body.EndDate)
if err != nil {
    return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity", err.Error())
}
req := body.toRequest(startDate, endDate)
```

- [ ] **Update buildPortfolio — remove tradecron/mode/schedule**

Find `buildPortfolio` (around line 239) and replace it:

```go
// buildPortfolio constructs a Portfolio value from a validated create request.
func (h *Handler) buildPortfolio(ownerSub string, norm CreateRequest, describe strategy.Describe,
	cloneURL string, describeJSON []byte) (Portfolio, error) {

	slug, err := Slug(norm, describe)
	if err != nil {
		return Portfolio{}, err
	}
	presetName := presetMatch(norm.Parameters, describe)
	p := Portfolio{
		OwnerSub:             ownerSub,
		Slug:                 slug,
		Name:                 norm.Name,
		StrategyCode:         norm.StrategyCode,
		StrategyCloneURL:     cloneURL,
		StrategyDescribeJSON: describeJSON,
		Parameters:           norm.Parameters,
		PresetName:           presetName,
		Benchmark:            norm.Benchmark,
		StartDate:            norm.StartDate,
		EndDate:              norm.EndDate,
		Status:               StatusPending,
	}
	if norm.StrategyVer != "" {
		v := norm.StrategyVer
		p.StrategyVer = &v
	}
	return p, nil
}
```

- [ ] **Update autoTriggerOrProblem — always submit**

Find `autoTriggerOrProblem` (around line 286) and replace it:

```go
// autoTriggerOrProblem dispatches an immediate backtest run on creation.
// Returns a non-zero status + body on failure; zero status means proceed.
func (h *Handler) autoTriggerOrProblem(c fiber.Ctx, created Portfolio) (int, problemBody) {
	if h.dispatcher == nil {
		return 0, problemBody{}
	}
	if _, err := h.dispatcher.Submit(c.Context(), created.ID); err != nil {
		if errors.Is(err, ErrQueueFull) {
			return fiber.StatusServiceUnavailable, problemBody{
				title:  "Service Unavailable",
				detail: "backtest queue is full, try again later",
			}
		}
		return fiber.StatusInternalServerError, problemBody{
			title:  "Internal Server Error",
			detail: err.Error(),
		}
	}
	return 0, problemBody{}
}
```

- [ ] **Update the Patch handler to allow startDate/endDate**

Find the `Patch` function (around line 315). Replace the strict single-field check with one that allows `name`, `startDate`, `endDate`:

```go
// Patch implements PATCH /portfolios/{slug}.
// Allows updating: name, startDate, endDate.
func (h *Handler) Patch(c fiber.Ctx) error {
	ownerSub, err := subject(c)
	if err != nil {
		return writeProblem(c, fiber.StatusUnauthorized, "Unauthorized", err.Error())
	}
	slug := string([]byte(c.Params("slug")))

	var raw map[string]json.RawMessage
	if err := sonic.Unmarshal(c.Body(), &raw); err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity",
			fmt.Sprintf("body is not valid JSON: %v", err))
	}
	allowed := map[string]bool{"name": true, "startDate": true, "endDate": true}
	for k := range raw {
		if !allowed[k] {
			return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity",
				"only `name`, `startDate`, `endDate` may be updated; rejected field: "+k)
		}
	}

	var body struct {
		Name      string `json:"name"`
		StartDate string `json:"startDate"`
		EndDate   string `json:"endDate"`
	}
	if err := sonic.Unmarshal(c.Body(), &body); err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity",
			fmt.Sprintf("body is not valid JSON: %v", err))
	}

	startDate, err := parseDate(body.StartDate)
	if err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity", err.Error())
	}
	endDate, err := parseDate(body.EndDate)
	if err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity", err.Error())
	}
	if err := validateDates(startDate, endDate); err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "Unprocessable Entity", err.Error())
	}

	if body.Name != "" {
		if err := h.store.UpdateName(c.Context(), ownerSub, slug, body.Name); err != nil {
			if errors.Is(err, ErrNotFound) {
				return writeProblem(c, fiber.StatusNotFound, "Not Found", "portfolio not found: "+slug)
			}
			return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
		}
	}
	if startDate != nil || endDate != nil {
		if err := h.store.UpdateDates(c.Context(), ownerSub, slug, startDate, endDate); err != nil {
			if errors.Is(err, ErrNotFound) {
				return writeProblem(c, fiber.StatusNotFound, "Not Found", "portfolio not found: "+slug)
			}
			return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
		}
	}

	p, err := h.store.Get(c.Context(), ownerSub, slug)
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	return writeJSON(c, fiber.StatusOK, toView(p))
}
```

- [ ] **Add UpdateDates to portfolio/db.go**

Add after `UpdateName` in `portfolio/db.go`:

```go
// UpdateDates updates a portfolio's startDate and/or endDate.
// Only non-nil values are applied. Returns ErrNotFound if no row matched.
func UpdateDates(ctx context.Context, pool *pgxpool.Pool, ownerSub, slug string, startDate, endDate *time.Time) error {
	tag, err := pool.Exec(ctx, `
		UPDATE portfolios
		   SET start_date = COALESCE($3, start_date),
		       end_date   = COALESCE($4, end_date),
		       updated_at = NOW()
		 WHERE owner_sub = $1 AND slug = $2
	`, ownerSub, slug, startDate, endDate)
	if err != nil {
		return fmt.Errorf("updating portfolio dates: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
```

- [ ] **Add UpdateDates to portfolio/store.go Store interface and PoolStore**

Add `UpdateDates` to the `Store` interface:

```go
Store interface {
	RunStore
	List(ctx context.Context, ownerSub string) ([]Portfolio, error)
	Get(ctx context.Context, ownerSub, slug string) (Portfolio, error)
	Insert(ctx context.Context, p Portfolio) error
	UpdateName(ctx context.Context, ownerSub, slug, name string) error
	UpdateDates(ctx context.Context, ownerSub, slug string, startDate, endDate *time.Time) error
	Delete(ctx context.Context, ownerSub, slug string) error
	ClaimDue(ctx context.Context, batchSize int) ([]uuid.UUID, error)
}
```

Add `time` import to `store.go` if not present (check whether it was removed earlier — if SetReady/MarkReadyTx signatures still use time.Time, it's still imported).

Add to `PoolStore`:

```go
// UpdateDates updates a portfolio's start_date and/or end_date.
func (p PoolStore) UpdateDates(ctx context.Context, ownerSub, slug string, startDate, endDate *time.Time) error {
	return UpdateDates(ctx, p.Pool, ownerSub, slug, startDate, endDate)
}
```

- [ ] **Remove tradecron import from handler.go**

Remove `"github.com/penny-vault/pvbt/tradecron"` from the import block in `portfolio/handler.go`.

### Step 7: Update portfolio/handler_test.go

- [ ] **Remove mode field from all request fixtures**

In `portfolio/handler_test.go`, remove `"mode": "one_shot"` from every request body map in the `Describe("portfolio.Handler", ...)` block. The field is simply omitted — it's no longer part of the API.

Also remove the `tradecron` import line from the file imports.

- [ ] **Replace the "Create for continuous portfolios" Describe block**

Find `var _ = Describe("Create for continuous portfolios", ...)` and replace the entire block with:

```go
var _ = Describe("Create with date period", func() {
	installedVer := "v1.0.0"
	describeJSON := []byte(`{"shortCode":"adm","name":"ADM","description":"","parameters":[{"name":"riskOn","type":"universe"}],"presets":[{"name":"standard","parameters":{"riskOn":"VFINX,PRIDX,QQQ"}}],"schedule":"@monthend","benchmark":"SPY"}`)

	newSetup := func(disp portfolio.Dispatcher) (*fakeStore, *fiber.App) {
		store := &fakeStore{}
		strategies := &fakeStrategyStore{
			row: strategy.Strategy{
				ShortCode:    "adm",
				IsOfficial:   true,
				InstalledVer: &installedVer,
				DescribeJSON: describeJSON,
			},
		}
		h := portfolio.NewHandler(store, strategies, nil, disp, nil, nil, strategy.EphemeralOptions{})
		app := fiber.New()
		app.Use(func(c fiber.Ctx) error {
			c.Locals(types.AuthSubjectKey{}, "auth0|user-1")
			return c.Next()
		})
		app.Post("/portfolios", h.Create)
		return store, app
	}

	It("always submits a run on creation", func() {
		disp := &countingDispatcher{runID: uuid.Must(uuid.NewV7())}
		_, app := newSetup(disp)

		body := `{"name":"test","strategyCode":"adm","parameters":{"riskOn":"VFINX,PRIDX,QQQ"}}`
		req := httptest.NewRequest("POST", "/portfolios", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusCreated))
		Expect(disp.calls.Load()).To(Equal(int64(1)))
	})

	It("stores startDate and endDate on the portfolio", func() {
		disp := &countingDispatcher{runID: uuid.Must(uuid.NewV7())}
		store, app := newSetup(disp)

		body := `{"name":"dated","strategyCode":"adm","parameters":{"riskOn":"VFINX,PRIDX,QQQ"},"startDate":"2020-01-01","endDate":"2024-12-31"}`
		req := httptest.NewRequest("POST", "/portfolios", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusCreated))
		Expect(store.rows).To(HaveLen(1))
		Expect(store.rows[0].StartDate).NotTo(BeNil())
		Expect(store.rows[0].StartDate.Format("2006-01-02")).To(Equal("2020-01-01"))
		Expect(store.rows[0].EndDate).NotTo(BeNil())
		Expect(store.rows[0].EndDate.Format("2006-01-02")).To(Equal("2024-12-31"))
	})

	It("returns 422 for an invalid startDate format", func() {
		disp := &countingDispatcher{runID: uuid.Must(uuid.NewV7())}
		_, app := newSetup(disp)

		body := `{"name":"x","strategyCode":"adm","parameters":{"riskOn":"VFINX,PRIDX,QQQ"},"startDate":"not-a-date"}`
		req := httptest.NewRequest("POST", "/portfolios", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusUnprocessableEntity))
	})

	It("returns 422 when endDate is before startDate", func() {
		disp := &countingDispatcher{runID: uuid.Must(uuid.NewV7())}
		_, app := newSetup(disp)

		body := `{"name":"x","strategyCode":"adm","parameters":{"riskOn":"VFINX,PRIDX,QQQ"},"startDate":"2024-01-01","endDate":"2020-01-01"}`
		req := httptest.NewRequest("POST", "/portfolios", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusUnprocessableEntity))
	})

	It("rolls back the portfolio row and returns 503 when dispatcher is full", func() {
		disp := &countingDispatcher{err: portfolio.ErrQueueFull}
		store, app := newSetup(disp)

		body := `{"name":"test","strategyCode":"adm","parameters":{"riskOn":"VFINX,PRIDX,QQQ"}}`
		req := httptest.NewRequest("POST", "/portfolios", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusServiceUnavailable))
		Expect(store.rows).To(BeEmpty())
	})
})
```

- [ ] **Update fakeStore to implement the new Store interface**

In `fakeStore`, replace `ClaimDueContinuous` with `ClaimDue`, and add `UpdateDates`:

```go
// ClaimDue stub — handler tests do not exercise the scheduler path.
func (f *fakeStore) ClaimDue(_ context.Context, _ int) ([]uuid.UUID, error) {
	return nil, nil
}

func (f *fakeStore) UpdateDates(_ context.Context, _, _ string, _, _ *time.Time) error {
	return nil
}
```

Remove the old `ClaimDueContinuous` stub.

- [ ] **Verify the portfolio package compiles and tests pass**

```bash
cd /Users/jdf/Developer/penny-vault/pv-api && go test ./portfolio/... 2>&1 | tail -10
```

Expected: `ok  	github.com/penny-vault/pv-api/portfolio`

- [ ] **Commit**

```bash
git add portfolio/
git commit -m "portfolio: add startDate/endDate; remove mode/schedule/runNow"
```

---

## Task 5: api/portfolios.go — rename POST /runs to /run

**Files:**
- Modify: `api/portfolios.go`

- [ ] **Step 1: Update both stub and real route registrations**

In `api/portfolios.go`, change every occurrence of `Post("/portfolios/:slug/runs"` to `Post("/portfolios/:slug/run"`:

Line ~41 in `RegisterPortfolioRoutes`:
```go
r.Post("/portfolios/:slug/run", stubPortfolio)
```

Line ~62 in `RegisterPortfolioRoutesWith`:
```go
r.Post("/portfolios/:slug/run", h.CreateRun)
```

Leave `GET /portfolios/:slug/runs` and `GET /portfolios/:slug/runs/:runId` unchanged.

- [ ] **Step 2: Verify compile**

```bash
cd /Users/jdf/Developer/penny-vault/pv-api && go build ./api/... 2>&1
```

- [ ] **Step 3: Commit**

```bash
git add api/portfolios.go
git commit -m "api: rename POST /runs to /run (singular)"
```

---

## Task 6: scheduler — simplify to daily ClaimDue

**Files:**
- Modify: `scheduler/scheduler.go`
- Modify: `scheduler/scheduler_test.go`

- [ ] **Step 1: Write new scheduler.go**

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

// Package scheduler runs an in-process ticker that claims open-ended portfolios
// not yet run today and submits them to the backtest dispatcher.
package scheduler

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/penny-vault/pv-api/backtest"
)

// PortfolioStore is the subset of portfolio store operations the scheduler needs.
type PortfolioStore interface {
	ClaimDue(ctx context.Context, batchSize int) ([]uuid.UUID, error)
}

// Dispatcher is the subset of backtest.Dispatcher the scheduler needs.
type Dispatcher interface {
	Submit(ctx context.Context, portfolioID uuid.UUID) (runID uuid.UUID, err error)
}

// Scheduler owns the tick loop that picks up due open-ended portfolios.
type Scheduler struct {
	store      PortfolioStore
	dispatcher Dispatcher
	cfg        Config
}

// New builds a Scheduler. cfg defaults are applied.
func New(cfg Config, store PortfolioStore, dispatcher Dispatcher) *Scheduler {
	cfg.ApplyDefaults()
	return &Scheduler{
		store:      store,
		dispatcher: dispatcher,
		cfg:        cfg,
	}
}

// Run blocks until ctx is cancelled, firing tickOnce immediately and then at
// each cfg.TickInterval.
func (s *Scheduler) Run(ctx context.Context) error {
	s.tickOnce(ctx)
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
	ids, err := s.store.ClaimDue(ctx, s.cfg.BatchSize)
	if err != nil {
		log.Error().Err(err).Msg("scheduler: claim failed")
		return
	}
	for _, id := range ids {
		runID, err := s.dispatcher.Submit(ctx, id)
		switch {
		case errors.Is(err, backtest.ErrQueueFull):
			log.Warn().Stringer("portfolio_id", id).Msg("scheduler: queue full, skipped until next tick")
		case err != nil:
			log.Error().Err(err).Stringer("portfolio_id", id).Msg("scheduler: submit failed")
		default:
			log.Info().Stringer("portfolio_id", id).Stringer("run_id", runID).Msg("scheduler: dispatched")
		}
	}
}
```

- [ ] **Step 2: Write new scheduler_test.go**

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

package scheduler_test

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/backtest"
	"github.com/penny-vault/pv-api/scheduler"
)

type stubStore struct {
	claimCalls atomic.Int64
	ids        []uuid.UUID
	err        error
}

func (s *stubStore) ClaimDue(_ context.Context, _ int) ([]uuid.UUID, error) {
	s.claimCalls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	return s.ids, nil
}

type stubDispatcher struct {
	submitCalls atomic.Int64
	err         error
}

func (d *stubDispatcher) Submit(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	d.submitCalls.Add(1)
	if d.err != nil {
		return uuid.Nil, d.err
	}
	return uuid.Must(uuid.NewV7()), nil
}

var _ = Describe("Scheduler.Run", func() {
	It("exits cleanly when context is cancelled", func() {
		store := &stubStore{}
		disp := &stubDispatcher{}
		sched := scheduler.New(scheduler.Config{TickInterval: time.Hour, BatchSize: 32}, store, disp)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := sched.Run(ctx)
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())
	})

	It("dispatches each claimed portfolio on the initial tick", func() {
		ids := []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}
		store := &stubStore{ids: ids}
		disp := &stubDispatcher{}
		sched := scheduler.New(scheduler.Config{TickInterval: time.Hour, BatchSize: 32}, store, disp)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		done := make(chan error, 1)
		go func() { done <- sched.Run(ctx) }()

		Eventually(func() int64 { return disp.submitCalls.Load() }, time.Second).Should(Equal(int64(2)))
		cancel()
		Expect(<-done).To(MatchError(context.Canceled))
		Expect(store.claimCalls.Load()).To(BeNumerically(">=", int64(1)))
	})

	It("continues past ErrQueueFull and dispatches remaining claims", func() {
		ids := []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}
		store := &stubStore{ids: ids}
		disp := &stubDispatcher{err: backtest.ErrQueueFull}
		sched := scheduler.New(scheduler.Config{TickInterval: time.Hour, BatchSize: 32}, store, disp)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- sched.Run(ctx) }()

		Eventually(func() int64 { return disp.submitCalls.Load() }, time.Second).Should(Equal(int64(2)))
		cancel()
		Expect(<-done).To(MatchError(context.Canceled))
	})

	It("continues past a generic dispatcher error", func() {
		ids := []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}
		store := &stubStore{ids: ids}
		disp := &stubDispatcher{err: errors.New("pool closed")}
		sched := scheduler.New(scheduler.Config{TickInterval: time.Hour, BatchSize: 32}, store, disp)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- sched.Run(ctx) }()

		Eventually(func() int64 { return disp.submitCalls.Load() }, time.Second).Should(Equal(int64(2)))
		cancel()
		Expect(<-done).To(MatchError(context.Canceled))
	})

	It("does not panic or exit when ClaimDue errors", func() {
		store := &stubStore{err: errors.New("db down")}
		disp := &stubDispatcher{}
		sched := scheduler.New(scheduler.Config{TickInterval: 10 * time.Millisecond, BatchSize: 32}, store, disp)

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		err := sched.Run(ctx)
		Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())
		Expect(store.claimCalls.Load()).To(BeNumerically(">=", int64(2)))
		Expect(disp.submitCalls.Load()).To(Equal(int64(0)))
	})

	It("fires the first tick immediately without waiting for TickInterval", func() {
		ids := []uuid.UUID{uuid.Must(uuid.NewV7())}
		store := &stubStore{ids: ids}
		disp := &stubDispatcher{}
		sched := scheduler.New(scheduler.Config{TickInterval: time.Hour, BatchSize: 32}, store, disp)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- sched.Run(ctx) }()

		Eventually(func() int64 { return disp.submitCalls.Load() }, 200*time.Millisecond).Should(Equal(int64(1)))
		cancel()
		<-done
	})
})
```

- [ ] **Step 3: Verify scheduler tests pass**

```bash
cd /Users/jdf/Developer/penny-vault/pv-api && go test ./scheduler/... 2>&1 | tail -5
```

Expected: `ok  	github.com/penny-vault/pv-api/scheduler`

- [ ] **Step 4: Commit**

```bash
git add scheduler/scheduler.go scheduler/scheduler_test.go
git commit -m "scheduler: simplify to daily ClaimDue; remove tradecron dependency"
```

---

## Task 7: cmd/server.go — update adapters

**Files:**
- Modify: `cmd/server.go`

- [ ] **Step 1: Update schedulerStoreAdapter**

Find `type schedulerStoreAdapter struct` and its `ClaimDueContinuous` method. Replace the entire adapter:

```go
// schedulerStoreAdapter adapts *portfolio.PoolStore to scheduler.PortfolioStore.
type schedulerStoreAdapter struct {
	store *portfolio.PoolStore
}

func (a schedulerStoreAdapter) ClaimDue(ctx context.Context, batchSize int) ([]uuid.UUID, error) {
	return a.store.ClaimDue(ctx, batchSize)
}
```

Add `"github.com/google/uuid"` to imports if not already present (it already is).

- [ ] **Step 2: Update scheduler.New call — remove nextRun parameter**

Find:
```go
sched := scheduler.New(schedCfg,
    schedulerStoreAdapter{store: portfolioStore},
    schedulerDispatcherAdapter{bt: dispatcher},
    scheduler.TradecronNext,
)
```

Replace with:
```go
sched := scheduler.New(schedCfg,
    schedulerStoreAdapter{store: portfolioStore},
    schedulerDispatcherAdapter{bt: dispatcher},
)
```

- [ ] **Step 3: Remove tradecron.SetMarketHolidays call**

Find and delete:
```go
// Initialize tradecron with no holiday data (future plan loads real
// holidays). Required before any @monthend/@quarter* schedule is
// evaluated anywhere in the process.
tradecron.SetMarketHolidays(nil)
log.Info().Msg("tradecron holidays disabled (no data loaded)")
```

- [ ] **Step 4: Remove tradecron import**

Remove `"github.com/penny-vault/pvbt/tradecron"` from the import block in `cmd/server.go`.

- [ ] **Step 5: Update backtestPortfolioStoreAdapter.GetByID to map StartDate/EndDate**

Find the `GetByID` method on `backtestPortfolioStoreAdapter` (around line 84). Update the return statement to include dates:

```go
func (a backtestPortfolioStoreAdapter) GetByID(ctx context.Context, id uuid.UUID) (backtest.PortfolioRow, error) {
	p, err := a.store.GetByID(ctx, id)
	if err != nil {
		return backtest.PortfolioRow{}, err
	}
	strategyVer := ""
	if p.StrategyVer != nil {
		strategyVer = *p.StrategyVer
	}
	return backtest.PortfolioRow{
		ID:               p.ID,
		StrategyCode:     p.StrategyCode,
		StrategyVer:      strategyVer,
		StrategyCloneURL: p.StrategyCloneURL,
		Parameters:       p.Parameters,
		Benchmark:        p.Benchmark,
		Status:           string(p.Status),
		SnapshotPath:     p.SnapshotPath,
		StartDate:        p.StartDate,
		EndDate:          p.EndDate,
	}, nil
}
```

- [ ] **Step 6: Verify full build**

```bash
cd /Users/jdf/Developer/penny-vault/pv-api && go build ./... 2>&1
```

Expected: no output.

- [ ] **Step 7: Commit**

```bash
git add cmd/server.go
git commit -m "cmd: update adapters for simplified scheduler and date-period fields"
```

---

## Task 8: Full test suite + lint

- [ ] **Step 1: Run all unit tests**

```bash
cd /Users/jdf/Developer/penny-vault/pv-api && go test ./... 2>&1 | tail -20
```

Expected: all packages pass. Fix any failures before continuing.

- [ ] **Step 2: Run lint**

```bash
cd /Users/jdf/Developer/penny-vault/pv-api && golangci-lint run ./... 2>&1 | head -40
```

Fix any lint errors.

- [ ] **Step 3: Commit lint fixes if any**

```bash
git add -p
git commit -m "polish: lint cleanup for date-period plan"
```

---

## Self-Review Checklist

- [x] Migration drops mode/schedule/next_run_at and portfolio_mode enum; adds start_date/end_date — Task 1
- [x] `BuildArgs` appends --start/--end — Task 2
- [x] `PortfolioRow` has StartDate/EndDate — Task 3
- [x] Orchestrator passes dates to BuildArgs — Task 3
- [x] `Portfolio` struct has StartDate/EndDate, no Mode/Schedule/NextRunAt — Task 4
- [x] Date validation (endDate >= startDate) — Task 4
- [x] createBody: no mode/schedule/runNow; startDate/endDate strings parsed — Task 4
- [x] portfolioView includes startDate/endDate, omits mode/schedule — Task 4
- [x] buildPortfolio no longer sets tradecron next_run_at — Task 4
- [x] autoTriggerOrProblem always submits (no mode switch) — Task 4
- [x] PATCH allows name/startDate/endDate — Task 4
- [x] fakeStore implements updated Store interface — Task 4
- [x] POST /run (singular) — Task 5
- [x] Scheduler uses ClaimDue; no tradecron — Task 6
- [x] cmd/server.go removes tradecron; adapters updated — Task 7
- [x] All tests pass — Task 8
