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
	"time"

	"github.com/oapi-codegen/runtime/types"

	"github.com/penny-vault/pv-api/openapi"
)

// Performance streams the portfolio + benchmark equity curves, optionally
// filtered by date range.
func (r *Reader) Performance(ctx context.Context, slug string, from, to *time.Time) (*openapi.PortfolioPerformance, error) {
	start, end, err := r.readDateWindow(ctx)
	if err != nil {
		return nil, err
	}
	if from == nil {
		from = &start
	}
	if to == nil {
		to = &end
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT date, metric, value FROM perf_data
		  WHERE metric IN ('portfolio_value','benchmark_value','PortfolioEquity','PortfolioBenchmark')
		    AND date >= ? AND date <= ?
		  ORDER BY date ASC`,
		from.Format(dateLayout), to.Format(dateLayout))
	if err != nil {
		return nil, fmt.Errorf("performance query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	points := map[string]*openapi.PerformancePoint{}
	var order []string
	for rows.Next() {
		var ds, metric string
		var v float64
		if err := rows.Scan(&ds, &metric, &v); err != nil {
			return nil, fmt.Errorf("performance scan: %w", err)
		}
		p, ok := points[ds]
		if !ok {
			t, _ := time.Parse(dateLayout, ds)
			p = &openapi.PerformancePoint{Date: types.Date{Time: t}}
			points[ds] = p
			order = append(order, ds)
		}
		switch metric {
		case "portfolio_value", "PortfolioEquity":
			p.PortfolioValue = v
		case "benchmark_value", "PortfolioBenchmark":
			p.BenchmarkValue = v
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := openapi.PortfolioPerformance{
		PortfolioSlug: slug,
		From:          types.Date{Time: *from},
		To:            types.Date{Time: *to},
		Points:        make([]openapi.PerformancePoint, 0, len(order)),
	}
	for _, k := range order {
		out.Points = append(out.Points, *points[k])
	}
	// PortfolioBenchmark is stored as the benchmark's raw asset price, so a
	// direct plot alongside portfolio equity (in dollars) is unreadable.
	// Rebase the benchmark to the portfolio's value on the first day of the
	// returned window so both series share the same starting dollar scale.
	rebaseBenchmark(out.Points)
	return &out, nil
}

func rebaseBenchmark(points []openapi.PerformancePoint) {
	for _, p := range points {
		if p.PortfolioValue > 0 && p.BenchmarkValue > 0 {
			scale := p.PortfolioValue / p.BenchmarkValue
			for i := range points {
				points[i].BenchmarkValue *= scale
			}
			return
		}
	}
}
