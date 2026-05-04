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

package backtest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// PortfolioSweeper is the callback StartupSweep invokes to mark any
// 'running' portfolios and any in-flight backtest_runs as 'failed' when the
// server restarts mid-run. Returns (portfolios updated, runs updated).
// Passed from cmd/server.go; nil for tests that only want .tmp cleanup.
type PortfolioSweeper interface {
	MarkAllRunningAsFailed(ctx context.Context, reason string) (int, int, error)
}

// StartupSweep removes stale .tmp files (>1h old) and flips any
// stuck-running portfolios to 'failed'. Logged at info.
func StartupSweep(ctx context.Context, snapshotsDir string, ps PortfolioSweeper) error {
	cutoff := time.Now().Add(-1 * time.Hour)
	top, err := os.ReadDir(snapshotsDir)
	if err != nil {
		return err
	}
	removed := 0
	for _, entry := range top {
		full := filepath.Join(snapshotsDir, entry.Name())
		if entry.IsDir() {
			removed += sweepStaleTmps(full, cutoff)
			continue
		}
		if removeIfStaleTmp(full, entry.Name(), cutoff) {
			removed++
		}
	}
	log.Info().Int("stale_tmp_removed", removed).Msg("snapshots sweep")

	if ps != nil {
		nPorts, nRuns, err := ps.MarkAllRunningAsFailed(ctx, "server restarted mid-run")
		if err != nil {
			return err
		}
		log.Info().
			Int("portfolios_running_to_failed", nPorts).
			Int("runs_inflight_to_failed", nRuns).
			Msg("portfolios sweep")
	}
	return nil
}

// sweepStaleTmps removes stale .sqlite.tmp files inside a per-portfolio
// subdirectory. Returns the count removed; per-subdir errors are swallowed
// so a single bad directory cannot abort the whole sweep.
func sweepStaleTmps(dir string, cutoff time.Time) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if removeIfStaleTmp(filepath.Join(dir, entry.Name()), entry.Name(), cutoff) {
			removed++
		}
	}
	return removed
}

func removeIfStaleTmp(path, name string, cutoff time.Time) bool {
	if !strings.HasSuffix(name, ".sqlite.tmp") {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.ModTime().Before(cutoff) {
		return false
	}
	return os.Remove(path) == nil
}
