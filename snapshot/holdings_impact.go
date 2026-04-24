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

package snapshot

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/oapi-codegen/runtime/types"

	"github.com/penny-vault/pv-api/openapi"
)

const (
	cashTicker        = "$CASH"
	residualTolerance = 1e-4 // dollar tolerance on the P&L vs equity-change identity
	minTopN           = 1
	maxTopN           = 50
)

// HoldingsImpact returns per-ticker contribution to portfolio return across
// canonical periods (inception, 5y, 3y, 1y, ytd). Items per period are capped
// at topN; the remainder is summed into rest. Contributions are expressed as
// fractions of the period's opening portfolio equity; items + rest sums to
// the period's cumulative return.
func (r *Reader) HoldingsImpact(ctx context.Context, slug string, topN int) (*openapi.HoldingsImpactResponse, error) {
	topN = clampTopN(topN)

	timeline, err := r.loadHoldingsImpactTimeline(ctx)
	if err != nil {
		return nil, err
	}
	if len(timeline) == 0 {
		return nil, ErrNotFound
	}

	windows := buildPeriodWindows(timeline)

	out := &openapi.HoldingsImpactResponse{
		PortfolioSlug: slug,
		AsOf:          types.Date{Time: timeline[len(timeline)-1].date},
		Currency:      "USD",
		Periods:       make([]openapi.HoldingsImpactPeriod, 0, len(windows)),
	}
	for _, w := range windows {
		p, err := computePeriod(timeline, w, topN)
		if err != nil {
			return nil, err
		}
		out.Periods = append(out.Periods, *p)
	}
	return out, nil
}

func clampTopN(n int) int {
	if n < minTopN {
		return minTopN
	}
	if n > maxTopN {
		return maxTopN
	}
	return n
}

// tickerKey identifies a holding by ticker + figi (figi may be empty for
// synthetic tickers like $CASH). Keying by both allows same-ticker rows with
// different FIGI to be treated as distinct holdings.
type tickerKey struct {
	ticker, figi string
}

type posDay struct {
	mv, qty float64
}

type timelineDay struct {
	date      time.Time
	v         float64 // perf_data portfolio equity
	positions map[tickerKey]posDay
	flows     map[tickerKey]float64
}

type periodWindow struct {
	id, label string
	startIdx  int
	endIdx    int
}

// loadHoldingsImpactTimeline reads perf_data, positions_daily, and transactions
// in three passes, returning one timelineDay per perf_data row keyed by date.
// Accepts both legacy (portfolio_value) and pvbt (PortfolioEquity) metric names.
func (r *Reader) loadHoldingsImpactTimeline(ctx context.Context) ([]timelineDay, error) {
	byDate := map[string]*timelineDay{}

	// Pass 1: portfolio equity — defines the set of dates.
	pfRows, err := r.db.QueryContext(ctx,
		`SELECT date, value FROM perf_data
		  WHERE `+portfolioValueClause+` ORDER BY date ASC`)
	if err != nil {
		return nil, fmt.Errorf("holdings-impact perf query: %w", err)
	}
	defer func() { _ = pfRows.Close() }()
	var order []string
	for pfRows.Next() {
		var ds string
		var v float64
		if err := pfRows.Scan(&ds, &v); err != nil {
			return nil, fmt.Errorf("holdings-impact perf scan: %w", err)
		}
		t, perr := time.Parse(dateLayout, ds)
		if perr != nil {
			return nil, fmt.Errorf("holdings-impact perf date %q: %w", ds, perr)
		}
		byDate[ds] = &timelineDay{
			date:      t,
			v:         v,
			positions: map[tickerKey]posDay{},
			flows:     map[tickerKey]float64{},
		}
		order = append(order, ds)
	}
	if err := pfRows.Err(); err != nil {
		return nil, fmt.Errorf("holdings-impact perf iterate: %w", err)
	}

	// Pass 2: positions_daily — merge into existing days; skip dates with no
	// perf_data row.
	posRows, err := r.db.QueryContext(ctx,
		`SELECT date, ticker, figi, market_value, quantity FROM positions_daily ORDER BY date ASC`)
	if err != nil {
		return nil, fmt.Errorf("holdings-impact positions query: %w", err)
	}
	defer func() { _ = posRows.Close() }()
	for posRows.Next() {
		var ds, ticker, figi string
		var mv, qty float64
		if err := posRows.Scan(&ds, &ticker, &figi, &mv, &qty); err != nil {
			return nil, fmt.Errorf("holdings-impact positions scan: %w", err)
		}
		day, ok := byDate[ds]
		if !ok {
			continue
		}
		day.positions[tickerKey{ticker: ticker, figi: figi}] = posDay{mv: mv, qty: qty}
	}
	if err := posRows.Err(); err != nil {
		return nil, fmt.Errorf("holdings-impact positions iterate: %w", err)
	}

	// Pass 3: transactions — attribute cash-flow effects to ticker and $CASH.
	txRows, err := r.db.QueryContext(ctx,
		`SELECT date, type, ticker, figi, amount FROM transactions ORDER BY date ASC, rowid ASC`)
	if err != nil {
		return nil, fmt.Errorf("holdings-impact transactions query: %w", err)
	}
	defer func() { _ = txRows.Close() }()
	for txRows.Next() {
		var ds, typeStr, ticker, figi string
		var amount float64
		if err := txRows.Scan(&ds, &typeStr, &ticker, &figi, &amount); err != nil {
			return nil, fmt.Errorf("holdings-impact transactions scan: %w", err)
		}
		day, ok := byDate[ds]
		if !ok {
			continue
		}
		applyTxFlow(day, typeStr, ticker, figi, amount)
	}
	if err := txRows.Err(); err != nil {
		return nil, fmt.Errorf("holdings-impact transactions iterate: %w", err)
	}

	out := make([]timelineDay, 0, len(order))
	for _, ds := range order {
		out = append(out, *byDate[ds])
	}
	return out, nil
}

// applyTxFlow attributes a transaction's amount to the relevant flow buckets.
// The sign convention is: flow counts as money moving *into* an asset bucket
// from external (non-asset) sources. pnl = (mv1 - mv0) - flowSum subtracts
// these out so the residual is pure price/yield P&L.
func applyTxFlow(day *timelineDay, typeStr, ticker, figi string, amount float64) {
	cashKey := tickerKey{ticker: cashTicker, figi: ""}
	assetKey := tickerKey{ticker: ticker, figi: figi}
	switch typeStr {
	case "buy":
		day.flows[assetKey] += amount
		day.flows[cashKey] -= amount
	case "sell":
		day.flows[assetKey] -= amount
		day.flows[cashKey] += amount
	case "dividend":
		day.flows[assetKey] -= amount
		day.flows[cashKey] += amount
	case "fee":
		day.flows[assetKey] += amount
		day.flows[cashKey] -= amount
	case "deposit":
		day.flows[cashKey] += amount
	case "withdrawal":
		day.flows[cashKey] -= amount
	}
}

// buildPeriodWindows returns the canonical periods in the order
// inception, 5y, 3y, 1y, ytd. A window is included only if its snapped
// start index is strictly before the end index.
func buildPeriodWindows(timeline []timelineDay) []periodWindow {
	last := timeline[len(timeline)-1].date
	endIdx := len(timeline) - 1

	type spec struct {
		id, label string
		start     time.Time
	}
	specs := []spec{
		{"inception", "Since inception", timeline[0].date},
		{"5y", "Last 5 years", time.Date(last.Year()-5, last.Month(), last.Day(), 0, 0, 0, 0, last.Location())},
		{"3y", "Last 3 years", time.Date(last.Year()-3, last.Month(), last.Day(), 0, 0, 0, 0, last.Location())},
		{"1y", "Last 1 year", time.Date(last.Year()-1, last.Month(), last.Day(), 0, 0, 0, 0, last.Location())},
		{"ytd", "Year to date", time.Date(last.Year(), 1, 1, 0, 0, 0, 0, last.Location())},
	}

	var out []periodWindow
	for _, s := range specs {
		// Omit fixed-length trailing windows (5y/3y/1y) when history is
		// shorter than the requested span — i.e. the requested start date
		// is before the earliest bar we have. YTD and inception are always
		// reported when any timeline exists.
		if s.id != "inception" && s.id != "ytd" && timeline[0].date.After(s.start) {
			continue
		}
		idx := snapForward(timeline, s.start)
		if idx < 0 || idx >= endIdx {
			continue
		}
		out = append(out, periodWindow{id: s.id, label: s.label, startIdx: idx, endIdx: endIdx})
	}
	return out
}

// snapForward returns the first index i such that timeline[i].date >= want,
// or -1 if none exists.
func snapForward(timeline []timelineDay, want time.Time) int {
	for i, d := range timeline {
		if !d.date.Before(want) {
			return i
		}
	}
	return -1
}

// computePeriod builds one HoldingsImpactPeriod. It enforces the identity
//
//	(V1 - V0) == sum_k pnl_k
//
// by summing pnl across every ticker that appears in positions on any day in
// [startIdx..endIdx] and checking the residual against residualTolerance.
func computePeriod(timeline []timelineDay, w periodWindow, topN int) (*openapi.HoldingsImpactPeriod, error) {
	t0 := timeline[w.startIdx].date
	t1 := timeline[w.endIdx].date
	v0 := timeline[w.startIdx].v
	v1 := timeline[w.endIdx].v

	if v0 == 0 {
		return nil, fmt.Errorf("holdings-impact period %s: V0 is zero", w.id)
	}

	// Collect every ticker that appears on any day in the window.
	keys := map[tickerKey]struct{}{}
	for i := w.startIdx; i <= w.endIdx; i++ {
		for k := range timeline[i].positions {
			keys[k] = struct{}{}
		}
	}

	items := make([]openapi.HoldingsImpactItem, 0, len(keys))
	var totalPNL float64

	for k := range keys {
		mv0 := timeline[w.startIdx].positions[k].mv
		mv1 := timeline[w.endIdx].positions[k].mv

		// flowSum is summed on (t0, t1] — transactions on t0 itself are
		// excluded, because mv0 already reflects the post-t0-transaction
		// balance.
		var flowSum float64
		for i := w.startIdx + 1; i <= w.endIdx; i++ {
			flowSum += timeline[i].flows[k]
		}
		pnl := (mv1 - mv0) - flowSum
		totalPNL += pnl

		var weightSum float64
		var weightCount int
		var holdingDays int64
		for i := w.startIdx; i <= w.endIdx; i++ {
			p := timeline[i].positions[k]
			if timeline[i].v > 0 {
				weightSum += p.mv / timeline[i].v
				weightCount++
			}
			if p.mv != 0 || p.qty != 0 {
				holdingDays++
			}
		}
		avgWeight := 0.0
		if weightCount > 0 {
			avgWeight = weightSum / float64(weightCount)
		}

		item := openapi.HoldingsImpactItem{
			Ticker:       k.ticker,
			Contribution: round6(pnl / v0),
			AvgWeight:    round6(avgWeight),
			HoldingDays:  holdingDays,
		}
		if k.figi != "" {
			figi := k.figi
			item.Figi = &figi
		}
		items = append(items, item)
	}

	residual := (v1 - v0) - totalPNL
	if math.Abs(residual) > residualTolerance {
		return nil, fmt.Errorf("holdings-impact residual check failed for period %s: residual=%g", w.id, residual)
	}

	sort.SliceStable(items, func(i, j int) bool {
		ai, aj := math.Abs(items[i].Contribution), math.Abs(items[j].Contribution)
		if ai != aj {
			return ai > aj
		}
		bi, bj := math.Abs(items[i].AvgWeight), math.Abs(items[j].AvgWeight)
		if bi != bj {
			return bi > bj
		}
		return items[i].Ticker < items[j].Ticker
	})

	// Split top-N into items; fold the remainder into rest.
	rest := openapi.HoldingsImpactRest{}
	if len(items) > topN {
		remainder := items[topN:]
		var restContribution float64
		for _, it := range remainder {
			restContribution += it.Contribution
		}
		rest.Contribution = round6(restContribution)
		rest.Count = int64(len(remainder))
		items = items[:topN]
	}

	cumulativeReturn := v1/v0 - 1
	years := t1.Sub(t0).Hours() / 24.0 / 365.25
	annualizedReturn := 0.0
	if years > 0 {
		annualizedReturn = math.Pow(1+cumulativeReturn, 1.0/years) - 1
	}

	return &openapi.HoldingsImpactPeriod{
		AnnualizedReturn: round6(annualizedReturn),
		CumulativeReturn: round6(cumulativeReturn),
		EndDate:          types.Date{Time: t1},
		Items:            items,
		Label:            w.label,
		Period:           openapi.HoldingsImpactPeriodPeriod(w.id),
		Rest:             rest,
		StartDate:        types.Date{Time: t0},
		Years:            round6(years),
	}, nil
}

// round6 rounds x to 6 decimal places.
func round6(x float64) float64 {
	return math.Round(x*1e6) / 1e6
}
