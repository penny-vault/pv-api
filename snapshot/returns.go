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
	"math"
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

// benchmarkLTCGRate is the flat long-term capital gains rate applied to the
// benchmark's cumulative gain to derive its after-tax row. The benchmark is
// treated as buy-and-hold so all gains are long-term.
const benchmarkLTCGRate = 0.15

// TrailingReturns emits four rows: portfolio, benchmark, portfolio-tax,
// benchmark-tax.
//
// Portfolio cells come straight from the metrics table: pvbt writes TWRR
// per window for the cumulative cells (ytd, 1yr) and CAGR per window for
// the annualized cells (3yr, 5yr, 10yr, since_inception). When the
// portfolio does not span a window pvbt does not write the row, so the
// cell is null.
//
// Benchmark cells are derived from the perf_data benchmark equity curve
// because pvbt does not write window-scoped benchmark metrics. The math
// mirrors pvbt: cumulative for sub-annual windows, CAGR for multi-year
// windows. No fallback to the earliest row — when the snapshot does not
// span the requested window, the cell is null.
//
// Portfolio-tax cells combine per-window TWRR with per-window TaxDrag
// (= estimated_tax_dollars / preTaxReturn_dollars). Cumulative cells
// scale TWRR by (1 - TaxDrag); multi-year cells re-annualize the
// after-tax cumulative return over the same window span pvbt used.
//
// Benchmark-tax cells assume a buy-and-hold benchmark with all gains
// realized as long-term, applying a flat 15% LTCG to the cumulative gain.
func (r *Reader) TrailingReturns(ctx context.Context) ([]openapi.TrailingReturnRow, error) {
	portfolioRow, err := r.portfolioTrailingRow(ctx)
	if err != nil {
		return nil, err
	}
	portfolioTaxRow, err := r.portfolioTaxTrailingRow(ctx)
	if err != nil {
		return nil, err
	}
	benchRow, benchTaxRow, err := r.benchmarkTrailingRows(ctx)
	if err != nil {
		return nil, err
	}
	return []openapi.TrailingReturnRow{portfolioRow, benchRow, portfolioTaxRow, benchTaxRow}, nil
}

func (r *Reader) portfolioTrailingRow(ctx context.Context) (openapi.TrailingReturnRow, error) {
	type cell struct {
		dest   **float64
		metric string
		window string
	}
	row := openapi.TrailingReturnRow{
		Title: "Portfolio",
		Kind:  openapi.ReturnRowKindPortfolio,
	}
	cells := []cell{
		{&row.Ytd, "TWRR", "ytd"},
		{&row.OneYear, "TWRR", "1yr"},
		{&row.ThreeYear, "CAGR", "3yr"},
		{&row.FiveYear, "CAGR", "5yr"},
		{&row.TenYear, "CAGR", "10yr"},
		{&row.SinceInception, "CAGR", "since_inception"},
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

func (r *Reader) benchmarkTrailingRows(ctx context.Context) (openapi.TrailingReturnRow, openapi.TrailingReturnRow, error) {
	row := openapi.TrailingReturnRow{
		Title: "Benchmark",
		Kind:  openapi.ReturnRowKindBenchmark,
	}
	taxRow := openapi.TrailingReturnRow{
		Title: "Benchmark (after tax)",
		Kind:  openapi.ReturnRowKindBenchmarkTax,
	}
	latestVal, latestDate, err := r.latestBenchmarkValueAndDate(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return row, taxRow, nil
	}
	if err != nil {
		return openapi.TrailingReturnRow{}, openapi.TrailingReturnRow{}, err
	}
	if err := r.fillBenchmarkShortWindowReturns(ctx, &row, &taxRow, latestVal, latestDate); err != nil {
		return openapi.TrailingReturnRow{}, openapi.TrailingReturnRow{}, err
	}
	if err := r.fillBenchmarkAnnualizedReturns(ctx, &row, &taxRow, latestVal, latestDate); err != nil {
		return openapi.TrailingReturnRow{}, openapi.TrailingReturnRow{}, err
	}
	if err := r.fillBenchmarkSinceInception(ctx, &row, &taxRow, latestVal, latestDate); err != nil {
		return openapi.TrailingReturnRow{}, openapi.TrailingReturnRow{}, err
	}
	return row, taxRow, nil
}

// fillBenchmarkShortWindowReturns populates Ytd and OneYear on the pre-tax
// and after-tax rows using the corresponding benchmark anchor values.
func (r *Reader) fillBenchmarkShortWindowReturns(ctx context.Context, row, taxRow *openapi.TrailingReturnRow, latestVal float64, latestDate time.Time) error {
	ytdStart := time.Date(latestDate.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	if v, err := r.benchmarkValueOnOrAfter(ctx, ytdStart); err != nil {
		return err
	} else if v != nil {
		row.Ytd = cumulativeReturn(latestVal, *v)
		taxRow.Ytd = afterTaxCumulative(row.Ytd)
	}
	if v, err := r.benchmarkValueOnOrBefore(ctx, latestDate.AddDate(-1, 0, 0)); err != nil {
		return err
	} else if v != nil {
		row.OneYear = cumulativeReturn(latestVal, *v)
		taxRow.OneYear = afterTaxCumulative(row.OneYear)
	}
	return nil
}

// fillBenchmarkAnnualizedReturns populates ThreeYear, FiveYear, and TenYear
// on both rows using benchmark anchor values N years prior.
func (r *Reader) fillBenchmarkAnnualizedReturns(ctx context.Context, row, taxRow *openapi.TrailingReturnRow, latestVal float64, latestDate time.Time) error {
	for _, n := range []int{3, 5, 10} {
		val, baseDate, err := r.benchmarkValueAndDateOnOrBefore(ctx, latestDate.AddDate(-n, 0, 0))
		if err != nil {
			return err
		}
		if val == nil {
			continue
		}
		years := latestDate.Sub(baseDate).Hours() / 24 / 365.25
		ann := annualizedReturn(latestVal, *val, years)
		annTax := afterTaxAnnualized(latestVal, *val, years)
		switch n {
		case 3:
			row.ThreeYear = ann
			taxRow.ThreeYear = annTax
		case 5:
			row.FiveYear = ann
			taxRow.FiveYear = annTax
		case 10:
			row.TenYear = ann
			taxRow.TenYear = annTax
		}
	}
	return nil
}

// fillBenchmarkSinceInception populates SinceInception with the annualized
// return from the earliest benchmark observation through latestDate, on both
// rows.
func (r *Reader) fillBenchmarkSinceInception(ctx context.Context, row, taxRow *openapi.TrailingReturnRow, latestVal float64, latestDate time.Time) error {
	earliestVal, earliestDate, err := r.earliestBenchmarkValueAndDate(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	years := latestDate.Sub(earliestDate).Hours() / 24 / 365.25
	row.SinceInception = annualizedReturn(latestVal, earliestVal, years)
	taxRow.SinceInception = afterTaxAnnualized(latestVal, earliestVal, years)
	return nil
}

// portfolioTaxTrailingRow combines per-window TWRR with per-window TaxDrag
// to produce after-tax cells. Cumulative cells (ytd, 1yr) scale TWRR by
// (1 - TaxDrag). Annualized cells (3yr, 5yr, 10yr, since_inception)
// re-annualize the after-tax cumulative return over the actual window span
// pvbt used (which equals the nominal years when the portfolio is long
// enough, otherwise the full portfolio span).
func (r *Reader) portfolioTaxTrailingRow(ctx context.Context) (openapi.TrailingReturnRow, error) {
	row := openapi.TrailingReturnRow{
		Title: "Portfolio (after tax)",
		Kind:  openapi.ReturnRowKindPortfolioTax,
	}
	span, err := r.portfolioSpanYears(ctx)
	if err != nil {
		return openapi.TrailingReturnRow{}, err
	}
	cumulative := []struct {
		dest   **float64
		window string
	}{
		{&row.Ytd, "ytd"},
		{&row.OneYear, "1yr"},
	}
	for _, c := range cumulative {
		v, err := r.portfolioAfterTaxCumulative(ctx, c.window)
		if err != nil {
			return openapi.TrailingReturnRow{}, err
		}
		*c.dest = v
	}
	annualized := []struct {
		dest    **float64
		window  string
		nominal float64
	}{
		{&row.ThreeYear, "3yr", 3},
		{&row.FiveYear, "5yr", 5},
		{&row.TenYear, "10yr", 10},
		{&row.SinceInception, "since_inception", math.MaxFloat64},
	}
	for _, c := range annualized {
		cum, err := r.portfolioAfterTaxCumulative(ctx, c.window)
		if err != nil {
			return openapi.TrailingReturnRow{}, err
		}
		if cum == nil {
			continue
		}
		years := math.Min(c.nominal, span)
		if years <= 0 || 1+*cum <= 0 {
			continue
		}
		v := math.Pow(1+*cum, 1.0/years) - 1
		*c.dest = &v
	}
	return row, nil
}

// portfolioAfterTaxCumulative reads TWRR and TaxDrag for the same window and
// returns TWRR * (1 - TaxDrag), or nil when either input is missing.
func (r *Reader) portfolioAfterTaxCumulative(ctx context.Context, window string) (*float64, error) {
	twrr, err := r.readWindowedMetric(ctx, "TWRR", window)
	if err != nil {
		return nil, err
	}
	if twrr == nil {
		return nil, nil
	}
	td, err := r.readWindowedMetric(ctx, "TaxDrag", window)
	if err != nil {
		return nil, err
	}
	if td == nil {
		return nil, nil
	}
	v := *twrr * (1 - *td)
	return &v, nil
}

// portfolioSpanYears returns the calendar-year span of the portfolio_value
// equity curve in perf_data, or 0 if the snapshot has no equity rows.
func (r *Reader) portfolioSpanYears(ctx context.Context) (float64, error) {
	var earliestStr, latestStr sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT MIN(date), MAX(date) FROM perf_data WHERE `+portfolioValueClause).
		Scan(&earliestStr, &latestStr)
	if err != nil {
		return 0, fmt.Errorf("portfolio span: %w", err)
	}
	if !earliestStr.Valid || !latestStr.Valid {
		return 0, nil
	}
	earliest, err := time.Parse(dateLayout, earliestStr.String)
	if err != nil {
		return 0, fmt.Errorf("parse earliest portfolio date: %w", err)
	}
	latest, err := time.Parse(dateLayout, latestStr.String)
	if err != nil {
		return 0, fmt.Errorf("parse latest portfolio date: %w", err)
	}
	return latest.Sub(earliest).Hours() / 24 / 365.25, nil
}

// afterTaxCumulative scales a cumulative return by (1 - benchmarkLTCGRate).
func afterTaxCumulative(pre *float64) *float64 {
	if pre == nil {
		return nil
	}
	v := *pre * (1 - benchmarkLTCGRate)
	return &v
}

// afterTaxAnnualized assumes a buy-and-hold benchmark with one realized LTCG
// at the end of the window. It applies benchmarkLTCGRate to the cumulative
// gain and re-annualizes over years.
func afterTaxAnnualized(latest, baseline, years float64) *float64 {
	if baseline <= 0 || latest <= 0 || years <= 0 {
		return nil
	}
	cum := latest/baseline - 1
	afterCum := cum * (1 - benchmarkLTCGRate)
	if 1+afterCum <= 0 {
		return nil
	}
	v := math.Pow(1+afterCum, 1.0/years) - 1
	return &v
}

// cumulativeReturn returns (latest - baseline) / baseline as *float64, or nil
// when baseline is non-positive (a degenerate snapshot).
func cumulativeReturn(latest, baseline float64) *float64 {
	if baseline <= 0 {
		return nil
	}
	v := (latest - baseline) / baseline
	return &v
}

// annualizedReturn returns the CAGR ((latest/baseline)^(1/years) - 1) as
// *float64, or nil for non-positive inputs.
func annualizedReturn(latest, baseline, years float64) *float64 {
	if baseline <= 0 || latest <= 0 || years <= 0 {
		return nil
	}
	v := math.Pow(latest/baseline, 1.0/years) - 1
	return &v
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

func (r *Reader) latestBenchmarkValueAndDate(ctx context.Context) (float64, time.Time, error) {
	var dateStr string
	var v float64
	err := r.db.QueryRowContext(ctx,
		`SELECT date, value FROM perf_data WHERE `+benchmarkValueClause+` ORDER BY date DESC LIMIT 1`,
	).Scan(&dateStr, &v)
	if err != nil {
		return 0, time.Time{}, err
	}
	t, err := time.Parse(dateLayout, dateStr)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("parse latest benchmark date: %w", err)
	}
	return v, t, nil
}

func (r *Reader) earliestBenchmarkValueAndDate(ctx context.Context) (float64, time.Time, error) {
	var dateStr string
	var v float64
	err := r.db.QueryRowContext(ctx,
		`SELECT date, value FROM perf_data WHERE `+benchmarkValueClause+` ORDER BY date ASC LIMIT 1`,
	).Scan(&dateStr, &v)
	if err != nil {
		return 0, time.Time{}, err
	}
	t, err := time.Parse(dateLayout, dateStr)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("parse earliest benchmark date: %w", err)
	}
	return v, t, nil
}

func (r *Reader) benchmarkValueOnOrAfter(ctx context.Context, t time.Time) (*float64, error) {
	var v float64
	err := r.db.QueryRowContext(ctx,
		`SELECT value FROM perf_data WHERE `+benchmarkValueClause+` AND date >= ? ORDER BY date ASC LIMIT 1`,
		t.Format(dateLayout)).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("benchmark on/after %s: %w", t.Format(dateLayout), err)
	}
	return &v, nil
}

func (r *Reader) benchmarkValueOnOrBefore(ctx context.Context, t time.Time) (*float64, error) {
	var v float64
	err := r.db.QueryRowContext(ctx,
		`SELECT value FROM perf_data WHERE `+benchmarkValueClause+` AND date <= ? ORDER BY date DESC LIMIT 1`,
		t.Format(dateLayout)).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("benchmark on/before %s: %w", t.Format(dateLayout), err)
	}
	return &v, nil
}

func (r *Reader) benchmarkValueAndDateOnOrBefore(ctx context.Context, t time.Time) (*float64, time.Time, error) {
	var dateStr string
	var v float64
	err := r.db.QueryRowContext(ctx,
		`SELECT date, value FROM perf_data WHERE `+benchmarkValueClause+` AND date <= ? ORDER BY date DESC LIMIT 1`,
		t.Format(dateLayout)).Scan(&dateStr, &v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, nil
	}
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("benchmark on/before %s: %w", t.Format(dateLayout), err)
	}
	parsed, perr := time.Parse(dateLayout, dateStr)
	if perr != nil {
		return nil, time.Time{}, fmt.Errorf("parse benchmark date: %w", perr)
	}
	return &v, parsed, nil
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
