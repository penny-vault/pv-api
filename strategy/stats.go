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

package strategy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bytedance/sonic"
	"github.com/rs/zerolog/log"
)

// ErrInvalidRefreshTime is returned by NewStatsRefresher when cfg.RefreshTime
// is not in HH:MM (24-hour) form or carries out-of-range hour/minute values.
var ErrInvalidRefreshTime = errors.New("invalid RefreshTime: expected HH:MM")

// ErrNoInstalledArtifact is returned by RunOne when the strategy row has not
// recorded a successful install (artifact_ref or artifact_kind is null).
var ErrNoInstalledArtifact = errors.New("strategy has no installed artifact")

// StatRunner executes a strategy artifact and writes a SQLite snapshot to OutPath.
// It is a narrow interface over backtest.Runner that avoids importing the backtest
// package (which would create an import cycle through snapshot -> portfolio -> strategy).
type StatRunner interface {
	Run(ctx context.Context, req StatRunRequest) error
}

// StatRunRequest carries the inputs needed to run a single-strategy backtest for
// stats purposes. It is intentionally minimal to avoid coupling to backtest types.
type StatRunRequest struct {
	// Artifact is the absolute filesystem path to the strategy binary.
	Artifact string
	// Args are the CLI flags to pass to the strategy binary (benchmark, start date, etc.).
	Args []string
	// OutPath is where the runner must write the SQLite snapshot.
	OutPath string
}

// SnapshotKpisFunc opens the SQLite snapshot at path, reads KPIs, and returns
// the scalar metrics needed for stats storage. Injected to break the import
// cycle between strategy and snapshot/portfolio.
type SnapshotKpisFunc func(ctx context.Context, path string) (StatKpis, error)

// StatKpis holds the scalar metrics the StatsRefresher persists.
type StatKpis struct {
	CAGR               float64
	MaxDrawdown        float64
	Sharpe             float64
	Sortino            float64
	UlcerIndex         float64
	Beta               float64
	Alpha              float64
	StdDev             float64
	TaxCostRatio       float64
	OneYearReturn      float64
	YtdReturn          float64
	BenchmarkYtdReturn float64
}

// StatsRefresherConfig configures the StatsRefresher.
type StatsRefresherConfig struct {
	// StartDate is the backtest start date (default 2010-01-01).
	StartDate time.Time
	// RefreshTime is the daily trigger time in US Eastern ("HH:MM"; default "17:00").
	RefreshTime string
	// TickInterval is how often the ticker checks the clock (default 5m).
	TickInterval time.Duration
	// SnapshotDir is the parent for temporary snapshot files ("" = OS default).
	SnapshotDir string
}

// StatsRefresher runs a default-parameter backtest for every installed strategy
// and writes cagr/max_drawdown/sharpe/sortino/stats_as_of back to the strategies table.
type StatsRefresher struct {
	store       StatsStore
	runner      StatRunner
	readKpis    SnapshotKpisFunc
	cfg         StatsRefresherConfig
	loc         *time.Location
	refreshHour int
	refreshMin  int
}

// NewStatsRefresher constructs a StatsRefresher with applied defaults.
func NewStatsRefresher(store StatsStore, runner StatRunner, readKpis SnapshotKpisFunc, cfg StatsRefresherConfig) (*StatsRefresher, error) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return nil, fmt.Errorf("loading America/New_York timezone: %w", err)
	}
	if cfg.TickInterval == 0 {
		cfg.TickInterval = 5 * time.Minute
	}
	if cfg.StartDate.IsZero() {
		cfg.StartDate = time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	if cfg.RefreshTime == "" {
		cfg.RefreshTime = "17:00"
	}
	var h, m int
	if n, err := fmt.Sscanf(cfg.RefreshTime, "%d:%d", &h, &m); err != nil || n != 2 || h < 0 || h > 23 || m < 0 || m > 59 {
		return nil, fmt.Errorf("%w: %q", ErrInvalidRefreshTime, cfg.RefreshTime)
	}
	return &StatsRefresher{
		store:       store,
		runner:      runner,
		readKpis:    readKpis,
		cfg:         cfg,
		loc:         loc,
		refreshHour: h,
		refreshMin:  m,
	}, nil
}

// Run starts the daily stats refresh loop. Blocks until ctx is cancelled.
func (r *StatsRefresher) Run(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.TickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !r.PastRefreshTime() {
				continue
			}
			_ = r.RefreshDue(ctx)
		}
	}
}

// PastRefreshTime reports whether the current US Eastern time is at or after
// the configured RefreshTime. Exported for testing.
func (r *StatsRefresher) PastRefreshTime() bool {
	now := time.Now().In(r.loc)
	threshold := time.Date(now.Year(), now.Month(), now.Day(), r.refreshHour, r.refreshMin, 0, 0, r.loc)
	return !now.Before(threshold)
}

// RefreshDue loads installed strategies and runs stats for any that have not
// been refreshed today. Exported for testing.
func (r *StatsRefresher) RefreshDue(ctx context.Context) error {
	strategies, err := r.store.ListInstalled(ctx)
	if err != nil {
		log.Error().Err(err).Msg("stats refresher: failed to list installed strategies")
		return err
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for _, s := range strategies {
		if s.StatsAsOf != nil && !s.StatsAsOf.Before(today) {
			continue
		}
		if err := r.RunOne(ctx, s.ShortCode); err != nil {
			log.Warn().Err(err).Str("short_code", s.ShortCode).Msg("stats refresh failed")
		}
	}
	return nil
}

// RunOne computes and stores stats for a single installed strategy.
// It bypasses the daily time gate — use this for immediate post-install refresh.
func (r *StatsRefresher) RunOne(ctx context.Context, shortCode string) error {
	s, err := r.store.Get(ctx, shortCode)
	if err != nil {
		return fmt.Errorf("loading strategy %s: %w", shortCode, err)
	}
	if s.ArtifactRef == nil || s.ArtifactKind == nil {
		return fmt.Errorf("%w: %s", ErrNoInstalledArtifact, shortCode)
	}
	if *s.ArtifactKind == artifactKindImage {
		log.Warn().Str("short_code", shortCode).
			Msg("stats skipped: docker runner mode not yet supported for stats")
		return nil
	}

	tmpDir, err := os.MkdirTemp(r.cfg.SnapshotDir, "stats-*")
	if err != nil {
		return fmt.Errorf("creating temp dir for stats snapshot: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	outPath := filepath.Join(tmpDir, "stats.sqlite")

	benchmark := r.parseBenchmark(s.DescribeJSON)
	startDate := r.cfg.StartDate
	args := buildArgs(benchmark, &startDate)

	req := StatRunRequest{
		Artifact: *s.ArtifactRef,
		Args:     args,
		OutPath:  outPath,
	}

	log.Info().Str("short_code", shortCode).Str("artifact", *s.ArtifactRef).Msg("computing strategy stats")
	if err := r.runner.Run(ctx, req); err != nil {
		errText := err.Error()
		_ = r.store.MarkStatsError(ctx, shortCode, errText)
		return fmt.Errorf("stats backtest for %s: %w", shortCode, err)
	}

	kpis, err := r.readKpis(ctx, outPath)
	if err != nil {
		errText := err.Error()
		_ = r.store.MarkStatsError(ctx, shortCode, errText)
		return fmt.Errorf("reading kpis for %s: %w", shortCode, err)
	}

	result := StatsResult{
		CAGR:               kpis.CAGR,
		MaxDrawdown:        kpis.MaxDrawdown,
		Sharpe:             kpis.Sharpe,
		Sortino:            kpis.Sortino,
		UlcerIndex:         kpis.UlcerIndex,
		Beta:               kpis.Beta,
		Alpha:              kpis.Alpha,
		StdDev:             kpis.StdDev,
		TaxCostRatio:       kpis.TaxCostRatio,
		OneYearReturn:      kpis.OneYearReturn,
		YtdReturn:          kpis.YtdReturn,
		BenchmarkYtdReturn: kpis.BenchmarkYtdReturn,
		AsOf:               time.Now().UTC(),
	}
	if err := r.store.UpdateStats(ctx, shortCode, result); err != nil {
		return fmt.Errorf("writing stats for %s: %w", shortCode, err)
	}
	log.Info().Str("short_code", shortCode).
		Float64("cagr", result.CAGR).
		Float64("max_drawdown", result.MaxDrawdown).
		Float64("sharpe", result.Sharpe).
		Float64("sortino", result.Sortino).
		Float64("ulcer_index", result.UlcerIndex).
		Float64("beta", result.Beta).
		Float64("alpha", result.Alpha).
		Float64("std_dev", result.StdDev).
		Float64("tax_cost_ratio", result.TaxCostRatio).
		Float64("one_year_return", result.OneYearReturn).
		Float64("ytd_return", result.YtdReturn).
		Float64("benchmark_ytd_return", result.BenchmarkYtdReturn).
		Msg("strategy stats updated")
	return nil
}

// buildArgs builds CLI args for a stats-only backtest (no user parameters).
func buildArgs(benchmark string, startDate *time.Time) []string {
	var args []string
	if startDate != nil {
		args = append(args, "--start", startDate.Format("2006-01-02"))
	}
	if benchmark != "" {
		args = append(args, "--benchmark", benchmark)
	}
	return args
}

func (r *StatsRefresher) parseBenchmark(describeJSON []byte) string {
	if describeJSON == nil {
		return ""
	}
	var d Describe
	if err := sonic.Unmarshal(describeJSON, &d); err != nil {
		return ""
	}
	return d.Benchmark
}
