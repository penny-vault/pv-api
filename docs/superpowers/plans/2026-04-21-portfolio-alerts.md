# Portfolio Alert Emails Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add per-portfolio email alerts (Mailgun) with scheduled_run / daily / weekly / monthly frequencies, showing balance delta, trades to execute, and target allocation.

**Architecture:** Post-run hook in the backtest orchestrator calls `alert.Checker.NotifyRunComplete` after every run. The checker evaluates each alert's frequency against `last_sent_at`, renders an HTML email via Go templates, and sends via the Mailgun HTTP API. Alert CRUD lives under `POST /portfolios/{slug}/alerts`.

**Tech Stack:** Go, pgx/v5, Mailgun REST API (net/http — no SDK), html/template, embed

---

## File Map

**New:**
- `alert/tradingdays/holidays.go` — hardcoded NYSE holiday list 2025–2026
- `alert/tradingdays/tradingdays.go` — IsTrading, IsLastTradingDayOfWeek, IsLastTradingDayOfMonth
- `alert/tradingdays/tradingdays_test.go`
- `alert/alert.go` — Alert type, Frequency constants
- `alert/evaluate.go` — isDue(alert, now) bool
- `alert/evaluate_test.go`
- `alert/db.go` — PoolStore: Create, List, Get, Update, Delete, MarkSent
- `alert/checker.go` — Checker struct, NotifyRunComplete, Notifier interface
- `alert/handler.go` — AlertHandler Fiber CRUD handlers
- `alert/email/template.go` — EmailPayload, Render(payload) (html, text, error)
- `alert/email/templates/success.html`
- `alert/email/templates/failure.html`
- `alert/email/template_test.go`
- `alert/email/email.go` — Send via Mailgun HTTP API
- `sql/migrations/10_portfolio_alerts.up.sql`
- `sql/migrations/10_portfolio_alerts.down.sql`

**Modified:**
- `snapshot/returns.go` — add BenchmarkCurrentValue, BenchmarkValueAt, PortfolioValueAt
- `backtest/run.go` — add optional Notifier to orchestrator; call after MarkReadyTx and in fail()
- `api/portfolios.go` — mount alert routes
- `cmd/config.go` — mailgunConf struct
- `cmd/viper.go` — mailgun viper defaults
- `cmd/server.go` — mailgun flags, create Checker, call WithNotifier

---

### Task 1: NYSE Trading Days Package

**Files:**
- Create: `alert/tradingdays/holidays.go`
- Create: `alert/tradingdays/tradingdays.go`
- Create: `alert/tradingdays/tradingdays_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// alert/tradingdays/tradingdays_test.go
package tradingdays_test

import (
	"testing"
	"time"

	"github.com/penny-vault/pv-api/alert/tradingdays"
)

func date(y, m, d int) time.Time {
	return time.Date(y, time.Month(m), d, 12, 0, 0, 0, time.UTC)
}

func TestIsTrading(t *testing.T) {
	tests := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"weekday", date(2025, 4, 22), true},                    // Tuesday
		{"saturday", date(2025, 4, 19), false},                  // Saturday
		{"sunday", date(2025, 4, 20), false},                    // Sunday
		{"new years 2025", date(2025, 1, 1), false},             // NYSE holiday
		{"good friday 2025", date(2025, 4, 18), false},          // NYSE holiday
		{"thanksgiving 2025", date(2025, 11, 27), false},        // NYSE holiday
		{"day after thanksgiving", date(2025, 11, 28), true},    // trading day
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tradingdays.IsTrading(tc.t); got != tc.want {
				t.Errorf("IsTrading(%v) = %v; want %v", tc.t.Format("2006-01-02"), got, tc.want)
			}
		})
	}
}

func TestIsLastTradingDayOfWeek(t *testing.T) {
	tests := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"friday no holiday", date(2025, 4, 25), true},          // regular Friday
		{"thursday before normal friday", date(2025, 4, 24), false},
		{"good friday — thursday is last", date(2025, 4, 17), true}, // Good Friday 4/18, so Thursday 4/17 is last
		{"good friday itself", date(2025, 4, 18), false},        // holiday, not a trading day
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tradingdays.IsLastTradingDayOfWeek(tc.t); got != tc.want {
				t.Errorf("IsLastTradingDayOfWeek(%v) = %v; want %v", tc.t.Format("2006-01-02"), got, tc.want)
			}
		})
	}
}

func TestIsLastTradingDayOfMonth(t *testing.T) {
	tests := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"last trading day april 2025", date(2025, 4, 30), true},  // Wednesday
		{"second to last", date(2025, 4, 29), false},
		{"last trading day dec 2025", date(2025, 12, 31), true},   // Wednesday
		{"christmas eve 2025", date(2025, 12, 24), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tradingdays.IsLastTradingDayOfMonth(tc.t); got != tc.want {
				t.Errorf("IsLastTradingDayOfMonth(%v) = %v; want %v", tc.t.Format("2006-01-02"), got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run to confirm failure**

```bash
go test ./alert/tradingdays/... 2>&1
```
Expected: build error (package does not exist yet).

- [ ] **Step 3: Create holidays.go**

```go
// alert/tradingdays/holidays.go
package tradingdays

import "time"

// nyseHolidays lists NYSE market holidays for 2025 and 2026.
var nyseHolidays = []time.Time{
	// 2025
	d(2025, 1, 1),   // New Year's Day
	d(2025, 1, 20),  // MLK Day
	d(2025, 2, 17),  // Presidents' Day
	d(2025, 4, 18),  // Good Friday
	d(2025, 5, 26),  // Memorial Day
	d(2025, 6, 19),  // Juneteenth
	d(2025, 7, 4),   // Independence Day
	d(2025, 9, 1),   // Labor Day
	d(2025, 11, 27), // Thanksgiving
	d(2025, 12, 25), // Christmas
	// 2026
	d(2026, 1, 1),   // New Year's Day
	d(2026, 1, 19),  // MLK Day
	d(2026, 2, 16),  // Presidents' Day
	d(2026, 4, 3),   // Good Friday
	d(2026, 5, 25),  // Memorial Day
	d(2026, 6, 19),  // Juneteenth
	d(2026, 7, 3),   // Independence Day (observed)
	d(2026, 9, 7),   // Labor Day
	d(2026, 11, 26), // Thanksgiving
	d(2026, 12, 25), // Christmas
}

func d(y, m, day int) time.Time {
	return time.Date(y, time.Month(m), day, 0, 0, 0, 0, time.UTC)
}
```

- [ ] **Step 4: Create tradingdays.go**

```go
// alert/tradingdays/tradingdays.go
package tradingdays

import "time"

// IsTrading reports whether t falls on a NYSE trading day.
func IsTrading(t time.Time) bool {
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
		return false
	}
	for _, h := range nyseHolidays {
		if day.Equal(h) {
			return false
		}
	}
	return true
}

// IsLastTradingDayOfWeek reports whether t is the last NYSE trading day in its
// ISO week (Monday–Friday window).
func IsLastTradingDayOfWeek(t time.Time) bool {
	if !IsTrading(t) {
		return false
	}
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	for next := day.AddDate(0, 0, 1); next.Weekday() != time.Saturday; next = next.AddDate(0, 0, 1) {
		if IsTrading(next) {
			return false
		}
	}
	return true
}

// IsLastTradingDayOfMonth reports whether t is the last NYSE trading day in its
// calendar month.
func IsLastTradingDayOfMonth(t time.Time) bool {
	if !IsTrading(t) {
		return false
	}
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	for next := day.AddDate(0, 0, 1); next.Month() == day.Month(); next = next.AddDate(0, 0, 1) {
		if IsTrading(next) {
			return false
		}
	}
	return true
}
```

- [ ] **Step 5: Run tests and confirm pass**

```bash
go test ./alert/tradingdays/... -v 2>&1
```
Expected: all tests PASS.

- [ ] **Step 6: Commit**

```bash
git add alert/tradingdays/
git commit -m "feat(alert): NYSE trading days package"
```

---

### Task 2: Alert Type and Evaluation Logic

**Files:**
- Create: `alert/alert.go`
- Create: `alert/evaluate.go`
- Create: `alert/evaluate_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// alert/evaluate_test.go
package alert

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func ptrTime(t time.Time) *time.Time { return &t }

func TestIsDue(t *testing.T) {
	// Tuesday 2025-04-22 — regular trading day, not last of week/month
	tuesday := time.Date(2025, 4, 22, 15, 0, 0, 0, time.UTC)
	// Friday 2025-04-25 — last trading day of week
	friday := time.Date(2025, 4, 25, 15, 0, 0, 0, time.UTC)
	// Wednesday 2025-04-30 — last trading day of April 2025
	lastOfApril := time.Date(2025, 4, 30, 15, 0, 0, 0, time.UTC)

	yesterday := ptrTime(tuesday.AddDate(0, 0, -1))
	sameDay := ptrTime(tuesday)

	tests := []struct {
		name string
		a    Alert
		now  time.Time
		want bool
	}{
		// scheduled_run: always true
		{"scheduled_run/no prior", Alert{ID: uuid.New(), Frequency: FrequencyScheduledRun}, tuesday, true},
		{"scheduled_run/sent today", Alert{ID: uuid.New(), Frequency: FrequencyScheduledRun, LastSentAt: sameDay}, tuesday, true},

		// daily: true if last sent before today
		{"daily/no prior", Alert{ID: uuid.New(), Frequency: FrequencyDaily}, tuesday, true},
		{"daily/sent yesterday", Alert{ID: uuid.New(), Frequency: FrequencyDaily, LastSentAt: yesterday}, tuesday, true},
		{"daily/sent today", Alert{ID: uuid.New(), Frequency: FrequencyDaily, LastSentAt: sameDay}, tuesday, false},

		// weekly: only on last trading day of week
		{"weekly/not last day", Alert{ID: uuid.New(), Frequency: FrequencyWeekly}, tuesday, false},
		{"weekly/last day no prior", Alert{ID: uuid.New(), Frequency: FrequencyWeekly}, friday, true},
		{"weekly/last day sent today", Alert{ID: uuid.New(), Frequency: FrequencyWeekly, LastSentAt: ptrTime(friday)}, friday, false},
		{"weekly/last day sent prior week", Alert{ID: uuid.New(), Frequency: FrequencyWeekly, LastSentAt: ptrTime(friday.AddDate(0, 0, -7))}, friday, true},

		// monthly: only on last trading day of month
		{"monthly/not last day", Alert{ID: uuid.New(), Frequency: FrequencyMonthly}, tuesday, false},
		{"monthly/last day no prior", Alert{ID: uuid.New(), Frequency: FrequencyMonthly}, lastOfApril, true},
		{"monthly/last day sent today", Alert{ID: uuid.New(), Frequency: FrequencyMonthly, LastSentAt: ptrTime(lastOfApril)}, lastOfApril, false},
		{"monthly/last day sent last month", Alert{ID: uuid.New(), Frequency: FrequencyMonthly, LastSentAt: ptrTime(lastOfApril.AddDate(0, -1, 0))}, lastOfApril, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDue(tc.a, tc.now); got != tc.want {
				t.Errorf("isDue() = %v; want %v", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run to confirm failure**

```bash
go test ./alert/... 2>&1
```
Expected: build error.

- [ ] **Step 3: Create alert.go**

```go
// alert/alert.go
package alert

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const (
	FrequencyScheduledRun = "scheduled_run"
	FrequencyDaily        = "daily"
	FrequencyWeekly       = "weekly"
	FrequencyMonthly      = "monthly"
)

// Alert mirrors one portfolio_alerts row.
type Alert struct {
	ID            uuid.UUID
	PortfolioID   uuid.UUID
	Frequency     string
	Recipients    []string
	LastSentAt    *time.Time
	LastSentValue *float64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Notifier is called by the backtest orchestrator after each run completes.
// Defined here so backtest/run.go can reference it without importing alert.
// (alert.Checker satisfies this interface structurally.)
type Notifier interface {
	NotifyRunComplete(ctx context.Context, portfolioID, runID uuid.UUID, success bool) error
}
```

- [ ] **Step 4: Create evaluate.go**

```go
// alert/evaluate.go
package alert

import (
	"time"

	"github.com/penny-vault/pv-api/alert/tradingdays"
)

// isDue reports whether a should fire at now.
func isDue(a Alert, now time.Time) bool {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	switch a.Frequency {
	case FrequencyScheduledRun:
		return true
	case FrequencyDaily:
		if a.LastSentAt == nil {
			return true
		}
		last := time.Date(a.LastSentAt.Year(), a.LastSentAt.Month(), a.LastSentAt.Day(), 0, 0, 0, 0, time.UTC)
		return today.After(last)
	case FrequencyWeekly:
		if !tradingdays.IsLastTradingDayOfWeek(now) {
			return false
		}
		if a.LastSentAt == nil {
			return true
		}
		last := time.Date(a.LastSentAt.Year(), a.LastSentAt.Month(), a.LastSentAt.Day(), 0, 0, 0, 0, time.UTC)
		return today.After(last)
	case FrequencyMonthly:
		if !tradingdays.IsLastTradingDayOfMonth(now) {
			return false
		}
		if a.LastSentAt == nil {
			return true
		}
		last := time.Date(a.LastSentAt.Year(), a.LastSentAt.Month(), a.LastSentAt.Day(), 0, 0, 0, 0, time.UTC)
		return today.After(last)
	default:
		return false
	}
}
```

- [ ] **Step 5: Run tests**

```bash
go test ./alert/... -v -run TestIsDue 2>&1
```
Expected: all tests PASS.

- [ ] **Step 6: Commit**

```bash
git add alert/
git commit -m "feat(alert): alert type and frequency evaluation"
```

---

### Task 3: Database Migration

**Files:**
- Create: `sql/migrations/10_portfolio_alerts.up.sql`
- Create: `sql/migrations/10_portfolio_alerts.down.sql`

- [ ] **Step 1: Write migration files**

```sql
-- sql/migrations/10_portfolio_alerts.up.sql
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

```sql
-- sql/migrations/10_portfolio_alerts.down.sql
DROP TABLE IF EXISTS portfolio_alerts;
```

- [ ] **Step 2: Verify migration runs cleanly**

```bash
go run . serve --config pvapi.toml 2>&1 | head -5
```
Expected: server starts without migration errors.

- [ ] **Step 3: Commit**

```bash
git add sql/migrations/10_portfolio_alerts.up.sql sql/migrations/10_portfolio_alerts.down.sql
git commit -m "feat(alert): add portfolio_alerts migration"
```

---

### Task 4: Alert PostgreSQL Store

**Files:**
- Create: `alert/db.go`

- [ ] **Step 1: Write db.go**

```go
// alert/db.go
package alert

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when an alert row is not found.
var ErrNotFound = errors.New("alert not found")

// Store persists portfolio alerts.
type Store interface {
	Create(ctx context.Context, portfolioID uuid.UUID, frequency string, recipients []string) (Alert, error)
	List(ctx context.Context, portfolioID uuid.UUID) ([]Alert, error)
	Get(ctx context.Context, id uuid.UUID) (Alert, error)
	Update(ctx context.Context, id uuid.UUID, frequency string, recipients []string) (Alert, error)
	Delete(ctx context.Context, id uuid.UUID) error
	MarkSent(ctx context.Context, id uuid.UUID, sentAt time.Time, value float64) error
}

const alertColumns = `id, portfolio_id, frequency, recipients, last_sent_at, last_sent_value, created_at, updated_at`

// PoolStore implements Store against a pgxpool.Pool.
type PoolStore struct {
	pool *pgxpool.Pool
}

// NewPoolStore constructs a PoolStore.
func NewPoolStore(pool *pgxpool.Pool) *PoolStore {
	return &PoolStore{pool: pool}
}

func scanAlert(row pgx.Row) (Alert, error) {
	var a Alert
	err := row.Scan(&a.ID, &a.PortfolioID, &a.Frequency, &a.Recipients,
		&a.LastSentAt, &a.LastSentValue, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Alert{}, ErrNotFound
	}
	return a, err
}

func (s *PoolStore) Create(ctx context.Context, portfolioID uuid.UUID, frequency string, recipients []string) (Alert, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO portfolio_alerts (portfolio_id, frequency, recipients)
		VALUES ($1, $2, $3)
		RETURNING `+alertColumns,
		portfolioID, frequency, recipients,
	)
	a, err := scanAlert(row)
	if err != nil {
		return Alert{}, fmt.Errorf("create alert: %w", err)
	}
	return a, nil
}

func (s *PoolStore) List(ctx context.Context, portfolioID uuid.UUID) ([]Alert, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+alertColumns+` FROM portfolio_alerts WHERE portfolio_id=$1 ORDER BY created_at`,
		portfolioID,
	)
	if err != nil {
		return nil, fmt.Errorf("list alerts: %w", err)
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		a, scanErr := scanAlert(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *PoolStore) Get(ctx context.Context, id uuid.UUID) (Alert, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+alertColumns+` FROM portfolio_alerts WHERE id=$1`, id)
	a, err := scanAlert(row)
	if err != nil {
		return Alert{}, fmt.Errorf("get alert: %w", err)
	}
	return a, nil
}

func (s *PoolStore) Update(ctx context.Context, id uuid.UUID, frequency string, recipients []string) (Alert, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE portfolio_alerts
		   SET frequency=$2, recipients=$3, updated_at=now()
		 WHERE id=$1
		RETURNING `+alertColumns,
		id, frequency, recipients,
	)
	a, err := scanAlert(row)
	if err != nil {
		return Alert{}, fmt.Errorf("update alert: %w", err)
	}
	return a, nil
}

func (s *PoolStore) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM portfolio_alerts WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("delete alert: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PoolStore) MarkSent(ctx context.Context, id uuid.UUID, sentAt time.Time, value float64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE portfolio_alerts
		   SET last_sent_at=$2, last_sent_value=$3, updated_at=now()
		 WHERE id=$1`,
		id, sentAt, value,
	)
	return err
}
```

- [ ] **Step 2: Build check**

```bash
go build ./alert/... 2>&1
```
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add alert/db.go
git commit -m "feat(alert): PostgreSQL alert store"
```

---

### Task 5: Snapshot Benchmark and Portfolio Value Helpers

**Files:**
- Modify: `snapshot/returns.go`

- [ ] **Step 1: Add three exported methods to snapshot/returns.go** (append after the last function in the file)

```go
// PortfolioValueAt returns the portfolio_value at or before t.
// Used by the alert package to compute the delta since the last email.
func (r *Reader) PortfolioValueAt(ctx context.Context, t time.Time) (float64, error) {
	return r.perfAsOf(ctx, "portfolio_value", t, false)
}

// BenchmarkCurrentValue returns the most recent benchmark_value in the snapshot.
func (r *Reader) BenchmarkCurrentValue(ctx context.Context) (float64, error) {
	return r.latestPerf(ctx, "benchmark_value")
}

// BenchmarkValueAt returns the benchmark_value at or before t.
func (r *Reader) BenchmarkValueAt(ctx context.Context, t time.Time) (float64, error) {
	return r.perfAsOf(ctx, "benchmark_value", t, false)
}
```

- [ ] **Step 2: Build check**

```bash
go build ./snapshot/... 2>&1
```
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add snapshot/returns.go
git commit -m "feat(snapshot): export PortfolioValueAt, BenchmarkCurrentValue, BenchmarkValueAt"
```

---

### Task 6: Email Templates and Payload

**Files:**
- Create: `alert/email/template.go`
- Create: `alert/email/templates/success.html`
- Create: `alert/email/templates/failure.html`

- [ ] **Step 1: Create template.go**

```go
// alert/email/template.go
package email

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"math"
	"strings"
	"time"
)

//go:embed templates/success.html
var successHTML string

//go:embed templates/failure.html
var failureHTML string

var (
	successTmpl = template.Must(template.New("success").Parse(successHTML))
	failureTmpl = template.Must(template.New("failure").Parse(failureHTML))
)

// TradeRow is one row in the Trades to Execute table.
type TradeRow struct {
	Ticker      string
	Action      string // "Buy" or "Sell"
	ActionColor string // CSS colour
	Shares      string
	Value       string // formatted "$1,234"
}

// HoldingRow is one row in the Target Allocation table.
type HoldingRow struct {
	Ticker    string
	WeightPct string // "45.3"
	Value     string // "$12,345"
}

// Payload carries all data the email templates need.
type Payload struct {
	// Header
	PortfolioName string
	StrategyCode  string
	RunDate       string
	Success       bool

	// Balance
	CurrentValue string // "$123,456"
	HasDelta     bool
	DeltaPct     string // "+12.0%"
	DeltaAbs     string // "+$1,240"
	SinceLabel   string // "Tuesday" or "Apr 15"
	DeltaColor   string // "#22c55e" or "#ef4444"

	// Benchmark
	Benchmark          string // "SPY"
	BenchmarkDeltaPct  string // "+10.8%"
	RelativeDelta      string // "+1.2%"
	RelativeColor      string // "#22c55e" or "#ef4444"

	// Trades
	Trades []TradeRow

	// Holdings
	Holdings []HoldingRow

	// Failure only
	ErrorMessage   string
	LastKnownValue string // may be empty
}

// Render executes the appropriate template and returns (html, plaintext, error).
func Render(p Payload) (string, string, error) {
	tmpl := successTmpl
	if !p.Success {
		tmpl = failureTmpl
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, p); err != nil {
		return "", "", fmt.Errorf("render email template: %w", err)
	}
	return buf.String(), buildPlaintext(p), nil
}

// FormatDelta builds the DeltaPct, DeltaAbs, DeltaColor, SinceLabel, HasDelta
// fields on a Payload given current/previous values and the send time.
func FormatDelta(currentValue, previousValue float64, lastSentAt time.Time) (deltaPct, deltaAbs, color, since string, hasDelta bool) {
	if previousValue <= 0 {
		return "", "", "", "", false
	}
	diff := currentValue - previousValue
	pct := diff / previousValue * 100
	sign := "+"
	if diff < 0 {
		sign = "-"
		diff = -diff
		pct = math.Abs(pct)
	}
	deltaPct = fmt.Sprintf("%s%.1f%%", sign, pct)
	deltaAbs = fmt.Sprintf("%s$%s", sign, formatMoney(diff))
	if sign == "+" {
		color = "#22c55e"
	} else {
		color = "#ef4444"
	}
	since = lastSentAt.Format("Monday")
	if time.Since(lastSentAt) > 7*24*time.Hour {
		since = lastSentAt.Format("Jan 2")
	}
	return deltaPct, deltaAbs, color, since, true
}

func formatMoney(v float64) string {
	s := fmt.Sprintf("%.0f", v)
	if len(s) <= 3 {
		return s
	}
	var result strings.Builder
	offset := len(s) % 3
	if offset > 0 {
		result.WriteString(s[:offset])
	}
	for i := offset; i < len(s); i += 3 {
		if i > 0 {
			result.WriteByte(',')
		}
		result.WriteString(s[i : i+3])
	}
	return result.String()
}

func buildPlaintext(p Payload) string {
	var b strings.Builder
	b.WriteString(p.PortfolioName + " — " + p.RunDate + "\n\n")
	if !p.Success {
		b.WriteString("ERROR: " + p.ErrorMessage + "\n")
		return b.String()
	}
	b.WriteString("Portfolio Value: " + p.CurrentValue + "\n")
	if p.HasDelta {
		b.WriteString(p.DeltaPct + " (" + p.DeltaAbs + ") since " + p.SinceLabel + "\n")
		b.WriteString("Benchmark (" + p.Benchmark + ") " + p.BenchmarkDeltaPct + "\n\n")
	}
	if len(p.Trades) == 0 {
		b.WriteString("No trades required.\n\n")
	} else {
		b.WriteString("Trades to Execute:\n")
		for _, tr := range p.Trades {
			b.WriteString(fmt.Sprintf("  %s %s %s shares (~%s)\n", tr.Action, tr.Ticker, tr.Shares, tr.Value))
		}
		b.WriteString("\n")
	}
	b.WriteString("Target Allocation:\n")
	for _, h := range p.Holdings {
		b.WriteString(fmt.Sprintf("  %s  %s%%  %s\n", h.Ticker, h.WeightPct, h.Value))
	}
	return b.String()
}
```

- [ ] **Step 2: Create success.html**

```html
<!-- alert/email/templates/success.html -->
<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1.0"></head>
<body style="margin:0;padding:0;background:#f0f2f5;font-family:Arial,Helvetica,sans-serif;">
<table width="100%" cellpadding="0" cellspacing="0" style="padding:32px 0;">
<tr><td align="center">
<table width="600" cellpadding="0" cellspacing="0" style="background:#ffffff;border-radius:10px;overflow:hidden;max-width:600px;width:100%;">

  <!-- Header -->
  <tr><td style="background:#0f172a;padding:28px 32px;">
    <p style="margin:0;color:#f8fafc;font-size:20px;font-weight:700;letter-spacing:-0.3px;">{{.PortfolioName}}</p>
    <p style="margin:6px 0 0;color:#94a3b8;font-size:13px;">{{.StrategyCode}} &middot; {{.RunDate}}</p>
  </td></tr>

  <!-- Balance -->
  <tr><td style="padding:28px 32px;border-bottom:1px solid #e2e8f0;">
    <p style="margin:0 0 4px;color:#64748b;font-size:11px;font-weight:600;text-transform:uppercase;letter-spacing:0.8px;">Portfolio Value</p>
    <p style="margin:0 0 10px;color:#0f172a;font-size:36px;font-weight:700;letter-spacing:-1px;">{{.CurrentValue}}</p>
    {{if .HasDelta}}
    <p style="margin:0 0 4px;font-size:15px;">
      <span style="color:{{.DeltaColor}};font-weight:600;">{{.DeltaPct}} ({{.DeltaAbs}}) since {{.SinceLabel}}</span>
    </p>
    <p style="margin:0;color:#64748b;font-size:13px;">
      Benchmark ({{.Benchmark}}) {{.BenchmarkDeltaPct}}
      &nbsp;<span style="color:{{.RelativeColor}};">{{.RelativeDelta}} vs portfolio</span>
    </p>
    {{end}}
  </td></tr>

  <!-- Trades -->
  <tr><td style="padding:28px 32px;border-bottom:1px solid #e2e8f0;">
    <p style="margin:0 0 16px;color:#0f172a;font-size:15px;font-weight:600;">Trades to Execute</p>
    {{if .Trades}}
    <table width="100%" cellpadding="0" cellspacing="0" style="border-collapse:collapse;">
      <tr style="background:#f8fafc;">
        <td style="padding:8px 10px;font-size:11px;color:#64748b;font-weight:600;text-transform:uppercase;letter-spacing:0.5px;">Ticker</td>
        <td style="padding:8px 10px;font-size:11px;color:#64748b;font-weight:600;text-transform:uppercase;letter-spacing:0.5px;">Action</td>
        <td style="padding:8px 10px;font-size:11px;color:#64748b;font-weight:600;text-transform:uppercase;letter-spacing:0.5px;text-align:right;">Shares</td>
        <td style="padding:8px 10px;font-size:11px;color:#64748b;font-weight:600;text-transform:uppercase;letter-spacing:0.5px;text-align:right;">Value</td>
      </tr>
      {{range .Trades}}
      <tr style="border-top:1px solid #e2e8f0;">
        <td style="padding:11px 10px;font-size:14px;font-weight:600;color:#0f172a;">{{.Ticker}}</td>
        <td style="padding:11px 10px;font-size:14px;color:{{.ActionColor}};font-weight:600;">{{.Action}}</td>
        <td style="padding:11px 10px;font-size:14px;color:#334155;text-align:right;">{{.Shares}}</td>
        <td style="padding:11px 10px;font-size:14px;color:#334155;text-align:right;">{{.Value}}</td>
      </tr>
      {{end}}
    </table>
    {{else}}
    <p style="margin:0;color:#64748b;font-size:14px;">No trades required.</p>
    {{end}}
  </td></tr>

  <!-- Allocation -->
  {{if .Holdings}}
  <tr><td style="padding:28px 32px;">
    <p style="margin:0 0 16px;color:#0f172a;font-size:15px;font-weight:600;">Target Allocation</p>
    <table width="100%" cellpadding="0" cellspacing="0" style="border-collapse:collapse;">
      <tr style="background:#f8fafc;">
        <td style="padding:8px 10px;font-size:11px;color:#64748b;font-weight:600;text-transform:uppercase;letter-spacing:0.5px;">Ticker</td>
        <td style="padding:8px 10px;font-size:11px;color:#64748b;font-weight:600;text-transform:uppercase;letter-spacing:0.5px;text-align:right;">Weight</td>
        <td style="padding:8px 10px;font-size:11px;color:#64748b;font-weight:600;text-transform:uppercase;letter-spacing:0.5px;text-align:right;">Value</td>
      </tr>
      {{range .Holdings}}
      <tr style="border-top:1px solid #e2e8f0;">
        <td style="padding:11px 10px;font-size:14px;font-weight:600;color:#0f172a;">{{.Ticker}}</td>
        <td style="padding:11px 10px;font-size:14px;color:#334155;text-align:right;">{{.WeightPct}}%</td>
        <td style="padding:11px 10px;font-size:14px;color:#334155;text-align:right;">{{.Value}}</td>
      </tr>
      {{end}}
    </table>
  </td></tr>
  {{end}}

  <!-- Footer -->
  <tr><td style="padding:16px 32px;background:#f8fafc;border-top:1px solid #e2e8f0;">
    <p style="margin:0;color:#94a3b8;font-size:12px;text-align:center;">Penny Vault &middot; Portfolio Alerts</p>
  </td></tr>

</table>
</td></tr>
</table>
</body>
</html>
```

- [ ] **Step 3: Create failure.html**

```html
<!-- alert/email/templates/failure.html -->
<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1.0"></head>
<body style="margin:0;padding:0;background:#f0f2f5;font-family:Arial,Helvetica,sans-serif;">
<table width="100%" cellpadding="0" cellspacing="0" style="padding:32px 0;">
<tr><td align="center">
<table width="600" cellpadding="0" cellspacing="0" style="background:#ffffff;border-radius:10px;overflow:hidden;max-width:600px;width:100%;">

  <!-- Header -->
  <tr><td style="background:#0f172a;padding:28px 32px;">
    <p style="margin:0;color:#f8fafc;font-size:20px;font-weight:700;letter-spacing:-0.3px;">{{.PortfolioName}}</p>
    <p style="margin:6px 0 0;color:#94a3b8;font-size:13px;">
      {{.StrategyCode}} &middot; {{.RunDate}}
      &nbsp;<span style="background:#ef4444;color:#fff;font-size:11px;font-weight:700;padding:2px 8px;border-radius:4px;text-transform:uppercase;letter-spacing:0.5px;">Error</span>
    </p>
  </td></tr>

  <!-- Error -->
  <tr><td style="padding:28px 32px;">
    {{if .LastKnownValue}}
    <p style="margin:0 0 4px;color:#64748b;font-size:11px;font-weight:600;text-transform:uppercase;letter-spacing:0.8px;">Last Known Value</p>
    <p style="margin:0 0 24px;color:#0f172a;font-size:28px;font-weight:700;">{{.LastKnownValue}}</p>
    {{end}}
    <p style="margin:0 0 8px;color:#64748b;font-size:11px;font-weight:600;text-transform:uppercase;letter-spacing:0.8px;">Error</p>
    <div style="background:#fef2f2;border-left:4px solid #ef4444;padding:14px 16px;border-radius:4px;">
      <p style="margin:0;color:#7f1d1d;font-size:14px;line-height:1.5;font-family:monospace;">{{.ErrorMessage}}</p>
    </div>
    <p style="margin:16px 0 0;color:#64748b;font-size:13px;">No trades were executed. The previous allocation remains unchanged.</p>
  </td></tr>

  <!-- Footer -->
  <tr><td style="padding:16px 32px;background:#f8fafc;border-top:1px solid #e2e8f0;">
    <p style="margin:0;color:#94a3b8;font-size:12px;text-align:center;">Penny Vault &middot; Portfolio Alerts</p>
  </td></tr>

</table>
</td></tr>
</table>
</body>
</html>
```

- [ ] **Step 4: Build check**

```bash
go build ./alert/... 2>&1
```
Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add alert/email/
git commit -m "feat(alert): email payload, templates, and renderer"
```

---

### Task 7: Email Template Tests

**Files:**
- Create: `alert/email/template_test.go`

- [ ] **Step 1: Write tests**

```go
// alert/email/template_test.go
package email_test

import (
	"strings"
	"testing"
	"time"

	"github.com/penny-vault/pv-api/alert/email"
)

func successPayload() email.Payload {
	return email.Payload{
		PortfolioName: "My Portfolio",
		StrategyCode:  "rsi-mean-reversion",
		RunDate:       "Monday, April 21, 2026",
		Success:       true,
		CurrentValue:  "$103,240",
		HasDelta:      true,
		DeltaPct:      "+12.0%",
		DeltaAbs:      "+$1,240",
		SinceLabel:    "Tuesday",
		DeltaColor:    "#22c55e",
		Benchmark:     "SPY",
		BenchmarkDeltaPct: "+10.8%",
		RelativeDelta: "+1.2%",
		RelativeColor: "#22c55e",
		Trades: []email.TradeRow{
			{Ticker: "VTI", Action: "Buy", ActionColor: "#22c55e", Shares: "12", Value: "$2,400"},
			{Ticker: "BND", Action: "Sell", ActionColor: "#ef4444", Shares: "5", Value: "$450"},
		},
		Holdings: []email.HoldingRow{
			{Ticker: "VTI", WeightPct: "90.0", Value: "$92,916"},
			{Ticker: "$CASH", WeightPct: "10.0", Value: "$10,324"},
		},
	}
}

func TestRenderSuccess(t *testing.T) {
	p := successPayload()
	html, text, err := email.Render(p)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	checks := []string{
		"My Portfolio",
		"$103,240",
		"+12.0%",
		"+$1,240",
		"since Tuesday",
		"SPY",
		"+10.8%",
		"VTI",
		"Buy",
		"BND",
		"Sell",
		"90.0%",
	}
	for _, want := range checks {
		if !strings.Contains(html, want) {
			t.Errorf("success HTML missing %q", want)
		}
		if !strings.Contains(text, want) {
			t.Errorf("success text missing %q", want)
		}
	}
}

func TestRenderFailure(t *testing.T) {
	p := email.Payload{
		PortfolioName:  "My Portfolio",
		StrategyCode:   "rsi-mean-reversion",
		RunDate:        "Monday, April 21, 2026",
		Success:        false,
		LastKnownValue: "$101,000",
		ErrorMessage:   "strategy binary exited with code 1",
	}
	html, text, err := email.Render(p)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{"My Portfolio", "Error", "strategy binary exited with code 1", "$101,000"} {
		if !strings.Contains(html, want) {
			t.Errorf("failure HTML missing %q", want)
		}
	}
	if !strings.Contains(text, "ERROR:") {
		t.Error("failure plaintext missing ERROR: prefix")
	}
}

func TestFormatDelta(t *testing.T) {
	now := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
	lastSent := time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC) // 7 days ago → "Apr 14"

	pct, abs, color, since, hasDelta := email.FormatDelta(103240, 102000, lastSent)
	if !hasDelta {
		t.Fatal("expected hasDelta=true")
	}
	if !strings.HasPrefix(pct, "+") {
		t.Errorf("pct = %q; want positive", pct)
	}
	if color != "#22c55e" {
		t.Errorf("color = %q; want green", color)
	}
	_ = abs
	_ = since
	_ = now

	// Negative delta
	pct2, _, color2, _, _ := email.FormatDelta(99000, 102000, lastSent)
	if !strings.HasPrefix(pct2, "-") {
		t.Errorf("pct = %q; want negative", pct2)
	}
	if color2 != "#ef4444" {
		t.Errorf("color = %q; want red", color2)
	}
}
```

- [ ] **Step 2: Run tests**

```bash
go test ./alert/email/... -v 2>&1
```
Expected: all PASS.

- [ ] **Step 3: Commit**

```bash
git add alert/email/template_test.go
git commit -m "test(alert): email template rendering tests"
```

---

### Task 8: Mailgun HTTP Sender

**Files:**
- Create: `alert/email/email.go`

- [ ] **Step 1: Write email.go**

```go
// alert/email/email.go
package email

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Config holds Mailgun credentials.
type Config struct {
	Domain      string
	APIKey      string
	FromAddress string
}

// Send delivers an email to all recipients via the Mailgun HTTP API.
// Returns nil without sending if cfg.APIKey is empty.
func Send(ctx context.Context, cfg Config, recipients []string, subject, htmlBody, textBody string) error {
	if cfg.APIKey == "" {
		return nil
	}
	endpoint := fmt.Sprintf("https://api.mailgun.net/v3/%s/messages", url.PathEscape(cfg.Domain))

	form := url.Values{}
	form.Set("from", cfg.FromAddress)
	form.Set("subject", subject)
	form.Set("html", htmlBody)
	form.Set("text", textBody)
	for _, r := range recipients {
		form.Add("to", r)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("mailgun: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("api:"+cfg.APIKey)))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("mailgun: send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("mailgun: unexpected status %d", resp.StatusCode)
	}
	return nil
}
```

- [ ] **Step 2: Write sender test using httpmock** (already in go.mod)

```go
// alert/email/email_test.go
package email_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/jarcoal/httpmock"

	"github.com/penny-vault/pv-api/alert/email"
)

func TestSend(t *testing.T) {
	httpmock.Activate()
	defer httpmock.DeactivateAndReset()

	httpmock.RegisterResponder(http.MethodPost,
		"https://api.mailgun.net/v3/mg.pennyvault.com/messages",
		httpmock.NewStringResponder(200, `{"id":"<abc>","message":"Queued"}`),
	)

	cfg := email.Config{
		Domain:      "mg.pennyvault.com",
		APIKey:      "key-test",
		FromAddress: "Penny Vault <no-reply@mg.pennyvault.com>",
	}
	err := email.Send(context.Background(), cfg,
		[]string{"user@example.com"},
		"Test Subject", "<p>hello</p>", "hello",
	)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if count := httpmock.GetTotalCallCount(); count != 1 {
		t.Errorf("expected 1 HTTP call, got %d", count)
	}
}

func TestSendSkipsWhenNoAPIKey(t *testing.T) {
	httpmock.Activate()
	defer httpmock.DeactivateAndReset()

	cfg := email.Config{Domain: "mg.pennyvault.com", APIKey: ""}
	err := email.Send(context.Background(), cfg, []string{"user@example.com"}, "subj", "<p>h</p>", "h")
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if count := httpmock.GetTotalCallCount(); count != 0 {
		t.Errorf("expected 0 HTTP calls, got %d", count)
	}
}
```

- [ ] **Step 3: Run tests**

```bash
go test ./alert/email/... -v 2>&1
```
Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add alert/email/email.go alert/email/email_test.go
git commit -m "feat(alert): Mailgun HTTP sender"
```

---

### Task 9: Checker — NotifyRunComplete

**Files:**
- Create: `alert/checker.go`

- [ ] **Step 1: Write checker.go**

```go
// alert/checker.go
package alert

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"

	"github.com/penny-vault/pv-api/alert/email"
	"github.com/penny-vault/pv-api/openapi"
	"github.com/penny-vault/pv-api/snapshot"
)

// portfolioData is the subset of portfolio fields needed for email rendering.
type portfolioData struct {
	Name         string
	StrategyCode string
	Benchmark    string
	CurrentValue *float64
	Status       string
	LastError    *string
	SnapshotPath *string
}

// Checker implements Notifier using Postgres and Mailgun.
type Checker struct {
	pool        *pgxpool.Pool
	store       *PoolStore
	emailConfig email.Config
}

// NewChecker creates a Checker. Sends are skipped if emailConfig.APIKey is empty.
func NewChecker(pool *pgxpool.Pool, cfg email.Config) *Checker {
	return &Checker{pool: pool, store: NewPoolStore(pool), emailConfig: cfg}
}

// NotifyRunComplete evaluates and dispatches alerts for portfolioID.
func (c *Checker) NotifyRunComplete(ctx context.Context, portfolioID, _ uuid.UUID, success bool) error {
	alerts, err := c.store.List(ctx, portfolioID)
	if err != nil {
		return fmt.Errorf("alert checker: list: %w", err)
	}
	if len(alerts) == 0 {
		return nil
	}

	now := time.Now().UTC()
	port, err := c.loadPortfolio(ctx, portfolioID)
	if err != nil {
		return fmt.Errorf("alert checker: load portfolio: %w", err)
	}

	for _, a := range alerts {
		if !isDue(a, now) {
			continue
		}
		if sendErr := c.sendOne(ctx, a, port, now, success); sendErr != nil {
			log.Warn().Err(sendErr).Stringer("alert_id", a.ID).Msg("alert: send failed")
		}
	}
	return nil
}

func (c *Checker) loadPortfolio(ctx context.Context, id uuid.UUID) (portfolioData, error) {
	var p portfolioData
	err := c.pool.QueryRow(ctx,
		`SELECT name, strategy_code, benchmark, current_value, status, last_error, snapshot_path
		   FROM portfolios WHERE id=$1`, id,
	).Scan(&p.Name, &p.StrategyCode, &p.Benchmark,
		&p.CurrentValue, &p.Status, &p.LastError, &p.SnapshotPath)
	return p, err
}

func (c *Checker) snapshotPathBefore(ctx context.Context, portfolioID uuid.UUID, before time.Time) *string {
	var path string
	err := c.pool.QueryRow(ctx,
		`SELECT snapshot_path FROM backtest_runs
		  WHERE portfolio_id=$1 AND status='success' AND finished_at <= $2
		  ORDER BY finished_at DESC LIMIT 1`,
		portfolioID, before,
	).Scan(&path)
	if err != nil {
		return nil
	}
	return &path
}

func (c *Checker) sendOne(ctx context.Context, a Alert, port portfolioData, now time.Time, success bool) error {
	payload := c.buildPayload(ctx, a, port, now, success)

	htmlBody, textBody, err := email.Render(payload)
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}

	subject := fmt.Sprintf("Portfolio Update: %s", port.Name)
	if !success {
		subject = fmt.Sprintf("Portfolio Error: %s", port.Name)
	}

	if err := email.Send(ctx, c.emailConfig, a.Recipients, subject, htmlBody, textBody); err != nil {
		return fmt.Errorf("send: %w", err)
	}

	curVal := 0.0
	if port.CurrentValue != nil {
		curVal = *port.CurrentValue
	}
	if markErr := c.store.MarkSent(ctx, a.ID, now, curVal); markErr != nil {
		log.Warn().Err(markErr).Stringer("alert_id", a.ID).Msg("alert: mark sent failed")
	}
	return nil
}

func (c *Checker) buildPayload(ctx context.Context, a Alert, port portfolioData, now time.Time, success bool) email.Payload {
	p := email.Payload{
		PortfolioName: port.Name,
		StrategyCode:  port.StrategyCode,
		RunDate:       now.Format("Monday, January 2, 2006"),
		Success:       success,
	}

	if !success {
		if port.LastError != nil {
			p.ErrorMessage = *port.LastError
		}
		if port.CurrentValue != nil {
			p.LastKnownValue = "$" + email.FormatMoneyVal(*port.CurrentValue)
		}
		return p
	}

	// Success: current value
	if port.CurrentValue != nil {
		p.CurrentValue = "$" + email.FormatMoneyVal(*port.CurrentValue)
	}

	// Delta since last send
	if a.LastSentAt != nil && a.LastSentValue != nil && *a.LastSentValue > 0 && port.CurrentValue != nil {
		pct, abs, color, since, hasDelta := email.FormatDelta(*port.CurrentValue, *a.LastSentValue, *a.LastSentAt)
		p.HasDelta = hasDelta
		p.DeltaPct = pct
		p.DeltaAbs = abs
		p.DeltaColor = color
		p.SinceLabel = since
	}

	// Benchmark delta from current snapshot
	if port.SnapshotPath != nil {
		c.fillBenchmarkDelta(ctx, &p, *port.SnapshotPath, a.LastSentAt, port.Benchmark)
	}

	// Holdings and trades diff
	if port.SnapshotPath != nil {
		c.fillHoldingsAndTrades(ctx, &p, a, port)
	}

	return p
}

func (c *Checker) fillBenchmarkDelta(ctx context.Context, p *email.Payload, snapshotPath string, lastSentAt *time.Time, benchmark string) {
	r, err := snapshot.Open(snapshotPath)
	if err != nil {
		return
	}
	defer r.Close()

	curBench, err := r.BenchmarkCurrentValue(ctx)
	if err != nil || curBench <= 0 {
		return
	}

	p.Benchmark = benchmark
	if lastSentAt == nil {
		return
	}

	prevBench, err := r.BenchmarkValueAt(ctx, *lastSentAt)
	if err != nil || prevBench <= 0 {
		return
	}

	benchPct := (curBench - prevBench) / prevBench * 100
	sign := "+"
	if benchPct < 0 {
		sign = "-"
		benchPct = -benchPct
	}
	p.BenchmarkDeltaPct = fmt.Sprintf("%s%.1f%%", sign, benchPct)

	// Relative delta vs portfolio
	if p.HasDelta && p.DeltaPct != "" {
		// parse portfolio pct back — simpler to recompute from values
		if p.DeltaColor == "#22c55e" {
		} // color already set
		// relative = portfolio% - benchmark%
		portPctVal := 0.0
		fmt.Sscanf(p.DeltaPct, "%f", &portPctVal)
		rel := portPctVal - benchPct
		relSign := "+"
		relColor := "#22c55e"
		if rel < 0 {
			relSign = "-"
			rel = -rel
			relColor = "#ef4444"
		}
		p.RelativeDelta = fmt.Sprintf("%s%.1f%%", relSign, rel)
		p.RelativeColor = relColor
	}
}

func (c *Checker) fillHoldingsAndTrades(ctx context.Context, p *email.Payload, a Alert, port portfolioData) {
	r, err := snapshot.Open(*port.SnapshotPath)
	if err != nil {
		return
	}
	defer r.Close()

	cur, err := r.CurrentHoldings(ctx)
	if err != nil || cur == nil {
		return
	}

	// Build target allocation
	total := 0.0
	for _, h := range cur.Items {
		total += h.MarketValue
	}
	for _, h := range cur.Items {
		weight := 0.0
		if total > 0 {
			weight = h.MarketValue / total * 100
		}
		p.Holdings = append(p.Holdings, email.HoldingRow{
			Ticker:    h.Ticker,
			WeightPct: fmt.Sprintf("%.1f", weight),
			Value:     "$" + email.FormatMoneyVal(h.MarketValue),
		})
	}

	// Trades diff: compare against previous snapshot
	if a.LastSentAt == nil {
		// First send: all current holdings are "buys"
		for _, h := range cur.Items {
			if h.Ticker == "$CASH" {
				continue
			}
			p.Trades = append(p.Trades, email.TradeRow{
				Ticker:      h.Ticker,
				Action:      "Buy",
				ActionColor: "#22c55e",
				Shares:      fmt.Sprintf("%.0f", h.Quantity),
				Value:       "$" + email.FormatMoneyVal(h.MarketValue),
			})
		}
		return
	}

	prevPath := c.snapshotPathBefore(ctx, a.PortfolioID, *a.LastSentAt)
	if prevPath == nil {
		return
	}
	prev, err := snapshot.Open(*prevPath)
	if err != nil {
		return
	}
	defer prev.Close()

	prevHoldings, err := prev.CurrentHoldings(ctx)
	if err != nil || prevHoldings == nil {
		return
	}

	// Build maps: ticker → quantity
	curMap := map[string]float64{}
	for _, h := range cur.Items {
		curMap[h.Ticker] = h.Quantity
	}
	prevMap := map[string]float64{}
	for _, h := range prevHoldings.Items {
		prevMap[h.Ticker] = h.Quantity
	}

	// Find diffs
	seen := map[string]bool{}
	for _, h := range cur.Items {
		if h.Ticker == "$CASH" {
			continue
		}
		seen[h.Ticker] = true
		diff := h.Quantity - prevMap[h.Ticker]
		if diff == 0 {
			continue
		}
		action, color := "Buy", "#22c55e"
		if diff < 0 {
			action, color = "Sell", "#ef4444"
			diff = -diff
		}
		p.Trades = append(p.Trades, email.TradeRow{
			Ticker:      h.Ticker,
			Action:      action,
			ActionColor: color,
			Shares:      fmt.Sprintf("%.0f", diff),
			Value:       "$" + email.FormatMoneyVal(diff*h.MarketValue/h.Quantity),
		})
	}
	// Positions fully closed (in prev, not in cur)
	for _, h := range prevHoldings.Items {
		if h.Ticker == "$CASH" || seen[h.Ticker] {
			continue
		}
		p.Trades = append(p.Trades, email.TradeRow{
			Ticker:      h.Ticker,
			Action:      "Sell",
			ActionColor: "#ef4444",
			Shares:      fmt.Sprintf("%.0f", h.Quantity),
			Value:       "$" + email.FormatMoneyVal(h.MarketValue),
		})
	}
}
```

- [ ] **Step 2: Export FormatMoneyVal from template.go** (rename the private `formatMoney` to `FormatMoneyVal`)

In `alert/email/template.go`, change:
```go
func formatMoney(v float64) string {
```
to:
```go
// FormatMoneyVal formats a float64 as a comma-separated integer string.
func FormatMoneyVal(v float64) string {
```

And update all internal callers in template.go from `formatMoney(` to `FormatMoneyVal(`.

- [ ] **Step 3: Build check**

```bash
go build ./alert/... 2>&1
```
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add alert/checker.go alert/email/template.go
git commit -m "feat(alert): CheckAndSend orchestration"
```

---

### Task 10: Wire Notifier into Backtest Orchestrator

**Files:**
- Modify: `backtest/run.go`

- [ ] **Step 1: Add Notifier interface and notifier field to orchestrator**

At the top of `backtest/run.go`, after the imports, add the interface and update the orchestrator struct:

```go
// Notifier is called after each run completes. alert.Checker implements this.
type Notifier interface {
	NotifyRunComplete(ctx context.Context, portfolioID, runID uuid.UUID, success bool) error
}
```

In the `orchestrator` struct (currently at line ~42), add `notifier Notifier`:

```go
type orchestrator struct {
	cfg          Config
	runner       Runner
	artifactKind ArtifactKind
	ps           PortfolioStore
	rs           RunStore
	resolve      ArtifactResolver
	notifier     Notifier // optional; nil = no alerts
}
```

Add `WithNotifier` setter after `NewRunner`:

```go
// WithNotifier attaches an alert notifier to the orchestrator.
func (o *orchestrator) WithNotifier(n Notifier) *orchestrator {
	o.notifier = n
	return o
}
```

- [ ] **Step 2: Call notifier after MarkReadyTx in Run()**

In `backtest/run.go`, after the `MarkReadyTx` success block:

```go
	if err := o.ps.MarkReadyTx(ctx, portfolioID, runID, final,
		kp.CurrentValue, kp.YtdReturn, kp.MaxDrawdown, kp.Sharpe, kp.Cagr,
		kp.InceptionDate, durationMs(time.Since(started))); err != nil {
		return o.fail(ctx, portfolioID, runID, started, fmt.Errorf("mark ready: %w", err))
	}
	if o.notifier != nil {
		if err := o.notifier.NotifyRunComplete(ctx, portfolioID, runID, true); err != nil {
			log.Warn().Err(err).Stringer("portfolio_id", portfolioID).Msg("alert notification failed")
		}
	}
	log.Info().Stringer("portfolio_id", portfolioID).Stringer("run_id", runID).Msg("backtest succeeded")
	return nil
```

- [ ] **Step 3: Call notifier in fail()**

In `backtest/run.go`, update `fail()` to notify after `MarkFailedTx`:

```go
func (o *orchestrator) fail(ctx context.Context, portfolioID, runID uuid.UUID, started time.Time, err error) error {
	msg := err.Error()
	if len(msg) > 2048 {
		msg = msg[:2048]
	}
	_ = o.ps.MarkFailedTx(ctx, portfolioID, runID, msg, durationMs(time.Since(started)))
	if o.notifier != nil {
		if notifyErr := o.notifier.NotifyRunComplete(ctx, portfolioID, runID, false); notifyErr != nil {
			log.Warn().Err(notifyErr).Stringer("portfolio_id", portfolioID).Msg("alert notification failed")
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %s", ErrTimedOut, msg)
	}
	return err
}
```

- [ ] **Step 4: Build and test**

```bash
go build ./backtest/... && go test ./backtest/... 2>&1
```
Expected: all pass (existing tests unchanged — notifier is nil by default).

- [ ] **Step 5: Commit**

```bash
git add backtest/run.go
git commit -m "feat(backtest): optional alert notifier hook post-run"
```

---

### Task 11: Alert HTTP Handler

**Files:**
- Create: `alert/handler.go`

- [ ] **Step 1: Write handler.go**

```go
// alert/handler.go
package alert

import (
	"errors"

	"github.com/bytedance/sonic"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"

	"github.com/penny-vault/pv-api/portfolio"
	"github.com/penny-vault/pv-api/types"
)

// AlertHandler serves CRUD endpoints for portfolio alerts.
type AlertHandler struct {
	portfolios portfolio.Store
	alerts     Store
}

// NewAlertHandler constructs a handler.
func NewAlertHandler(portfolios portfolio.Store, alerts Store) *AlertHandler {
	return &AlertHandler{portfolios: portfolios, alerts: alerts}
}

// Create implements POST /portfolios/:slug/alerts.
func (h *AlertHandler) Create(c fiber.Ctx) error {
	ownerSub, p, err := h.resolvePortfolio(c)
	if err != nil {
		return err
	}
	_ = ownerSub

	var body struct {
		Frequency  string   `json:"frequency"`
		Recipients []string `json:"recipients"`
	}
	if err := sonic.Unmarshal(c.Body(), &body); err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "invalid body", err.Error())
	}
	if !validFrequency(body.Frequency) {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "invalid frequency",
			"frequency must be one of: scheduled_run, daily, weekly, monthly")
	}
	if len(body.Recipients) == 0 {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "recipients required",
			"at least one recipient is required")
	}

	a, err := h.alerts.Create(c.Context(), p.ID, body.Frequency, body.Recipients)
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	return c.Status(fiber.StatusCreated).JSON(toView(a))
}

// List implements GET /portfolios/:slug/alerts.
func (h *AlertHandler) List(c fiber.Ctx) error {
	_, p, err := h.resolvePortfolio(c)
	if err != nil {
		return err
	}
	alerts, err := h.alerts.List(c.Context(), p.ID)
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	out := make([]alertView, 0, len(alerts))
	for _, a := range alerts {
		out = append(out, toView(a))
	}
	return c.JSON(out)
}

// Update implements PATCH /portfolios/:slug/alerts/:alertId.
func (h *AlertHandler) Update(c fiber.Ctx) error {
	_, p, err := h.resolvePortfolio(c)
	if err != nil {
		return err
	}
	alertID, err := uuid.Parse(c.Params("alertId"))
	if err != nil {
		return writeProblem(c, fiber.StatusBadRequest, "Bad Request", "invalid alertId")
	}
	existing, err := h.alerts.Get(c.Context(), alertID)
	if errors.Is(err, ErrNotFound) {
		return writeProblem(c, fiber.StatusNotFound, "Not Found", "alert not found")
	}
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	if existing.PortfolioID != p.ID {
		return writeProblem(c, fiber.StatusNotFound, "Not Found", "alert not found")
	}

	var body struct {
		Frequency  string   `json:"frequency"`
		Recipients []string `json:"recipients"`
	}
	if err := sonic.Unmarshal(c.Body(), &body); err != nil {
		return writeProblem(c, fiber.StatusUnprocessableEntity, "invalid body", err.Error())
	}

	freq := existing.Frequency
	if body.Frequency != "" {
		if !validFrequency(body.Frequency) {
			return writeProblem(c, fiber.StatusUnprocessableEntity, "invalid frequency",
				"frequency must be one of: scheduled_run, daily, weekly, monthly")
		}
		freq = body.Frequency
	}
	recips := existing.Recipients
	if len(body.Recipients) > 0 {
		recips = body.Recipients
	}

	updated, err := h.alerts.Update(c.Context(), alertID, freq, recips)
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	return c.JSON(toView(updated))
}

// Delete implements DELETE /portfolios/:slug/alerts/:alertId.
func (h *AlertHandler) Delete(c fiber.Ctx) error {
	_, p, err := h.resolvePortfolio(c)
	if err != nil {
		return err
	}
	alertID, err := uuid.Parse(c.Params("alertId"))
	if err != nil {
		return writeProblem(c, fiber.StatusBadRequest, "Bad Request", "invalid alertId")
	}
	existing, err := h.alerts.Get(c.Context(), alertID)
	if errors.Is(err, ErrNotFound) {
		return writeProblem(c, fiber.StatusNotFound, "Not Found", "alert not found")
	}
	if err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	if existing.PortfolioID != p.ID {
		return writeProblem(c, fiber.StatusNotFound, "Not Found", "alert not found")
	}
	if err := h.alerts.Delete(c.Context(), alertID); err != nil {
		return writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// resolvePortfolio extracts ownerSub from auth context and looks up the portfolio by slug.
func (h *AlertHandler) resolvePortfolio(c fiber.Ctx) (string, portfolio.Portfolio, error) {
	ownerSub, ok := c.Locals(types.AuthSubjectKey{}).(string)
	if !ok || ownerSub == "" {
		return "", portfolio.Portfolio{}, writeProblem(c, fiber.StatusUnauthorized, "Unauthorized", "missing subject")
	}
	slug := string([]byte(c.Params("slug")))
	p, err := h.portfolios.Get(c.Context(), ownerSub, slug)
	if errors.Is(err, portfolio.ErrNotFound) {
		return "", portfolio.Portfolio{}, writeProblem(c, fiber.StatusNotFound, "Not Found", "portfolio not found: "+slug)
	}
	if err != nil {
		return "", portfolio.Portfolio{}, writeProblem(c, fiber.StatusInternalServerError, "Internal Server Error", err.Error())
	}
	return ownerSub, p, nil
}

type alertView struct {
	ID          uuid.UUID  `json:"id"`
	PortfolioID uuid.UUID  `json:"portfolioId"`
	Frequency   string     `json:"frequency"`
	Recipients  []string   `json:"recipients"`
	LastSentAt  *string    `json:"lastSentAt"`
}

func toView(a Alert) alertView {
	v := alertView{
		ID:          a.ID,
		PortfolioID: a.PortfolioID,
		Frequency:   a.Frequency,
		Recipients:  a.Recipients,
	}
	if a.LastSentAt != nil {
		s := a.LastSentAt.Format("2006-01-02T15:04:05Z")
		v.LastSentAt = &s
	}
	return v
}

func validFrequency(f string) bool {
	switch f {
	case FrequencyScheduledRun, FrequencyDaily, FrequencyWeekly, FrequencyMonthly:
		return true
	}
	return false
}

func writeProblem(c fiber.Ctx, status int, title, detail string) error {
	return c.Status(status).JSON(fiber.Map{"title": title, "detail": detail})
}
```

- [ ] **Step 2: Build check**

```bash
go build ./alert/... 2>&1
```
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add alert/handler.go
git commit -m "feat(alert): CRUD HTTP handler"
```

---

### Task 12: Mount Alert Routes

**Files:**
- Modify: `api/portfolios.go`

- [ ] **Step 1: Add alert route registration to api/portfolios.go**

Add stub and real registration functions:

```go
// In RegisterPortfolioRoutes (stub section), add:
r.Post("/portfolios/:slug/alerts", stubPortfolio)
r.Get("/portfolios/:slug/alerts", stubPortfolio)
r.Patch("/portfolios/:slug/alerts/:alertId", stubPortfolio)
r.Delete("/portfolios/:slug/alerts/:alertId", stubPortfolio)
```

Add a new registration function after `RegisterPortfolioRoutesWith`:

```go
// RegisterAlertRoutesWith mounts alert CRUD endpoints backed by h.
func RegisterAlertRoutesWith(r fiber.Router, h *alert.AlertHandler) {
	r.Post("/portfolios/:slug/alerts", h.Create)
	r.Get("/portfolios/:slug/alerts", h.List)
	r.Patch("/portfolios/:slug/alerts/:alertId", h.Update)
	r.Delete("/portfolios/:slug/alerts/:alertId", h.Delete)
}
```

Add `"github.com/penny-vault/pv-api/alert"` to the import block in `api/portfolios.go`.

- [ ] **Step 2: Call RegisterAlertRoutesWith from api/server.go**

In `api/server.go`, inside the `if conf.Pool != nil` block, after `RegisterPortfolioRoutesWith(...)`:

```go
alertStore := alert.NewPoolStore(conf.Pool)
alertHandler := alert.NewAlertHandler(portfolioStore, alertStore)
RegisterAlertRoutesWith(protected, alertHandler)
```

Add `"github.com/penny-vault/pv-api/alert"` to imports in `api/server.go`.

- [ ] **Step 3: Build check**

```bash
go build ./... 2>&1
```
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add api/portfolios.go api/server.go
git commit -m "feat(alert): mount alert CRUD routes"
```

---

### Task 13: Config and Server Wiring

**Files:**
- Modify: `cmd/config.go`
- Modify: `cmd/viper.go`
- Modify: `cmd/server.go`

- [ ] **Step 1: Add mailgunConf to cmd/config.go**

In the `Config` struct, add `Mailgun mailgunConf`. Add the struct below:

```go
// mailgunConf holds Mailgun API credentials.
type mailgunConf struct {
	Domain      string
	APIKey      string `mapstructure:"api_key"`
	FromAddress string `mapstructure:"from_address"`
}
```

- [ ] **Step 2: Add viper defaults to cmd/viper.go**

```go
viper.SetDefault("mailgun.domain", "")
viper.SetDefault("mailgun.api_key", "")
viper.SetDefault("mailgun.from_address", "Penny Vault <no-reply@mg.pennyvault.com>")
```

- [ ] **Step 3: Register flags in cmd/server.go init()**

```go
serverCmd.Flags().String("mailgun-domain", "", "Mailgun sending domain (e.g. mg.pennyvault.com)")
serverCmd.Flags().String("mailgun-api-key", "", "Mailgun API key; empty disables email alerts")
serverCmd.Flags().String("mailgun-from-address", "Penny Vault <no-reply@mg.pennyvault.com>", "From address for alert emails")
```

These bind automatically via `bindPFlagsToViper` since `mailgun-domain` → `mailgun.domain`, etc.

- [ ] **Step 4: Create Checker and attach to orchestrator in cmd/server.go**

In the `serve` command's `RunE`, after `orch := backtest.NewRunner(...)`:

```go
checker := alert.NewChecker(pool, email.Config{
    Domain:      conf.Mailgun.Domain,
    APIKey:      conf.Mailgun.APIKey,
    FromAddress: conf.Mailgun.FromAddress,
})
orch.WithNotifier(checker)
```

Add imports to `cmd/server.go`:
```go
"github.com/penny-vault/pv-api/alert"
"github.com/penny-vault/pv-api/alert/email"
```

- [ ] **Step 5: Add mailgun to pvapi.toml**

```toml
[mailgun]
domain       = ""
api_key      = ""
from_address = "Penny Vault <no-reply@mg.pennyvault.com>"
```

- [ ] **Step 6: Full build, all tests, smoke run**

```bash
go build ./... 2>&1
go test ./... 2>&1
go run . serve --config pvapi.toml 2>&1 &
sleep 3
kill %1
wait
```
Expected: clean build, all tests pass, server starts without errors.

- [ ] **Step 7: Commit**

```bash
git add cmd/config.go cmd/viper.go cmd/server.go pvapi.toml
git commit -m "feat(alert): mailgun config and server wiring"
```

---

## Self-Review

**Spec coverage check:**
- ✅ `scheduled_run / daily / weekly / monthly` frequencies — Task 1+2
- ✅ `portfolio_alerts` table — Task 3
- ✅ PostgreSQL store — Task 4
- ✅ `BenchmarkCurrentValue / BenchmarkValueAt / PortfolioValueAt` — Task 5
- ✅ HTML templates (success + failure) — Task 6
- ✅ Template tests — Task 7
- ✅ Mailgun send — Task 8
- ✅ CheckAndSend / NotifyRunComplete — Task 9
- ✅ Post-run hook in backtest orchestrator — Task 10
- ✅ CRUD handlers — Task 11
- ✅ Route mounting — Task 12
- ✅ Config flags + server wiring — Task 13

**Type consistency check:**
- `email.FormatMoneyVal` exported in Task 9 step 2 and used in checker.go — consistent.
- `alert.Notifier` interface defined in `alert/alert.go`, `backtest.Notifier` defined in `backtest/run.go` — both have the same `NotifyRunComplete` signature; `alert.Checker` satisfies `backtest.Notifier` structurally.
- `alert.PoolStore` created via `alert.NewPoolStore(pool)` — consistent with db.go.
- `email.Config` used in checker.go and server.go — same type from `alert/email` package.

**Placeholder scan:** None found.
