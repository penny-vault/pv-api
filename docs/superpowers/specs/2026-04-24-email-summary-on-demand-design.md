# Email Summary On Demand

**Date:** 2026-04-24
**Status:** Approved

## Overview

Add a POST endpoint that sends a portfolio summary email to a caller-supplied recipient on demand. Intended primarily for testing the email pipeline without waiting for a scheduled backtest run.

## Endpoint

```
POST /api/v3/portfolios/:slug/email-summary
```

**Request body:**
```json
{ "recipient": "user@example.com" }
```

**Responses:**
- `201 Created` — email accepted by Mailgun
- `400 Bad Request` — missing or invalid recipient
- `401 Unauthorized` — unauthenticated caller
- `404 Not Found` — portfolio not found for authenticated user
- `503 Service Unavailable` — Mailgun not configured (no API key)

## Architecture

### `alert.Checker.SendSummary`

New public method on the existing `Checker`:

```go
func (c *Checker) SendSummary(ctx context.Context, portfolioID uuid.UUID, recipient string) error
```

- Loads portfolio data via the existing `loadPortfolio` helper.
- Builds `email.Payload` by calling the existing `buildPayload` with a zero-value `Alert` (no `LastSentAt`, no `LastSentValue`), so `HasDelta` will be false and no delta section appears in the email.
- Renders via `email.Render`, sends via `email.Send`.
- Returns an error if Mailgun is not configured (`cfg.APIKey == ""`); the handler maps this to `503`.

### `alert.AlertHandler`

- Gains an optional `checker *Checker` field.
- New constructor `NewAlertHandlerWithChecker(portfolios portfolio.Store, alerts Store, checker *Checker)` replaces the existing `NewAlertHandler` at call sites where a checker is available. `NewAlertHandler` can remain as a convenience wrapper passing `nil`.
- New handler method `SendSummary(c fiber.Ctx) error`:
  - Resolves the portfolio via the existing `resolvePortfolio` helper.
  - Parses `{"recipient": "..."}` from the request body.
  - Returns `503` if `h.checker == nil`.
  - Calls `h.checker.SendSummary(...)`.
  - Returns `201 Created` with an empty body on success.

### `api.Config`

New optional field:
```go
AlertChecker *alert.Checker
```

In `api/server.go`, when building `alertHandler`, pass `conf.AlertChecker` if non-nil (using `NewAlertHandlerWithChecker`).

### `cmd/server.go`

The `checker` already created for the scheduler notifier is passed into `api.Config.AlertChecker`.

### Route registration

`api/portfolios.go`:
- `RegisterPortfolioRoutes` adds a stub entry for `POST /portfolios/:slug/email-summary`.
- `RegisterAlertRoutesWith` gains a new parameter or the route is registered in a new `RegisterAlertRoutesWith` variant — whichever is tidier. Most likely just add the route to the existing function since it is already per-slug.

## Data Flow

```
POST /portfolios/:slug/email-summary
  → AlertHandler.SendSummary
    → resolvePortfolio (auth + slug → portfolio.ID)
    → Checker.SendSummary(ctx, portfolioID, recipient)
      → loadPortfolio (name, strategy_code, snapshot_path, etc.)
      → buildPayload (zero Alert → no delta)
      → email.Render → email.Send (Mailgun)
  ← 201 Created
```

## What Is Not Included

- No `lastSentAt` / `lastSentValue` update — this is a one-off send, not a tracked alert send.
- No support for multiple recipients in a single call — callers can fire the endpoint multiple times.
- No rate limiting — this is a test/internal endpoint; rate limiting is out of scope.
