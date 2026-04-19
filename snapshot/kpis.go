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
	CurrentValue  float64
	YtdReturn     float64
	OneYearReturn float64
	Cagr          float64
	MaxDrawdown   float64
	Sharpe        float64
	Sortino       float64
	Beta          float64
	Alpha         float64
	StdDev        float64
	UlcerIndex    float64
	TaxCostRatio  float64
	InceptionDate time.Time
}

// Kpis computes the internal KPI struct from the snapshot.
func (r *Reader) Kpis(ctx context.Context) (Kpis, error) {
	start, _, err := r.readDateWindow(ctx)
	if err != nil {
		return Kpis{}, err
	}

	var curVal, startVal float64
	if err := r.db.QueryRowContext(ctx,
		`SELECT value FROM perf_data WHERE metric='portfolio_value' ORDER BY date DESC LIMIT 1`).
		Scan(&curVal); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Kpis{}, fmt.Errorf("kpis current value: %w", err)
	}
	if err := r.db.QueryRowContext(ctx,
		`SELECT value FROM perf_data WHERE metric='portfolio_value' ORDER BY date ASC LIMIT 1`).
		Scan(&startVal); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Kpis{}, fmt.Errorf("kpis start value: %w", err)
	}

	ytdStart := time.Date(time.Now().Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	var ytdBaseline float64
	err = r.db.QueryRowContext(ctx,
		`SELECT value FROM perf_data WHERE metric='portfolio_value' AND date >= ?
		  ORDER BY date ASC LIMIT 1`, ytdStart.Format(dateLayout)).Scan(&ytdBaseline)
	if errors.Is(err, sql.ErrNoRows) {
		ytdBaseline = startVal
	} else if err != nil {
		return Kpis{}, fmt.Errorf("kpis ytd baseline: %w", err)
	}
	ytdReturn := 0.0
	if ytdBaseline > 0 {
		ytdReturn = (curVal - ytdBaseline) / ytdBaseline
	}

	var oneYearBaseline float64
	cutoff := time.Now().AddDate(-1, 0, 0)
	err = r.db.QueryRowContext(ctx,
		`SELECT value FROM perf_data WHERE metric='portfolio_value' AND date <= ?
		  ORDER BY date DESC LIMIT 1`, cutoff.Format(dateLayout)).Scan(&oneYearBaseline)
	if errors.Is(err, sql.ErrNoRows) {
		oneYearBaseline = startVal
	} else if err != nil {
		return Kpis{}, fmt.Errorf("kpis 1y baseline: %w", err)
	}
	oneYearReturn := 0.0
	if oneYearBaseline > 0 {
		oneYearReturn = (curVal - oneYearBaseline) / oneYearBaseline
	}

	years := time.Since(start).Hours() / 24 / 365.25
	cagr := 0.0
	if startVal > 0 && years > 0 {
		cagr = math.Pow(curVal/startVal, 1/years) - 1
	}

	names := []string{"sharpe_ratio", "sortino_ratio", "beta", "alpha", "std_dev", "ulcer_index", "tax_cost_ratio", "max_drawdown"}
	vals := map[string]float64{}
	for _, n := range names {
		v, err := r.readMetric(ctx, n)
		if err != nil {
			return Kpis{}, err
		}
		vals[n] = v
	}

	return Kpis{
		CurrentValue:  curVal,
		YtdReturn:     ytdReturn,
		OneYearReturn: oneYearReturn,
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
