# pvapi 3.0 — Plan 6: Scheduler + continuous mode — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an in-process scheduler goroutine that picks up due continuous portfolios, advances `next_run_at` via `tradecron.Next`, and submits them through the existing `backtest.Dispatcher` (shared worker pool with `POST /runs`). Also tighten `portfolio.Create` so continuous portfolios validate their schedule at create time, bootstrap `next_run_at`, and always auto-enqueue a first run.

**Architecture:** New `scheduler/` package owning the ticker loop and tick logic. `portfolio.PoolStore` gains `ClaimDueContinuous` (SELECT FOR UPDATE SKIP LOCKED + UPDATE `next_run_at` in one tx). Adapters in `cmd/server.go` convert between `portfolio.DueContinuous`/`scheduler.Claim` and `portfolio.NextRunFunc`/`scheduler.NextRunFunc`, preserving the established dependency direction (`scheduler → backtest`, no `portfolio → scheduler`).

**Tech Stack:** Go 1.24, pvbt v0.7.5 (`tradecron`), pgx v5 / pgxpool, Ginkgo/Gomega, zerolog.

**Source of truth:** `docs/superpowers/specs/2026-04-19-pvapi-3-0-scheduler.md`.

---

## Preconditions

Before starting execution, the harness must ensure a clean baseline:

- Worktree set up via `superpowers:using-git-worktrees` (feature branch).
- `go test ./...` passes on main as of `9af3c18` (Plan 5 merge).
- `pvbt v0.7.5` is pinned in `go.mod`. Do NOT bump pvbt as part of Plan 6.

## File structure

**New files:**

| File | Responsibility |
|---|---|
| `scheduler/config.go` | `Config{TickInterval, BatchSize}`, `ApplyDefaults`, `Validate`, config sentinels |
| `scheduler/config_test.go` | Config defaulting + validation specs |
| `scheduler/scheduler.go` | `Scheduler` struct, `New`, `Run(ctx)`, `tickOnce`, `PortfolioStore` + `Dispatcher` interfaces, `Claim`, `NextRunFunc`, `TradecronNext` helper |
| `scheduler/scheduler_test.go` | Ginkgo specs for tick loop (happy path, queue-full, dispatcher error, claim error, ctx cancel, immediate first tick) |
| `scheduler/scheduler_suite_test.go` | Ginkgo suite entry point |

**Modified files:**

| File | Change |
|---|---|
| `portfolio/types.go` | Add `NextRunAt *time.Time` field to `Portfolio` |
| `portfolio/db.go` | Add `next_run_at` to `portfolioColumns`, `scan()`, and `Insert()`; add `DueContinuous`, `NextRunFunc`, `ClaimDueContinuous` method, and `ErrInvalidSchedule` sentinel |
| `portfolio/store.go` | Add `ClaimDueContinuous` to the `Store` interface and a `PoolStore.ClaimDueContinuous` forwarder |
| `portfolio/validate.go` | Add schedule validation via `tradecron.New` (new `ErrInvalidSchedule` sentinel) |
| `portfolio/handler.go` | `Create`: validate schedule for continuous; bootstrap `next_run_at`; force `runNow=true` for continuous (ignore request value); on `Submit` error, delete the row and return 503/500 |
| `portfolio/handler_test.go` | Add specs for the new Create behavior (invalid schedule → 422; continuous → dispatcher.Submit called unconditionally; `ErrQueueFull` → 503 + row rolled back) |
| `cmd/config.go` | Add `schedulerConf` + `Config.Scheduler` field |
| `cmd/viper.go` | Defaults for `scheduler.tick_interval`, `scheduler.batch_size`, `scheduler.enabled` |
| `cmd/server.go` | Add `schedulerStoreAdapter` + `schedulerDispatcherAdapter` + scheduler instantiation and goroutine launch |

**No migration.** Schema already has `portfolios.next_run_at TIMESTAMPTZ` and `idx_portfolios_due` partial index from `1_init`.

**No OpenAPI change.** `runNow` field remains; description text is not updated.

---

## Task 1: Scheduler config + defaults

**Files:**
- Create: `scheduler/config.go`
- Create: `scheduler/config_test.go`
- Create: `scheduler/scheduler_suite_test.go`

- [ ] **Step 1: Write the Ginkgo suite entry**

Create `scheduler/scheduler_suite_test.go`:

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
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestScheduler(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Scheduler Suite")
}
```

- [ ] **Step 2: Write the failing config test**

Create `scheduler/config_test.go`:

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
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/scheduler"
)

var _ = Describe("Config", func() {
	Describe("ApplyDefaults", func() {
		It("fills in TickInterval=60s when zero", func() {
			c := scheduler.Config{}
			c.ApplyDefaults()
			Expect(c.TickInterval).To(Equal(60 * time.Second))
		})

		It("fills in BatchSize=32 when zero", func() {
			c := scheduler.Config{}
			c.ApplyDefaults()
			Expect(c.BatchSize).To(Equal(32))
		})

		It("does not overwrite non-zero values", func() {
			c := scheduler.Config{TickInterval: 5 * time.Second, BatchSize: 4}
			c.ApplyDefaults()
			Expect(c.TickInterval).To(Equal(5 * time.Second))
			Expect(c.BatchSize).To(Equal(4))
		})
	})

	Describe("Validate", func() {
		It("rejects negative TickInterval", func() {
			c := scheduler.Config{TickInterval: -1 * time.Second, BatchSize: 32}
			Expect(errors.Is(c.Validate(), scheduler.ErrInvalidTickInterval)).To(BeTrue())
		})

		It("rejects negative BatchSize", func() {
			c := scheduler.Config{TickInterval: time.Second, BatchSize: -1}
			Expect(errors.Is(c.Validate(), scheduler.ErrInvalidBatchSize)).To(BeTrue())
		})

		It("accepts positive values", func() {
			c := scheduler.Config{TickInterval: time.Second, BatchSize: 1}
			Expect(c.Validate()).To(Succeed())
		})
	})
})
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./scheduler/... -run TestScheduler -v`
Expected: compile error (package `scheduler` does not exist).

- [ ] **Step 4: Implement the config**

Create `scheduler/config.go`:

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

package scheduler

import (
	"errors"
	"time"
)

// Config controls the scheduler tick loop.
type Config struct {
	// TickInterval is the cadence at which the scheduler polls for due
	// continuous portfolios. Defaults to 60s.
	TickInterval time.Duration
	// BatchSize is the maximum number of portfolios claimed per tick.
	// Defaults to 32.
	BatchSize int
}

// ApplyDefaults fills zero-valued fields with their documented defaults.
func (c *Config) ApplyDefaults() {
	if c.TickInterval == 0 {
		c.TickInterval = 60 * time.Second
	}
	if c.BatchSize == 0 {
		c.BatchSize = 32
	}
}

// Validate reports invalid configuration.
func (c Config) Validate() error {
	if c.TickInterval < 0 {
		return ErrInvalidTickInterval
	}
	if c.BatchSize < 0 {
		return ErrInvalidBatchSize
	}
	return nil
}

var (
	// ErrInvalidTickInterval is returned by Config.Validate when TickInterval < 0.
	ErrInvalidTickInterval = errors.New("scheduler: tick_interval must be >= 0")
	// ErrInvalidBatchSize is returned by Config.Validate when BatchSize < 0.
	ErrInvalidBatchSize = errors.New("scheduler: batch_size must be >= 0")
)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./scheduler/... -v`
Expected: PASS (3 ApplyDefaults specs + 3 Validate specs).

- [ ] **Step 6: Commit**

```bash
git add scheduler/config.go scheduler/config_test.go scheduler/scheduler_suite_test.go
git commit -m "add scheduler.Config with defaults + validation"
```

---

## Task 2: TradecronNext helper

**Files:**
- Modify (create section of): `scheduler/scheduler.go`
- Create (append to): `scheduler/scheduler_test.go`

- [ ] **Step 1: Write the failing test**

Create `scheduler/scheduler_test.go`:

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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/scheduler"
)

var _ = Describe("TradecronNext", func() {
	It("returns a future time for @monthend", func() {
		now := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)
		next, err := scheduler.TradecronNext("@monthend", now)
		Expect(err).NotTo(HaveOccurred())
		Expect(next.After(now)).To(BeTrue())
	})

	It("returns an error for an unparseable schedule", func() {
		_, err := scheduler.TradecronNext("not-a-schedule", time.Now())
		Expect(err).To(HaveOccurred())
	})
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./scheduler/... -v`
Expected: compile error (scheduler.TradecronNext / Scheduler type not defined).

- [ ] **Step 3: Implement scheduler.go skeleton + TradecronNext**

Create `scheduler/scheduler.go`:

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

// Package scheduler runs an in-process ticker that picks up due continuous
// portfolios, advances their next_run_at via tradecron, and submits each to
// the backtest dispatcher.
package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/penny-vault/pvbt/tradecron"
)

// NextRunFunc computes the next scheduled execution for a tradecron schedule.
// Returning an error causes the row to be skipped by ClaimDueContinuous.
type NextRunFunc func(schedule string, now time.Time) (time.Time, error)

// Claim is a portfolio picked up by a scheduler tick, with its newly-advanced
// next_run_at already committed in the claim tx.
type Claim struct {
	PortfolioID uuid.UUID
	Schedule    string
	NextRunAt   time.Time
}

// PortfolioStore is the subset of portfolio store operations the scheduler
// needs. Implementations claim due portfolios in a single tx that advances
// next_run_at before commit.
type PortfolioStore interface {
	ClaimDueContinuous(ctx context.Context, before time.Time, batchSize int, nextRun NextRunFunc) ([]Claim, error)
}

// Dispatcher is the subset of backtest.Dispatcher the scheduler needs.
type Dispatcher interface {
	Submit(ctx context.Context, portfolioID uuid.UUID) (runID uuid.UUID, err error)
}

// TradecronNext is the production NextRunFunc. It parses schedule via
// tradecron.New with RegularHours.
func TradecronNext(schedule string, now time.Time) (time.Time, error) {
	tc, err := tradecron.New(schedule, tradecron.RegularHours)
	if err != nil {
		return time.Time{}, fmt.Errorf("tradecron.New(%q): %w", schedule, err)
	}
	return tc.Next(now), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./scheduler/... -v`
Expected: PASS (2 new specs).

- [ ] **Step 5: Commit**

```bash
git add scheduler/scheduler.go scheduler/scheduler_test.go
git commit -m "add scheduler.TradecronNext helper backed by pvbt/tradecron"
```

---

## Task 3: Scheduler struct + Run skeleton (context cancel exits)

**Files:**
- Modify: `scheduler/scheduler.go`
- Modify: `scheduler/scheduler_test.go`

- [ ] **Step 1: Append the failing test**

Append to `scheduler/scheduler_test.go`:

```go
// --- Scheduler.Run tests ---

type stubStore struct {
	claimCalls int
	claims     []scheduler.Claim
	err        error
}

func (s *stubStore) ClaimDueContinuous(_ context.Context, _ time.Time, _ int, _ scheduler.NextRunFunc) ([]scheduler.Claim, error) {
	s.claimCalls++
	if s.err != nil {
		return nil, s.err
	}
	return s.claims, nil
}

type stubDispatcher struct {
	submitCalls int
	err         error
}

func (d *stubDispatcher) Submit(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	d.submitCalls++
	if d.err != nil {
		return uuid.Nil, d.err
	}
	return uuid.Must(uuid.NewV7()), nil
}

func stubNextRun(_ string, now time.Time) (time.Time, error) {
	return now.Add(time.Hour), nil
}

var _ = Describe("Scheduler.Run", func() {
	It("exits cleanly when context is cancelled", func() {
		store := &stubStore{}
		disp := &stubDispatcher{}
		sched := scheduler.New(scheduler.Config{TickInterval: time.Hour, BatchSize: 32},
			store, disp, stubNextRun)

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		err := sched.Run(ctx)
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())
	})
})
```

Also add to the imports at the top of `scheduler/scheduler_test.go`:

```go
import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/scheduler"
)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./scheduler/... -v`
Expected: compile error (`scheduler.New` / `(*Scheduler).Run` not defined).

- [ ] **Step 3: Add Scheduler, New, and Run skeleton**

Append to `scheduler/scheduler.go`:

```go
// Scheduler owns the tick loop that picks up due continuous portfolios.
type Scheduler struct {
	store      PortfolioStore
	dispatcher Dispatcher
	cfg        Config
	nextRun    NextRunFunc
}

// New builds a Scheduler. cfg defaults are applied.
func New(cfg Config, store PortfolioStore, dispatcher Dispatcher, nextRun NextRunFunc) *Scheduler {
	cfg.ApplyDefaults()
	return &Scheduler{
		store:      store,
		dispatcher: dispatcher,
		cfg:        cfg,
		nextRun:    nextRun,
	}
}

// Run blocks until ctx is cancelled, firing tickOnce immediately and then at
// each cfg.TickInterval. Errors in a single tick are logged but do not exit
// the loop.
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

func (s *Scheduler) tickOnce(_ context.Context) {
	// Filled in by Task 4.
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./scheduler/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add scheduler/scheduler.go scheduler/scheduler_test.go
git commit -m "add scheduler.Scheduler + Run loop skeleton"
```

---

## Task 4: tickOnce happy path

**Files:**
- Modify: `scheduler/scheduler.go`
- Modify: `scheduler/scheduler_test.go`

- [ ] **Step 1: Append the failing test**

Append inside the existing `Describe("Scheduler.Run", ...)` block in `scheduler/scheduler_test.go`:

```go
It("dispatches each claimed portfolio on the initial tick", func() {
    claims := []scheduler.Claim{
        {PortfolioID: uuid.Must(uuid.NewV7()), Schedule: "@monthend", NextRunAt: time.Now().Add(time.Hour)},
        {PortfolioID: uuid.Must(uuid.NewV7()), Schedule: "@daily", NextRunAt: time.Now().Add(24 * time.Hour)},
    }
    store := &stubStore{claims: claims}
    disp := &stubDispatcher{}
    sched := scheduler.New(scheduler.Config{TickInterval: time.Hour, BatchSize: 32},
        store, disp, stubNextRun)

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    done := make(chan error, 1)
    go func() { done <- sched.Run(ctx) }()

    Eventually(func() int { return disp.submitCalls }, time.Second).Should(Equal(2))
    cancel()
    Expect(<-done).To(MatchError(context.Canceled))
    Expect(store.claimCalls).To(BeNumerically(">=", 1))
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./scheduler/... -v -run TestScheduler/Scheduler.Run`
Expected: FAIL — `disp.submitCalls` stays at 0 because `tickOnce` is empty.

- [ ] **Step 3: Implement tickOnce**

Replace the empty `tickOnce` in `scheduler/scheduler.go` with:

```go
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
				Stringer("portfolio_id", c.PortfolioID).
				Time("next_run_at", c.NextRunAt).
				Msg("scheduler: queue full, firing skipped until next boundary")
		case err != nil:
			log.Error().Err(err).
				Stringer("portfolio_id", c.PortfolioID).
				Msg("scheduler: submit failed")
		default:
			log.Info().
				Stringer("portfolio_id", c.PortfolioID).
				Stringer("run_id", runID).
				Time("next_run_at", c.NextRunAt).
				Msg("scheduler: dispatched")
		}
	}
}
```

Update the `scheduler/scheduler.go` imports to include the new dependencies:

```go
import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/penny-vault/pvbt/tradecron"
	"github.com/rs/zerolog/log"

	"github.com/penny-vault/pv-api/backtest"
)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./scheduler/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add scheduler/scheduler.go scheduler/scheduler_test.go
git commit -m "add scheduler.tickOnce happy-path dispatch"
```

---

## Task 5: tickOnce error paths (queue full, submit error, claim error)

**Files:**
- Modify: `scheduler/scheduler_test.go`

- [ ] **Step 1: Append the failing tests**

Append inside the existing `Describe("Scheduler.Run", ...)` block:

```go
It("continues past ErrQueueFull and dispatches remaining claims", func() {
    claims := []scheduler.Claim{
        {PortfolioID: uuid.Must(uuid.NewV7()), Schedule: "@daily"},
        {PortfolioID: uuid.Must(uuid.NewV7()), Schedule: "@daily"},
    }
    store := &stubStore{claims: claims}
    disp := &stubDispatcher{err: backtest.ErrQueueFull}
    sched := scheduler.New(scheduler.Config{TickInterval: time.Hour, BatchSize: 32},
        store, disp, stubNextRun)

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    done := make(chan error, 1)
    go func() { done <- sched.Run(ctx) }()

    Eventually(func() int { return disp.submitCalls }, time.Second).Should(Equal(2))
    cancel()
    Expect(<-done).To(MatchError(context.Canceled))
})

It("continues past a generic dispatcher error", func() {
    claims := []scheduler.Claim{
        {PortfolioID: uuid.Must(uuid.NewV7()), Schedule: "@daily"},
        {PortfolioID: uuid.Must(uuid.NewV7()), Schedule: "@daily"},
    }
    store := &stubStore{claims: claims}
    disp := &stubDispatcher{err: errors.New("pool closed")}
    sched := scheduler.New(scheduler.Config{TickInterval: time.Hour, BatchSize: 32},
        store, disp, stubNextRun)

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    done := make(chan error, 1)
    go func() { done <- sched.Run(ctx) }()

    Eventually(func() int { return disp.submitCalls }, time.Second).Should(Equal(2))
    cancel()
    Expect(<-done).To(MatchError(context.Canceled))
})

It("does not panic or exit when ClaimDueContinuous errors", func() {
    store := &stubStore{err: errors.New("db down")}
    disp := &stubDispatcher{}
    sched := scheduler.New(scheduler.Config{TickInterval: 10 * time.Millisecond, BatchSize: 32},
        store, disp, stubNextRun)

    ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
    defer cancel()
    err := sched.Run(ctx)
    Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())
    Expect(store.claimCalls).To(BeNumerically(">=", 2))
    Expect(disp.submitCalls).To(Equal(0))
})
```

Ensure the test imports include `"github.com/penny-vault/pv-api/backtest"` and `"errors"` (they should already from Task 3 and 4; add if missing).

- [ ] **Step 2: Run tests to verify they pass**

The tickOnce impl from Task 4 already handles all of these correctly (returns early on claim err; switch on submit err with no goto-abort). Run:

Run: `go test ./scheduler/... -v`
Expected: PASS — all new specs green without implementation changes. If any fail, compare the logged output against tickOnce and fix the logic.

- [ ] **Step 3: Commit**

```bash
git add scheduler/scheduler_test.go
git commit -m "add scheduler tick error-path specs (queue full, submit err, claim err)"
```

---

## Task 6: Immediate first tick at startup

**Files:**
- Modify: `scheduler/scheduler_test.go`

- [ ] **Step 1: Append the failing test**

Append inside the existing `Describe("Scheduler.Run", ...)` block:

```go
It("fires the first tick immediately without waiting for TickInterval", func() {
    claims := []scheduler.Claim{
        {PortfolioID: uuid.Must(uuid.NewV7()), Schedule: "@daily"},
    }
    store := &stubStore{claims: claims}
    disp := &stubDispatcher{}
    // TickInterval far in the future; the only way Submit fires is the
    // initial tick before the ticker.
    sched := scheduler.New(scheduler.Config{TickInterval: time.Hour, BatchSize: 32},
        store, disp, stubNextRun)

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    done := make(chan error, 1)
    go func() { done <- sched.Run(ctx) }()

    Eventually(func() int { return disp.submitCalls }, 200*time.Millisecond).Should(Equal(1))
    cancel()
    <-done
})
```

- [ ] **Step 2: Run test to verify it passes**

Run: `go test ./scheduler/... -v`
Expected: PASS — Task 3's `Run` already invokes `tickOnce` before entering the ticker loop.

- [ ] **Step 3: Commit**

```bash
git add scheduler/scheduler_test.go
git commit -m "add scheduler spec verifying immediate first tick"
```

---

## Task 7: Add NextRunAt to Portfolio + DB columns

**Files:**
- Modify: `portfolio/types.go`
- Modify: `portfolio/db.go`

- [ ] **Step 1: Add NextRunAt to Portfolio struct**

In `portfolio/types.go`, extend the `Portfolio` struct. Replace the existing struct with:

```go
// Portfolio is the internal representation of a portfolios row — config
// fields only. Derived summary columns (`current_value`, `ytd_return`,
// JSONB blobs) are not exposed here; Plan 5 adds a separate derived-row
// shape when the runner starts populating those columns.
type Portfolio struct {
	ID           uuid.UUID
	OwnerSub     string
	Slug         string
	Name         string
	StrategyCode string
	StrategyVer  string
	Parameters   map[string]any
	PresetName   *string
	Benchmark    string
	Mode         Mode
	Schedule     *string
	Status       Status
	LastRunAt    *time.Time
	NextRunAt    *time.Time
	LastError    *string
	SnapshotPath *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
```

- [ ] **Step 2: Add next_run_at to portfolioColumns + scan + Insert**

In `portfolio/db.go`:

Replace `portfolioColumns`:

```go
const portfolioColumns = `
	id, owner_sub, slug, name, strategy_code, strategy_ver, parameters,
	preset_name, benchmark, mode, schedule, status, last_run_at, next_run_at,
	last_error, snapshot_path, created_at, updated_at
`
```

Replace the Insert function body to include `next_run_at`:

```go
func Insert(ctx context.Context, pool *pgxpool.Pool, p Portfolio) error {
	paramsJSON, err := json.Marshal(p.Parameters)
	if err != nil {
		return fmt.Errorf("marshaling parameters: %w", err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO portfolios (
			owner_sub, slug, name, strategy_code, strategy_ver, parameters,
			preset_name, benchmark, mode, schedule, status, next_run_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`, p.OwnerSub, p.Slug, p.Name, p.StrategyCode, p.StrategyVer, paramsJSON,
		p.PresetName, p.Benchmark, string(p.Mode), p.Schedule, string(p.Status), p.NextRunAt)
	if err != nil {
		if uniqueViolation(err) {
			return ErrDuplicateSlug
		}
		return fmt.Errorf("inserting portfolio: %w", err)
	}
	return nil
}
```

Replace the `scan` function body to read next_run_at (between LastRunAt and LastError):

```go
func scan(r scanner) (Portfolio, error) {
	var (
		p          Portfolio
		modeStr    string
		statusStr  string
		paramsJSON []byte
	)
	err := r.Scan(
		&p.ID, &p.OwnerSub, &p.Slug, &p.Name, &p.StrategyCode, &p.StrategyVer,
		&paramsJSON, &p.PresetName, &p.Benchmark, &modeStr, &p.Schedule,
		&statusStr, &p.LastRunAt, &p.NextRunAt, &p.LastError, &p.SnapshotPath,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return Portfolio{}, err
	}
	p.Mode = Mode(modeStr)
	p.Status = Status(statusStr)
	if len(paramsJSON) > 0 {
		if err := json.Unmarshal(paramsJSON, &p.Parameters); err != nil {
			return Portfolio{}, fmt.Errorf("unmarshaling parameters: %w", err)
		}
	}
	return p, nil
}
```

- [ ] **Step 3: Verify existing tests still pass**

Run: `go test ./portfolio/... -v`
Expected: PASS — existing tests construct `Portfolio` without `NextRunAt` (zero value is nil, which is fine).

- [ ] **Step 4: Commit**

```bash
git add portfolio/types.go portfolio/db.go
git commit -m "add NextRunAt to portfolio.Portfolio and DB I/O paths"
```

---

## Task 8: portfolio.ClaimDueContinuous SQL + Store wiring

**Files:**
- Modify: `portfolio/db.go`
- Modify: `portfolio/store.go`

- [ ] **Step 1: Add DueContinuous, NextRunFunc, and ClaimDueContinuous**

Append to `portfolio/db.go` (before the `scanner` type definition):

```go
// DueContinuous is a portfolio picked up by a scheduler tick, with its new
// next_run_at already committed inside the claim tx.
type DueContinuous struct {
	PortfolioID uuid.UUID
	Schedule    string
	NextRunAt   time.Time
}

// NextRunFunc computes the next scheduled execution time for a tradecron
// schedule string. Returning an error causes that row to be skipped by
// ClaimDueContinuous without aborting the batch.
type NextRunFunc func(schedule string, now time.Time) (time.Time, error)

// ClaimDueContinuous claims up to batchSize due continuous portfolios and
// advances their next_run_at in a single Postgres transaction. Rows are
// selected FOR UPDATE SKIP LOCKED so concurrent instances never pick up the
// same portfolio; the UPDATE inside the same tx means subsequent ticks
// see the advanced next_run_at.
//
// nextRun is invoked per row to compute the new next_run_at. An error from
// nextRun causes that single row to be skipped (logged); other rows in the
// batch still process.
func ClaimDueContinuous(ctx context.Context, pool *pgxpool.Pool, before time.Time,
	batchSize int, nextRun NextRunFunc) ([]DueContinuous, error) {

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback on failure is best-effort

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
				Stringer("portfolio_id", p.id).
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

Update the imports at the top of `portfolio/db.go` to include the logger:

```go
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
)
```

- [ ] **Step 2: Add ClaimDueContinuous to the Store interface and PoolStore**

In `portfolio/store.go`, extend the `Store` interface and add the forwarder on `PoolStore`.

Replace the `Store` interface:

```go
// Store is the subset of db operations the handler needs.
type Store interface {
	RunStore
	List(ctx context.Context, ownerSub string) ([]Portfolio, error)
	Get(ctx context.Context, ownerSub, slug string) (Portfolio, error)
	Insert(ctx context.Context, p Portfolio) error
	UpdateName(ctx context.Context, ownerSub, slug, name string) error
	Delete(ctx context.Context, ownerSub, slug string) error
	ClaimDueContinuous(ctx context.Context, before time.Time, batchSize int, nextRun NextRunFunc) ([]DueContinuous, error)
}
```

Append this method to `PoolStore` in `portfolio/store.go`:

```go
// ClaimDueContinuous claims due continuous portfolios for the scheduler and
// advances their next_run_at in the same tx.
func (p PoolStore) ClaimDueContinuous(ctx context.Context, before time.Time,
	batchSize int, nextRun NextRunFunc) ([]DueContinuous, error) {
	return ClaimDueContinuous(ctx, p.Pool, before, batchSize, nextRun)
}
```

- [ ] **Step 3: Update existing fakeStore in handler_test.go to satisfy Store**

Append to `portfolio/handler_test.go` (below the existing `Delete` method on `fakeStore`):

```go
// ClaimDueContinuous stub — handler tests do not exercise the scheduler path.
func (f *fakeStore) ClaimDueContinuous(_ context.Context, _ time.Time, _ int,
	_ portfolio.NextRunFunc) ([]portfolio.DueContinuous, error) {
	return nil, nil
}
```

- [ ] **Step 4: Verify it compiles and all existing tests pass**

Run: `go build ./... && go test ./portfolio/... -v`
Expected: PASS (no new specs; existing specs still green).

- [ ] **Step 5: Commit**

```bash
git add portfolio/db.go portfolio/store.go portfolio/handler_test.go
git commit -m "add portfolio.ClaimDueContinuous with FOR UPDATE SKIP LOCKED claim tx"
```

---

## Task 9: Schedule validation in portfolio.ValidateCreate

**Files:**
- Modify: `portfolio/validate.go`
- Modify: `portfolio/validate_test.go`

- [ ] **Step 1: Append failing test for invalid schedule**

Append a new `Describe` block to `portfolio/validate_test.go`:

```go
var _ = Describe("Schedule validation", func() {
	strategyFixture := func() strategy.Strategy {
		ver := "v1.0.0"
		return strategy.Strategy{
			ShortCode:    "adm",
			InstalledVer: &ver,
			DescribeJSON: []byte(`{
				"shortCode": "adm",
				"benchmark": "SPY",
				"parameters": [],
				"presets": []
			}`),
		}
	}

	It("accepts a valid tradecron schedule on continuous", func() {
		req := portfolio.CreateRequest{
			Name: "x", StrategyCode: "adm", Parameters: map[string]any{},
			Mode: portfolio.ModeContinuous, Schedule: "@monthend",
		}
		_, err := portfolio.ValidateCreate(req, strategyFixture())
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects an invalid schedule on continuous with ErrInvalidSchedule", func() {
		req := portfolio.CreateRequest{
			Name: "x", StrategyCode: "adm", Parameters: map[string]any{},
			Mode: portfolio.ModeContinuous, Schedule: "garbage",
		}
		_, err := portfolio.ValidateCreate(req, strategyFixture())
		Expect(errors.Is(err, portfolio.ErrInvalidSchedule)).To(BeTrue())
	})
})
```

If `validate_test.go` lacks the `errors` or `strategy` imports, add them. (Existing tests likely import these already; verify before adding duplicates.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./portfolio/... -v -run TestPortfolio`
Expected: FAIL — `ErrInvalidSchedule` does not exist.

- [ ] **Step 3: Add the sentinel + validation**

In `portfolio/validate.go`:

Add a new sentinel near the existing ones:

```go
var (
	ErrLiveNotSupported        = errors.New("live mode unavailable")
	ErrScheduleRequired        = errors.New("schedule required for continuous mode")
	ErrScheduleForbidden       = errors.New("schedule forbidden for non-continuous mode")
	ErrInvalidSchedule         = errors.New("invalid schedule")
	ErrStrategyNotReady        = errors.New("strategy not installed")
	ErrStrategyVersionMismatch = errors.New("strategy version not installed")
	ErrUnknownParameter        = errors.New("unknown parameter")
	ErrMissingParameter        = errors.New("missing required parameter")
	ErrInvalidStrategyDescribe = errors.New("strategy describe JSON is malformed")
	ErrUnsupportedMode         = errors.New("unsupported mode")
)
```

Extend `validateMode` to tradecron-check the schedule when mode is continuous. Replace the `case ModeContinuous:` block:

```go
	case ModeContinuous:
		if req.Schedule == "" {
			return fmt.Errorf("%w", ErrScheduleRequired)
		}
		if _, err := tradecron.New(req.Schedule, tradecron.RegularHours); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidSchedule, err)
		}
```

Update imports in `portfolio/validate.go`:

```go
import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/penny-vault/pvbt/tradecron"

	"github.com/penny-vault/pv-api/strategy"
)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./portfolio/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add portfolio/validate.go portfolio/validate_test.go
git commit -m "add schedule validation in portfolio.ValidateCreate via tradecron"
```

---

## Task 10: Handler.Create bootstraps next_run_at and forces runNow for continuous

**Files:**
- Modify: `portfolio/handler.go`
- Modify: `portfolio/handler_test.go`

- [ ] **Step 1: Inspect existing handler_test.go helpers**

Before writing new tests, read the full `portfolio/handler_test.go` to see what helpers already exist (`fakeStore`, likely a strategy-store fake, a fiber auth-injector, and a dispatcher fake of some kind). Reuse them. The task below assumes you add only the specs and any missing helpers.

Also read `strategy/store.go` to see the full `strategy.ReadStore` interface — any existing strategy fake must satisfy it, and your added specs must use the same fake.

- [ ] **Step 2: Append failing specs**

Add a `Describe` block for continuous-mode Create behavior to `portfolio/handler_test.go`. Where the existing dispatcher fake is reusable, use it; otherwise declare a local `countingDispatcher` (pointer-receiver `Submit` to track calls, canned return values):

```go
// countingDispatcher is a portfolio.Dispatcher that records calls and
// returns a canned (runID, err). Declared at package scope so multiple
// Describes can use it; skip this declaration if the file already has
// an equivalent helper.
type countingDispatcher struct {
	calls  int
	runID  uuid.UUID
	err    error
}

func (d *countingDispatcher) Submit(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	d.calls++
	return d.runID, d.err
}

var _ = Describe("Create for continuous portfolios", func() {
	validBody := `{
		"name": "test",
		"strategyCode": "adm",
		"parameters": {},
		"mode": "continuous",
		"schedule": "@monthend"
	}`

	var strategies portfolio.Handler // placeholder to satisfy compile; replace below

	_ = strategies

	// Build a strategy fake with an installed version and a minimal
	// describe_json. Reuse an existing file-level fakeStrategyStore if
	// present; otherwise add one at package scope.
	installedVer := "v1.0.0"
	describeJSON := []byte(`{
		"shortCode": "adm",
		"benchmark": "SPY",
		"parameters": [],
		"presets": []
	}`)

	newHandler := func(store portfolio.Store, disp portfolio.Dispatcher) *portfolio.Handler {
		stratStore := &fakeStrategyStore{rows: []strategy.Strategy{{
			ShortCode:    "adm",
			InstalledVer: &installedVer,
			DescribeJSON: describeJSON,
		}}}
		return portfolio.NewHandler(store, stratStore, nil, disp)
	}

	newApp := func(h *portfolio.Handler) *fiber.App {
		app := fiber.New()
		app.Post("/portfolios", authInject("user-1"), h.Create)
		return app
	}

	It("submits a run even when runNow is omitted", func() {
		store := &fakeStore{}
		disp := &countingDispatcher{runID: uuid.Must(uuid.NewV7())}
		h := newHandler(store, disp)

		req := httptest.NewRequest("POST", "/portfolios", bytes.NewBufferString(validBody))
		req.Header.Set("Content-Type", "application/json")
		resp, err := newApp(h).Test(req, -1)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusCreated))
		Expect(disp.calls).To(Equal(1))
	})

	It("bootstraps next_run_at on the inserted portfolio", func() {
		store := &fakeStore{}
		disp := &countingDispatcher{runID: uuid.Must(uuid.NewV7())}
		h := newHandler(store, disp)

		req := httptest.NewRequest("POST", "/portfolios", bytes.NewBufferString(validBody))
		req.Header.Set("Content-Type", "application/json")
		resp, err := newApp(h).Test(req, -1)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusCreated))
		Expect(store.rows).To(HaveLen(1))
		Expect(store.rows[0].NextRunAt).NotTo(BeNil())
		Expect(store.rows[0].NextRunAt.After(time.Now().Add(-time.Minute))).To(BeTrue())
	})

	It("returns 422 for an invalid schedule", func() {
		store := &fakeStore{}
		disp := &countingDispatcher{runID: uuid.Must(uuid.NewV7())}
		h := newHandler(store, disp)

		body := `{"name":"x","strategyCode":"adm","parameters":{},"mode":"continuous","schedule":"garbage"}`
		req := httptest.NewRequest("POST", "/portfolios", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := newApp(h).Test(req, -1)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusUnprocessableEntity))
		Expect(store.rows).To(BeEmpty())
		Expect(disp.calls).To(Equal(0))
	})

	It("rolls back the portfolio row and returns 503 when dispatcher is full", func() {
		store := &fakeStore{}
		disp := &countingDispatcher{err: portfolio.ErrQueueFull}
		h := newHandler(store, disp)

		req := httptest.NewRequest("POST", "/portfolios", bytes.NewBufferString(validBody))
		req.Header.Set("Content-Type", "application/json")
		resp, err := newApp(h).Test(req, -1)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(fiber.StatusServiceUnavailable))
		Expect(store.rows).To(BeEmpty())
	})
})
```

If `fakeStrategyStore` or `authInject` is not already declared in the file, add it. Minimal versions:

```go
type fakeStrategyStore struct {
	rows []strategy.Strategy
}

func (s *fakeStrategyStore) Get(_ context.Context, code string) (strategy.Strategy, error) {
	for _, r := range s.rows {
		if r.ShortCode == code {
			return r, nil
		}
	}
	return strategy.Strategy{}, strategy.ErrNotFound
}

func authInject(sub string) fiber.Handler {
	return func(c fiber.Ctx) error {
		c.Locals(types.AuthSubjectKey{}, sub)
		return c.Next()
	}
}
```

Before adding either, run `grep -n 'fakeStrategyStore\|authInject' portfolio/handler_test.go` — only add what's missing. If `strategy.ReadStore` requires methods beyond `Get` (check `strategy/store.go`), add stub implementations to `fakeStrategyStore` so it satisfies the full interface.

Drop the placeholder `var strategies portfolio.Handler` and `_ = strategies` lines before committing — they're scaffolding to keep the editor happy while you paste the block in, not production code.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./portfolio/... -v`
Expected: FAIL — `fakeStrategyStore` may not satisfy `strategy.ReadStore`, and the Create handler does not bootstrap next_run_at or force runNow.

If the compile fails because of a `ReadStore` signature mismatch, open `strategy/store.go`, find the `ReadStore` interface, and add stub methods on `fakeStrategyStore` for each missing method (return zero values / `ErrNotFound`).

- [ ] **Step 3: Implement schedule bootstrap + forced runNow + rollback**

In `portfolio/handler.go`:

Replace `buildPortfolio` with:

```go
// buildPortfolio constructs a Portfolio value from a validated create request.
// For continuous portfolios, next_run_at is bootstrapped to the next
// tradecron boundary.
func (h *Handler) buildPortfolio(ownerSub string, norm CreateRequest, s strategy.Strategy) (Portfolio, error) {
	var describe strategy.Describe
	if err := json.Unmarshal(s.DescribeJSON, &describe); err != nil {
		return Portfolio{}, errStrategyMalformed
	}
	slug, err := Slug(norm, describe)
	if err != nil {
		return Portfolio{}, err
	}
	presetName := presetMatch(norm.Parameters, describe)
	p := Portfolio{
		OwnerSub:     ownerSub,
		Slug:         slug,
		Name:         norm.Name,
		StrategyCode: norm.StrategyCode,
		StrategyVer:  norm.StrategyVer,
		Parameters:   norm.Parameters,
		PresetName:   presetName,
		Benchmark:    norm.Benchmark,
		Mode:         norm.Mode,
		Status:       StatusPending,
	}
	if norm.Schedule != "" {
		sch := norm.Schedule
		p.Schedule = &sch
	}
	if norm.Mode == ModeContinuous {
		tc, err := tradecron.New(norm.Schedule, tradecron.RegularHours)
		if err != nil {
			// Should be unreachable: ValidateCreate already checks this.
			return Portfolio{}, fmt.Errorf("%w: %w", ErrInvalidSchedule, err)
		}
		nextAt := tc.Next(time.Now())
		p.NextRunAt = &nextAt
	}
	return p, nil
}
```

Add the tradecron import in `portfolio/handler.go`:

```go
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/penny-vault/pvbt/tradecron"
	"github.com/rs/zerolog/log"

	"github.com/penny-vault/pv-api/openapi"
	"github.com/penny-vault/pv-api/strategy"
	"github.com/penny-vault/pv-api/types"
)
```

Replace the final portion of `Create` (starting after the `stored, err := h.store.Get(...)` block) to force Submit for continuous and handle rollback:

```go
	// Re-read so CreatedAt / UpdatedAt / ID reflect the DB row. Falls back
	// to an in-memory view if the read fails.
	stored, err := h.store.Get(c.Context(), ownerSub, p.Slug)
	created := p
	if err == nil {
		created = stored
	}

	if status, body := h.autoTriggerOrProblem(c, created); status != 0 {
		// Rollback the portfolio row because we could not queue its first run.
		if delErr := h.store.Delete(c.Context(), ownerSub, created.Slug); delErr != nil {
			log.Warn().Err(delErr).Stringer("portfolio_id", created.ID).Msg("rollback delete failed")
		}
		return writeProblem(c, status, body.title, body.detail)
	}

	return writeJSON(c, fiber.StatusCreated, toView(created))
}
```

Delete the `maybeAutoTrigger` method and replace with:

```go
// autoTriggerOrProblem dispatches a backtest run for modes that require one
// and returns a problem status + body on failure. A zero status means the
// caller should proceed normally.
func (h *Handler) autoTriggerOrProblem(c fiber.Ctx, created Portfolio) (int, problemBody) {
	if h.dispatcher == nil {
		// Runner not wired — Plan 4 behavior. Caller proceeds as 201.
		return 0, problemBody{}
	}
	switch created.Mode {
	case ModeOneShot, ModeContinuous:
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
	}
	return 0, problemBody{}
}

type problemBody struct {
	title  string
	detail string
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./portfolio/... -v`
Expected: PASS.

If any existing Create spec fails because it counted on the previous swallow-warn behavior for one_shot queue-full, update it to expect 503 + row rollback. That's the intended new behavior.

- [ ] **Step 5: Commit**

```bash
git add portfolio/handler.go portfolio/handler_test.go
git commit -m "portfolio.Create: bootstrap next_run_at, force Submit, rollback on dispatch failure"
```

---

## Task 11: cmd/config.go + cmd/viper.go — schedulerConf + defaults

**Files:**
- Modify: `cmd/config.go`
- Modify: `cmd/viper.go`

- [ ] **Step 1: Add schedulerConf to Config**

In `cmd/config.go`, extend the `Config` struct and add `schedulerConf`:

```go
// Config is the top-level pvapi configuration shape. New sections are added
// as later plans land (runner, scheduler, ...).
type Config struct {
	Log       logConf
	Server    serverConf
	Auth0     auth0Conf
	GitHub    githubConf
	Strategy  strategyConf
	Backtest  backtestConf
	Runner    runnerConf
	Scheduler schedulerConf
}
```

Append at the bottom of the file (before `var conf Config`):

```go
// schedulerConf controls the in-process scheduler that picks up due
// continuous portfolios and submits them to the backtest dispatcher.
type schedulerConf struct {
	TickInterval time.Duration `mapstructure:"tick_interval"`
	BatchSize    int           `mapstructure:"batch_size"`
	Enabled      bool          `mapstructure:"enabled"`
}
```

- [ ] **Step 2: Add defaults**

In `cmd/viper.go`, extend `setViperDefaults`:

```go
func setViperDefaults() {
	viper.SetDefault("backtest.snapshots_dir", "/var/lib/pvapi/snapshots")
	viper.SetDefault("backtest.max_concurrency", 0)
	viper.SetDefault("backtest.timeout", "15m")
	viper.SetDefault("runner.mode", "host")
	viper.SetDefault("scheduler.tick_interval", "60s")
	viper.SetDefault("scheduler.batch_size", 32)
	viper.SetDefault("scheduler.enabled", true)
}
```

- [ ] **Step 3: Verify the project still builds**

Run: `go build ./...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/config.go cmd/viper.go
git commit -m "add [scheduler] config section with tick_interval/batch_size/enabled"
```

---

## Task 12: cmd/server.go — adapters + scheduler wiring

**Files:**
- Modify: `cmd/server.go`

- [ ] **Step 1: Add scheduler adapter types**

Append these adapters to `cmd/server.go` (anywhere above `func init()`):

```go
// schedulerStoreAdapter adapts *portfolio.PoolStore to scheduler.PortfolioStore,
// translating portfolio.DueContinuous → scheduler.Claim and
// scheduler.NextRunFunc → portfolio.NextRunFunc.
type schedulerStoreAdapter struct {
	store *portfolio.PoolStore
}

func (a schedulerStoreAdapter) ClaimDueContinuous(
	ctx context.Context, before time.Time, batchSize int,
	nextRun scheduler.NextRunFunc,
) ([]scheduler.Claim, error) {
	portRun := portfolio.NextRunFunc(nextRun)
	dues, err := a.store.ClaimDueContinuous(ctx, before, batchSize, portRun)
	if err != nil {
		return nil, err
	}
	out := make([]scheduler.Claim, len(dues))
	for i, d := range dues {
		out[i] = scheduler.Claim{
			PortfolioID: d.PortfolioID,
			Schedule:    d.Schedule,
			NextRunAt:   d.NextRunAt,
		}
	}
	return out, nil
}

// schedulerDispatcherAdapter adapts *backtest.Dispatcher to scheduler.Dispatcher.
// We intentionally do NOT translate ErrQueueFull here — scheduler.tickOnce
// checks errors.Is against backtest.ErrQueueFull directly.
type schedulerDispatcherAdapter struct {
	bt *backtest.Dispatcher
}

func (a schedulerDispatcherAdapter) Submit(ctx context.Context, id uuid.UUID) (uuid.UUID, error) {
	return a.bt.Submit(ctx, id)
}
```

Add the scheduler import:

```go
import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/penny-vault/pv-api/api"
	"github.com/penny-vault/pv-api/backtest"
	"github.com/penny-vault/pv-api/portfolio"
	"github.com/penny-vault/pv-api/scheduler"
	"github.com/penny-vault/pv-api/snapshot"
	"github.com/penny-vault/pv-api/sql"
	"github.com/penny-vault/pv-api/strategy"
)
```

- [ ] **Step 2: Wire the scheduler into the server lifecycle**

In `cmd/server.go`, inside the `serverCmd.RunE` body, after `dispatcher.Start(ctx)` and after `backtest.StartupSweep(...)`, add:

```go
			if conf.Scheduler.Enabled {
				schedCfg := scheduler.Config{
					TickInterval: conf.Scheduler.TickInterval,
					BatchSize:    conf.Scheduler.BatchSize,
				}
				schedCfg.ApplyDefaults()
				if err := schedCfg.Validate(); err != nil {
					log.Fatal().Err(err).Msg("scheduler config")
				}
				sched := scheduler.New(schedCfg,
					schedulerStoreAdapter{store: portfolioStore},
					schedulerDispatcherAdapter{bt: dispatcher},
					scheduler.TradecronNext,
				)
				go func() {
					if err := sched.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
						log.Error().Err(err).Msg("scheduler exited with error")
					}
				}()
				log.Info().
					Dur("tick_interval", schedCfg.TickInterval).
					Int("batch_size", schedCfg.BatchSize).
					Msg("scheduler started")
			} else {
				log.Info().Msg("scheduler disabled")
			}
```

Place this block after the existing `backtest.StartupSweep(...)` call and before the `api.NewApp(...)` call.

- [ ] **Step 3: Verify the project builds**

Run: `go build ./...`
Expected: PASS.

- [ ] **Step 4: Run the full test suite**

Run: `go test ./... -race`
Expected: PASS across all suites.

- [ ] **Step 5: Commit**

```bash
git add cmd/server.go
git commit -m "wire scheduler into cmd/server.go with adapters + goroutine lifecycle"
```

---

## Task 13: Update plan-sequence memory

**Files:**
- Modify: `/Users/jdf/.claude/projects/-Users-jdf-Developer-penny-vault-pv-api/memory/project_pvapi_3_0_plan_sequence.md`

- [ ] **Step 1: Update the table**

In the memory file, change the Plan 6 row to:

```
| 6 | Scheduler + continuous mode | ✅ merged to `main` | In-process cron; `tradecron.Next`; continuous portfolios re-run on schedule; `next_run_at` advance driven by `FOR UPDATE SKIP LOCKED` query; shares bounded worker pool with runs dispatched from `POST /runs`. `portfolio.Create` tightens for continuous: schedule validated via `tradecron.New` at create time, `next_run_at` bootstrapped, `runNow` ignored (always auto-runs). No schema change — `next_run_at` + `idx_portfolios_due` already existed from 1_init. |
```

And change the Plan 7 row to:

```
| 7 | **Unofficial strategies** | next up | `POST /strategies` with owner-scoped visibility; ephemeral clone+build at backtest time under `/tmp/pvapi-strategies/<uuid>/`; `?include=unofficial` filter on `GET /strategies`. |
```

- [ ] **Step 2: No commit (memory file is not under git)**

Memory files live outside the repo. Skip `git add`.

---

## Self-review checklist (run once all tasks complete before handoff)

- [ ] `go test ./... -race` passes with the new scheduler suite green (≥ 8 new specs) and no regressions in other suites.
- [ ] `golangci-lint run` is clean (same config used in Plan 5).
- [ ] `scheduler/` exposes: `Config{TickInterval, BatchSize}`, `New`, `(*Scheduler).Run`, `Claim`, `NextRunFunc`, `TradecronNext`, `ErrInvalidTickInterval`, `ErrInvalidBatchSize`.
- [ ] `portfolio/` exposes: `DueContinuous`, `NextRunFunc`, `PoolStore.ClaimDueContinuous`, `ErrInvalidSchedule`.
- [ ] `portfolio.Handler.Create` for `mode=continuous` returns 422 on an unparseable schedule, inserts with a non-null `next_run_at`, and always calls `dispatcher.Submit`.
- [ ] `portfolio.Handler.Create` rolls back the inserted row and returns 503 when the dispatcher returns `portfolio.ErrQueueFull`.
- [ ] `cmd/server.go` starts the scheduler only when `scheduler.enabled=true` and logs either "scheduler started" or "scheduler disabled" at startup.
- [ ] `cmd/server.go`'s scheduler goroutine exits cleanly on `ctx.Done()` (no leaked goroutine reported by `-race`).
- [ ] No migration was added.
- [ ] No OpenAPI file changes were made.
- [ ] `go.mod` still pins `github.com/penny-vault/pvbt v0.7.5`.
- [ ] Plan-sequence memory updated: Plan 6 ✅, Plan 7 next up.
