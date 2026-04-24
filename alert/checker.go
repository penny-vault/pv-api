package alert

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"

	"github.com/penny-vault/pv-api/alert/email"
	"github.com/penny-vault/pv-api/openapi"
	"github.com/penny-vault/pv-api/snapshot"
)

// ErrEmailNotConfigured is returned by SendSummary when no Mailgun API key is set.
var ErrEmailNotConfigured = errors.New("email not configured: no Mailgun API key")

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

// SendSummary sends a one-off portfolio summary email to recipient.
// Returns ErrEmailNotConfigured if no Mailgun API key is set.
func (c *Checker) SendSummary(ctx context.Context, portfolioID uuid.UUID, recipient string) error {
	if c.emailConfig.APIKey == "" {
		return ErrEmailNotConfigured
	}
	port, err := c.loadPortfolio(ctx, portfolioID)
	if err != nil {
		return fmt.Errorf("send summary: load portfolio: %w", err)
	}
	now := time.Now().UTC()
	payload := c.buildPayload(ctx, Alert{}, port, now, port.Status == "ready")
	payload.Trades = nil // no trade context for an on-demand summary
	htmlBody, textBody, err := email.Render(payload)
	if err != nil {
		return fmt.Errorf("send summary: render: %w", err)
	}
	subject := fmt.Sprintf("Portfolio Update: %s", port.Name)
	if port.Status != "ready" {
		subject = fmt.Sprintf("Portfolio Error: %s", port.Name)
	}
	if err := email.Send(ctx, c.emailConfig, []string{recipient}, subject, htmlBody, textBody); err != nil {
		return fmt.Errorf("send summary: send: %w", err)
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

	if port.CurrentValue != nil {
		p.CurrentValue = "$" + email.FormatMoneyVal(*port.CurrentValue)
	}

	if a.LastSentAt != nil && a.LastSentValue != nil && *a.LastSentValue > 0 && port.CurrentValue != nil {
		pct, abs, color, since, hasDelta := email.FormatDelta(*port.CurrentValue, *a.LastSentValue, *a.LastSentAt)
		p.HasDelta = hasDelta
		p.DeltaPct = pct
		p.DeltaAbs = abs
		p.DeltaColor = color
		p.SinceLabel = since
	}

	if port.SnapshotPath != nil {
		c.fillBenchmarkDelta(ctx, &p, *port.SnapshotPath, a.LastSentAt, port.Benchmark)
	}

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

	if p.HasDelta && p.DeltaPct != "" {
		portPctVal := 0.0
		fmt.Sscanf(p.DeltaPct, "%f%%", &portPctVal)
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

	if a.LastSentAt == nil {
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

	curMap := map[string]openapi.Holding{}
	for _, h := range cur.Items {
		curMap[h.Ticker] = h
	}
	prevMap := map[string]float64{}
	for _, h := range prevHoldings.Items {
		prevMap[h.Ticker] = h.Quantity
	}

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
