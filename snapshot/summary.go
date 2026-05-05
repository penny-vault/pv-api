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
	"strings"

	"github.com/penny-vault/pv-api/openapi"
)

// Summary returns the top-line KPI strip. Every field is sourced verbatim
// from pvbt's metrics table; pv-api performs no return-style computation.
func (r *Reader) Summary(ctx context.Context) (*openapi.PortfolioSummary, error) {
	k, err := r.Kpis(ctx)
	if err != nil {
		return nil, err
	}
	return &openapi.PortfolioSummary{
		CurrentValue:       k.CurrentValue,
		YtdReturn:          k.YtdReturn,
		OneYearReturn:      k.OneYearReturn,
		CagrSinceInception: k.Cagr,
		MaxDrawDown:        k.MaxDrawdown,
		Sharpe:             k.Sharpe,
		Sortino:            k.Sortino,
		Beta:               k.Beta,
		Alpha:              k.Alpha,
		StdDev:             k.StdDev,
		UlcerIndex:         k.UlcerIndex,
		TaxCostRatio:       k.TaxCostRatio,
	}, nil
}

// readMetric returns the value of the full-window metric for any of the given names
// and a found flag (false when absent). Accepts aliases to support both legacy
// snake_case names (window='full') and pvbt PascalCase names (window='since_inception').
func (r *Reader) readMetric(ctx context.Context, name string, aliases ...string) (float64, bool, error) {
	allNames := append([]string{name}, aliases...)
	ph := make([]string, len(allNames))
	args := make([]any, len(allNames))
	for i, n := range allNames {
		ph[i] = "?"
		args[i] = n
	}
	var v float64
	err := r.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT value FROM metrics WHERE name IN (%s) AND window IN ('full','since_inception') ORDER BY date DESC LIMIT 1`,
			strings.Join(ph, ",")), args...).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read metric %s: %w", name, err)
	}
	return v, true, nil
}
