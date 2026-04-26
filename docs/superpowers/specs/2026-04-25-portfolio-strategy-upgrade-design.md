# Portfolio Strategy Upgrade

**Date:** 2026-04-25
**Status:** Approved

## Overview

Add the ability to upgrade an existing portfolio in place to the latest installed version of its strategy. Today portfolios are deliberately immutable post-creation (PATCH allows only `name`, `start_date`, `end_date`); a portfolio's `strategy_ver` is pinned at create time and there is no path to re-pin it without recreating the portfolio. This change adds an explicit upgrade action that preserves portfolio identity (slug, UUID, name) while replacing the strategy version, the frozen `strategy_describe_json`, and parameters.

This work also bundles a portfolio-level run retention policy, defaulting to keeping the 2 most recent runs. The existing model accumulates a `backtest_runs` row plus a snapshot file per run with no built-in pruning.

## Goals

- Upgrade a portfolio to the registry's currently installed strategy version in place, without recreating it.
- Detect parameter incompatibilities up front and refuse to silently remap.
- Keep past run history bounded.

## Non-Goals

- Pinning a portfolio to an arbitrary historical strategy version. The registry installs one version per strategy; "upgrade" always targets `strategies.installed_ver`.
- Notifying users that an upgrade is available (UI/UX concern, separate work).
- Cross-strategy migration (changing `strategy_code` or `strategy_clone_url`). Identity is "same strategy, newer version."
- Rerunning historical fills incrementally. A successful upgrade kicks off a fresh full backtest run.

## Endpoint

```
POST /portfolios/:slug/upgrade
```

Optimistic-by-default. An empty body attempts to keep the existing parameters as-is. A body is required only when a prior call returned `409 parameters_incompatible`.

### Request body

```json
{ }
```

or

```json
{
  "parameters": { "...": "..." }
}
```

When `parameters` is supplied, it is validated against the **new** strategy's `Describe.Parameters[]` (not the old one). Each declared parameter must be supplied; unknown parameters are rejected. This mirrors the create-time validator in `portfolio/validate.go`.

### Response — 200 `upgraded`

```json
{
  "status": "upgraded",
  "from_version": "v1.2.3",
  "to_version": "v1.3.0",
  "run_id": "<uuid>"
}
```

### Response — 200 `already_at_latest`

```json
{ "status": "already_at_latest", "version": "v1.3.0" }
```

No-op. No new run triggered.

### Response — 409 `parameters_incompatible`

```json
{
  "error": "parameters_incompatible",
  "from_version": "v1.2.3",
  "to_version": "v1.3.0",
  "incompatibilities": {
    "removed": ["lookback"],
    "added_without_default": ["window_days"],
    "retyped": [
      { "name": "asset_count", "from": "int", "to": "string" }
    ]
  },
  "current_parameters": { "...": "..." },
  "new_describe": { "...": "..." }
}
```

The full new `Describe` is included so a client can render a remap UI without an extra round trip. No state mutation occurs on a 409.

### Response — 409 `run_in_progress`

The portfolio currently has a run with status `running`. The user must wait for the run to finish (or cancel it) before upgrading. No state mutation.

### Response — 422 `strategy_not_installable`

The registry has no usable artifact for the portfolio's `strategy_clone_url` (either `installed_ver IS NULL` or `install_error IS NOT NULL`). The upgrade target does not exist.

### Response — 400 `parameters_invalid`

The supplied `parameters` body fails validation against the new describe (missing required, unknown key, type mismatch). No state mutation.

### Response — 404 `not_found`

Portfolio missing or not owned by the caller.

## Flow

1. Load portfolio by slug, scoped by `owner_sub` (404 if missing).
2. If `portfolio.status == 'running'` → 409 `run_in_progress`.
3. Look up `strategies.installed_ver` for the portfolio's `strategy_clone_url`. If `NULL` or `install_error IS NOT NULL` → 422 `strategy_not_installable`.
4. If `portfolio.strategy_ver == installed_ver` → 200 `already_at_latest`. No run.
5. Resolve the new `Describe` from the strategy registry (use the registry's stored `describe_json` for the installed version).
6. Compute parameter diff between `portfolio.parameters` (validated under old describe) and the new describe's `Parameters[]`. "Type-compatible" means the `type` field on the new declaration matches the `type` field on the old declaration; finer-grained constraint changes (min/max, enum membership) are not inspected here — see Open Risks. Each parameter is classified:
   - `kept` — same name, type-compatible
   - `added_with_default` — present in new describe, absent in old portfolio params, has a default
   - `added_without_default` — present in new describe, absent in old portfolio params, no default
   - `removed` — present in old portfolio params, absent in new describe
   - `retyped` — same name, incompatible type
7. **Compatible** when `removed`, `retyped`, and `added_without_default` are all empty.
8. Branch on request body:
   - **Body empty + compatible** → merge `kept` + `added_with_default` defaults to form the new parameters set; proceed.
   - **Body empty + incompatible** → 409 `parameters_incompatible` with the diff payload. No mutation.
   - **Body has `parameters`** → validate the supplied set against the new describe; on failure 400 `parameters_invalid`; on success use the supplied set as the new parameters.
9. Upgrade transactionally (single SQL transaction):
   - `strategy_ver` ← `installed_ver`
   - `strategy_describe_json` ← new describe
   - `parameters` ← merged or supplied set
   - `preset_name` ← name of the preset whose parameters exactly equal the new set, else `NULL`
   - Insert a new `backtest_runs` row (status `queued`).
   - Set portfolio `status = 'pending'`, `last_error = NULL`.
10. After commit, dispatch the run via the existing backtest dispatcher (mirroring create-time auto-trigger).

## Run Retention

Independent of upgrades, the system bounds the per-portfolio run history.

### Schema change

Add column to `portfolios`:

```sql
ALTER TABLE portfolios
  ADD COLUMN run_retention INT NOT NULL DEFAULT 2;
```

A check constraint enforces a sensible floor:

```sql
ALTER TABLE portfolios
  ADD CONSTRAINT portfolios_run_retention_min CHECK (run_retention >= 1);
```

### Settable

- POST `/portfolios` accepts an optional `run_retention` field (defaults to 2 when omitted).
- PATCH `/portfolios/:slug` adds `run_retention` to the existing allowlist alongside `name`, `start_date`, `end_date`.
- Lowering `run_retention` does not retroactively prune; the next run completion will prune to the new value.

### Prune step

Triggered by the orchestrator after `SetReady` or `SetFailed` commits. Implemented as `portfolio.Store.PruneRuns(ctx, portfolioID)`:

1. Read `portfolios.run_retention` for this portfolio.
2. Select run IDs and `snapshot_path` for this portfolio ordered by `created_at DESC`, skipping the first `run_retention` rows.
3. Delete those `backtest_runs` rows in a transaction.
4. After the DB commit, best-effort delete each snapshot file (log on failure; do not error).

Retention counts all runs regardless of status — the simplest mental model is "keep the last N rows by `created_at`."

The prune is its own transaction. A failed prune cannot roll back the run completion. The prune is idempotent, so a subsequent run completion or a manual retry cleans up.

## Data Model Changes

### `portfolio/types.go`

```go
type Portfolio struct {
    // ...existing fields
    RunRetention int
}
```

JSON tag: `"run_retention"`.

### `portfolio/db.go`

New methods:

```go
// ApplyUpgrade atomically replaces version, describe, parameters, and preset_name,
// inserts a queued run row, and sets portfolio status to pending.
// Returns the new run ID.
func (s *Store) ApplyUpgrade(ctx context.Context, p Portfolio, newVer string, newDescribe json.RawMessage, newParams json.RawMessage, newPresetName *string) (uuid.UUID, error)

// PruneRuns deletes backtest_runs rows older than the most recent run_retention
// runs and returns the snapshot paths of deleted rows so the caller can remove
// them from disk.
func (s *Store) PruneRuns(ctx context.Context, portfolioID uuid.UUID) ([]string, error)
```

Existing PATCH path (`Update`) gains a `RunRetention` field; the validator rejects values < 1.

### `portfolio/validate.go`

```go
type ParameterDiff struct {
    Kept                []string
    AddedWithDefault    []string
    AddedWithoutDefault []string
    Removed             []string
    Retyped             []ParameterRetype
}

type ParameterRetype struct {
    Name string `json:"name"`
    From string `json:"from"`
    To   string `json:"to"`
}

func (d ParameterDiff) Compatible() bool

// DiffParameters compares the portfolio's stored parameters (already-validated
// under the old describe) against the new describe's Parameters[] declaration.
func DiffParameters(currentParams map[string]any, newDescribe Describe) ParameterDiff
```

The existing parameter validator (used at create time) is reused for the supplied-body branch — it already enforces "every declared parameter present, no unknown parameters, type-compatible."

### `portfolio/upgrade.go` (new)

Orchestrates: load → status check → registry lookup → diff → branch → commit → enqueue.

```go
func (h *Handler) Upgrade(c fiber.Ctx) error
```

### Route registration

`api/portfolios.go`:

```go
g.Post("/portfolios/:slug/upgrade", portfolioHandler.Upgrade)
```

### Backtest orchestrator

In whichever file owns `SetReady` / `SetFailed` (`backtest/run.go` per the explore brief), after the terminal-state commit:

```go
deletedSnapshots, err := h.portfolios.PruneRuns(ctx, portfolioID)
if err != nil {
    log.Warn().Err(err).Msg("prune runs failed; will retry on next completion")
} else {
    for _, p := range deletedSnapshots {
        if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
            log.Warn().Err(err).Str("path", p).Msg("snapshot delete failed")
        }
    }
}
```

### Migration

`sql/migrations/N_portfolio_run_retention.up.sql`:

```sql
ALTER TABLE portfolios
    ADD COLUMN run_retention INT NOT NULL DEFAULT 2;
ALTER TABLE portfolios
    ADD CONSTRAINT portfolios_run_retention_min CHECK (run_retention >= 1);
```

`...down.sql`:

```sql
ALTER TABLE portfolios DROP CONSTRAINT IF EXISTS portfolios_run_retention_min;
ALTER TABLE portfolios DROP COLUMN IF EXISTS run_retention;
```

## What Changes On The Portfolio Row

| Field | Behavior on upgrade |
|---|---|
| `strategy_code`, `strategy_clone_url` | Unchanged. Upgrade is same-strategy only. |
| `strategy_ver` | Replaced with `strategies.installed_ver`. |
| `strategy_describe_json` | Replaced with the new describe (re-frozen). |
| `parameters` | Merged (kept + defaults) on optimistic path; replaced on resubmit path. |
| `preset_name` | Recomputed against the new describe's presets; `NULL` if no exact match. |
| `slug`, `name`, `start_date`, `end_date` | Unchanged. |
| `current_value`, `ytd_return`, `max_drawdown`, `sharpe`, `cagr_since_inception`, `inception_date` | Unchanged at upgrade commit; overwritten when the new run completes. |
| `last_run_at`, `last_error` | `last_error` cleared at upgrade commit; `last_run_at` updated when the new run completes. |
| `status` | Set to `pending`. |
| Past `backtest_runs` rows | Retained. Subsequent retention pruning may delete them when the new run completes. |

## Errors

| Status | Code | Cause |
|---|---|---|
| 200 | `upgraded` | Successful upgrade, run queued. |
| 200 | `already_at_latest` | Portfolio version equals `installed_ver`. No run triggered. |
| 400 | `parameters_invalid` | Resubmit body fails validation against the new describe. |
| 404 | `not_found` | Portfolio missing or not owned by caller. |
| 409 | `run_in_progress` | Active run; user must wait. |
| 409 | `parameters_incompatible` | Diff returned; resubmit required. |
| 422 | `strategy_not_installable` | Registry has no usable artifact for the strategy. |

## Idempotency & Concurrency

- An upgrade call when already at latest is a 200 no-op; safe to retry.
- A `run_in_progress` 409 is the only race window. The registry's `installed_ver` could change after the request begins; we read it once and use that value for the entire upgrade transaction.
- Two simultaneous upgrade calls for the same portfolio: each opens a transaction; the second will see the row updated and return `already_at_latest` (or proceed against the same `installed_ver` and produce the same result). The transactional update on `(strategy_ver, strategy_describe_json, parameters, preset_name)` is idempotent for identical target inputs.

## Testing

### Unit (`portfolio/validate_test.go`)

`DiffParameters` table-driven tests covering every combination:
- All kept (no-op).
- Added with default → `added_with_default`.
- Added without default → `added_without_default`.
- Removed → `removed`.
- Same name different type → `retyped`.
- Mixed cases including all five buckets at once.
- `ParameterDiff.Compatible()` returns true only when `removed`, `retyped`, and `added_without_default` are empty.

### Integration (`portfolio/upgrade_test.go`)

- Compatible upgrade with empty body → 200, version + describe replaced, run queued.
- Incompatible upgrade with empty body → 409 with diff payload, row unchanged.
- Resubmit with valid parameters → 200, parameters replaced with supplied set.
- Resubmit with invalid parameters → 400, row unchanged.
- Already at latest → 200 `already_at_latest`, no new run row.
- Run in progress → 409 `run_in_progress`, row unchanged.
- Strategy not installable → 422, row unchanged.
- Preset re-matching: if supplied parameters match a preset on the new describe, `preset_name` is set; otherwise `NULL`.
- Other-owner portfolio returns 404 (not 403) for the caller.

### Run retention (`portfolio/prune_test.go`)

- Create N+1 runs, run prune, verify only the most recent N rows remain.
- Verify deleted runs' snapshot files are removed from disk.
- Snapshot file missing on disk → prune succeeds, no error.
- Snapshot file deletion fails (permissions) → prune logs and succeeds; row already deleted.
- `run_retention = 1` honored; lowering retention via PATCH then completing a run prunes to the new value.
- Concurrent run completion + prune is safe (separate transactions).

## Open Risks

- **Stale `installed_ver` race.** A pre-existing concern: `strategies.installed_ver` can change between the upgrade endpoint reading it and the dispatcher resolving the artifact at run time. Acceptable today; the run will use whatever artifact resolves at run time. If this becomes a problem we can pin the artifact reference at run insert.
- **Snapshot file orphaning.** If the prune transaction commits but the file delete fails and the failure is unobserved, the file orphans. We log on failure but don't otherwise track. A future periodic janitor task can reconcile.
- **Parameter type compatibility is shallow.** "Same type" today means matching the `type` field in the describe. Constraint changes (e.g., min/max bound tightening) are not detected as incompatibilities. Acceptable: the parameter validator on the new describe will reject a stored value that no longer satisfies a constraint, and the user will see `parameters_incompatible` in practice via the validator's response on commit. If this surfaces as a real issue, we can extend `DiffParameters` to inspect constraints.
