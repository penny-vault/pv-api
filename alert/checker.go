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

package alert

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"

	"github.com/penny-vault/pv-api/alert/email"
	"github.com/penny-vault/pv-api/snapshot"
)

// ErrEmailNotConfigured is returned by SendSummary when no Mailgun API key is set.
var ErrEmailNotConfigured = errors.New("email not configured: no Mailgun API key")

// displayTZ is the timezone used for human-facing dates in alert emails. NYSE
// time matches the trading-session calendar these portfolios run against and
// avoids "tomorrow" appearing for a US reader after late-evening UTC rollover.
var displayTZ = func() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.UTC
	}
	return loc
}()

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

// Checker implements Notifier using Postgres and Mailgun.
type Checker struct {
	pool              *pgxpool.Pool
	store             *PoolStore
	emailConfig       email.Config
	appBaseURL        string
	unsubscribeSecret string
}

// NewChecker creates a Checker. Sends are skipped if emailConfig.APIKey is empty.
func NewChecker(pool *pgxpool.Pool, cfg email.Config, appBaseURL, unsubscribeSecret string) *Checker {
	return &Checker{
		pool:              pool,
		store:             NewPoolStore(pool),
		emailConfig:       cfg,
		appBaseURL:        appBaseURL,
		unsubscribeSecret: unsubscribeSecret,
	}
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

// loadPortfolio returns pgx.ErrNoRows if the portfolio does not exist. Callers
// that reach this via SendSummary have already validated the portfolio by slug,
// so a not-found here is a transient race and is acceptable as a 500.
func (c *Checker) loadPortfolio(ctx context.Context, id uuid.UUID) (portfolioData, error) {
	var p portfolioData
	err := c.pool.QueryRow(ctx,
		`SELECT name, slug, strategy_code, benchmark, current_value, status, last_error, snapshot_path
		   FROM portfolios WHERE id=$1`, id,
	).Scan(&p.Name, &p.Slug, &p.StrategyCode, &p.Benchmark,
		&p.CurrentValue, &p.Status, &p.LastError, &p.SnapshotPath)
	return p, err
}

func (c *Checker) sendOne(ctx context.Context, a Alert, port portfolioData, now time.Time, success bool) error {
	basePayload := c.buildPayload(ctx, a, port, now, success)

	if c.appBaseURL != "" && port.Slug != "" {
		basePayload.PortfolioURL = c.appBaseURL + "/portfolios/" + port.Slug
	}

	subject := fmt.Sprintf("Portfolio Update: %s", port.Name)
	if !success {
		subject = fmt.Sprintf("Portfolio Error: %s", port.Name)
	}

	for _, recipient := range a.Recipients {
		p := basePayload
		if c.unsubscribeSecret != "" && c.appBaseURL != "" {
			tok, err := GenerateUnsubscribeToken(c.unsubscribeSecret, a.ID, recipient)
			if err == nil {
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

func (c *Checker) buildPayload(ctx context.Context, a Alert, port portfolioData, now time.Time, success bool) email.Payload {
	displayNow := now.In(displayTZ)
	p := email.Payload{
		PortfolioName: port.Name,
		StrategyCode:  port.StrategyCode,
		RunDate:       displayNow.Format("Monday, January 2, 2006"),
		Success:       success,
		LogoDataURL:   email.LogoDataURL(),
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
		pct, abs, color, since, hasDelta := email.FormatDelta(*port.CurrentValue, *a.LastSentValue, a.LastSentAt.In(displayTZ), displayNow)
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
		c.fillReturns(ctx, &p, *port.SnapshotPath)
		c.fillHoldingsAndTrades(ctx, &p, port)
	}

	return p
}

func (c *Checker) fillBenchmarkDelta(ctx context.Context, p *email.Payload, snapshotPath string, lastSentAt *time.Time, benchmark string) {
	r, err := snapshot.Open(snapshotPath)
	if err != nil {
		return
	}
	defer func() {
		if err := r.Close(); err != nil {
			log.Warn().Err(err).Msg("alert: snapshot close")
		}
	}()

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
		if _, err := fmt.Sscanf(p.DeltaPct, "%f%%", &portPctVal); err != nil {
			log.Warn().Err(err).Str("delta_pct", p.DeltaPct).Msg("alert: parse portfolio delta percent")
		}
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

func (c *Checker) fillReturns(ctx context.Context, p *email.Payload, snapshotPath string) {
	r, err := snapshot.Open(snapshotPath)
	if err != nil {
		return
	}
	defer func() {
		if err := r.Close(); err != nil {
			log.Warn().Err(err).Msg("alert: snapshot close")
		}
	}()

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
	if kpis.YtdReturn != nil {
		p.YtdPct, p.YtdColor = email.FormatReturnPct(*kpis.YtdReturn)
	}
	if kpis.OneYearReturn != nil {
		p.OneYearPct, p.OneYearColor = email.FormatReturnPct(*kpis.OneYearReturn)
	}
}

func (c *Checker) fillHoldingsAndTrades(ctx context.Context, p *email.Payload, port portfolioData) {
	r, err := snapshot.Open(*port.SnapshotPath)
	if err != nil {
		return
	}
	defer func() {
		if err := r.Close(); err != nil {
			log.Warn().Err(err).Msg("alert: snapshot close")
		}
	}()

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
		var shares string
		if h.Ticker == "$CASH" {
			shares = "—"
		} else {
			shares = email.FormatMoneyVal(h.Quantity)
		}
		tickerColor := "#0f172a"
		if h.Ticker == "$CASH" {
			tickerColor = "#94a3b8"
		}
		p.Holdings = append(p.Holdings, email.HoldingRow{
			Ticker:      h.Ticker,
			TickerColor: tickerColor,
			Shares:      shares,
			WeightPct:   fmt.Sprintf("%.1f", weight),
			Value:       "$" + email.FormatMoneyVal(h.MarketValue),
		})
	}

	trades, err := r.LatestBatchTrades(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("alert: latest batch trades")
		return
	}
	for _, tx := range trades {
		action := strings.ToUpper(tx.Type[:1]) + tx.Type[1:]
		actionColor, actionBgColor := "#16a34a", "#dcfce7"
		if tx.Type == "sell" {
			actionColor, actionBgColor = "#ef4444", "#fee2e2"
		}
		p.Trades = append(p.Trades, email.TradeRow{
			Ticker:        tx.Ticker,
			Action:        action,
			ActionColor:   actionColor,
			ActionBgColor: actionBgColor,
			Shares:        email.FormatMoneyVal(tx.Quantity),
			Value:         "$" + email.FormatMoneyVal(tx.Amount),
		})
	}
}
