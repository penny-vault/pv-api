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
// 'running' portfolios as 'failed' when the server restarts mid-run.
// Passed from cmd/server.go; nil for tests that only want .tmp cleanup.
type PortfolioSweeper interface {
	MarkAllRunningAsFailed(ctx context.Context, reason string) (int, error)
}

// StartupSweep removes stale .tmp files (>1h old) and flips any
// stuck-running portfolios to 'failed'. Logged at info.
func StartupSweep(ctx context.Context, snapshotsDir string, ps PortfolioSweeper) error {
	cutoff := time.Now().Add(-1 * time.Hour)
	entries, err := os.ReadDir(snapshotsDir)
	if err != nil {
		return err
	}
	removed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sqlite.tmp") {
			continue
		}
		full := filepath.Join(snapshotsDir, e.Name())
		info, ierr := os.Stat(full)
		if ierr != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(full); err == nil {
				removed++
			}
		}
	}
	log.Info().Int("stale_tmp_removed", removed).Msg("snapshots sweep")

	if ps != nil {
		n, err := ps.MarkAllRunningAsFailed(ctx, "server restarted mid-run")
		if err != nil {
			return err
		}
		log.Info().Int("running_to_failed", n).Msg("portfolios sweep")
	}
	return nil
}
