# Portfolio Alert Emails — Design Spec

**Date:** 2026-04-21

## Overview

Send beautiful HTML emails to portfolio owners (and additional recipients) when a portfolio runs, summarising balance changes and trades to execute. Alerts are per-portfolio with configurable frequency.

## Frequencies

| Value | When it fires |
|---|---|
| `scheduled_run` | Every time the portfolio's backtest run completes (success or failure) |
| `daily` | Once per day, on the first run completion of that day |
| `weekly` | On the last trading day of the week (NYSE calendar) |
| `monthly` | On the last trading day of the month (NYSE calendar) |

Dedup is enforced via `last_sent_at` — no double-sends if a portfolio is manually re-run on the same day.

## Data Model

New migration: `portfolio_alerts` table.

```sql
CREATE TABLE portfolio_alerts (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    portfolio_id     UUID NOT NULL REFERENCES portfolios(id) ON DELETE CASCADE,
    frequency        TEXT NOT NULL CHECK (frequency IN ('scheduled_run','daily','weekly','monthly')),
    recipients       TEXT[] NOT NULL,
    last_sent_at     TIMESTAMPTZ,
    last_sent_value  NUMERIC,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_alerts_portfolio ON portfolio_alerts(portfolio_id);
```

`last_sent_at` and `last_sent_value` track when the alert last fired and the portfolio value at that time, enabling delta calculation in subsequent emails.

## Trigger Point

After every backtest run completes — in both `MarkReadyTx` and `MarkFailedTx` paths — the backtest worker calls `alert.CheckAndSend(ctx, portfolioID, runID)`. This is the single trigger point for all frequency types. Since all continuous portfolios run at least daily, `daily/weekly/monthly` alerts get evaluated every run day.

## Alert Evaluation (`alert.CheckAndSend`)

1. Load all `portfolio_alerts` for the portfolio.
2. For each alert, check if it's due:
   - `scheduled_run`: always due.
   - `daily`: due if `last_sent_at` is before today (UTC).
   - `weekly`: due if today is the last NYSE trading day of the week AND `last_sent_at` is before today.
   - `monthly`: due if today is the last NYSE trading day of the month AND `last_sent_at` is before today.
3. For due alerts: build email payload, send via Mailgun.
4. On successful send: update `last_sent_at = now()`, `last_sent_value = portfolio.current_value`.

## Email Content

### Success email

- **Header:** Portfolio name, strategy name, run date
- **Balance section:**
  - Current portfolio value
  - Delta since last email: `+12.0% ($1,240) since Tuesday`
  - Benchmark delta: `Benchmark (SPY) +10.8% (+1.2% vs portfolio)`
  - If first send (no `last_sent_value`): show absolute value only, no delta
- **Trades to execute:** diff of holdings between this run and the previous run stored at `last_sent_at`. Table: Ticker | Action (Buy/Sell) | Shares | Approx. Value. If no holdings changed: "No trades required."
- **New target allocation:** Table: Ticker | Weight % | Value

### Failure email

- **Header:** Portfolio name, strategy name, run date — with error badge
- **Last known value** (from `last_sent_value` if available)
- **Error message** from `portfolio.last_error`
- No trades or allocation sections

### Design principles

- HTML email, table-based layout for broad email client compatibility
- Green for positive deltas, red for negative
- Single-column, readable at a glance on mobile
- Built with Go `html/template`; plain-text alternative included

## Trades Diff

Holdings are read from the backtest snapshot via the existing snapshot reader. The diff compares:
- **Previous holdings:** snapshot from the run at `last_sent_at` (looked up via `backtest_runs` by portfolio + `finished_at <= last_sent_at ORDER BY finished_at DESC LIMIT 1`)
- **Current holdings:** snapshot from the just-completed run

If no previous snapshot exists (first send), the trades section shows the full initial allocation as buys.

## Package Structure

```
alert/
  alert.go          — CheckAndSend, Store interface, Alert type
  db.go             — PostgreSQL store implementation
  evaluate.go       — frequency due-check logic
  tradingdays/
    tradingdays.go  — IsLastTradingDayOfWeek, IsLastTradingDayOfMonth
    holidays.go     — hardcoded NYSE holiday list (rolling 2-year window)
  email/
    email.go        — Mailgun client wrapper, Send(ctx, Alert, Payload)
    template.go     — HTML + plain-text template rendering
    templates/
      success.html
      failure.html
```

## API

All endpoints are scoped under `/portfolios/{slug}/alerts` and require the portfolio to belong to the authenticated user.

| Method | Path | Body | Notes |
|---|---|---|---|
| `POST` | `/portfolios/{slug}/alerts` | `{frequency, recipients[]}` | Create alert |
| `GET` | `/portfolios/{slug}/alerts` | — | List alerts for portfolio |
| `PATCH` | `/portfolios/{slug}/alerts/{alertId}` | `{frequency?, recipients?[]}` | Update frequency or recipients |
| `DELETE` | `/portfolios/{slug}/alerts/{alertId}` | — | Delete alert |

`recipients` is the full list on every PATCH (not additive). Caller is responsible for including the owner's email if desired.

## Configuration

```toml
[mailgun]
domain       = "mg.pennyvault.com"
api_key      = ""
from_address = "Penny Vault <no-reply@mg.pennyvault.com>"
```

Exposed as `--mailgun-domain`, `--mailgun-api-key`, `--mailgun-from-address` flags and `PVAPI_MAILGUN_*` env vars.

If `mailgun.api_key` is empty, alert sending is skipped with a warning log — server still starts cleanly.

## Testing

- `tradingdays` package: table-driven unit tests for known holidays, last-day-of-week, last-day-of-month edge cases
- `evaluate.go`: unit tests for each frequency type covering due / not-due / dedup cases
- `email` package: template rendering tests asserting key content (delta strings, trade table rows) without sending
- Integration test: stub Mailgun HTTP server, run `CheckAndSend` against a test DB, assert email body and `last_sent_at` update
