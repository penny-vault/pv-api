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
)

// Kpis is the internal scalar-KPI struct written to portfolios columns by
// backtest.Run after a successful snapshot.
type Kpis struct {
	CurrentValue       float64
	YtdReturn          float64
	BenchmarkYtdReturn float64
	OneYearReturn      float64
	Cagr               float64
	MaxDrawdown        float64
	Sharpe             float64
	Sortino            float64
	Beta               float64
	Alpha              float64
	StdDev             float64
	UlcerIndex         float64
	TaxCostRatio       float64
	InceptionDate      time.Time
}

// Kpis computes the internal KPI struct from the snapshot.
func (r *Reader) Kpis(ctx context.Context) (Kpis, error) {
	start, end, err := r.readDateWindow(ctx)
	if err != nil {
		return Kpis{}, err
	}

	curVal, startVal, err := r.kpisPortfolioValues(ctx)
	if err != nil {
		return Kpis{}, err
	}

	ytdReturn, err := r.kpisYTDReturn(ctx, curVal, startVal)
	if err != nil {
		return Kpis{}, err
	}
	benchmarkYtdReturn, err := r.kpisBenchmarkYTDReturn(ctx)
	if err != nil {
		return Kpis{}, err
	}
	oneYearReturn, err := r.kpisOneYearReturn(ctx, curVal, startVal)
	if err != nil {
		return Kpis{}, err
	}

	years := end.Sub(start).Hours() / 24 / 365.25
	cagr := 0.0
	if startVal > 0 && years > 0 {
		cagr = math.Pow(curVal/startVal, 1/years) - 1
	}

	metricAliases := [][2]string{
		{"sharpe_ratio", "Sharpe"},
		{"sortino_ratio", "Sortino"},
		{"beta", "Beta"},
		{"alpha", "Alpha"},
		{"std_dev", "StdDev"},
		{"ulcer_index", "UlcerIndex"},
		{"tax_cost_ratio", "TaxCostRatio"},
		{"max_drawdown", "MaxDrawdown"},
	}
	vals := map[string]float64{}
	for _, pair := range metricAliases {
		v, err := r.readMetric(ctx, pair[0], pair[1])
		if err != nil {
			return Kpis{}, err
		}
		vals[pair[0]] = v
	}

	return Kpis{
		CurrentValue:       curVal,
		YtdReturn:          ytdReturn,
		BenchmarkYtdReturn: benchmarkYtdReturn,
		OneYearReturn:      oneYearReturn,
		Cagr:          cagr,
		MaxDrawdown:   vals["max_drawdown"],
		Sharpe:        vals["sharpe_ratio"],
		Sortino:       vals["sortino_ratio"],
		Beta:          vals["beta"],
		Alpha:         vals["alpha"],
		StdDev:        vals["std_dev"],
		UlcerIndex:    vals["ulcer_index"],
		TaxCostRatio:  vals["tax_cost_ratio"],
		InceptionDate: start,
	}, nil
}

// kpisPortfolioValues returns the most-recent and oldest portfolio equity rows.
// Accepts both legacy metric name 'portfolio_value' and pvbt name 'PortfolioEquity'.
func (r *Reader) kpisPortfolioValues(ctx context.Context) (curVal, startVal float64, err error) {
	if scanErr := r.db.QueryRowContext(ctx,
		`SELECT value FROM perf_data WHERE metric IN ('portfolio_value','PortfolioEquity') ORDER BY date DESC LIMIT 1`).
		Scan(&curVal); scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		return 0, 0, fmt.Errorf("kpis current value: %w", scanErr)
	}
	if scanErr := r.db.QueryRowContext(ctx,
		`SELECT value FROM perf_data WHERE metric IN ('portfolio_value','PortfolioEquity') ORDER BY date ASC LIMIT 1`).
		Scan(&startVal); scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		return 0, 0, fmt.Errorf("kpis start value: %w", scanErr)
	}
	return curVal, startVal, nil
}

// kpisYTDReturn computes the year-to-date return from the snapshot data.
func (r *Reader) kpisYTDReturn(ctx context.Context, curVal, startVal float64) (float64, error) {
	ytdStart := time.Date(time.Now().Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	var ytdBaseline float64
	err := r.db.QueryRowContext(ctx,
		`SELECT value FROM perf_data WHERE metric IN ('portfolio_value','PortfolioEquity') AND date >= ?
		  ORDER BY date ASC LIMIT 1`, ytdStart.Format(dateLayout)).Scan(&ytdBaseline)
	if errors.Is(err, sql.ErrNoRows) {
		ytdBaseline = startVal
	} else if err != nil {
		return 0, fmt.Errorf("kpis ytd baseline: %w", err)
	}
	if ytdBaseline > 0 {
		return (curVal - ytdBaseline) / ytdBaseline, nil
	}
	return 0, nil
}

// kpisBenchmarkYTDReturn computes the year-to-date return of the benchmark series.
func (r *Reader) kpisBenchmarkYTDReturn(ctx context.Context) (float64, error) {
	var latestBench float64
	if err := r.db.QueryRowContext(ctx,
		`SELECT value FROM perf_data WHERE `+benchmarkValueClause+` ORDER BY date DESC LIMIT 1`).
		Scan(&latestBench); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("kpis benchmark latest: %w", err)
	}

	ytdStart := time.Date(time.Now().Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	var ytdBaseline float64
	err := r.db.QueryRowContext(ctx,
		`SELECT value FROM perf_data WHERE `+benchmarkValueClause+` AND date >= ?
		  ORDER BY date ASC LIMIT 1`, ytdStart.Format(dateLayout)).Scan(&ytdBaseline)
	if errors.Is(err, sql.ErrNoRows) {
		var earliest float64
		if err2 := r.db.QueryRowContext(ctx,
			`SELECT value FROM perf_data WHERE `+benchmarkValueClause+` ORDER BY date ASC LIMIT 1`).
			Scan(&earliest); err2 != nil {
			return 0, nil
		}
		ytdBaseline = earliest
	} else if err != nil {
		return 0, fmt.Errorf("kpis benchmark ytd baseline: %w", err)
	}

	if ytdBaseline > 0 {
		return (latestBench - ytdBaseline) / ytdBaseline, nil
	}
	return 0, nil
}

// kpisOneYearReturn computes the trailing 1-year return from the snapshot data.
func (r *Reader) kpisOneYearReturn(ctx context.Context, curVal, startVal float64) (float64, error) {
	cutoff := time.Now().AddDate(-1, 0, 0)
	var oneYearBaseline float64
	err := r.db.QueryRowContext(ctx,
		`SELECT value FROM perf_data WHERE metric IN ('portfolio_value','PortfolioEquity') AND date <= ?
		  ORDER BY date DESC LIMIT 1`, cutoff.Format(dateLayout)).Scan(&oneYearBaseline)
	if errors.Is(err, sql.ErrNoRows) {
		oneYearBaseline = startVal
	} else if err != nil {
		return 0, fmt.Errorf("kpis 1y baseline: %w", err)
	}
	if oneYearBaseline > 0 {
		return (curVal - oneYearBaseline) / oneYearBaseline, nil
	}
	return 0, nil
}
