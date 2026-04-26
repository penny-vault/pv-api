# Portfolio Strategy Upgrade Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `POST /portfolios/:slug/upgrade` for in-place strategy version upgrades with diff-then-confirm parameter handling, plus a portfolio-level `run_retention` policy defaulting to 2.

**Architecture:** Same-strategy version bump on the portfolio row (replaces `strategy_ver`, `strategy_describe_json`, `parameters`, `preset_name`) followed by an auto-triggered backtest run. Parameter compatibility is computed up front; incompatible upgrades return 409 with a structured diff and the new describe so the client can resubmit. Run retention prunes after every terminal run-state transition in a separate transaction; snapshot file deletion is best-effort.

**Tech Stack:** Go, Fiber v3, pgx/v5, go-migrate, Ginkgo + Gomega, sonic for JSON, golang-migrate file migrations under `sql/migrations`.

**Spec:** [`docs/superpowers/specs/2026-04-25-portfolio-strategy-upgrade-design.md`](../specs/2026-04-25-portfolio-strategy-upgrade-design.md)

---

## File Map

**New:**
- `sql/migrations/12_portfolio_run_retention.up.sql`
- `sql/migrations/12_portfolio_run_retention.down.sql`
- `portfolio/upgrade.go` — `Upgrade` handler + helpers
- `portfolio/upgrade_test.go` — integration tests for the upgrade endpoint
- `portfolio/prune_test.go` — retention behaviour tests

**Modified:**
- `portfolio/types.go:35` — add `RunRetention int` to `Portfolio`; add `*int` to `CreateRequest`
- `portfolio/db.go` — extend `portfolioColumns`, `Insert`, `scan`; add `ApplyUpgrade`, `PruneRuns`, `Update`(retention)
- `portfolio/store.go` — extend `Store` interface; add `PoolStore` wrappers
- `portfolio/validate.go` — add `ParameterDiff` types, `DiffParameters`, `MatchPresetName`, `validateRunRetention`
- `portfolio/validate_test.go` — `DiffParameters` and `MatchPresetName` table tests
- `portfolio/handler.go` — extend `createBody`, `patchBody`, `parsePatchBody`, `Patch`; reuse `insertAndDispatch` shape for upgrade
- `backtest/dispatcher.go` — extend orchestrator's `PortfolioStore` interface (after line 53) with `PruneRuns`
- `backtest/run.go` — call `PruneRuns` after both `MarkReadyTx` (line 142) and `MarkFailedTx` (line 213)
- `api/portfolios.go` — add stub route entry + real route (POST `:slug/upgrade`)
- `api/portfolios_test.go` — add 401 entry for upgrade
- `openapi/openapi.yaml` — document `/portfolios/{slug}/upgrade` and `run_retention` field

---

## Task 1: Migration — add `run_retention` column

**Files:**
- Create: `sql/migrations/12_portfolio_run_retention.up.sql`
- Create: `sql/migrations/12_portfolio_run_retention.down.sql`

- [ ] **Step 1: Confirm 12 is the next migration number**

Run: `ls sql/migrations/ | sort -V | tail -3`
Expected: `11_stats_error.down.sql`, `11_stats_error.up.sql`, then nothing else after 11. If something else has shipped to `12_`, bump this task to the next free integer and update every reference downstream.

- [ ] **Step 2: Write the up migration**

`sql/migrations/12_portfolio_run_retention.up.sql`:
```sql
ALTER TABLE portfolios
    ADD COLUMN run_retention INT NOT NULL DEFAULT 2;

ALTER TABLE portfolios
    ADD CONSTRAINT portfolios_run_retention_min CHECK (run_retention >= 1);
```

- [ ] **Step 3: Write the down migration**

`sql/migrations/12_portfolio_run_retention.down.sql`:
```sql
ALTER TABLE portfolios
    DROP CONSTRAINT IF EXISTS portfolios_run_retention_min;

ALTER TABLE portfolios
    DROP COLUMN IF EXISTS run_retention;
```

- [ ] **Step 4: Run the migration locally**

Run: `go test ./portfolio/... -run TestPortfolio -count=1`
Expected: PASS — the test suite calls `migrate.Up()` against a test database via `sql.NewDatabaseSchema`, so a passing run confirms the migration applies cleanly. (If your local setup uses a different harness, run `go test ./sql/...` instead.)

- [ ] **Step 5: Commit**

```bash
git add sql/migrations/12_portfolio_run_retention.up.sql sql/migrations/12_portfolio_run_retention.down.sql
git commit -m "feat(db): add portfolios.run_retention column (default 2)"
```

---

## Task 2: Plumb `RunRetention` through the Portfolio type and DB layer

**Files:**
- Modify: `portfolio/types.go:35-55` (Portfolio struct)
- Modify: `portfolio/db.go:38-43` (`portfolioColumns`)
- Modify: `portfolio/db.go:84-106` (`Insert`)
- Modify: `portfolio/db.go` (`scan` helper — find with `grep -n "func scan(" portfolio/db.go`)

- [ ] **Step 1: Write the failing test**

Append to `portfolio/handler_test.go` (or wherever round-trip insert/get tests live; search `grep -n "It(\"round trips\\|It(\"persists\\|It(\"creates a portfolio" portfolio/*_test.go` and add to that file):

```go
It("persists run_retention with a default of 2 when omitted", func() {
    p := makeTestPortfolio() // existing helper that returns a fully populated portfolio
    Expect(store.Insert(ctx, p)).To(Succeed())

    got, err := store.Get(ctx, p.OwnerSub, p.Slug)
    Expect(err).NotTo(HaveOccurred())
    Expect(got.RunRetention).To(Equal(2))
})

It("persists an explicit run_retention", func() {
    p := makeTestPortfolio()
    p.RunRetention = 5
    Expect(store.Insert(ctx, p)).To(Succeed())

    got, err := store.Get(ctx, p.OwnerSub, p.Slug)
    Expect(err).NotTo(HaveOccurred())
    Expect(got.RunRetention).To(Equal(5))
})
```

If `makeTestPortfolio` does not exist, find the existing helper used by `handler_test.go` and use it; otherwise inline a literal `Portfolio{...}`.

- [ ] **Step 2: Run the test and verify failure**

Run: `go test ./portfolio/ -run TestPortfolio -count=1 -v -ginkgo.focus="run_retention"`
Expected: FAIL with a compile error on `p.RunRetention` (field does not exist).

- [ ] **Step 3: Add the field to the Portfolio struct**

In `portfolio/types.go`, locate `type Portfolio struct {` (line 35). Add the field at the end:

```go
type Portfolio struct {
    // ...existing fields preserved...
    RunRetention int `json:"run_retention"`
}
```

Place the field after the existing JSON-mapped fields. Match the surrounding struct-tag style.

- [ ] **Step 4: Add the column to `portfolioColumns`**

In `portfolio/db.go` line 38-43, replace the constant:

```go
const portfolioColumns = `
    id, owner_sub, slug, name, strategy_code, strategy_ver, strategy_clone_url,
    strategy_describe_json, parameters,
    preset_name, benchmark, start_date, end_date, status, last_run_at,
    last_error, snapshot_path, created_at, updated_at, run_retention
`
```

- [ ] **Step 5: Update `scan` to read the new column**

Find `scan` with `grep -n "func scan(" portfolio/db.go`. Append `&p.RunRetention` to the row.Scan(...) argument list, after `&p.UpdatedAt`. Order MUST match `portfolioColumns`.

- [ ] **Step 6: Update `Insert` to write `run_retention` (with COALESCE default 2)**

Replace `portfolio/db.go:89-98` so `RunRetention` is included. The simplest approach: make the column optional with a DB-side default by NOT including it in the INSERT when zero, OR always include and require the caller to set it. We will go with the second approach since `RunRetention=0` is invalid (CHECK constraint `>= 1`):

```go
_, err = pool.Exec(ctx, `
    INSERT INTO portfolios (
        owner_sub, slug, name, strategy_code, strategy_ver,
        strategy_clone_url, strategy_describe_json, parameters,
        preset_name, benchmark, start_date, end_date, status, run_retention
    ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
`, p.OwnerSub, p.Slug, p.Name, p.StrategyCode, p.StrategyVer,
    p.StrategyCloneURL, p.StrategyDescribeJSON, paramsJSON,
    p.PresetName, p.Benchmark, p.StartDate, p.EndDate,
    string(p.Status), retentionOrDefault(p.RunRetention))
```

Add a helper at the bottom of `portfolio/db.go`:

```go
func retentionOrDefault(v int) int {
    if v <= 0 {
        return 2
    }
    return v
}
```

- [ ] **Step 7: Run the test and verify pass**

Run: `go test ./portfolio/ -run TestPortfolio -count=1 -v -ginkgo.focus="run_retention"`
Expected: PASS for both test cases.

- [ ] **Step 8: Run the full portfolio suite to catch regressions**

Run: `go test ./portfolio/... -count=1`
Expected: PASS. Existing fixtures will get `RunRetention=2` from `retentionOrDefault`.

- [ ] **Step 9: Commit**

```bash
git add portfolio/types.go portfolio/db.go portfolio/handler_test.go
git commit -m "feat(portfolio): plumb RunRetention through Portfolio struct and DB"
```

---

## Task 3: Accept `run_retention` on POST `/portfolios`

**Files:**
- Modify: `portfolio/types.go:58` (CreateRequest)
- Modify: `portfolio/handler.go:413` (createBody) and `handler.go:424` (`toRequest`)
- Modify: `portfolio/validate.go` (add `validateRunRetention`)
- Modify: `portfolio/handler.go:254` (`buildPortfolio`) to copy through

- [ ] **Step 1: Write the failing test**

Append to `portfolio/handler_test.go` (in the `Describe("Create")` block; find with `grep -n 'Describe("Create"' portfolio/handler_test.go`):

```go
It("accepts run_retention=5 and persists it", func() {
    body := []byte(`{
        "name":"foo",
        "strategyCode":"adm",
        "parameters":{"riskOn":"SPY"},
        "runRetention":5
    }`)
    resp := postJSON("/portfolios", body, ownerSub)
    Expect(resp.StatusCode).To(Equal(201))

    p, err := store.Get(ctx, ownerSub, slugFrom(resp))
    Expect(err).NotTo(HaveOccurred())
    Expect(p.RunRetention).To(Equal(5))
})

It("defaults run_retention to 2 when omitted", func() {
    body := []byte(`{
        "name":"bar",
        "strategyCode":"adm",
        "parameters":{"riskOn":"SPY"}
    }`)
    resp := postJSON("/portfolios", body, ownerSub)
    Expect(resp.StatusCode).To(Equal(201))

    p, err := store.Get(ctx, ownerSub, slugFrom(resp))
    Expect(err).NotTo(HaveOccurred())
    Expect(p.RunRetention).To(Equal(2))
})

It("rejects run_retention=0 with 422", func() {
    body := []byte(`{
        "name":"baz",
        "strategyCode":"adm",
        "parameters":{"riskOn":"SPY"},
        "runRetention":0
    }`)
    resp := postJSON("/portfolios", body, ownerSub)
    Expect(resp.StatusCode).To(Equal(422))
})
```

`postJSON` and `slugFrom` are existing helpers — use the same shape used by sibling Create tests in this file.

- [ ] **Step 2: Run the test and verify failure**

Run: `go test ./portfolio/ -run TestPortfolio -count=1 -v -ginkgo.focus="run_retention"`
Expected: FAIL — `runRetention` field not parsed; default not set; validation absent.

- [ ] **Step 3: Add the field to `CreateRequest`**

In `portfolio/types.go` line 58 in `type CreateRequest struct`, add:

```go
RunRetention *int `json:"run_retention"`
```

Use a pointer so we can distinguish "omitted" from "explicit zero".

- [ ] **Step 4: Add the field to `createBody` and `toRequest`**

In `portfolio/handler.go` line 413 (`type createBody struct`), add:

```go
RunRetention *int `json:"runRetention"`
```

In `portfolio/handler.go:424` (`toRequest`), pass it through:

```go
return CreateRequest{
    // ...existing assignments...
    RunRetention: b.RunRetention,
}
```

- [ ] **Step 5: Add `validateRunRetention` to validate.go**

In `portfolio/validate.go`, add near the other validators:

```go
// ErrInvalidRunRetention is returned when run_retention is supplied but < 1.
var ErrInvalidRunRetention = errors.New("run_retention must be >= 1")

func validateRunRetention(v *int) error {
    if v == nil {
        return nil
    }
    if *v < 1 {
        return ErrInvalidRunRetention
    }
    return nil
}
```

Then call it from the existing `ValidateCreate` function (find with `grep -n "func ValidateCreate" portfolio/validate.go`) early in the function:

```go
if err := validateRunRetention(req.RunRetention); err != nil {
    return CreateRequest{}, err
}
```

- [ ] **Step 6: Apply default + copy through in `buildPortfolio`**

In `portfolio/handler.go:254` (`buildPortfolio`), set the field on the returned Portfolio:

```go
retention := 2
if norm.RunRetention != nil {
    retention = *norm.RunRetention
}
// ...build portfolio struct, then before returning:
p.RunRetention = retention
```

- [ ] **Step 7: Run the test and verify pass**

Run: `go test ./portfolio/ -run TestPortfolio -count=1 -v -ginkgo.focus="run_retention"`
Expected: PASS for all three test cases.

- [ ] **Step 8: Commit**

```bash
git add portfolio/types.go portfolio/handler.go portfolio/validate.go portfolio/handler_test.go
git commit -m "feat(portfolio): accept run_retention on POST /portfolios"
```

---

## Task 4: Accept `run_retention` on PATCH `/portfolios/:slug`

**Files:**
- Modify: `portfolio/handler.go:311-345` (patchBody, parsePatchBody, Patch)
- Modify: `portfolio/store.go:32` (Store interface) and `store.go:61-66` (PoolStore)
- Modify: `portfolio/db.go` (add `UpdateRunRetention`)

- [ ] **Step 1: Write the failing test**

Append to the existing `Describe("Patch"...)` block in `portfolio/handler_test.go`:

```go
It("updates run_retention via PATCH", func() {
    p := mustCreatePortfolio() // helper that creates a portfolio and returns slug
    resp := patchJSON("/portfolios/"+p.Slug, []byte(`{"runRetention":4}`), ownerSub)
    Expect(resp.StatusCode).To(Equal(200))

    got, err := store.Get(ctx, ownerSub, p.Slug)
    Expect(err).NotTo(HaveOccurred())
    Expect(got.RunRetention).To(Equal(4))
})

It("rejects PATCH with run_retention=0", func() {
    p := mustCreatePortfolio()
    resp := patchJSON("/portfolios/"+p.Slug, []byte(`{"runRetention":0}`), ownerSub)
    Expect(resp.StatusCode).To(Equal(422))
})
```

- [ ] **Step 2: Run the test and verify failure**

Run: `go test ./portfolio/ -run TestPortfolio -count=1 -v -ginkgo.focus="run_retention via PATCH"`
Expected: FAIL — `runRetention` rejected by `parsePatchBody` as an unknown field.

- [ ] **Step 3: Extend `patchBody` and `parsePatchBody`**

In `portfolio/handler.go:311`:

```go
type patchBody struct {
    Name         string `json:"name"`
    StartDate    string `json:"startDate"`
    EndDate      string `json:"endDate"`
    RunRetention *int   `json:"runRetention"`
}
```

In `portfolio/handler.go:324`, expand the allowlist:

```go
allowed := map[string]bool{"name": true, "startDate": true, "endDate": true, "runRetention": true}
```

After the date validation block (around line 343), add:

```go
if err := validateRunRetention(body.RunRetention); err != nil {
    return patchBody{}, nil, nil, err
}
```

- [ ] **Step 4: Add `UpdateRunRetention` to the store**

In `portfolio/db.go`, add:

```go
// UpdateRunRetention updates a portfolio's run_retention. Returns ErrNotFound
// if the (ownerSub, slug) pair does not match any row.
func UpdateRunRetention(ctx context.Context, pool *pgxpool.Pool, ownerSub, slug string, value int) error {
    tag, err := pool.Exec(ctx,
        `UPDATE portfolios SET run_retention=$3, updated_at=NOW()
         WHERE owner_sub=$1 AND slug=$2`,
        ownerSub, slug, value,
    )
    if err != nil {
        return fmt.Errorf("updating run_retention: %w", err)
    }
    if tag.RowsAffected() == 0 {
        return ErrNotFound
    }
    return nil
}
```

In `portfolio/store.go:32` (the `Store` interface — find with `grep -n "type Store " portfolio/store.go`), add:

```go
UpdateRunRetention(ctx context.Context, ownerSub, slug string, value int) error
```

Then add the `PoolStore` wrapper near `store.go:61`:

```go
func (p PoolStore) UpdateRunRetention(ctx context.Context, ownerSub, slug string, value int) error {
    return UpdateRunRetention(ctx, p.Pool, ownerSub, slug, value)
}
```

- [ ] **Step 5: Wire the new field into `Patch`**

In `portfolio/handler.go:380` (after the `startDate || endDate` block):

```go
if body.RunRetention != nil {
    if err := applyStoreUpdate(c, slug, func() error {
        return h.store.UpdateRunRetention(c.Context(), ownerSub, slug, *body.RunRetention)
    }); err != nil {
        return err
    }
}
```

- [ ] **Step 6: Run the test and verify pass**

Run: `go test ./portfolio/ -run TestPortfolio -count=1 -v -ginkgo.focus="run_retention via PATCH"`
Expected: PASS.

- [ ] **Step 7: Run the full portfolio suite**

Run: `go test ./portfolio/... -count=1`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add portfolio/handler.go portfolio/store.go portfolio/db.go portfolio/handler_test.go
git commit -m "feat(portfolio): allow PATCH of run_retention"
```

---

## Task 5: `PruneRuns` store method (no orchestrator hook yet)

**Files:**
- Create: `portfolio/prune_test.go`
- Modify: `portfolio/db.go` (add `PruneRuns`)
- Modify: `portfolio/store.go` (Store interface + PoolStore wrapper)

- [ ] **Step 1: Write the failing tests**

`portfolio/prune_test.go`:

```go
// Copyright 2021-2026
// SPDX-License-Identifier: Apache-2.0

package portfolio_test

import (
    "context"
    "time"

    . "github.com/onsi/ginkgo/v2"
    . "github.com/onsi/gomega"

    "github.com/penny-vault/pv-api/portfolio"
)

var _ = Describe("PruneRuns", func() {
    var (
        ctx       context.Context
        store     portfolio.Store
        runStore  *portfolio.PoolRunStore
        portID    uuid.UUID
    )

    BeforeEach(func() {
        ctx = context.Background()
        store, runStore, portID = freshPortfolioWithStore() // helper: creates portfolio, returns its UUID
    })

    It("returns no deleted snapshots when run count <= retention", func() {
        // retention defaults to 2; create 1 run
        run, err := runStore.CreateRun(ctx, portID, "queued")
        Expect(err).NotTo(HaveOccurred())
        _ = run

        deleted, err := store.PruneRuns(ctx, portID)
        Expect(err).NotTo(HaveOccurred())
        Expect(deleted).To(BeEmpty())
    })

    It("keeps the most recent N runs and returns paths of pruned ones", func() {
        // retention default = 2
        var snapshots []string
        for i := 0; i < 4; i++ {
            run, err := runStore.CreateRun(ctx, portID, "queued")
            Expect(err).NotTo(HaveOccurred())
            path := fmt.Sprintf("/tmp/snap-%d.sqlite", i)
            Expect(runStore.UpdateRunSuccess(ctx, run.ID, path, 100)).To(Succeed())
            snapshots = append(snapshots, path)
            time.Sleep(2 * time.Millisecond) // ensure distinct created_at
        }

        deleted, err := store.PruneRuns(ctx, portID)
        Expect(err).NotTo(HaveOccurred())
        // The 2 oldest snapshots are returned for deletion.
        Expect(deleted).To(ConsistOf(snapshots[0], snapshots[1]))

        rows, err := runStore.ListRuns(ctx, portID)
        Expect(err).NotTo(HaveOccurred())
        Expect(rows).To(HaveLen(2))
    })

    It("honors a per-portfolio override", func() {
        Expect(store.UpdateRunRetention(ctx, ownerSubFor(portID), slugFor(portID), 1)).To(Succeed())
        for i := 0; i < 3; i++ {
            run, err := runStore.CreateRun(ctx, portID, "queued")
            Expect(err).NotTo(HaveOccurred())
            Expect(runStore.UpdateRunSuccess(ctx, run.ID, fmt.Sprintf("/tmp/x-%d", i), 100)).To(Succeed())
            time.Sleep(2 * time.Millisecond)
        }
        deleted, err := store.PruneRuns(ctx, portID)
        Expect(err).NotTo(HaveOccurred())
        Expect(deleted).To(HaveLen(2))

        rows, err := runStore.ListRuns(ctx, portID)
        Expect(err).NotTo(HaveOccurred())
        Expect(rows).To(HaveLen(1))
    })
})
```

`freshPortfolioWithStore`, `ownerSubFor`, `slugFor` are helpers you will need in the suite — search `portfolio_suite_test.go` and `handler_test.go` for similar fixture helpers and reuse / extend.

- [ ] **Step 2: Run and verify failure**

Run: `go test ./portfolio/ -run TestPortfolio -count=1 -v -ginkgo.focus="PruneRuns"`
Expected: FAIL with compile error — `store.PruneRuns` does not exist.

- [ ] **Step 3: Add `PruneRuns` to `db.go`**

```go
// PruneRuns deletes backtest_runs rows older than the most recent run_retention
// runs for the given portfolio. Returns the snapshot file paths of deleted
// rows so the caller can remove them from disk.
func PruneRuns(ctx context.Context, pool *pgxpool.Pool, portfolioID uuid.UUID) ([]string, error) {
    tx, err := pool.Begin(ctx)
    if err != nil {
        return nil, fmt.Errorf("begin: %w", err)
    }
    defer func() { _ = tx.Rollback(ctx) }()

    var retention int
    if err := tx.QueryRow(ctx,
        `SELECT run_retention FROM portfolios WHERE id=$1`, portfolioID,
    ).Scan(&retention); err != nil {
        if errors.Is(err, pgx.ErrNoRows) {
            return nil, ErrNotFound
        }
        return nil, fmt.Errorf("loading run_retention: %w", err)
    }

    rows, err := tx.Query(ctx, `
        WITH ranked AS (
            SELECT id, snapshot_path,
                   ROW_NUMBER() OVER (ORDER BY created_at DESC) AS rn
            FROM backtest_runs
            WHERE portfolio_id = $1
        )
        DELETE FROM backtest_runs
         WHERE id IN (SELECT id FROM ranked WHERE rn > $2)
        RETURNING COALESCE(snapshot_path, '')
    `, portfolioID, retention)
    if err != nil {
        return nil, fmt.Errorf("pruning backtest_runs: %w", err)
    }
    defer rows.Close()

    var deleted []string
    for rows.Next() {
        var p string
        if err := rows.Scan(&p); err != nil {
            return nil, err
        }
        if p != "" {
            deleted = append(deleted, p)
        }
    }
    if err := rows.Err(); err != nil {
        return nil, err
    }

    if err := tx.Commit(ctx); err != nil {
        return nil, fmt.Errorf("commit: %w", err)
    }
    return deleted, nil
}
```

- [ ] **Step 4: Add to the `Store` interface and `PoolStore`**

In `portfolio/store.go`, add to the `Store` interface:

```go
PruneRuns(ctx context.Context, portfolioID uuid.UUID) ([]string, error)
```

Add the wrapper:

```go
func (p PoolStore) PruneRuns(ctx context.Context, portfolioID uuid.UUID) ([]string, error) {
    return PruneRuns(ctx, p.Pool, portfolioID)
}
```

- [ ] **Step 5: Run the tests and verify pass**

Run: `go test ./portfolio/ -run TestPortfolio -count=1 -v -ginkgo.focus="PruneRuns"`
Expected: PASS for all three cases.

- [ ] **Step 6: Commit**

```bash
git add portfolio/db.go portfolio/store.go portfolio/prune_test.go
git commit -m "feat(portfolio): add PruneRuns store method honoring run_retention"
```

---

## Task 6: Hook `PruneRuns` into the orchestrator + best-effort snapshot delete

**Files:**
- Modify: `backtest/dispatcher.go:50-53` (`PortfolioStore` interface)
- Modify: `backtest/run.go:142` (after `MarkReadyTx`) and `backtest/run.go:213` (after `MarkFailedTx`)
- Modify: `backtest/run_test.go` (or fakeclient as needed) so existing tests still satisfy the extended interface

- [ ] **Step 1: Write the failing test**

In `backtest/run_test.go`, add an integration test that asserts `PruneRuns` is called after a successful run completes:

```go
It("calls PruneRuns after MarkReadyTx", func() {
    fake := newFakePortfolioStore() // existing test fake
    o := NewOrchestrator(fake, /*...other deps...*/)
    o.RunOne(ctx, portID, runID)

    Expect(fake.PruneRunsCalls).To(HaveLen(1))
    Expect(fake.PruneRunsCalls[0]).To(Equal(portID))
})

It("calls PruneRuns after MarkFailedTx", func() {
    fake := newFakePortfolioStore()
    fake.SimulateFailure = true
    o := NewOrchestrator(fake, /*...*/)
    o.RunOne(ctx, portID, runID)

    Expect(fake.PruneRunsCalls).To(HaveLen(1))
})

It("removes snapshot files returned by PruneRuns", func() {
    tmp := tempSnapshotFile(GinkgoT())
    fake := newFakePortfolioStore()
    fake.PruneRunsReturn = []string{tmp}
    o := NewOrchestrator(fake, /*...*/)
    o.RunOne(ctx, portID, runID)

    _, err := os.Stat(tmp)
    Expect(os.IsNotExist(err)).To(BeTrue())
})
```

If the existing test fake is in `backtest/fakeclient_test.go`, extend it there. If `RunOne` is named differently, search with `grep -n "func.*Orchestrator\|func.*RunOne" backtest/run.go`.

- [ ] **Step 2: Run and verify failure**

Run: `go test ./backtest/... -count=1 -v -ginkgo.focus="PruneRuns"`
Expected: FAIL with compile error.

- [ ] **Step 3: Extend the orchestrator's `PortfolioStore` interface**

In `backtest/dispatcher.go:50-53`, add a method to the existing interface:

```go
PruneRuns(ctx context.Context, portfolioID uuid.UUID) ([]string, error)
```

- [ ] **Step 4: Hook into the success path**

In `backtest/run.go:142`, immediately after the `MarkReadyTx` call commits:

```go
if err := o.ps.MarkReadyTx(ctx, portfolioID, runID, final, /*...*/); err != nil {
    // existing error handling
}
o.prune(ctx, portfolioID)
```

And on the failure path at `backtest/run.go:213`, after `MarkFailedTx`:

```go
_ = o.ps.MarkFailedTx(ctx, portfolioID, runID, msg, durationMs(time.Since(started)))
o.prune(ctx, portfolioID)
```

Add the helper to the same file (just below the run-handler function or at the bottom):

```go
func (o *Orchestrator) prune(ctx context.Context, portfolioID uuid.UUID) {
    paths, err := o.ps.PruneRuns(ctx, portfolioID)
    if err != nil {
        log.Warn().Err(err).Stringer("portfolio_id", portfolioID).Msg("prune runs failed; will retry on next completion")
        return
    }
    for _, p := range paths {
        if p == "" {
            continue
        }
        if rmErr := os.Remove(p); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
            log.Warn().Err(rmErr).Str("path", p).Msg("snapshot delete failed")
        }
    }
}
```

Imports to add: `"errors"`, `"io/fs"`, `"os"`, plus zerolog if not already imported in this file.

- [ ] **Step 5: Update the test fake**

In `backtest/fakeclient_test.go` (or wherever the fake `PortfolioStore` lives — `grep -n "PruneRuns\|MarkReadyTx" backtest/*_test.go`), add:

```go
type fakePortfolioStore struct {
    // ...existing fields...
    PruneRunsCalls  []uuid.UUID
    PruneRunsReturn []string
}

func (f *fakePortfolioStore) PruneRuns(_ context.Context, id uuid.UUID) ([]string, error) {
    f.PruneRunsCalls = append(f.PruneRunsCalls, id)
    return f.PruneRunsReturn, nil
}
```

- [ ] **Step 6: Run the tests and verify pass**

Run: `go test ./backtest/... -count=1 -v -ginkgo.focus="PruneRuns"`
Expected: PASS.

- [ ] **Step 7: Run the full backtest + portfolio suites**

Run: `go test ./backtest/... ./portfolio/... -count=1`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add backtest/dispatcher.go backtest/run.go backtest/fakeclient_test.go backtest/run_test.go
git commit -m "feat(backtest): prune backtest_runs after every terminal transition"
```

---

## Task 7: `DiffParameters` and `MatchPresetName`

**Files:**
- Modify: `portfolio/validate.go`
- Modify: `portfolio/validate_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `portfolio/validate_test.go`:

```go
var _ = Describe("DiffParameters", func() {
    intP := strategy.DescribeParameter{Name: "p", Type: "int"}
    intPDefault := strategy.DescribeParameter{Name: "p", Type: "int", Default: ptrAny(7)}
    strP := strategy.DescribeParameter{Name: "p", Type: "string"}

    It("classifies an unchanged parameter as kept", func() {
        d := portfolio.DiffParameters(
            map[string]any{"p": 1},
            strategy.Describe{Parameters: []strategy.DescribeParameter{intP}},
        )
        Expect(d.Kept).To(ConsistOf("p"))
        Expect(d.Compatible()).To(BeTrue())
    })

    It("classifies a new parameter with default as added_with_default", func() {
        d := portfolio.DiffParameters(
            map[string]any{},
            strategy.Describe{Parameters: []strategy.DescribeParameter{intPDefault}},
        )
        Expect(d.AddedWithDefault).To(ConsistOf("p"))
        Expect(d.Compatible()).To(BeTrue())
    })

    It("classifies a new parameter without default as added_without_default", func() {
        d := portfolio.DiffParameters(
            map[string]any{},
            strategy.Describe{Parameters: []strategy.DescribeParameter{intP}},
        )
        Expect(d.AddedWithoutDefault).To(ConsistOf("p"))
        Expect(d.Compatible()).To(BeFalse())
    })

    It("classifies a removed parameter", func() {
        d := portfolio.DiffParameters(
            map[string]any{"p": 1, "q": 2},
            strategy.Describe{Parameters: []strategy.DescribeParameter{intP}},
        )
        Expect(d.Removed).To(ConsistOf("q"))
        Expect(d.Compatible()).To(BeFalse())
    })

    It("classifies a retyped parameter", func() {
        d := portfolio.DiffParameters(
            map[string]any{"p": 1},
            strategy.Describe{Parameters: []strategy.DescribeParameter{strP}},
        )
        Expect(d.Retyped).To(HaveLen(1))
        Expect(d.Retyped[0].Name).To(Equal("p"))
        Expect(d.Retyped[0].From).To(Equal("int"))
        Expect(d.Retyped[0].To).To(Equal("string"))
        Expect(d.Compatible()).To(BeFalse())
    })

    It("handles a mix of all five buckets", func() {
        d := portfolio.DiffParameters(
            map[string]any{"kept": 1, "removed": 2, "retyped": 3},
            strategy.Describe{Parameters: []strategy.DescribeParameter{
                {Name: "kept", Type: "int"},
                {Name: "added_default", Type: "int", Default: ptrAny(0)},
                {Name: "added_no_default", Type: "int"},
                {Name: "retyped", Type: "string"},
            }},
        )
        Expect(d.Kept).To(ConsistOf("kept"))
        Expect(d.AddedWithDefault).To(ConsistOf("added_default"))
        Expect(d.AddedWithoutDefault).To(ConsistOf("added_no_default"))
        Expect(d.Removed).To(ConsistOf("removed"))
        Expect(d.Retyped).To(HaveLen(1))
        Expect(d.Compatible()).To(BeFalse())
    })
})

var _ = Describe("MatchPresetName", func() {
    presets := []strategy.DescribePreset{
        {Name: "conservative", Parameters: map[string]any{"p": 1.0}},
        {Name: "aggressive", Parameters: map[string]any{"p": 9.0}},
    }

    It("returns the matching preset name when parameters match", func() {
        n := portfolio.MatchPresetName(map[string]any{"p": 1.0}, strategy.Describe{Presets: presets})
        Expect(n).NotTo(BeNil())
        Expect(*n).To(Equal("conservative"))
    })

    It("returns nil when no preset matches", func() {
        n := portfolio.MatchPresetName(map[string]any{"p": 5.0}, strategy.Describe{Presets: presets})
        Expect(n).To(BeNil())
    })
})

func ptrAny(v any) *any { return &v }
```

(If the `strategy` package's `DescribeParameter` does not have a field literally named `Default`, look at `strategy/types.go` for the actual name and use it. Same for `DescribePreset`. Adjust the test accordingly — do not change the strategy package.)

- [ ] **Step 2: Run and verify failure**

Run: `go test ./portfolio/ -run TestPortfolio -count=1 -v -ginkgo.focus="DiffParameters|MatchPresetName"`
Expected: FAIL with compile errors — `portfolio.DiffParameters`, `portfolio.MatchPresetName`, `ParameterDiff` not defined.

- [ ] **Step 3: Add the types and `DiffParameters`**

In `portfolio/validate.go`:

```go
// ParameterRetype describes a parameter whose declared type changed between
// strategy versions.
type ParameterRetype struct {
    Name string `json:"name"`
    From string `json:"from"`
    To   string `json:"to"`
}

// ParameterDiff is the result of comparing a portfolio's stored parameters
// against a new strategy describe. A diff is "compatible" only when all
// changes can be applied without losing or remapping user input.
type ParameterDiff struct {
    Kept                []string          `json:"kept"`
    AddedWithDefault    []string          `json:"added_with_default"`
    AddedWithoutDefault []string          `json:"added_without_default"`
    Removed             []string          `json:"removed"`
    Retyped             []ParameterRetype `json:"retyped"`
}

// Compatible reports whether the diff can be applied automatically:
// no removed, no retyped, no added-without-default parameters.
func (d ParameterDiff) Compatible() bool {
    return len(d.Removed) == 0 && len(d.Retyped) == 0 && len(d.AddedWithoutDefault) == 0
}

// DiffParameters compares the portfolio's stored parameter values against the
// new describe's declared Parameters. Type compatibility is shallow: only the
// `type` field is compared.
func DiffParameters(current map[string]any, newDescribe strategy.Describe) ParameterDiff {
    var d ParameterDiff
    declared := map[string]strategy.DescribeParameter{}
    for _, p := range newDescribe.Parameters {
        declared[p.Name] = p
    }
    // Walk current to find kept/removed/retyped.
    for name := range current {
        decl, ok := declared[name]
        if !ok {
            d.Removed = append(d.Removed, name)
            continue
        }
        oldType := paramType(current[name])
        if oldType != "" && decl.Type != "" && oldType != decl.Type {
            d.Retyped = append(d.Retyped, ParameterRetype{Name: name, From: oldType, To: decl.Type})
            continue
        }
        d.Kept = append(d.Kept, name)
    }
    // Walk declared to find added.
    for name, decl := range declared {
        if _, ok := current[name]; ok {
            continue
        }
        if hasDefault(decl) {
            d.AddedWithDefault = append(d.AddedWithDefault, name)
        } else {
            d.AddedWithoutDefault = append(d.AddedWithoutDefault, name)
        }
    }
    return d
}

func hasDefault(p strategy.DescribeParameter) bool {
    return p.Default != nil // adjust if the strategy package uses a different sentinel
}

// paramType returns a coarse type label for a value pulled from the JSONB
// parameters column. Returns "" if the type is unknown; in that case the
// caller treats the parameter as `kept` (no retype detected).
func paramType(v any) string {
    switch v.(type) {
    case bool:
        return "bool"
    case float64, int, int64:
        return "number"
    case string:
        return "string"
    case []any:
        return "array"
    case map[string]any:
        return "object"
    default:
        return ""
    }
}
```

NOTE: The test uses `Type: "int"` and `Type: "string"` to match parameter declarations. The `paramType` function above returns `"number"` for numeric values, not `"int"`. Reconcile this by either: (a) updating the test fixture to use the strategy package's actual numeric type label (look at `strategy/types.go` for what describe parameters use — `"int"`, `"float"`, `"number"`, etc.) and aligning `paramType` to return the same label; or (b) introducing a thin `normalizeType` mapping that compares `"int"` and `"number"` as equivalent. Pick (a): make `paramType` return whatever the strategy package's declared `Type` strings look like for stored numeric values.

- [ ] **Step 4: Add `MatchPresetName`**

```go
// MatchPresetName returns the name of a preset whose parameters exactly match
// the supplied values, or nil if no preset matches.
func MatchPresetName(current map[string]any, newDescribe strategy.Describe) *string {
    for _, preset := range newDescribe.Presets {
        if reflect.DeepEqual(preset.Parameters, current) {
            name := preset.Name
            return &name
        }
    }
    return nil
}
```

Add `"reflect"` to the imports if not already present.

- [ ] **Step 5: Run the tests and verify pass**

Run: `go test ./portfolio/ -run TestPortfolio -count=1 -v -ginkgo.focus="DiffParameters|MatchPresetName"`
Expected: PASS for all cases.

- [ ] **Step 6: Commit**

```bash
git add portfolio/validate.go portfolio/validate_test.go
git commit -m "feat(portfolio): add DiffParameters and MatchPresetName"
```

---

## Task 8: `ApplyUpgrade` store method

**Files:**
- Modify: `portfolio/db.go` (add `ApplyUpgrade`)
- Modify: `portfolio/store.go` (Store interface + PoolStore wrapper)
- Create test cases in `portfolio/upgrade_test.go` (file is reused by Task 9)

- [ ] **Step 1: Write the failing tests**

Create `portfolio/upgrade_test.go` with the apply-upgrade DB test only (handler tests come in Task 9):

```go
// Copyright 2021-2026
// SPDX-License-Identifier: Apache-2.0

package portfolio_test

import (
    "context"

    . "github.com/onsi/ginkgo/v2"
    . "github.com/onsi/gomega"

    "github.com/google/uuid"
    "github.com/penny-vault/pv-api/portfolio"
)

var _ = Describe("ApplyUpgrade", func() {
    var (
        ctx    context.Context
        store  portfolio.Store
        portID uuid.UUID
    )

    BeforeEach(func() {
        ctx = context.Background()
        store, _, portID = freshPortfolioWithStore()
    })

    It("replaces version, describe, parameters and preset_name; inserts a queued run; sets status pending", func() {
        newDescribe := []byte(`{"shortCode":"adm","name":"ADM","parameters":[{"name":"riskOn","type":"universe"}],"schedule":"@monthend","benchmark":"SPY"}`)
        newParams := []byte(`{"riskOn":"QQQ"}`)
        presetName := "balanced"

        runID, err := store.ApplyUpgrade(ctx, portID,
            "v1.3.0",
            newDescribe,
            newParams,
            &presetName,
        )
        Expect(err).NotTo(HaveOccurred())
        Expect(runID).NotTo(Equal(uuid.Nil))

        p, err := store.GetByID(ctx, portID)
        Expect(err).NotTo(HaveOccurred())
        Expect(p.StrategyVer).NotTo(BeNil())
        Expect(*p.StrategyVer).To(Equal("v1.3.0"))
        Expect(string(p.StrategyDescribeJSON)).To(MatchJSON(string(newDescribe)))
        Expect(p.PresetName).NotTo(BeNil())
        Expect(*p.PresetName).To(Equal("balanced"))
        Expect(string(p.Status)).To(Equal("pending"))
        Expect(p.LastError).To(Or(BeEmpty(), Equal(""))) // last_error cleared

        rs, err := portfolio.NewPoolRunStore(testPool).ListRuns(ctx, portID) // existing helper
        Expect(err).NotTo(HaveOccurred())
        Expect(rs[0].Status).To(Equal("queued"))
        Expect(rs[0].ID).To(Equal(runID))
    })

    It("nils preset_name when nil is passed", func() {
        runID, err := store.ApplyUpgrade(ctx, portID,
            "v1.3.0",
            []byte(`{}`),
            []byte(`{}`),
            nil,
        )
        Expect(err).NotTo(HaveOccurred())
        Expect(runID).NotTo(Equal(uuid.Nil))

        p, err := store.GetByID(ctx, portID)
        Expect(err).NotTo(HaveOccurred())
        Expect(p.PresetName).To(BeNil())
    })

    It("returns ErrNotFound when the portfolio does not exist", func() {
        _, err := store.ApplyUpgrade(ctx, uuid.New(),
            "v1.3.0", []byte(`{}`), []byte(`{}`), nil,
        )
        Expect(err).To(MatchError(portfolio.ErrNotFound))
    })
})
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./portfolio/ -run TestPortfolio -count=1 -v -ginkgo.focus="ApplyUpgrade"`
Expected: FAIL with compile error — `store.ApplyUpgrade` does not exist.

- [ ] **Step 3: Implement `ApplyUpgrade` in `db.go`**

```go
// ApplyUpgrade atomically replaces strategy_ver, strategy_describe_json,
// parameters and preset_name on the portfolio, sets status='pending' and
// last_error=NULL, and inserts a new queued backtest_runs row. Returns the
// new run UUID.
//
// Returns ErrNotFound when the portfolio does not exist.
func ApplyUpgrade(ctx context.Context, pool *pgxpool.Pool, portfolioID uuid.UUID,
    newVer string, newDescribe json.RawMessage, newParams json.RawMessage,
    newPresetName *string,
) (uuid.UUID, error) {
    tx, err := pool.Begin(ctx)
    if err != nil {
        return uuid.Nil, fmt.Errorf("begin: %w", err)
    }
    defer func() { _ = tx.Rollback(ctx) }()

    tag, err := tx.Exec(ctx, `
        UPDATE portfolios SET
            strategy_ver = $2,
            strategy_describe_json = $3,
            parameters = $4,
            preset_name = $5,
            status = 'pending',
            last_error = NULL,
            updated_at = NOW()
        WHERE id = $1
    `, portfolioID, newVer, newDescribe, newParams, newPresetName)
    if err != nil {
        return uuid.Nil, fmt.Errorf("updating portfolio: %w", err)
    }
    if tag.RowsAffected() == 0 {
        return uuid.Nil, ErrNotFound
    }

    var runID uuid.UUID
    if err := tx.QueryRow(ctx, `
        INSERT INTO backtest_runs (portfolio_id, status, created_at)
        VALUES ($1, 'queued', NOW())
        RETURNING id
    `, portfolioID).Scan(&runID); err != nil {
        return uuid.Nil, fmt.Errorf("inserting run: %w", err)
    }

    if err := tx.Commit(ctx); err != nil {
        return uuid.Nil, fmt.Errorf("commit: %w", err)
    }
    return runID, nil
}
```

If the existing `backtest_runs` schema requires more columns at insert time, mirror what `PoolRunStore.CreateRun` does (find with `grep -n "INSERT INTO backtest_runs" portfolio/runs.go`) and align the column list.

- [ ] **Step 4: Add to the `Store` interface and `PoolStore`**

```go
ApplyUpgrade(ctx context.Context, portfolioID uuid.UUID, newVer string,
    newDescribe json.RawMessage, newParams json.RawMessage,
    newPresetName *string) (uuid.UUID, error)
```

```go
func (p PoolStore) ApplyUpgrade(ctx context.Context, portfolioID uuid.UUID,
    newVer string, newDescribe json.RawMessage, newParams json.RawMessage,
    newPresetName *string,
) (uuid.UUID, error) {
    return ApplyUpgrade(ctx, p.Pool, portfolioID, newVer, newDescribe, newParams, newPresetName)
}
```

- [ ] **Step 5: Run the tests and verify pass**

Run: `go test ./portfolio/ -run TestPortfolio -count=1 -v -ginkgo.focus="ApplyUpgrade"`
Expected: PASS for all three cases.

- [ ] **Step 6: Commit**

```bash
git add portfolio/db.go portfolio/store.go portfolio/upgrade_test.go
git commit -m "feat(portfolio): add ApplyUpgrade store method"
```

---

## Task 9: Upgrade handler — happy path + already-at-latest + run-in-progress

**Files:**
- Create: `portfolio/upgrade.go`
- Modify: `portfolio/upgrade_test.go` (add handler tests)

- [ ] **Step 1: Write the failing tests**

Append to `portfolio/upgrade_test.go`:

```go
var _ = Describe("Upgrade handler", func() {
    // Reuse ginkgo fixture helpers (handler, store, ownerSub, ctx).

    It("returns 200 already_at_latest when the portfolio is at the registry's installed_ver", func() {
        slug := mustCreatePortfolio(map[string]any{"riskOn": "SPY"}) // pinned to installed_ver
        resp := postEmpty("/portfolios/" + slug + "/upgrade")
        Expect(resp.StatusCode).To(Equal(200))
        Expect(bodyJSON(resp)["status"]).To(Equal("already_at_latest"))
    })

    It("returns 404 when the portfolio is missing", func() {
        resp := postEmpty("/portfolios/does-not-exist/upgrade")
        Expect(resp.StatusCode).To(Equal(404))
    })

    It("returns 409 run_in_progress when status='running'", func() {
        slug := mustCreatePortfolio(map[string]any{"riskOn": "SPY"})
        Expect(store.SetRunning(ctx, idFor(slug))).To(Succeed())
        resp := postEmpty("/portfolios/" + slug + "/upgrade")
        Expect(resp.StatusCode).To(Equal(409))
        Expect(bodyJSON(resp)["error"]).To(Equal("run_in_progress"))
    })

    It("upgrades when the registry has a newer compatible version (empty body)", func() {
        slug := mustCreatePortfolioAtVersion("v1.0.0", map[string]any{"riskOn": "SPY"})
        bumpRegistryTo("v1.1.0", /* identical describe params */)

        resp := postEmpty("/portfolios/" + slug + "/upgrade")
        Expect(resp.StatusCode).To(Equal(200))
        body := bodyJSON(resp)
        Expect(body["status"]).To(Equal("upgraded"))
        Expect(body["from_version"]).To(Equal("v1.0.0"))
        Expect(body["to_version"]).To(Equal("v1.1.0"))
        Expect(body["run_id"]).NotTo(BeEmpty())

        p, err := store.Get(ctx, ownerSub, slug)
        Expect(err).NotTo(HaveOccurred())
        Expect(*p.StrategyVer).To(Equal("v1.1.0"))
        Expect(string(p.Status)).To(Equal("pending"))
    })
})
```

`mustCreatePortfolio`, `mustCreatePortfolioAtVersion`, `bumpRegistryTo`, `postEmpty`, `bodyJSON`, `idFor` are fixtures you may need to add. Reuse the patterns in `handler_test.go` for the existing Create tests; specifically the registry seeding helper (search `grep -n "Strategy{\\|InstalledVer:" portfolio/handler_test.go`).

- [ ] **Step 2: Run and verify failure**

Run: `go test ./portfolio/ -run TestPortfolio -count=1 -v -ginkgo.focus="Upgrade handler"`
Expected: FAIL — route not registered, handler does not exist.

- [ ] **Step 3: Create `portfolio/upgrade.go` with the handler skeleton**

```go
// Copyright 2021-2026
// SPDX-License-Identifier: Apache-2.0

package portfolio

import (
    "encoding/json"
    "errors"

    "github.com/bytedance/sonic"
    "github.com/gofiber/fiber/v3"
)

type upgradeRequestBody struct {
    Parameters map[string]any `json:"parameters"`
}

// Upgrade implements POST /portfolios/{slug}/upgrade.
//
// See docs/superpowers/specs/2026-04-25-portfolio-strategy-upgrade-design.md.
func (h *Handler) Upgrade(c fiber.Ctx) error {
    ownerSub, err := subject(c)
    if err != nil {
        return writeProblem(c, fiber.StatusUnauthorized, "Unauthorized", err.Error())
    }
    slug := string([]byte(c.Params("slug")))

    p, err := h.store.Get(c.Context(), ownerSub, slug)
    if err != nil {
        if errors.Is(err, ErrNotFound) {
            return writeProblem(c, fiber.StatusNotFound, "Not Found", "portfolio not found: "+slug)
        }
        return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
    }

    if string(p.Status) == "running" {
        return writeJSON(c, fiber.StatusConflict, fiber.Map{"error": "run_in_progress"})
    }

    s, err := h.strategies.Get(c.Context(), p.StrategyCode, p.StrategyCloneURL)
    if err != nil {
        return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
    }
    if s.InstalledVer == nil || s.InstallError != nil {
        return writeJSON(c, fiber.StatusUnprocessableEntity, fiber.Map{"error": "strategy_not_installable"})
    }

    if p.StrategyVer != nil && *p.StrategyVer == *s.InstalledVer {
        return writeJSON(c, fiber.StatusOK, fiber.Map{
            "status":  "already_at_latest",
            "version": *s.InstalledVer,
        })
    }

    // Compatibility checks and apply happen in Tasks 10 + 11.
    return writeProblem(c, fiber.StatusNotImplemented, "Not Implemented", "upgrade flow not yet implemented")
}
```

The `h.strategies` accessor must already exist on the `Handler` (validate.go uses it for ValidateCreate). Search `grep -n "strategies " portfolio/handler.go portfolio/types.go` and adapt accordingly. If it does not exist, find the matching dependency in `Handler` (e.g., `strategyStore`) and use that; do NOT add a new field.

- [ ] **Step 4: Wire the route**

In `api/portfolios.go:55+` (the `RegisterPortfolioRoutes` real-routes block), add:

```go
r.Post("/portfolios/:slug/upgrade", h.Upgrade)
```

In `api/portfolios.go:30+` (the stub list), add:

```go
r.Post("/portfolios/:slug/upgrade", stubPortfolio)
```

- [ ] **Step 5: Run the tests and verify pass for the three skeleton tests**

Run: `go test ./portfolio/ -run TestPortfolio -count=1 -v -ginkgo.focus="Upgrade handler"`
Expected: PASS for the 404, already_at_latest, and run_in_progress tests. FAIL for "upgrades when registry has a newer compatible version" (still 501 — that lands in Task 10).

- [ ] **Step 6: Commit**

```bash
git add portfolio/upgrade.go portfolio/upgrade_test.go api/portfolios.go
git commit -m "feat(portfolio): wire POST /portfolios/:slug/upgrade with skeleton checks"
```

---

## Task 10: Upgrade handler — compatible path with auto-merge + dispatch

**Files:**
- Modify: `portfolio/upgrade.go`

- [ ] **Step 1: Run the existing Task-9 happy-path test and confirm it still fails**

Run: `go test ./portfolio/ -run TestPortfolio -count=1 -v -ginkgo.focus="upgrades when the registry has a newer compatible version"`
Expected: FAIL with 501 Not Implemented.

- [ ] **Step 2: Implement the compatible-empty-body path**

Replace the trailing `return writeProblem(... NotImplemented ...)` in `Upgrade` with:

```go
// Parse the (possibly empty) body.
var body upgradeRequestBody
if raw := c.Body(); len(raw) > 0 {
    if err := sonic.Unmarshal(raw, &body); err != nil {
        return writeProblem(c, fiber.StatusBadRequest, "Bad Request", "body is not valid JSON")
    }
}

// Resolve the new describe.
var newDescribe strategy.Describe
if err := json.Unmarshal(s.DescribeJSON, &newDescribe); err != nil {
    return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", "registry describe is invalid: "+err.Error())
}

// Decode current parameters.
var current map[string]any
if len(p.Parameters) > 0 {
    if err := json.Unmarshal(p.Parameters, &current); err != nil {
        return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
    }
}

diff := DiffParameters(current, newDescribe)

var nextParams map[string]any
switch {
case body.Parameters != nil:
    // Validate supplied set against the new describe (Task 11).
    return writeProblem(c, fiber.StatusNotImplemented, "Not Implemented", "supplied parameters path implemented in next task")
case diff.Compatible():
    nextParams = mergeKeptAndDefaults(current, newDescribe, diff)
default:
    // 409 path (Task 11).
    return writeProblem(c, fiber.StatusNotImplemented, "Not Implemented", "incompatible-diff path implemented in next task")
}

paramsJSON, err := json.Marshal(nextParams)
if err != nil {
    return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
}
presetName := MatchPresetName(nextParams, newDescribe)

runID, err := h.store.ApplyUpgrade(c.Context(), p.ID,
    *s.InstalledVer, json.RawMessage(s.DescribeJSON), paramsJSON, presetName,
)
if err != nil {
    return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
}

// Dispatch the queued run via the same dispatcher Create uses.
// Find the dispatcher accessor on Handler — search `grep -n "dispatcher\|dispatch" portfolio/handler.go`.
// Mirror exactly what insertAndDispatch does for the run-trigger half.
h.dispatchRun(c.Context(), p.ID, runID)

return writeJSON(c, fiber.StatusOK, fiber.Map{
    "status":       "upgraded",
    "from_version": deref(p.StrategyVer),
    "to_version":   *s.InstalledVer,
    "run_id":       runID.String(),
})
```

Add helpers at the bottom of `portfolio/upgrade.go`:

```go
func mergeKeptAndDefaults(current map[string]any, d strategy.Describe, diff ParameterDiff) map[string]any {
    out := make(map[string]any, len(current)+len(diff.AddedWithDefault))
    for _, k := range diff.Kept {
        out[k] = current[k]
    }
    for _, name := range diff.AddedWithDefault {
        for _, p := range d.Parameters {
            if p.Name == name {
                out[name] = p.Default // adjust to whatever field name strategy package uses
                break
            }
        }
    }
    return out
}

func deref(s *string) string {
    if s == nil {
        return ""
    }
    return *s
}
```

If `h.dispatchRun` does not exist, look at `insertAndDispatch` in `handler.go:212` — the run-dispatch tail of that function is what you want to extract or mirror inline.

- [ ] **Step 3: Run the tests and verify pass for the compatible path**

Run: `go test ./portfolio/ -run TestPortfolio -count=1 -v -ginkgo.focus="upgrades when the registry has a newer compatible version"`
Expected: PASS.

- [ ] **Step 4: Run all upgrade-handler tests**

Run: `go test ./portfolio/ -run TestPortfolio -count=1 -v -ginkgo.focus="Upgrade handler"`
Expected: PASS for the four already-written tests; the not-yet-implemented tests in Task 11 fail with 501.

- [ ] **Step 5: Commit**

```bash
git add portfolio/upgrade.go
git commit -m "feat(portfolio): handle compatible upgrade with auto-merge and dispatch"
```

---

## Task 11: Upgrade handler — incompatible 409 + resubmit branch

**Files:**
- Modify: `portfolio/upgrade.go`
- Modify: `portfolio/upgrade_test.go` (add 409 + resubmit tests)

- [ ] **Step 1: Add the failing tests**

Append to `portfolio/upgrade_test.go`:

```go
It("returns 409 parameters_incompatible with a diff body when params changed", func() {
    slug := mustCreatePortfolioAtVersion("v1.0.0", map[string]any{"riskOn": "SPY"})
    bumpRegistryToWithParams("v2.0.0", []strategy.DescribeParameter{
        {Name: "riskOff", Type: "universe"},  // riskOn removed; riskOff added without default
    })

    resp := postEmpty("/portfolios/" + slug + "/upgrade")
    Expect(resp.StatusCode).To(Equal(409))

    body := bodyJSON(resp)
    Expect(body["error"]).To(Equal("parameters_incompatible"))
    Expect(body["from_version"]).To(Equal("v1.0.0"))
    Expect(body["to_version"]).To(Equal("v2.0.0"))
    inc := body["incompatibilities"].(map[string]any)
    Expect(inc["removed"]).To(ConsistOf("riskOn"))
    Expect(inc["added_without_default"]).To(ConsistOf("riskOff"))

    // No state change.
    p, err := store.Get(ctx, ownerSub, slug)
    Expect(err).NotTo(HaveOccurred())
    Expect(*p.StrategyVer).To(Equal("v1.0.0"))
})

It("accepts a resubmit with valid parameters", func() {
    slug := mustCreatePortfolioAtVersion("v1.0.0", map[string]any{"riskOn": "SPY"})
    bumpRegistryToWithParams("v2.0.0", []strategy.DescribeParameter{
        {Name: "riskOff", Type: "universe"},
    })

    resp := postJSON("/portfolios/"+slug+"/upgrade", []byte(`{"parameters":{"riskOff":"QQQ"}}`))
    Expect(resp.StatusCode).To(Equal(200))
    Expect(bodyJSON(resp)["status"]).To(Equal("upgraded"))
})

It("rejects a resubmit with invalid parameters as 400", func() {
    slug := mustCreatePortfolioAtVersion("v1.0.0", map[string]any{"riskOn": "SPY"})
    bumpRegistryToWithParams("v2.0.0", []strategy.DescribeParameter{
        {Name: "riskOff", Type: "universe"},
    })

    resp := postJSON("/portfolios/"+slug+"/upgrade", []byte(`{"parameters":{"unknown":"X"}}`))
    Expect(resp.StatusCode).To(Equal(400))

    p, err := store.Get(ctx, ownerSub, slug)
    Expect(err).NotTo(HaveOccurred())
    Expect(*p.StrategyVer).To(Equal("v1.0.0"))
})

It("returns 422 when the strategy is not installable", func() {
    slug := mustCreatePortfolioAtVersion("v1.0.0", map[string]any{"riskOn": "SPY"})
    setRegistryNotInstallable() // sets installed_ver=NULL or install_error
    resp := postEmpty("/portfolios/" + slug + "/upgrade")
    Expect(resp.StatusCode).To(Equal(422))
    Expect(bodyJSON(resp)["error"]).To(Equal("strategy_not_installable"))
})
```

- [ ] **Step 2: Run and verify failure**

Run: `go test ./portfolio/ -run TestPortfolio -count=1 -v -ginkgo.focus="parameters_incompatible|resubmit|strategy_not_installable"`
Expected: FAIL — current code returns 501 for both branches.

- [ ] **Step 3: Implement the incompatible (empty body) branch**

Replace the `default:` case in the switch:

```go
default:
    return writeJSON(c, fiber.StatusConflict, fiber.Map{
        "error":             "parameters_incompatible",
        "from_version":      deref(p.StrategyVer),
        "to_version":        *s.InstalledVer,
        "incompatibilities": fiber.Map{
            "removed":               diff.Removed,
            "added_without_default": diff.AddedWithoutDefault,
            "retyped":               diff.Retyped,
        },
        "current_parameters": current,
        "new_describe":       json.RawMessage(s.DescribeJSON),
    })
```

- [ ] **Step 4: Implement the resubmit branch**

Replace the `case body.Parameters != nil` arm:

```go
case body.Parameters != nil:
    if err := ValidateParametersAgainstDescribe(body.Parameters, newDescribe); err != nil {
        return writeProblem(c, fiber.StatusBadRequest, "Bad Request", err.Error())
    }
    nextParams = body.Parameters
```

`ValidateParametersAgainstDescribe` is the existing parameter validator used by `ValidateCreate`. Find it with `grep -n "func validateParameters\|func ValidateParameters" portfolio/validate.go`. If it is currently unexported, export it (rename to `ValidateParametersAgainstDescribe`) — keep the original signature. Update its single existing caller in `ValidateCreate` to use the new name in the same commit.

- [ ] **Step 5: Run the tests and verify pass**

Run: `go test ./portfolio/ -run TestPortfolio -count=1 -v -ginkgo.focus="Upgrade handler"`
Expected: PASS for all upgrade-handler tests.

- [ ] **Step 6: Run the full test suite**

Run: `go test ./... -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add portfolio/upgrade.go portfolio/upgrade_test.go portfolio/validate.go
git commit -m "feat(portfolio): handle incompatible-params 409 and resubmit branch"
```

---

## Task 12: Stub-route 401 test entry + OpenAPI doc

**Files:**
- Modify: `api/portfolios_test.go:54` (Entry list)
- Modify: `openapi/openapi.yaml` (add `/portfolios/{slug}/upgrade` and `run_retention`)

- [ ] **Step 1: Add the 401 stub-route table entry**

In `api/portfolios_test.go:54` (the existing `Entry(...)` block), add:

```go
Entry("upgrade strategy", "POST", "/portfolios/adm-standard-aq35/upgrade"),
```

- [ ] **Step 2: Run the stub test and confirm pass**

Run: `go test ./api/... -count=1 -v -ginkgo.focus="upgrade strategy"`
Expected: PASS — the stub already returns 401 because the upgrade route is registered in the stub list (Task 9, Step 4).

- [ ] **Step 3: Document the endpoint in OpenAPI**

In `openapi/openapi.yaml`, add an entry alongside `/portfolios/{slug}/run` (line 251 area) and the existing email-summary entry (line 764). Use the spec document's request/response shapes verbatim. Add a `RunRetention` property (default 2, min 1) to the existing portfolio request and response schemas (CreatePortfolioRequest, UpdatePortfolioRequest, Portfolio).

A minimal block to drop in (adapt to the file's exact YAML conventions — match the surrounding style):

```yaml
  /portfolios/{slug}/upgrade:
    post:
      summary: Upgrade portfolio to the latest installed strategy version
      operationId: upgradePortfolio
      parameters:
        - $ref: '#/components/parameters/Slug'
      requestBody:
        required: false
        content:
          application/json:
            schema:
              type: object
              properties:
                parameters:
                  type: object
                  additionalProperties: true
      responses:
        '200':
          description: Upgraded or already at latest
        '400':
          description: Resubmit body invalid
        '404':
          description: Portfolio not found
        '409':
          description: Run in progress or parameters incompatible
        '422':
          description: Strategy not installable
```

- [ ] **Step 4: Validate the OpenAPI**

Run whatever validator the project uses — check the README or `Makefile` for a target named `openapi-validate` or similar. If none, run `npx --yes @redocly/cli lint openapi/openapi.yaml`.
Expected: PASS / no errors.

- [ ] **Step 5: Commit**

```bash
git add api/portfolios_test.go openapi/openapi.yaml
git commit -m "docs(api): document POST /portfolios/{slug}/upgrade and run_retention"
```

---

## Self-review (executor: skip)

This section was used by the planning author to spot gaps before publication. It is not part of the implementation work — go on to Task 1.

**Spec coverage:**
- Endpoint definition + flow steps 1–10 → Tasks 9–11
- 200 / 400 / 404 / 409 / 422 responses → Tasks 9, 10, 11
- Run retention column + migration → Task 1
- `RunRetention` field plumbing → Task 2
- POST/PATCH accepting `run_retention` → Tasks 3, 4
- `PruneRuns` store method → Task 5
- Orchestrator hook + best-effort file delete → Task 6
- `DiffParameters`, `MatchPresetName` → Task 7
- `ApplyUpgrade` → Task 8
- OpenAPI + stub route → Task 12

**Type-name consistency:** `ParameterDiff`, `ParameterRetype`, `DiffParameters`, `MatchPresetName`, `ApplyUpgrade`, `PruneRuns`, `UpdateRunRetention`, `validateRunRetention`, `RunRetention`, `Upgrade` (handler) — single name per concept, used identically across tasks.

**Known soft spots flagged inline for the executor:**
- Task 7, Step 3 — `paramType` label set ("number" vs "int" vs "float") must be reconciled with whatever the strategy package emits in declared `Type`.
- Task 7, Step 3 — `hasDefault` predicate depends on the strategy package's actual sentinel for "no default" (could be `*any`, `json.RawMessage{}`, etc.).
- Task 8, Step 3 — `INSERT INTO backtest_runs` column list must match what `PoolRunStore.CreateRun` already does.
- Task 9, Step 3 — accessor names on `Handler` (`h.strategies`, `h.dispatchRun`) need to be confirmed and adapted to whatever already exists.
