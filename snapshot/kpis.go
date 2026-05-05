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
)

// Kpis is the scalar KPI struct sourced verbatim from pvbt's `metrics`
// table. Every metric field is nullable: nil means pvbt did not emit a
// value for that (name, window) pair in this snapshot.
type Kpis struct {
	CurrentValue       float64
	YtdReturn          *float64
	OneYearReturn      *float64
	BenchmarkYtdReturn *float64
	Cagr               *float64
	MaxDrawdown        *float64
	Sharpe             *float64
	Sortino            *float64
	Beta               *float64
	Alpha              *float64
	StdDev             *float64
	UlcerIndex         *float64
	TaxCostRatio       *float64
	InceptionDate      time.Time
}

// Kpis assembles the KPI struct by reading pvbt-emitted rows from the
// snapshot. It performs no return-style computation of its own.
func (r *Reader) Kpis(ctx context.Context) (Kpis, error) {
	start, _, err := r.readDateWindow(ctx)
	if err != nil {
		return Kpis{}, err
	}

	curVal, err := r.kpisCurrentValue(ctx)
	if err != nil {
		return Kpis{}, err
	}

	windowed := []struct {
		dest   **float64
		metric string
		window string
	}{
		{nil, "TWRR", "ytd"},
		{nil, "TWRR", "1yr"},
		{nil, "BenchmarkTWRR", "ytd"},
		{nil, "CAGR", "since_inception"},
	}
	out := Kpis{CurrentValue: curVal, InceptionDate: start}
	windowed[0].dest = &out.YtdReturn
	windowed[1].dest = &out.OneYearReturn
	windowed[2].dest = &out.BenchmarkYtdReturn
	windowed[3].dest = &out.Cagr
	for _, c := range windowed {
		v, rerr := r.readWindowedMetric(ctx, c.metric, c.window)
		if rerr != nil {
			return Kpis{}, rerr
		}
		*c.dest = v
	}

	fullWindow := []struct {
		dest         **float64
		legacyName   string
		canonicalAlt string
	}{
		{&out.MaxDrawdown, "max_drawdown", "MaxDrawdown"},
		{&out.Sharpe, "sharpe_ratio", "Sharpe"},
		{&out.Sortino, "sortino_ratio", "Sortino"},
		{&out.Beta, "beta", "Beta"},
		{&out.Alpha, "alpha", "Alpha"},
		{&out.StdDev, "std_dev", "StdDev"},
		{&out.UlcerIndex, "ulcer_index", "UlcerIndex"},
		{&out.TaxCostRatio, "tax_cost_ratio", "TaxCostRatio"},
	}
	for _, f := range fullWindow {
		v, ok, rerr := r.readMetric(ctx, f.legacyName, f.canonicalAlt)
		if rerr != nil {
			return Kpis{}, rerr
		}
		if ok {
			cp := v
			*f.dest = &cp
		}
	}

	return out, nil
}

// kpisCurrentValue returns the most recent portfolio equity row from
// perf_data. Accepts both legacy 'portfolio_value' and pvbt 'PortfolioEquity'.
func (r *Reader) kpisCurrentValue(ctx context.Context) (float64, error) {
	var v float64
	err := r.db.QueryRowContext(ctx,
		`SELECT value FROM perf_data WHERE metric IN ('portfolio_value','PortfolioEquity') ORDER BY date DESC LIMIT 1`).
		Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("kpis current value: %w", err)
	}
	return v, nil
}
