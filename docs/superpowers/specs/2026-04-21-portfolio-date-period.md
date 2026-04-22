# Portfolio Date Period

**Date:** 2026-04-21
**Status:** approved

## Overview

Add optional `startDate` and `endDate` fields to portfolios. These control the date window passed to pvbt via `--start` / `--end`. Portfolios with an `endDate` are fixed historical windows; portfolios without one are refreshed daily by the scheduler.

This change also removes `mode`, `schedule`, and `runNow` — concepts that are no longer needed once the scheduler rule simplifies to "run all open-ended portfolios every day."

## Schema Changes (Migration 9)

**`portfolios` table:**
- Drop `mode` column
- Drop `schedule` column
- Drop `next_run_at` column
- Drop `portfolio_mode` enum
- Add `start_date DATE` (nullable)
- Add `end_date DATE` (nullable)

No changes to the `runs` table.

## API Changes

### `POST /portfolios`

Removed fields: `mode`, `schedule`, `runNow`

New optional fields:
- `startDate` — YYYY-MM-DD; passed as `--start` to pvbt when set
- `endDate` — YYYY-MM-DD; passed as `--end` to pvbt when set; must be >= `startDate` if both are provided

Every portfolio creation triggers an immediate run regardless of whether `endDate` is set.

### `POST /portfolios/{slug}/run` (renamed from `/runs`)

Singular resource name. No request body. Triggers a manual run using the portfolio's stored `startDate` / `endDate`.

### `GET /portfolios`, `GET /portfolios/{slug}`

Portfolio view gains `startDate` and `endDate` fields (omitted when null). Drops `mode`, `schedule`, `status` (was tied to mode).

### `PATCH /portfolios/{slug}`

Can update `startDate` and `endDate` in addition to `name`.

## Scheduler

The scheduler replaces the tradecron-based `next_run_at` approach with a simple daily check:

```sql
WHERE end_date IS NULL
  AND (last_run_at IS NULL OR last_run_at < CURRENT_DATE)
```

- Portfolios with `end_date IS NOT NULL` are never picked up by the scheduler (fixed historical window).
- `next_run_at` is dropped; `last_run_at` already exists on the table.
- The scheduler tick interval remains 60s (configurable); the query is idempotent.

## `BuildArgs` Changes

`BuildArgs` gains `startDate, endDate *string` parameters (YYYY-MM-DD strings, nullable):

```
--start YYYY-MM-DD   (appended only when startDate != nil)
--end   YYYY-MM-DD   (appended only when endDate != nil)
```

These are appended after strategy parameters and before `--benchmark`.

## `PortfolioRow` Changes

`backtest.PortfolioRow` gains:
- `StartDate *time.Time`
- `EndDate *time.Time`

The orchestrator reads these and passes them to `BuildArgs`.

## Validation

- `startDate` and `endDate` must parse as YYYY-MM-DD if provided.
- If both are provided, `endDate >= startDate` is required.
- Invalid dates return 422.

## Removed Concepts

| Removed | Reason |
|---------|--------|
| `mode` (`backtest`/`continuous`) | Implicit: `endDate` set = fixed, `endDate` null = continuous |
| `schedule` (cron expression) | Replaced by fixed daily cadence for all open-ended portfolios |
| `runNow` | All portfolios run immediately on creation — no flag needed |
| `portfolio_mode` enum | Dropped with `mode` |
| `next_run_at` | Replaced by `last_run_at < CURRENT_DATE` check |
