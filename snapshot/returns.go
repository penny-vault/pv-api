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
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/penny-vault/pv-api/openapi"
)

// perf_data rows are keyed by metric name. Legacy fixtures use snake_case
// ('portfolio_value', 'benchmark_value'); pvbt binaries write PascalCase
// ('PortfolioEquity', 'PortfolioBenchmark'). Queries accept both.
const (
	portfolioValueClause = `metric IN ('portfolio_value','PortfolioEquity')`
	benchmarkValueClause = `metric IN ('benchmark_value','PortfolioBenchmark')`
)

// TrailingReturns emits two rows — one portfolio, one benchmark — using
// the portfolio and benchmark equity series in perf_data.
func (r *Reader) TrailingReturns(ctx context.Context) ([]openapi.TrailingReturnRow, error) {
	portfolioRow, err := r.trailingRow(ctx, portfolioValueClause, "Portfolio", openapi.ReturnRowKindPortfolio)
	if err != nil {
		return nil, err
	}
	benchRow, err := r.trailingRow(ctx, benchmarkValueClause, "Benchmark", openapi.ReturnRowKindBenchmark)
	if err != nil {
		return nil, err
	}
	return []openapi.TrailingReturnRow{portfolioRow, benchRow}, nil
}

func (r *Reader) trailingRow(ctx context.Context, metricClause, title string, kind openapi.ReturnRowKind) (openapi.TrailingReturnRow, error) {
	latestVal, err := r.latestPerf(ctx, metricClause)
	if err != nil {
		return openapi.TrailingReturnRow{}, err
	}

	ytdStart := time.Date(time.Now().Year(), 1, 1, 0, 0, 0, 0, time.UTC)

	ytdV, err := r.perfAsOf(ctx, metricClause, ytdStart, true)
	if err != nil {
		return openapi.TrailingReturnRow{}, err
	}
	oneYV, err := r.perfAsOf(ctx, metricClause, time.Now().AddDate(-1, 0, 0), false)
	if err != nil {
		return openapi.TrailingReturnRow{}, err
	}
	threeYV, err := r.perfAsOf(ctx, metricClause, time.Now().AddDate(-3, 0, 0), false)
	if err != nil {
		return openapi.TrailingReturnRow{}, err
	}
	fiveYV, err := r.perfAsOf(ctx, metricClause, time.Now().AddDate(-5, 0, 0), false)
	if err != nil {
		return openapi.TrailingReturnRow{}, err
	}
	tenYV, err := r.perfAsOf(ctx, metricClause, time.Now().AddDate(-10, 0, 0), false)
	if err != nil {
		return openapi.TrailingReturnRow{}, err
	}
	earliestV, err := r.earliestPerf(ctx, metricClause)
	if err != nil {
		return openapi.TrailingReturnRow{}, err
	}

	pct := func(baseline float64) float64 {
		if baseline <= 0 {
			return 0
		}
		return (latestVal - baseline) / baseline
	}

	return openapi.TrailingReturnRow{
		Title:          title,
		Kind:           kind,
		Ytd:            pct(ytdV),
		OneYear:        pct(oneYV),
		ThreeYear:      pct(threeYV),
		FiveYear:       pct(fiveYV),
		TenYear:        pct(tenYV),
		SinceInception: pct(earliestV),
	}, nil
}

func (r *Reader) latestPerf(ctx context.Context, metricClause string) (float64, error) {
	var v float64
	err := r.db.QueryRowContext(ctx,
		`SELECT value FROM perf_data WHERE `+metricClause+` ORDER BY date DESC LIMIT 1`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("latest %s: %w", metricClause, err)
	}
	return v, nil
}

func (r *Reader) earliestPerf(ctx context.Context, metricClause string) (float64, error) {
	var v float64
	err := r.db.QueryRowContext(ctx,
		`SELECT value FROM perf_data WHERE `+metricClause+` ORDER BY date ASC LIMIT 1`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("earliest %s: %w", metricClause, err)
	}
	return v, nil
}

func (r *Reader) perfAsOf(ctx context.Context, metricClause string, t time.Time, onOrAfter bool) (float64, error) {
	q := `SELECT value FROM perf_data WHERE ` + metricClause + ` AND date <= ? ORDER BY date DESC LIMIT 1`
	if onOrAfter {
		q = `SELECT value FROM perf_data WHERE ` + metricClause + ` AND date >= ? ORDER BY date ASC LIMIT 1`
	}
	var v float64
	err := r.db.QueryRowContext(ctx, q, t.Format(dateLayout)).Scan(&v)
	if err == nil {
		return v, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return r.earliestPerf(ctx, metricClause)
	}
	return 0, err
}

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

	pctReturn := func(baseline float64) float64 {
		if baseline <= 0 {
			return 0
		}
		return (latest.val - baseline) / baseline
	}

	// Day: second element in series (previous trading day).
	dayBaseline := latest.val
	if len(series) > 1 {
		dayBaseline = series[1].val
	}

	// WTD: oldest row on or after the Monday of latest's week.
	// series is DESC so iterating 0→len-1 goes newest→oldest; the last
	// qualifying assignment gives the oldest qualifying row (the baseline).
	weekday := int(latest.date.Weekday())
	if weekday == 0 {
		weekday = 7 // Sunday → 7
	}
	monday := latest.date.AddDate(0, 0, -(weekday - 1))
	mondayStr := monday.Format(dateLayout)
	wtdBaseline := latest.val
	for i := 0; i < len(series); i++ {
		if series[i].date.Format(dateLayout) >= mondayStr {
			wtdBaseline = series[i].val
		}
	}

	// MTD: oldest row on or after the 1st of latest's month.
	monthStart := time.Date(latest.date.Year(), latest.date.Month(), 1, 0, 0, 0, 0, time.UTC)
	monthStr := monthStart.Format(dateLayout)
	mtdBaseline := latest.val
	for i := 0; i < len(series); i++ {
		if series[i].date.Format(dateLayout) >= monthStr {
			mtdBaseline = series[i].val
		}
	}

	return ShortTermReturns{
		Day: pctReturn(dayBaseline),
		WTD: pctReturn(wtdBaseline),
		MTD: pctReturn(mtdBaseline),
	}, nil
}

// PortfolioValueAt returns the portfolio equity at or before t.
func (r *Reader) PortfolioValueAt(ctx context.Context, t time.Time) (float64, error) {
	return r.perfAsOf(ctx, portfolioValueClause, t, false)
}

// BenchmarkCurrentValue returns the most recent benchmark value in the snapshot.
func (r *Reader) BenchmarkCurrentValue(ctx context.Context) (float64, error) {
	return r.latestPerf(ctx, benchmarkValueClause)
}

// BenchmarkValueAt returns the benchmark value at or before t.
func (r *Reader) BenchmarkValueAt(ctx context.Context, t time.Time) (float64, error) {
	return r.perfAsOf(ctx, benchmarkValueClause, t, false)
}
