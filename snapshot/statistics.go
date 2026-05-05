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

	"github.com/penny-vault/pv-api/openapi"
)

var statisticMeta = []struct {
	Name   string
	Alias  string // pvbt PascalCase name (window='since_inception')
	Label  string
	Format openapi.MetricFormat
}{
	{"cagr", "CAGR", "CAGR", openapi.Percent},
	{"sharpe_ratio", "Sharpe", "Sharpe Ratio", openapi.Number},
	{"sortino_ratio", "Sortino", "Sortino Ratio", openapi.Number},
	{"calmar_ratio", "Calmar", "Calmar Ratio", openapi.Number},
	{"information_ratio", "InformationRatio", "Information Ratio", openapi.Number},
	{"beta", "Beta", "Beta", openapi.Number},
	{"alpha", "Alpha", "Alpha", openapi.Percent},
	{"std_dev", "StdDev", "Standard Deviation", openapi.Percent},
	{"ulcer_index", "UlcerIndex", "Ulcer Index", openapi.Number},
	{"max_drawdown", "MaxDrawdown", "Max Drawdown", openapi.Percent},
	{"upside_capture", "UpsideCaptureRatio", "Upside Capture", openapi.Percent},
	{"downside_capture", "DownsideCaptureRatio", "Downside Capture", openapi.Percent},
	{"win_rate", "WinRate", "Win Rate", openapi.Percent},
	{"profit_factor", "ProfitFactor", "Profit Factor", openapi.Number},
	{"tax_cost_ratio", "TaxCostRatio", "Tax Cost Ratio", openapi.Percent},
}

// Statistics returns the risk/style statistic rows for the UI panel.
// Value is nil when the underlying metric is absent from the snapshot
// (e.g., snapshots produced before pvbt began emitting that metric).
func (r *Reader) Statistics(ctx context.Context) ([]openapi.PortfolioStatistic, error) {
	out := make([]openapi.PortfolioStatistic, 0, len(statisticMeta))
	for _, m := range statisticMeta {
		v, found, err := r.readMetric(ctx, m.Name, m.Alias)
		if err != nil {
			return nil, fmt.Errorf("statistics %s: %w", m.Name, err)
		}
		row := openapi.PortfolioStatistic{
			Label:  m.Label,
			Format: m.Format,
		}
		if found {
			val := v
			row.Value = &val
		}
		out = append(out, row)
	}
	return out, nil
}
