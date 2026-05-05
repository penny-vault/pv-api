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

// trailingRowSpec describes how to build one row of the trailing-returns
// response from the metrics table. Cumulative cells (ytd, 1yr) read the
// `cumulative` metric per window; annualized cells (3yr, 5yr, 10yr,
// since_inception) read the `annualized` metric per window.
type trailingRowSpec struct {
	title      string
	kind       openapi.ReturnRowKind
	cumulative string
	annualized string
}

// TrailingReturns emits four rows: portfolio, benchmark, portfolio-tax,
// benchmark-tax. Every cell is read verbatim from pvbt's metrics table —
// pv-api performs no return-style computation. A cell is null when pvbt
// did not emit a value for the (metric, window) pair (typically because
// the snapshot did not span the window).
func (r *Reader) TrailingReturns(ctx context.Context) ([]openapi.TrailingReturnRow, error) {
	specs := []trailingRowSpec{
		{"Portfolio", openapi.ReturnRowKindPortfolio, "TWRR", "CAGR"},
		{"Benchmark", openapi.ReturnRowKindBenchmark, "BenchmarkTWRR", "BenchmarkCAGR"},
		{"Portfolio (after tax)", openapi.ReturnRowKindPortfolioTax, "AfterTaxTWRR", "AfterTaxCAGR"},
		{"Benchmark (after tax)", openapi.ReturnRowKindBenchmarkTax, "BenchmarkAfterTaxTWRR", "BenchmarkAfterTaxCAGR"},
	}
	out := make([]openapi.TrailingReturnRow, len(specs))
	for i, s := range specs {
		row, err := r.trailingRow(ctx, s)
		if err != nil {
			return nil, err
		}
		out[i] = row
	}
	return out, nil
}

func (r *Reader) trailingRow(ctx context.Context, s trailingRowSpec) (openapi.TrailingReturnRow, error) {
	row := openapi.TrailingReturnRow{Title: s.title, Kind: s.kind}
	cells := []struct {
		dest   **float64
		metric string
		window string
	}{
		{&row.Ytd, s.cumulative, "ytd"},
		{&row.OneYear, s.cumulative, "1yr"},
		{&row.ThreeYear, s.annualized, "3yr"},
		{&row.FiveYear, s.annualized, "5yr"},
		{&row.TenYear, s.annualized, "10yr"},
		{&row.SinceInception, s.annualized, "since_inception"},
	}
	for _, c := range cells {
		v, err := r.readWindowedMetric(ctx, c.metric, c.window)
		if err != nil {
			return openapi.TrailingReturnRow{}, err
		}
		*c.dest = v
	}
	return row, nil
}

// readWindowedMetric returns the latest value of a (name, window) pair from
// the metrics table, or nil if no row exists or the value is NULL.
func (r *Reader) readWindowedMetric(ctx context.Context, name, window string) (*float64, error) {
	var v sql.NullFloat64
	err := r.db.QueryRowContext(ctx,
		`SELECT value FROM metrics WHERE name = ? AND window = ? ORDER BY date DESC LIMIT 1`,
		name, window).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read metric %s/%s: %w", name, window, err)
	}
	if !v.Valid {
		return nil, nil
	}
	out := v.Float64
	return &out, nil
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
