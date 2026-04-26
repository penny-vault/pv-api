# Email Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the sparse portfolio alert email with a visually polished MJML-based notification showing returns (Day/WTD/MTD/YTD/1Y), current holdings, trades, a portfolio link, and an unsubscribe mechanism — with automatic light/dark mode adaptation.

**Architecture:** MJML source files in `alert/email/templates/src/` are compiled to HTML and committed to git; Go embeds the compiled HTML unchanged. `Payload` gains new fields for returns, shares, logo, portfolio URL, and unsubscribe URL. `Checker` gains a `fillReturns` helper and sends one email per recipient (for per-recipient unsubscribe tokens). An unauthenticated `GET /api/alerts/unsubscribe` endpoint verifies HMAC tokens and removes recipients.

**Tech Stack:** Go 1.23, MJML 5.x (Node 22, npx), text/template, HMAC-SHA256, Fiber v3, pgx v5, modernc/sqlite (tests)

---

## File Map

| Action | Path | Purpose |
|--------|------|---------|
| Create | `alert/email/assets/pv-icon-blue.jpg` | Original icon (copied from frontend-ng) |
| Create | `alert/email/assets/logo-80.jpg` | Resized 80×80 icon (Makefile generates) |
| Create | `alert/email/templates/src/package.json` | MJML dev dependency |
| Create | `alert/email/templates/src/success.mjml` | MJML source for success email |
| Create | `alert/email/templates/src/failure.mjml` | MJML source for failure email |
| Modify | `alert/email/templates/success.html` | Compiled output (committed) |
| Modify | `alert/email/templates/failure.html` | Compiled output (committed) |
| Modify | `alert/email/template.go` | Embed logo, extend Payload/HoldingRow types |
| Modify | `alert/checker.go` | fillReturns, per-recipient send loop, portfolio URL |
| Create | `alert/unsubscribe_token.go` | HMAC token generation and verification |
| Modify | `alert/handler.go` | Unsubscribe handler method |
| Modify | `alert/db.go` | RemoveRecipient DB method |
| Modify | `api/portfolios.go` | Register GET /api/alerts/unsubscribe route |
| Modify | `snapshot/returns.go` | ShortTermReturns method |
| Modify | `snapshot/returns_test.go` | Tests for ShortTermReturns |
| Modify | `cmd/server.go` | --app-base-url, --unsubscribe-secret flags |
| Modify | `Makefile` | email-templates target |

---

## Task 1: MJML build pipeline

**Files:**
- Create: `alert/email/assets/` directory (copy icon, Makefile generates resized)
- Create: `alert/email/templates/src/package.json`
- Modify: `Makefile`

- [ ] **Step 1: Copy the icon into the repo**

```bash
mkdir -p alert/email/assets
cp /Users/jdf/Developer/penny-vault/frontend-ng/public/pv-icon-blue.jpg alert/email/assets/pv-icon-blue.jpg
```

- [ ] **Step 2: Create package.json for MJML**

Create `alert/email/templates/src/package.json`:
```json
{
  "name": "pv-email-templates",
  "private": true,
  "devDependencies": {
    "mjml": "5.0.0-alpha.4"
  },
  "scripts": {
    "build": "mjml success.mjml -o ../success.html && mjml failure.mjml -o ../failure.html"
  }
}
```

- [ ] **Step 3: Add Makefile target**

Add to `Makefile` after the existing `gen:` target:
```makefile
email-templates:
	sips -z 80 80 alert/email/assets/pv-icon-blue.jpg --out alert/email/assets/logo-80.jpg
	cd alert/email/templates/src && npm install && npm run build
```

- [ ] **Step 4: Verify npm install works**

```bash
cd alert/email/templates/src && npm install
```

Expected: `node_modules/mjml/` created, no errors.

- [ ] **Step 5: Commit pipeline skeleton**

```bash
git add alert/email/assets/pv-icon-blue.jpg alert/email/templates/src/package.json alert/email/templates/src/package-lock.json Makefile
git commit -m "build(email): add MJML pipeline and icon asset"
```

---

## Task 2: Logo embed and data URL

**Files:**
- Modify: `alert/email/template.go`

- [ ] **Step 1: Generate logo-80.jpg**

```bash
make email-templates
```

(This will fail at the `npm run build` step since `.mjml` files don't exist yet — that's fine. Run just the sips step:)

```bash
sips -z 80 80 alert/email/assets/pv-icon-blue.jpg --out alert/email/assets/logo-80.jpg
```

Expected: `alert/email/assets/logo-80.jpg` exists, ~8–15 KB.

- [ ] **Step 2: Add embed and init to template.go**

Add after the existing `//go:embed` lines in `alert/email/template.go`:

```go
import (
    "bytes"
    _ "embed"
    "encoding/base64"
    "fmt"
    "math"
    "strings"
    "text/template"
    "time"
)

//go:embed assets/logo-80.jpg
var logoBytes []byte

var logoDataURL string

func init() {
    enc := base64.StdEncoding.EncodeToString(logoBytes)
    logoDataURL = "data:image/jpeg;base64," + enc
}
```

- [ ] **Step 3: Verify it compiles**

```bash
go build ./alert/email/
```

Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add alert/email/assets/logo-80.jpg alert/email/template.go
git commit -m "feat(email): embed logo as base64 data URL"
```

---

## Task 3: Extend Payload and HoldingRow types

**Files:**
- Modify: `alert/email/template.go`

- [ ] **Step 1: Update HoldingRow to add Shares**

In `alert/email/template.go`, replace:
```go
type HoldingRow struct {
    Ticker    string
    WeightPct string
    Value     string
}
```
with:
```go
type HoldingRow struct {
    Ticker    string
    Shares    string // formatted with commas; "—" for $CASH
    WeightPct string
    Value     string
}
```

- [ ] **Step 2: Add new fields to Payload**

Replace the full `Payload` struct with:
```go
type Payload struct {
    PortfolioName string
    StrategyCode  string
    RunDate       string
    Success       bool

    LogoDataURL string

    CurrentValue string
    HasDelta     bool
    DeltaPct     string
    DeltaAbs     string
    SinceLabel   string
    DeltaColor   string

    Benchmark         string
    BenchmarkDeltaPct string
    RelativeDelta     string
    RelativeColor     string

    DayChangePct   string
    DayChangeColor string
    WtdPct         string
    WtdColor       string
    MtdPct         string
    MtdColor       string
    YtdPct         string
    YtdColor       string
    OneYearPct     string
    OneYearColor   string

    Trades   []TradeRow
    Holdings []HoldingRow

    PortfolioURL   string
    UnsubscribeURL string

    ErrorMessage   string
    LastKnownValue string
}
```

- [ ] **Step 3: Update buildPlaintext to include new fields**

Replace `buildPlaintext` in `alert/email/template.go`:
```go
func buildPlaintext(p Payload) string {
    var b strings.Builder
    b.WriteString(p.PortfolioName + " — " + p.RunDate + "\n\n")
    if !p.Success {
        b.WriteString("ERROR: " + p.ErrorMessage + "\n")
        return b.String()
    }
    b.WriteString("Portfolio Value: " + p.CurrentValue + "\n")
    if p.DayChangePct != "" {
        b.WriteString(fmt.Sprintf("Day: %s  WTD: %s  MTD: %s  YTD: %s  1Y: %s\n\n",
            p.DayChangePct, p.WtdPct, p.MtdPct, p.YtdPct, p.OneYearPct))
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
    if len(p.Holdings) > 0 {
        b.WriteString("Current Holdings:\n")
        for _, h := range p.Holdings {
            b.WriteString(fmt.Sprintf("  %s  %s shares  %s  %s%%\n", h.Ticker, h.Shares, h.Value, h.WeightPct))
        }
        b.WriteString("\n")
    }
    if p.PortfolioURL != "" {
        b.WriteString("View portfolio: " + p.PortfolioURL + "\n")
    }
    if p.UnsubscribeURL != "" {
        b.WriteString("Unsubscribe: " + p.UnsubscribeURL + "\n")
    }
    return b.String()
}
```

- [ ] **Step 4: Build to verify**

```bash
go build ./alert/...
```

Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add alert/email/template.go
git commit -m "feat(email): extend Payload with returns, shares, logo, and URL fields"
```

---

## Task 4: snapshot.Reader.ShortTermReturns

**Files:**
- Modify: `snapshot/returns.go`
- Modify: `snapshot/returns_test.go`

- [ ] **Step 1: Write the failing test**

Add to `snapshot/returns_test.go`:
```go
var _ = Describe("ShortTermReturns", func() {
    It("computes day, WTD, and MTD from perf_data", func() {
        path := filepath.Join(GinkgoT().TempDir(), "f.sqlite")
        Expect(snapshot.BuildTestSnapshot(path)).To(Succeed())
        r, err := snapshot.Open(path)
        Expect(err).NotTo(HaveOccurred())
        defer r.Close()

        ret, err := r.ShortTermReturns(context.Background())
        Expect(err).NotTo(HaveOccurred())
        // Last date is 2024-01-08 (value 103000), prev trading day 2024-01-05 (value 102000)
        // Day = (103000-102000)/102000 ≈ 0.0098
        Expect(ret.Day).To(BeNumerically("~", 0.0098, 0.001))
        // WTD: 2024-01-08 is Monday; first row on/after that Monday is 2024-01-08 itself → 0
        Expect(ret.WTD).To(BeNumerically("~", 0.0, 0.001))
        // MTD: first row on/after 2024-01-01 is 2024-01-02 (100000)
        // MTD = (103000-100000)/100000 = 0.03
        Expect(ret.MTD).To(BeNumerically("~", 0.03, 0.001))
    })
})
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./snapshot/ -v -run ShortTermReturns 2>&1 | tail -5
```

Expected: `undefined: snapshot.ShortTermReturns` or similar compile error.

- [ ] **Step 3: Implement ShortTermReturns**

Add to `snapshot/returns.go`:
```go
// ShortTermReturns holds Day, WTD, and MTD returns computed from perf_data.
// All values are fractional (e.g. 0.03 = 3%). Calculation is relative to the
// most recent date in perf_data, not today's wall clock.
type ShortTermReturns struct {
    Day float64
    WTD float64
    MTD float64
}

// ShortTermReturns reads the portfolio equity series and derives the return
// since the previous trading day, the most recent Monday, and the 1st of the
// current month (all relative to the last date in perf_data).
func (r *Reader) ShortTermReturns(ctx context.Context) (ShortTermReturns, error) {
    // Load all rows ordered newest-first.
    rows, err := r.db.QueryContext(ctx,
        `SELECT date, value FROM perf_data WHERE `+portfolioValueClause+
            ` ORDER BY date DESC`)
    if err != nil {
        return ShortTermReturns{}, fmt.Errorf("short term returns: %w", err)
    }
    defer func() { _ = rows.Close() }()

    type perfRow struct {
        date time.Time
        val  float64
    }
    var series []perfRow
    for rows.Next() {
        var dateStr string
        var val float64
        if err := rows.Scan(&dateStr, &val); err != nil {
            return ShortTermReturns{}, fmt.Errorf("short term returns scan: %w", err)
        }
        t, err := time.Parse(dateLayout, dateStr)
        if err != nil {
            return ShortTermReturns{}, fmt.Errorf("short term returns parse: %w", err)
        }
        series = append(series, perfRow{t, val})
    }
    if err := rows.Err(); err != nil {
        return ShortTermReturns{}, err
    }
    if len(series) == 0 {
        return ShortTermReturns{}, nil
    }

    latest := series[0]

    ret := func(baseline float64) float64 {
        if baseline <= 0 {
            return 0
        }
        return (latest.val - baseline) / baseline
    }

    // Day: second element in series (previous trading day).
    dayVal := latest.val
    if len(series) > 1 {
        dayVal = series[1].val
    }

    // WTD: first row on or after the Monday of latest's week.
    weekday := int(latest.date.Weekday())
    if weekday == 0 {
        weekday = 7 // Sunday → 7
    }
    monday := latest.date.AddDate(0, 0, -(weekday - 1))
    mondayStr := monday.Format(dateLayout)
    wtdVal := latest.val
    for i := len(series) - 1; i >= 0; i-- {
        if series[i].date.Format(dateLayout) >= mondayStr {
            wtdVal = series[i].val
        }
    }

    // MTD: first row on or after the 1st of latest's month.
    monthStart := time.Date(latest.date.Year(), latest.date.Month(), 1, 0, 0, 0, 0, time.UTC)
    monthStr := monthStart.Format(dateLayout)
    mtdVal := latest.val
    for i := len(series) - 1; i >= 0; i-- {
        if series[i].date.Format(dateLayout) >= monthStr {
            mtdVal = series[i].val
        }
    }

    return ShortTermReturns{
        Day: ret(dayVal),
        WTD: ret(wtdVal),
        MTD: ret(mtdVal),
    }, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./snapshot/ -v -run ShortTermReturns
```

Expected: PASS.

- [ ] **Step 5: Run full snapshot suite**

```bash
go test ./snapshot/
```

Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add snapshot/returns.go snapshot/returns_test.go
git commit -m "feat(snapshot): add ShortTermReturns for Day/WTD/MTD email returns"
```

---

## Task 5: fillReturns in checker.go

**Files:**
- Modify: `alert/checker.go`

- [ ] **Step 1: Add fillReturns helper**

Add to `alert/checker.go` (after `fillBenchmarkDelta`):
```go
func (c *Checker) fillReturns(ctx context.Context, p *email.Payload, snapshotPath string) {
    r, err := snapshot.Open(snapshotPath)
    if err != nil {
        return
    }
    defer r.Close()

    short, err := r.ShortTermReturns(ctx)
    if err != nil {
        return
    }
    kpis, err := r.Kpis(ctx)
    if err != nil {
        return
    }

    p.DayChangePct, p.DayChangeColor = email.FormatReturnPct(short.Day)
    p.WtdPct, p.WtdColor = email.FormatReturnPct(short.WTD)
    p.MtdPct, p.MtdColor = email.FormatReturnPct(short.MTD)
    p.YtdPct, p.YtdColor = email.FormatReturnPct(kpis.YtdReturn)
    p.OneYearPct, p.OneYearColor = email.FormatReturnPct(kpis.OneYearReturn)
}
```

- [ ] **Step 2: Add FormatReturnPct to template.go**

Add to `alert/email/template.go`:
```go
// FormatReturnPct formats a fractional return (e.g. 0.034) as "+3.4%" with
// a sign, and returns the appropriate light-mode color string.
func FormatReturnPct(v float64) (pct, color string) {
    sign := "+"
    if v < 0 {
        sign = "-"
        v = -v
    }
    pct = fmt.Sprintf("%s%.1f%%", sign, v*100)
    if sign == "+" {
        color = "#16a34a"
    } else {
        color = "#dc2626"
    }
    return
}
```

- [ ] **Step 3: Wire fillReturns into buildPayload**

In `checker.go`'s `buildPayload`, after `c.fillBenchmarkDelta(...)`:
```go
if port.SnapshotPath != nil {
    c.fillReturns(ctx, &p, *port.SnapshotPath)
}
```

- [ ] **Step 4: Build**

```bash
go build ./alert/...
```

Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add alert/checker.go alert/email/template.go
git commit -m "feat(email): populate returns grid (Day/WTD/MTD/YTD/1Y) in alert payload"
```

---

## Task 6: Add Shares to holdings and logo to payload

**Files:**
- Modify: `alert/checker.go`

- [ ] **Step 1: Update fillHoldingsAndTrades to populate Shares**

In `checker.go`'s `fillHoldingsAndTrades`, replace the holdings loop:
```go
for _, h := range cur.Items {
    weight := 0.0
    if total > 0 {
        weight = h.MarketValue / total * 100
    }
    shares := fmt.Sprintf("%.0f", h.Quantity)
    if h.Ticker == "$CASH" {
        shares = "—"
    } else {
        shares = email.FormatMoneyVal(h.Quantity)
    }
    p.Holdings = append(p.Holdings, email.HoldingRow{
        Ticker:    h.Ticker,
        Shares:    shares,
        WeightPct: fmt.Sprintf("%.1f", weight),
        Value:     "$" + email.FormatMoneyVal(h.MarketValue),
    })
}
```

- [ ] **Step 2: Inject logo into buildPayload**

In `buildPayload`, after `p := email.Payload{...}`, add:
```go
p.LogoDataURL = email.LogoDataURL()
```

Add the following exported accessor to `alert/email/template.go`:
```go
// LogoDataURL returns the embedded logo as a data URL string.
func LogoDataURL() string { return logoDataURL }
```

- [ ] **Step 3: Build and run tests**

```bash
go build ./alert/... && go test ./alert/... ./snapshot/...
```

Expected: all pass.

- [ ] **Step 4: Commit**

```bash
git add alert/checker.go alert/email/template.go
git commit -m "feat(email): add Shares to holdings rows and inject logo data URL"
```

---

## Task 7: Config flags (--app-base-url, --unsubscribe-secret)

**Files:**
- Modify: `cmd/server.go`
- Modify: `alert/checker.go`

- [ ] **Step 1: Add flags to cmd/server.go**

Find the block where `--mailgun-*` flags are defined (around line 185) and add:
```go
serverCmd.Flags().String("app-base-url", "https://www.pennyvault.com", "Base URL for the Penny Vault web app (used in email links)")
serverCmd.Flags().String("unsubscribe-secret", "", "HMAC secret for signing unsubscribe tokens; if empty, unsubscribe links are omitted")
```

- [ ] **Step 2: Read flags and pass to Checker**

Find where `alert.NewChecker` is called (around line 344) and update. First extend `Checker` to hold these values.

In `alert/checker.go`, add fields to `Checker`:
```go
type Checker struct {
    pool             *pgxpool.Pool
    store            *PoolStore
    emailConfig      email.Config
    appBaseURL       string
    unsubscribeSecret string
}
```

Add constructor parameters:
```go
func NewChecker(pool *pgxpool.Pool, cfg email.Config, appBaseURL, unsubscribeSecret string) *Checker {
    return &Checker{
        pool:              pool,
        store:             NewPoolStore(pool),
        emailConfig:       cfg,
        appBaseURL:        appBaseURL,
        unsubscribeSecret: unsubscribeSecret,
    }
}
```

- [ ] **Step 3: Update call site in cmd/server.go**

Find the `alert.NewChecker(pool, alertEmail.Config{...})` call and replace:
```go
appBaseURL, _ := cmd.Flags().GetString("app-base-url")
unsubscribeSecret, _ := cmd.Flags().GetString("unsubscribe-secret")
checker := alert.NewChecker(pool, alertEmail.Config{
    Domain:      mailgunDomain,
    APIKey:      mailgunAPIKey,
    FromAddress: mailgunFrom,
}, appBaseURL, unsubscribeSecret)
```

- [ ] **Step 4: Build**

```bash
go build ./...
```

Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add cmd/server.go alert/checker.go
git commit -m "feat(email): add --app-base-url and --unsubscribe-secret config flags"
```

---

## Task 8: Unsubscribe token

**Files:**
- Create: `alert/unsubscribe_token.go`
- Create: `alert/unsubscribe_token_test.go`

- [ ] **Step 1: Write failing tests**

Create `alert/unsubscribe_token_test.go`:
```go
package alert_test

import (
    "testing"

    "github.com/google/uuid"
    "github.com/penny-vault/pv-api/alert"
)

func TestUnsubscribeTokenRoundtrip(t *testing.T) {
    secret := "test-secret-32-bytes-long-enough!"
    alertID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
    recipient := "user@example.com"

    tok, err := alert.GenerateUnsubscribeToken(secret, alertID, recipient)
    if err != nil {
        t.Fatalf("generate: %v", err)
    }
    gotID, gotRecipient, err := alert.VerifyUnsubscribeToken(secret, tok)
    if err != nil {
        t.Fatalf("verify: %v", err)
    }
    if gotID != alertID {
        t.Errorf("alert ID: got %v want %v", gotID, alertID)
    }
    if gotRecipient != recipient {
        t.Errorf("recipient: got %q want %q", gotRecipient, recipient)
    }
}

func TestUnsubscribeTokenWrongSecret(t *testing.T) {
    alertID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
    tok, _ := alert.GenerateUnsubscribeToken("secret-a", alertID, "user@example.com")
    _, _, err := alert.VerifyUnsubscribeToken("secret-b", tok)
    if err == nil {
        t.Fatal("expected error for wrong secret, got nil")
    }
}
```

- [ ] **Step 2: Run to verify they fail**

```bash
go test ./alert/ -run TestUnsubscribeToken 2>&1 | tail -5
```

Expected: compile error — `alert.GenerateUnsubscribeToken` undefined.

- [ ] **Step 3: Implement the token**

Create `alert/unsubscribe_token.go`:
```go
package alert

import (
    "crypto/hmac"
    "crypto/sha256"
    "encoding/base64"
    "errors"
    "fmt"
    "strings"

    "github.com/google/uuid"
)

var ErrInvalidUnsubscribeToken = errors.New("invalid unsubscribe token")

// GenerateUnsubscribeToken produces a URL-safe token encoding alertID and
// recipient, signed with HMAC-SHA256 using secret.
// Format: base64url(alertID:recipient) + "." + base64url(hmac)
func GenerateUnsubscribeToken(secret string, alertID uuid.UUID, recipient string) (string, error) {
    payload := alertID.String() + ":" + recipient
    mac := hmac.New(sha256.New, []byte(secret))
    if _, err := mac.Write([]byte(payload)); err != nil {
        return "", fmt.Errorf("generate unsubscribe token: %w", err)
    }
    sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
    body := base64.RawURLEncoding.EncodeToString([]byte(payload))
    return body + "." + sig, nil
}

// VerifyUnsubscribeToken validates the token and returns alertID and recipient.
func VerifyUnsubscribeToken(secret, token string) (uuid.UUID, string, error) {
    parts := strings.SplitN(token, ".", 2)
    if len(parts) != 2 {
        return uuid.UUID{}, "", ErrInvalidUnsubscribeToken
    }
    payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
    if err != nil {
        return uuid.UUID{}, "", ErrInvalidUnsubscribeToken
    }
    sigBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
    if err != nil {
        return uuid.UUID{}, "", ErrInvalidUnsubscribeToken
    }
    payload := string(payloadBytes)
    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write([]byte(payload)) //nolint:errcheck // bytes.Buffer.Write never fails
    expected := mac.Sum(nil)
    if !hmac.Equal(sigBytes, expected) {
        return uuid.UUID{}, "", ErrInvalidUnsubscribeToken
    }
    idx := strings.Index(payload, ":")
    if idx < 0 {
        return uuid.UUID{}, "", ErrInvalidUnsubscribeToken
    }
    id, err := uuid.Parse(payload[:idx])
    if err != nil {
        return uuid.UUID{}, "", ErrInvalidUnsubscribeToken
    }
    return id, payload[idx+1:], nil
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./alert/ -run TestUnsubscribeToken -v
```

Expected: both tests PASS.

- [ ] **Step 5: Commit**

```bash
git add alert/unsubscribe_token.go alert/unsubscribe_token_test.go
git commit -m "feat(alert): HMAC-SHA256 unsubscribe token generation and verification"
```

---

## Task 9: Unsubscribe endpoint + DB method

**Files:**
- Modify: `alert/db.go`
- Modify: `alert/handler.go`
- Modify: `api/portfolios.go`

- [ ] **Step 1: Add RemoveRecipient to db.go**

Add to `alert/db.go`'s `Store` interface:
```go
RemoveRecipient(ctx context.Context, id uuid.UUID, recipient string) error
```

Add implementation to `PoolStore`:
```go
// RemoveRecipient removes one recipient from the alert. If recipients becomes
// empty, the alert is deleted.
func (s *PoolStore) RemoveRecipient(ctx context.Context, id uuid.UUID, recipient string) error {
    // Remove the recipient from the array.
    tag, err := s.pool.Exec(ctx, `
        UPDATE portfolio_alerts
           SET recipients = array_remove(recipients, $2), updated_at = now()
         WHERE id = $1`,
        id, recipient,
    )
    if err != nil {
        return fmt.Errorf("remove recipient: %w", err)
    }
    if tag.RowsAffected() == 0 {
        return ErrNotFound
    }
    // Delete the alert if no recipients remain.
    _, err = s.pool.Exec(ctx, `
        DELETE FROM portfolio_alerts
         WHERE id = $1 AND array_length(recipients, 1) IS NULL`,
        id,
    )
    return err
}
```

- [ ] **Step 2: Add Unsubscribe method to AlertHandler**

In `alert/handler.go`, add a field for the unsubscribe secret and a new handler:

Update `AlertHandler` struct:
```go
type AlertHandler struct {
    portfolios         PortfolioReader
    alerts             Store
    summarizer         EmailSummarizer
    unsubscribeSecret  string
}
```

Update `NewAlertHandlerWithChecker`:
```go
func NewAlertHandlerWithChecker(portfolios PortfolioReader, alerts Store, summarizer EmailSummarizer, unsubscribeSecret string) *AlertHandler {
    return &AlertHandler{
        portfolios:        portfolios,
        alerts:            alerts,
        summarizer:        summarizer,
        unsubscribeSecret: unsubscribeSecret,
    }
}
```

Add the handler method:
```go
// Unsubscribe handles GET /api/alerts/unsubscribe?token=<token>.
// It is unauthenticated — the HMAC token is the credential.
func (h *AlertHandler) Unsubscribe(c fiber.Ctx) error {
    if h.unsubscribeSecret == "" {
        return c.Status(fiber.StatusNotFound).SendString("Unsubscribe is not configured.")
    }
    token := string([]byte(c.Query("token")))
    alertID, recipient, err := VerifyUnsubscribeToken(h.unsubscribeSecret, token)
    if err != nil {
        return c.Status(fiber.StatusBadRequest).SendString("Invalid or expired unsubscribe link.")
    }
    a, err := h.alerts.Get(c.Context(), alertID)
    if errors.Is(err, ErrNotFound) {
        // Already removed — treat as success.
        return c.Status(fiber.StatusOK).Type("html").
            SendString("<html><body><p>You have been unsubscribed.</p></body></html>")
    }
    if err != nil {
        return c.Status(fiber.StatusInternalServerError).SendString("Something went wrong.")
    }
    if err := h.alerts.RemoveRecipient(c.Context(), alertID, recipient); err != nil && !errors.Is(err, ErrNotFound) {
        return c.Status(fiber.StatusInternalServerError).SendString("Something went wrong.")
    }
    // Load portfolio name for the confirmation page.
    portName := a.PortfolioID.String()
    html := fmt.Sprintf(`<!DOCTYPE html>
<html><head><meta charset="UTF-8"><title>Unsubscribed</title>
<style>body{font-family:-apple-system,sans-serif;max-width:480px;margin:80px auto;padding:0 24px;color:#0f172a}
h1{color:#0ea5e9}p{color:#64748b}</style></head>
<body><h1>Penny Vault</h1>
<p>You have been unsubscribed from portfolio alerts for <strong>%s</strong>.</p>
</body></html>`, portName)
    return c.Status(fiber.StatusOK).Type("html").SendString(html)
}
```

- [ ] **Step 3: Update NewAlertHandler (no-checker path) signature**

Update `NewAlertHandler` to accept the secret (pass `""` when not needed):
```go
func NewAlertHandler(portfolios PortfolioReader, alerts Store) *AlertHandler {
    return &AlertHandler{portfolios: portfolios, alerts: alerts}
}
```

(No change needed here — it stays as-is. Only `NewAlertHandlerWithChecker` got the new param.)

- [ ] **Step 4: Wire the route in api/portfolios.go**

In `RegisterAlertRoutesWith`, add:
```go
func RegisterAlertRoutesWith(r fiber.Router, h *alert.AlertHandler) {
    r.Post("/portfolios/:slug/alerts", h.Create)
    r.Get("/portfolios/:slug/alerts", h.List)
    r.Patch("/portfolios/:slug/alerts/:alertId", h.Update)
    r.Delete("/portfolios/:slug/alerts/:alertId", h.Delete)
    r.Post("/portfolios/:slug/email-summary", h.SendSummary)
    r.Get("/api/alerts/unsubscribe", h.Unsubscribe)
}
```

- [ ] **Step 5: Update call site in api/server.go**

Find `alert.NewAlertHandlerWithChecker(portfolioStore, alertStore, conf.AlertChecker)` and add the secret. First add `UnsubscribeSecret` to `api.Config`:

In `api/server.go`, add to the `Config` struct:
```go
UnsubscribeSecret string
```

Then update the call:
```go
alertHandler := alert.NewAlertHandlerWithChecker(portfolioStore, alertStore, conf.AlertChecker, conf.UnsubscribeSecret)
```

In `cmd/server.go`, pass the flag value when building `api.Config`:
```go
unsubscribeSecret, _ := cmd.Flags().GetString("unsubscribe-secret")
// add to api.Config literal:
UnsubscribeSecret: unsubscribeSecret,
```

- [ ] **Step 6: Build**

```bash
go build ./...
```

Expected: no errors.

- [ ] **Step 7: Commit**

```bash
git add alert/db.go alert/handler.go api/portfolios.go api/server.go cmd/server.go
git commit -m "feat(alert): unsubscribe endpoint with HMAC token verification"
```

---

## Task 10: Wire portfolio URL and unsubscribe URL into sendOne

**Files:**
- Modify: `alert/checker.go`

- [ ] **Step 1: Change sendOne to send per-recipient**

The current `sendOne` sends one email to all recipients. To embed a per-recipient unsubscribe URL, we must send individually.

Replace `sendOne` in `alert/checker.go`:
```go
func (c *Checker) sendOne(ctx context.Context, a Alert, port portfolioData, now time.Time, success bool) error {
    basePayload := c.buildPayload(ctx, a, port, now, success)

    // Build portfolio URL.
    if c.appBaseURL != "" && port.Slug != "" {
        basePayload.PortfolioURL = c.appBaseURL + "/portfolios/" + port.Slug
    }

    subject := fmt.Sprintf("Portfolio Update: %s", port.Name)
    if !success {
        subject = fmt.Sprintf("Portfolio Error: %s", port.Name)
    }

    for _, recipient := range a.Recipients {
        p := basePayload
        if c.unsubscribeSecret != "" {
            tok, err := GenerateUnsubscribeToken(c.unsubscribeSecret, a.ID, recipient)
            if err == nil && c.appBaseURL != "" {
                p.UnsubscribeURL = c.appBaseURL + "/api/alerts/unsubscribe?token=" + tok
            }
        }
        htmlBody, textBody, err := email.Render(p)
        if err != nil {
            log.Warn().Err(err).Str("recipient", recipient).Msg("alert: render failed")
            continue
        }
        if err := email.Send(ctx, c.emailConfig, []string{recipient}, subject, htmlBody, textBody); err != nil {
            log.Warn().Err(err).Str("recipient", recipient).Msg("alert: send failed")
        }
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
```

- [ ] **Step 2: Add Slug to portfolioData and loadPortfolio**

`portfolioData` needs the portfolio slug to construct the URL.

In `alert/checker.go`, update `portfolioData`:
```go
type portfolioData struct {
    Name         string
    Slug         string
    StrategyCode string
    Benchmark    string
    CurrentValue *float64
    Status       string
    LastError    *string
    SnapshotPath *string
}
```

Update `loadPortfolio`:
```go
func (c *Checker) loadPortfolio(ctx context.Context, id uuid.UUID) (portfolioData, error) {
    var p portfolioData
    err := c.pool.QueryRow(ctx,
        `SELECT name, slug, strategy_code, benchmark, current_value, status, last_error, snapshot_path
           FROM portfolios WHERE id=$1`, id,
    ).Scan(&p.Name, &p.Slug, &p.StrategyCode, &p.Benchmark,
        &p.CurrentValue, &p.Status, &p.LastError, &p.SnapshotPath)
    return p, err
}
```

- [ ] **Step 3: Build**

```bash
go build ./...
```

Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add alert/checker.go
git commit -m "feat(email): inject per-recipient unsubscribe URL and portfolio link"
```

---

## Task 11: Write success.mjml

**Files:**
- Create: `alert/email/templates/src/success.mjml`

- [ ] **Step 1: Create the MJML file**

Create `alert/email/templates/src/success.mjml` with the full contents below. Note: Go template conditionals at section level use `<mj-raw>` to pass through without confusing MJML's XML parser. Conditionals inside `<mj-text>` work as plain HTML text.

```xml
<mjml>
  <mj-head>
    <mj-title>{{.PortfolioName}} — Penny Vault</mj-title>
    <mj-attributes>
      <mj-all font-family="-apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif" />
      <mj-body background-color="#f1f5f9" />
    </mj-attributes>
    <mj-style>
      @media (prefers-color-scheme: dark) {
        body, .email-bg { background-color: #0f172a !important; }
        .card { background-color: #1e293b !important; }
        .pv-heading { color: #f1f5f9 !important; }
        .pv-value { color: #f1f5f9 !important; }
        .pv-muted { color: #94a3b8 !important; }
        .pv-divider { border-color: #334155 !important; border-top-color: #334155 !important; }
        .pv-divider-right { border-right-color: #334155 !important; }
        td { color: #f1f5f9 !important; }
      }
    </mj-style>
  </mj-head>
  <mj-body background-color="#f1f5f9" css-class="email-bg">

    <!-- Top spacer -->
    <mj-section padding="16px 0 0" background-color="#f1f5f9">
      <mj-column><mj-text> </mj-text></mj-column>
    </mj-section>

    <!-- HEADER: logo + portfolio name -->
    <mj-section background-color="#ffffff" padding="0"
                border-radius="12px 12px 0 0"
                border-top="4px solid #0ea5e9"
                css-class="card">
      <mj-column width="88px" padding="20px 0 20px 24px" vertical-align="middle">
        <mj-image src="{{.LogoDataURL}}" width="56px" alt="Penny Vault"
                  border-radius="10px" padding="0" />
      </mj-column>
      <mj-column padding="20px 24px 20px 8px" vertical-align="middle">
        <mj-text font-size="10px" font-weight="700" color="#0ea5e9"
                 text-transform="uppercase" letter-spacing="2px"
                 padding="0 0 4px">PENNY VAULT</mj-text>
        <mj-text font-size="21px" font-weight="700" color="#0f172a"
                 letter-spacing="-0.5px" padding="0" css-class="pv-heading">{{.PortfolioName}}</mj-text>
        <mj-text font-size="12px" color="#64748b" padding="5px 0 0"
                 css-class="pv-muted">{{.StrategyCode}} &middot; {{.RunDate}}</mj-text>
      </mj-column>
    </mj-section>

    <!-- HERO: Portfolio Value -->
    <mj-section background-color="#ffffff" padding="28px 24px 8px"
                border-top="1px solid #e2e8f0" css-class="card pv-divider">
      <mj-column>
        <mj-text font-size="10px" font-weight="700" color="#64748b"
                 text-transform="uppercase" letter-spacing="1.2px"
                 padding="0 0 8px" css-class="pv-muted">Portfolio Value</mj-text>
        <mj-text font-size="42px" font-weight="800" color="#0f172a"
                 letter-spacing="-2px" line-height="1" padding="0"
                 css-class="pv-value">{{.CurrentValue}}</mj-text>
      </mj-column>
    </mj-section>

    <!-- RETURNS GRID: Day / WTD / MTD / YTD / 1Y -->
    <mj-section background-color="#ffffff" padding="20px 24px 28px"
                border-bottom="1px solid #e2e8f0" css-class="card pv-divider">
      <mj-column border-right="1px solid #e2e8f0" padding="0 16px 0 0" css-class="pv-divider-right">
        <mj-text font-size="10px" font-weight="600" color="#94a3b8"
                 text-transform="uppercase" letter-spacing="0.8px"
                 align="center" padding="0 0 6px" css-class="pv-muted">Day</mj-text>
        <mj-text font-size="16px" font-weight="700" color="{{.DayChangeColor}}"
                 align="center" padding="0">{{.DayChangePct}}</mj-text>
      </mj-column>
      <mj-column border-right="1px solid #e2e8f0" padding="0 16px" css-class="pv-divider-right">
        <mj-text font-size="10px" font-weight="600" color="#94a3b8"
                 text-transform="uppercase" letter-spacing="0.8px"
                 align="center" padding="0 0 6px" css-class="pv-muted">WTD</mj-text>
        <mj-text font-size="16px" font-weight="700" color="{{.WtdColor}}"
                 align="center" padding="0">{{.WtdPct}}</mj-text>
      </mj-column>
      <mj-column border-right="1px solid #e2e8f0" padding="0 16px" css-class="pv-divider-right">
        <mj-text font-size="10px" font-weight="600" color="#94a3b8"
                 text-transform="uppercase" letter-spacing="0.8px"
                 align="center" padding="0 0 6px" css-class="pv-muted">MTD</mj-text>
        <mj-text font-size="16px" font-weight="700" color="{{.MtdColor}}"
                 align="center" padding="0">{{.MtdPct}}</mj-text>
      </mj-column>
      <mj-column border-right="1px solid #e2e8f0" padding="0 16px" css-class="pv-divider-right">
        <mj-text font-size="10px" font-weight="600" color="#94a3b8"
                 text-transform="uppercase" letter-spacing="0.8px"
                 align="center" padding="0 0 6px" css-class="pv-muted">YTD</mj-text>
        <mj-text font-size="16px" font-weight="700" color="{{.YtdColor}}"
                 align="center" padding="0">{{.YtdPct}}</mj-text>
      </mj-column>
      <mj-column padding="0 0 0 16px">
        <mj-text font-size="10px" font-weight="600" color="#94a3b8"
                 text-transform="uppercase" letter-spacing="0.8px"
                 align="center" padding="0 0 6px" css-class="pv-muted">1Y</mj-text>
        <mj-text font-size="16px" font-weight="700" color="{{.OneYearColor}}"
                 align="center" padding="0">{{.OneYearPct}}</mj-text>
      </mj-column>
    </mj-section>

    <!-- TRADES -->
    <mj-section background-color="#ffffff" padding="24px 24px 0"
                border-top="1px solid #e2e8f0" css-class="card pv-divider">
      <mj-column>
        <mj-text font-size="11px" font-weight="700" color="#0f172a"
                 text-transform="uppercase" letter-spacing="1px"
                 padding="0 0 16px" css-class="pv-heading">Trades to Execute</mj-text>
        <mj-text padding="0 0 24px">
          <table width="100%" cellpadding="0" cellspacing="0"
                 style="border-collapse:collapse;width:100%;border-radius:8px;overflow:hidden;">
            <thead>
              <tr style="background-color:#0ea5e9;">
                <td style="padding:10px 14px;font-size:11px;color:#ffffff;font-weight:700;text-transform:uppercase;letter-spacing:0.5px;">Ticker</td>
                <td style="padding:10px 14px;font-size:11px;color:#ffffff;font-weight:700;text-transform:uppercase;letter-spacing:0.5px;">Action</td>
                <td style="padding:10px 14px;font-size:11px;color:#ffffff;font-weight:700;text-transform:uppercase;letter-spacing:0.5px;text-align:right;">Shares</td>
                <td style="padding:10px 14px;font-size:11px;color:#ffffff;font-weight:700;text-transform:uppercase;letter-spacing:0.5px;text-align:right;">Value</td>
              </tr>
            </thead>
            <tbody>
              {{if .Trades}}{{range .Trades}}
              <tr style="border-top:1px solid #e2e8f0;">
                <td style="padding:13px 14px;font-size:14px;font-weight:700;color:#0f172a;">{{.Ticker}}</td>
                <td style="padding:13px 14px;font-size:13px;">
                  <span style="display:inline-block;padding:3px 10px;border-radius:20px;font-weight:700;font-size:12px;background-color:{{if eq .Action "Buy"}}#dcfce7{{else}}#fee2e2{{end}};color:{{.ActionColor}};">{{.Action}}</span>
                </td>
                <td style="padding:13px 14px;font-size:14px;color:#334155;text-align:right;">{{.Shares}}</td>
                <td style="padding:13px 14px;font-size:14px;color:#334155;font-weight:600;text-align:right;">{{.Value}}</td>
              </tr>
              {{end}}{{else}}
              <tr>
                <td colspan="4" style="padding:20px 14px;font-size:14px;color:#94a3b8;text-align:center;font-style:italic;">No trades required</td>
              </tr>
              {{end}}
            </tbody>
          </table>
        </mj-text>
      </mj-column>
    </mj-section>

    <!-- HOLDINGS -->
    <mj-section background-color="#ffffff" padding="24px 24px 0"
                border-top="1px solid #e2e8f0" css-class="card pv-divider">
      <mj-column>
        <mj-text font-size="11px" font-weight="700" color="#0f172a"
                 text-transform="uppercase" letter-spacing="1px"
                 padding="0 0 16px" css-class="pv-heading">Current Holdings</mj-text>
        <mj-text padding="0 0 28px">
          <table width="100%" cellpadding="0" cellspacing="0"
                 style="border-collapse:collapse;width:100%;border-radius:8px;overflow:hidden;">
            <thead>
              <tr style="background-color:#0ea5e9;">
                <td style="padding:10px 14px;font-size:11px;color:#ffffff;font-weight:700;text-transform:uppercase;letter-spacing:0.5px;">Ticker</td>
                <td style="padding:10px 14px;font-size:11px;color:#ffffff;font-weight:700;text-transform:uppercase;letter-spacing:0.5px;text-align:right;">Shares</td>
                <td style="padding:10px 14px;font-size:11px;color:#ffffff;font-weight:700;text-transform:uppercase;letter-spacing:0.5px;text-align:right;">Value</td>
                <td style="padding:10px 14px;font-size:11px;color:#ffffff;font-weight:700;text-transform:uppercase;letter-spacing:0.5px;text-align:right;">Weight</td>
              </tr>
            </thead>
            <tbody>
              {{range .Holdings}}
              <tr style="border-top:1px solid #e2e8f0;">
                <td style="padding:13px 14px;font-size:14px;font-weight:700;color:{{if eq .Ticker "$CASH"}}#94a3b8{{else}}#0f172a{{end}};">{{.Ticker}}</td>
                <td style="padding:13px 14px;font-size:14px;color:#334155;text-align:right;">{{.Shares}}</td>
                <td style="padding:13px 14px;font-size:14px;color:#334155;font-weight:600;text-align:right;">{{.Value}}</td>
                <td style="padding:13px 14px;font-size:14px;color:#64748b;text-align:right;">{{.WeightPct}}%</td>
              </tr>
              {{end}}
            </tbody>
          </table>
        </mj-text>
      </mj-column>
    </mj-section>

    <!-- CTA BUTTON -->
    <mj-raw>{{if .PortfolioURL}}</mj-raw>
    <mj-section background-color="#ffffff" padding="4px 24px 28px"
                border-top="1px solid #e2e8f0" css-class="card pv-divider">
      <mj-column>
        <mj-button background-color="#0ea5e9" color="#ffffff"
                   font-size="14px" font-weight="700"
                   padding="14px 36px" border-radius="8px"
                   href="{{.PortfolioURL}}" letter-spacing="0.2px"
                   inner-padding="0">
          View Portfolio &rarr;
        </mj-button>
      </mj-column>
    </mj-section>
    <mj-raw>{{end}}</mj-raw>

    <!-- FOOTER -->
    <mj-section background-color="#f8fafc" padding="20px 24px"
                border-top="1px solid #e2e8f0"
                border-radius="0 0 12px 12px" css-class="card pv-divider">
      <mj-column>
        <mj-text font-size="12px" color="#94a3b8" align="center"
                 padding="0 0 8px" css-class="pv-muted">
          Penny Vault &middot; Portfolio Alerts
        </mj-text>
        <mj-text font-size="11px" color="#cbd5e1" align="center"
                 padding="0" css-class="pv-muted">
          {{if .UnsubscribeURL}}<a href="{{.UnsubscribeURL}}" style="color:#94a3b8;text-decoration:underline;">Unsubscribe</a>{{end}}
        </mj-text>
      </mj-column>
    </mj-section>

    <!-- Bottom spacer -->
    <mj-section padding="16px 0" background-color="#f1f5f9">
      <mj-column><mj-text> </mj-text></mj-column>
    </mj-section>

  </mj-body>
</mjml>
```

- [ ] **Step 2: Commit the MJML source**

```bash
git add alert/email/templates/src/success.mjml
git commit -m "feat(email): add success.mjml MJML source template"
```

---

## Task 12: Write failure.mjml

**Files:**
- Create: `alert/email/templates/src/failure.mjml`

- [ ] **Step 1: Create failure.mjml**

Create `alert/email/templates/src/failure.mjml`:

```xml
<mjml>
  <mj-head>
    <mj-title>{{.PortfolioName}} — Run Failed — Penny Vault</mj-title>
    <mj-attributes>
      <mj-all font-family="-apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif" />
      <mj-body background-color="#f1f5f9" />
    </mj-attributes>
    <mj-style>
      @media (prefers-color-scheme: dark) {
        body, .email-bg { background-color: #0f172a !important; }
        .card { background-color: #1e293b !important; }
        .pv-heading { color: #f1f5f9 !important; }
        .pv-muted { color: #94a3b8 !important; }
        .pv-divider { border-color: #334155 !important; }
        .error-card { background-color: #3f1111 !important; border-color: #f87171 !important; }
        td { color: #f1f5f9 !important; }
      }
    </mj-style>
  </mj-head>
  <mj-body background-color="#f1f5f9" css-class="email-bg">

    <mj-section padding="16px 0 0" background-color="#f1f5f9">
      <mj-column><mj-text> </mj-text></mj-column>
    </mj-section>

    <!-- HEADER -->
    <mj-section background-color="#ffffff" padding="0"
                border-radius="12px 12px 0 0"
                border-top="4px solid #dc2626"
                css-class="card">
      <mj-column width="88px" padding="20px 0 20px 24px" vertical-align="middle">
        <mj-image src="{{.LogoDataURL}}" width="56px" alt="Penny Vault"
                  border-radius="10px" padding="0" />
      </mj-column>
      <mj-column padding="20px 24px 20px 8px" vertical-align="middle">
        <mj-text font-size="10px" font-weight="700" color="#dc2626"
                 text-transform="uppercase" letter-spacing="2px"
                 padding="0 0 4px">PENNY VAULT</mj-text>
        <mj-text font-size="21px" font-weight="700" color="#0f172a"
                 letter-spacing="-0.5px" padding="0" css-class="pv-heading">{{.PortfolioName}}</mj-text>
        <mj-text font-size="12px" color="#64748b" padding="5px 0 0"
                 css-class="pv-muted">{{.StrategyCode}} &middot; {{.RunDate}}</mj-text>
      </mj-column>
    </mj-section>

    <!-- LAST KNOWN VALUE -->
    <mj-raw>{{if .LastKnownValue}}</mj-raw>
    <mj-section background-color="#ffffff" padding="24px 24px 0"
                border-top="1px solid #e2e8f0" css-class="card pv-divider">
      <mj-column>
        <mj-text font-size="10px" font-weight="700" color="#64748b"
                 text-transform="uppercase" letter-spacing="1.2px"
                 padding="0 0 8px" css-class="pv-muted">Last Known Value</mj-text>
        <mj-text font-size="36px" font-weight="800" color="#0f172a"
                 letter-spacing="-1.5px" line-height="1" padding="0 0 24px"
                 css-class="pv-value">{{.LastKnownValue}}</mj-text>
      </mj-column>
    </mj-section>
    <mj-raw>{{end}}</mj-raw>

    <!-- ERROR CARD -->
    <mj-section background-color="#fef2f2" padding="24px"
                border-top="1px solid #dc2626"
                border-radius="0 0 0 0" css-class="card error-card">
      <mj-column>
        <mj-text font-size="14px" font-weight="700" color="#dc2626"
                 padding="0 0 12px">&#9888; Run Failed</mj-text>
        <mj-text font-size="13px" color="#7f1d1d" padding="0"
                 font-family="'Menlo','Consolas','Courier New',monospace">{{.ErrorMessage}}</mj-text>
      </mj-column>
    </mj-section>

    <!-- FOOTER -->
    <mj-section background-color="#f8fafc" padding="20px 24px"
                border-top="1px solid #e2e8f0"
                border-radius="0 0 12px 12px" css-class="card pv-divider">
      <mj-column>
        <mj-text font-size="12px" color="#94a3b8" align="center"
                 padding="0 0 8px" css-class="pv-muted">
          Penny Vault &middot; Portfolio Alerts
        </mj-text>
        <mj-text font-size="11px" color="#cbd5e1" align="center"
                 padding="0" css-class="pv-muted">
          {{if .UnsubscribeURL}}<a href="{{.UnsubscribeURL}}" style="color:#94a3b8;text-decoration:underline;">Unsubscribe</a>{{end}}
        </mj-text>
      </mj-column>
    </mj-section>

    <mj-section padding="16px 0" background-color="#f1f5f9">
      <mj-column><mj-text> </mj-text></mj-column>
    </mj-section>

  </mj-body>
</mjml>
```

- [ ] **Step 2: Commit**

```bash
git add alert/email/templates/src/failure.mjml
git commit -m "feat(email): add failure.mjml MJML source template"
```

---

## Task 13: Compile templates and verify

**Files:**
- Modify: `alert/email/templates/success.html`
- Modify: `alert/email/templates/failure.html`

- [ ] **Step 1: Run make email-templates**

```bash
make email-templates
```

Expected: `alert/email/templates/success.html` and `failure.html` written. No MJML errors.

If MJML reports an error on `<mj-raw>` blocks: MJML 5.x supports `<mj-raw>` natively. If using an older version, check `package.json` — version should be `5.0.0-alpha.4` or newer.

- [ ] **Step 2: Spot-check the compiled HTML**

```bash
grep -c "{{.PortfolioName}}" alert/email/templates/success.html
grep -c "{{.LogoDataURL}}" alert/email/templates/success.html
grep -c "{{if .PortfolioURL}}" alert/email/templates/success.html
```

Expected: each `grep` returns `1` (the Go template variables survived compilation).

- [ ] **Step 3: Build the full binary**

```bash
go build ./...
```

Expected: no errors.

- [ ] **Step 4: Run full test suite**

```bash
go test ./...
```

Expected: all pass.

- [ ] **Step 5: Commit compiled output**

```bash
git add alert/email/templates/success.html alert/email/templates/failure.html \
        alert/email/templates/src/package-lock.json
git commit -m "feat(email): compiled MJML success and failure email templates"
```

---

## Self-Review Checklist

**Spec coverage:**
- [x] MJML pipeline — Task 1
- [x] Logo data URL — Task 2
- [x] Payload extensions (returns, shares, URLs) — Task 3
- [x] ShortTermReturns (Day/WTD/MTD) — Task 4
- [x] fillReturns + FormatReturnPct — Task 5
- [x] Holdings Shares + logo inject — Task 6
- [x] Config flags — Task 7
- [x] Unsubscribe token — Task 8
- [x] Unsubscribe endpoint + DB — Task 9
- [x] Per-recipient send loop + portfolio URL — Task 10
- [x] success.mjml — Task 11
- [x] failure.mjml — Task 12
- [x] Compile + verify — Task 13
- [x] Plaintext updated — Task 3, Step 3

**Type consistency:** `HoldingRow.Shares` added in Task 3, populated in Task 6. `LogoDataURL()` defined in Task 6, called in Task 6's buildPayload step. `FormatReturnPct` defined in Task 5, called in Task 5. `ShortTermReturns` struct defined and returned in Task 4, used in Task 5. `GenerateUnsubscribeToken`/`VerifyUnsubscribeToken` defined in Task 8, used in Tasks 9 and 10. `RemoveRecipient` defined in Task 9, called in Task 9.

**Placeholder scan:** None found.
