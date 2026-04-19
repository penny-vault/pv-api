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

// Drawdowns streams the portfolio_value series and emits a Drawdown
// record for each peak-to-trough-to-recovery cycle, sorted by depth
// (deepest first).
func (r *Reader) Drawdowns(ctx context.Context) ([]openapi.Drawdown, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT date, value FROM perf_data
		  WHERE metric='portfolio_value' ORDER BY date ASC`)
	if err != nil {
		return nil, fmt.Errorf("drawdowns query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var series []perfPoint
	for rows.Next() {
		var s string
		var v float64
		if err := rows.Scan(&s, &v); err != nil {
			return nil, fmt.Errorf("drawdowns scan: %w", err)
		}
		t, _ := time.Parse(dateLayout, s)
		series = append(series, perfPoint{t, v})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(series) == 0 {
		return nil, nil
	}
	dds := detectDrawdowns(series)
	sortDrawdowns(dds)
	return dds, nil
}

type perfPoint struct {
	d time.Time
	v float64
}

// detectDrawdowns scans the equity series and emits a Drawdown for each
// peak-to-trough(-to-recovery) cycle.
func detectDrawdowns(series []perfPoint) []openapi.Drawdown {
	indexOfDate := func(d time.Time) int {
		for i, p := range series {
			if p.d.Equal(d) {
				return i
			}
		}
		return 0
	}

	var dds []openapi.Drawdown
	peak := series[0]
	inDrawdown := false
	var trough perfPoint
	var depth float64

	for i := 1; i < len(series); i++ {
		p := series[i]
		if p.v >= peak.v {
			if inDrawdown {
				days := i - indexOfDate(peak.d)
				recovery := types.Date{Time: p.d}
				dds = append(dds, openapi.Drawdown{
					Start:    types.Date{Time: peak.d},
					Trough:   types.Date{Time: trough.d},
					Recovery: &recovery,
					Depth:    depth,
					Days:     &days,
				})
				inDrawdown = false
			}
			peak = p
			continue
		}
		if !inDrawdown || p.v < trough.v {
			trough = p
			depth = (p.v - peak.v) / peak.v
		}
		inDrawdown = true
	}
	if inDrawdown {
		days := len(series) - indexOfDate(peak.d)
		dds = append(dds, openapi.Drawdown{
			Start:  types.Date{Time: peak.d},
			Trough: types.Date{Time: trough.d},
			Depth:  depth,
			Days:   &days,
		})
	}
	return dds
}

// sortDrawdowns insertion-sorts dds by depth ascending (more negative = deeper first).
func sortDrawdowns(dds []openapi.Drawdown) {
	for i := 1; i < len(dds); i++ {
		for j := i; j > 0 && dds[j].Depth < dds[j-1].Depth; j-- {
			dds[j], dds[j-1] = dds[j-1], dds[j]
		}
	}
}
