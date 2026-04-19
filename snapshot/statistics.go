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
	Label  string
	Format openapi.MetricFormat
}{
	{"sharpe_ratio", "Sharpe Ratio", openapi.Number},
	{"sortino_ratio", "Sortino Ratio", openapi.Number},
	{"beta", "Beta", openapi.Number},
	{"alpha", "Alpha", openapi.Percent},
	{"std_dev", "Standard Deviation", openapi.Percent},
	{"ulcer_index", "Ulcer Index", openapi.Number},
	{"tax_cost_ratio", "Tax Cost Ratio", openapi.Percent},
	{"max_drawdown", "Max Drawdown", openapi.Percent},
}

// Statistics returns the risk/style statistic rows for the UI panel.
func (r *Reader) Statistics(ctx context.Context) ([]openapi.PortfolioStatistic, error) {
	out := make([]openapi.PortfolioStatistic, 0, len(statisticMeta))
	for _, m := range statisticMeta {
		v, err := r.readMetric(ctx, m.Name)
		if err != nil {
			return nil, fmt.Errorf("statistics %s: %w", m.Name, err)
		}
		out = append(out, openapi.PortfolioStatistic{
			Label:  m.Label,
			Value:  v,
			Format: m.Format,
		})
	}
	return out, nil
}
