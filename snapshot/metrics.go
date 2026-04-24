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
	"strings"

	"github.com/penny-vault/pv-api/openapi"
)

type metricEntry struct {
	Name     string
	Category string
}

var metricMeta = []metricEntry{
	// summary
	{"TWRR", "summary"},
	{"MWRR", "summary"},
	{"Sharpe", "summary"},
	{"Sortino", "summary"},
	{"Calmar", "summary"},
	{"KellerRatio", "summary"},
	{"MaxDrawdown", "summary"},
	{"StdDev", "summary"},
	// risk
	{"Beta", "risk"},
	{"Alpha", "risk"},
	{"TrackingError", "risk"},
	{"DownsideDeviation", "risk"},
	{"InformationRatio", "risk"},
	{"Treynor", "risk"},
	{"UlcerIndex", "risk"},
	{"ExcessKurtosis", "risk"},
	{"Skewness", "risk"},
	{"RSquared", "risk"},
	{"ValueAtRisk", "risk"},
	{"UpsideCaptureRatio", "risk"},
	{"DownsideCaptureRatio", "risk"},
	// trade
	{"WinRate", "trade"},
	{"AverageWin", "trade"},
	{"AverageLoss", "trade"},
	{"ProfitFactor", "trade"},
	{"AverageHoldingPeriod", "trade"},
	{"Turnover", "trade"},
	{"NPositivePeriods", "trade"},
	{"TradeGainLossRatio", "trade"},
	{"AverageMFE", "trade"},
	{"AverageMAE", "trade"},
	{"MedianMFE", "trade"},
	{"MedianMAE", "trade"},
	{"EdgeRatio", "trade"},
	{"TradeCaptureRatio", "trade"},
	{"LongWinRate", "trade"},
	{"ShortWinRate", "trade"},
	{"LongProfitFactor", "trade"},
	{"ShortProfitFactor", "trade"},
	// withdrawal
	{"SafeWithdrawalRate", "withdrawal"},
	{"PerpetualWithdrawalRate", "withdrawal"},
	{"DynamicWithdrawalRate", "withdrawal"},
	// tax
	{"LTCG", "tax"},
	{"STCG", "tax"},
	{"UnrealizedLTCG", "tax"},
	{"UnrealizedSTCG", "tax"},
	{"QualifiedDividends", "tax"},
	{"NonQualifiedIncome", "tax"},
	{"TaxCostRatio", "tax"},
	{"TaxDrag", "tax"},
	// advanced
	{"CAGR", "advanced"},
	{"ActiveReturn", "advanced"},
	{"SmartSharpe", "advanced"},
	{"SmartSortino", "advanced"},
	{"ProbabilisticSharpe", "advanced"},
	{"KRatio", "advanced"},
	{"KellyCriterion", "advanced"},
	{"OmegaRatio", "advanced"},
	{"GainToPainRatio", "advanced"},
	{"CVaR", "advanced"},
	{"TailRatio", "advanced"},
	{"RecoveryFactor", "advanced"},
	{"Exposure", "advanced"},
	{"ConsecutiveWins", "advanced"},
	{"ConsecutiveLosses", "advanced"},
	{"AvgDrawdown", "advanced"},
	{"AvgDrawdownDays", "advanced"},
	{"GainLossRatio", "advanced"},
	{"AvgUlcerIndex", "advanced"},
	{"P90UlcerIndex", "advanced"},
	{"MedianUlcerIndex", "advanced"},
}

var validWindows = map[string]bool{
	"since_inception": true,
	"5yr":             true,
	"3yr":             true,
	"1yr":             true,
	"ytd":             true,
	"mtd":             true,
	"wtd":             true,
}

// Metrics returns all requested metrics grouped by pvbt category.
// windows is the ordered list of windows to include; defaults to ["since_inception"] if empty.
// metrics is the list of pvbt PascalCase metric names; returns all if empty.
// Unknown names and windows are silently dropped.
// Metrics absent from the snapshot are omitted from the response.
func (r *Reader) Metrics(ctx context.Context, windows, metrics []string) (*openapi.PortfolioMetrics, error) {
	resolvedWindows := filterWindows(windows)
	if len(resolvedWindows) == 0 {
		resolvedWindows = []string{"since_inception"}
	}
	resolvedMeta := filterMetrics(metrics)

	names := make([]string, len(resolvedMeta))
	for i, m := range resolvedMeta {
		names[i] = m.Name
	}

	dbRows, err := r.queryMetricRows(ctx, names, resolvedWindows)
	if err != nil {
		return nil, err
	}

	return buildPortfolioMetrics(resolvedWindows, resolvedMeta, dbRows), nil
}

func filterWindows(requested []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(requested))
	for _, w := range requested {
		if validWindows[w] && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

func filterMetrics(requested []string) []metricEntry {
	if len(requested) == 0 {
		return metricMeta
	}
	want := make(map[string]bool, len(requested))
	for _, n := range requested {
		want[n] = true
	}
	out := make([]metricEntry, 0, len(requested))
	for _, m := range metricMeta {
		if want[m.Name] {
			out = append(out, m)
		}
	}
	return out
}

// queryMetricRows returns map[name][window] = value for the latest date per (name, window) pair.
func (r *Reader) queryMetricRows(ctx context.Context, names, windows []string) (map[string]map[string]float64, error) {
	if len(names) == 0 || len(windows) == 0 {
		return map[string]map[string]float64{}, nil
	}

	args := make([]any, 0, len(names)+len(windows))
	namePH := make([]string, len(names))
	for i, n := range names {
		namePH[i] = "?"
		args = append(args, n)
	}
	windowPH := make([]string, len(windows))
	for i, w := range windows {
		windowPH[i] = "?"
		args = append(args, w)
	}

	query := fmt.Sprintf(`
		SELECT name, window, value FROM (
			SELECT name, window, value,
				   ROW_NUMBER() OVER (PARTITION BY name, window ORDER BY date DESC) AS rn
			FROM metrics WHERE name IN (%s) AND window IN (%s)
		) WHERE rn = 1`,
		strings.Join(namePH, ","),
		strings.Join(windowPH, ","),
	)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query metrics: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make(map[string]map[string]float64)
	for rows.Next() {
		var name, window string
		var value float64
		if err := rows.Scan(&name, &window, &value); err != nil {
			return nil, fmt.Errorf("scan metric row: %w", err)
		}
		if result[name] == nil {
			result[name] = make(map[string]float64)
		}
		result[name][window] = value
	}
	return result, rows.Err()
}

func buildPortfolioMetrics(windows []string, entries []metricEntry, dbRows map[string]map[string]float64) *openapi.PortfolioMetrics {
	out := &openapi.PortfolioMetrics{Windows: windows}

	for _, m := range entries {
		windowVals, ok := dbRows[m.Name]
		if !ok {
			continue
		}
		vals := make([]*float64, len(windows))
		hasAny := false
		for i, w := range windows {
			if v, exists := windowVals[w]; exists {
				cp := v
				vals[i] = &cp
				hasAny = true
			}
		}
		if !hasAny {
			continue
		}
		setMetricValue(out, m.Category, m.Name, vals)
	}
	return out
}

func setMetricValue(out *openapi.PortfolioMetrics, category, name string, vals []*float64) {
	switch category {
	case "summary":
		if out.Summary == nil {
			g := make(openapi.MetricGroup)
			out.Summary = &g
		}
		(*out.Summary)[name] = vals
	case "risk":
		if out.Risk == nil {
			g := make(openapi.MetricGroup)
			out.Risk = &g
		}
		(*out.Risk)[name] = vals
	case "trade":
		if out.Trade == nil {
			g := make(openapi.MetricGroup)
			out.Trade = &g
		}
		(*out.Trade)[name] = vals
	case "withdrawal":
		if out.Withdrawal == nil {
			g := make(openapi.MetricGroup)
			out.Withdrawal = &g
		}
		(*out.Withdrawal)[name] = vals
	case "tax":
		if out.Tax == nil {
			g := make(openapi.MetricGroup)
			out.Tax = &g
		}
		(*out.Tax)[name] = vals
	case "advanced":
		if out.Advanced == nil {
			g := make(openapi.MetricGroup)
			out.Advanced = &g
		}
		(*out.Advanced)[name] = vals
	}
}
