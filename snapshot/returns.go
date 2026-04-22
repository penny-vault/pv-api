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

// TrailingReturns emits two rows — one portfolio, one benchmark — using
// the portfolio_value and benchmark_value series in perf_data.
func (r *Reader) TrailingReturns(ctx context.Context) ([]openapi.TrailingReturnRow, error) {
	portfolioRow, err := r.trailingRow(ctx, "portfolio_value", "Portfolio", openapi.ReturnRowKindPortfolio)
	if err != nil {
		return nil, err
	}
	benchRow, err := r.trailingRow(ctx, "benchmark_value", "Benchmark", openapi.ReturnRowKindBenchmark)
	if err != nil {
		return nil, err
	}
	return []openapi.TrailingReturnRow{portfolioRow, benchRow}, nil
}

func (r *Reader) trailingRow(ctx context.Context, metric, title string, kind openapi.ReturnRowKind) (openapi.TrailingReturnRow, error) {
	latestVal, err := r.latestPerf(ctx, metric)
	if err != nil {
		return openapi.TrailingReturnRow{}, err
	}

	ytdStart := time.Date(time.Now().Year(), 1, 1, 0, 0, 0, 0, time.UTC)

	ytdV, err := r.perfAsOf(ctx, metric, ytdStart, true)
	if err != nil {
		return openapi.TrailingReturnRow{}, err
	}
	oneYV, err := r.perfAsOf(ctx, metric, time.Now().AddDate(-1, 0, 0), false)
	if err != nil {
		return openapi.TrailingReturnRow{}, err
	}
	threeYV, err := r.perfAsOf(ctx, metric, time.Now().AddDate(-3, 0, 0), false)
	if err != nil {
		return openapi.TrailingReturnRow{}, err
	}
	fiveYV, err := r.perfAsOf(ctx, metric, time.Now().AddDate(-5, 0, 0), false)
	if err != nil {
		return openapi.TrailingReturnRow{}, err
	}
	tenYV, err := r.perfAsOf(ctx, metric, time.Now().AddDate(-10, 0, 0), false)
	if err != nil {
		return openapi.TrailingReturnRow{}, err
	}
	earliestV, err := r.earliestPerf(ctx, metric)
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

func (r *Reader) latestPerf(ctx context.Context, metric string) (float64, error) {
	var v float64
	err := r.db.QueryRowContext(ctx,
		`SELECT value FROM perf_data WHERE metric=? ORDER BY date DESC LIMIT 1`, metric).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("latest %s: %w", metric, err)
	}
	return v, nil
}

func (r *Reader) earliestPerf(ctx context.Context, metric string) (float64, error) {
	var v float64
	err := r.db.QueryRowContext(ctx,
		`SELECT value FROM perf_data WHERE metric=? ORDER BY date ASC LIMIT 1`, metric).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("earliest %s: %w", metric, err)
	}
	return v, nil
}

func (r *Reader) perfAsOf(ctx context.Context, metric string, t time.Time, onOrAfter bool) (float64, error) {
	q := `SELECT value FROM perf_data WHERE metric=? AND date <= ? ORDER BY date DESC LIMIT 1`
	if onOrAfter {
		q = `SELECT value FROM perf_data WHERE metric=? AND date >= ? ORDER BY date ASC LIMIT 1`
	}
	var v float64
	err := r.db.QueryRowContext(ctx, q, metric, t.Format(dateLayout)).Scan(&v)
	if err == nil {
		return v, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return r.earliestPerf(ctx, metric)
	}
	return 0, err
}

// PortfolioValueAt returns the portfolio_value at or before t.
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
