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
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// OrphanGCStore is the subset of portfolio store ops the orphan sweep needs.
// Returns the set of UUIDs currently referenced in each table so the sweep
// can identify files that no row owns.
type OrphanGCStore interface {
	AllPortfolioIDs(ctx context.Context) (map[uuid.UUID]struct{}, error)
	AllRunIDs(ctx context.Context) (map[uuid.UUID]struct{}, error)
}

// orphanCandidate describes one item discovered on disk that may need to be
// reaped if no DB row references it.
type orphanCandidate struct {
	path        string    // absolute path
	portfolioID uuid.UUID // portfolio dir UUID
	runID       uuid.UUID // file UUID; uuid.Nil for whole-dir candidates
	wholeDir    bool      // true when the candidate is a portfolio dir
}

// SweepOrphans removes snapshot files and per-portfolio dirs that no DB row
// references. Walks the filesystem first, then queries the DB; any rows or
// files created between those two reads are kept (the file wasn't in the
// walk, so it's safe). Returns the count of removed entries.
//
// Only operates on the per-portfolio layout
// (<snapshotsDir>/<portfolioID>/<runID>.sqlite). Top-level legacy files and
// .sqlite.tmp files are left alone — those are owned by the stale-tmp
// sweep.
func SweepOrphans(ctx context.Context, snapshotsDir string, store OrphanGCStore) (int, error) {
	candidates, err := collectOrphanCandidates(snapshotsDir)
	if err != nil {
		return 0, err
	}

	// Query AFTER walking so that runs created between the walk and this
	// query are not on the candidate list at all. Inverting the order opens
	// a race window where a freshly-written snapshot could be deleted.
	portfolios, err := store.AllPortfolioIDs(ctx)
	if err != nil {
		return 0, err
	}
	runs, err := store.AllRunIDs(ctx)
	if err != nil {
		return 0, err
	}

	removed := 0
	for _, c := range candidates {
		if c.wholeDir {
			if _, ok := portfolios[c.portfolioID]; ok {
				continue
			}
			if rerr := os.RemoveAll(c.path); rerr != nil {
				log.Warn().Err(rerr).Str("dir", c.path).Msg("orphan dir cleanup failed")
				continue
			}
			removed++
			continue
		}
		if _, ok := runs[c.runID]; ok {
			continue
		}
		if rerr := os.Remove(c.path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			log.Warn().Err(rerr).Str("file", c.path).Msg("orphan snapshot cleanup failed")
			continue
		}
		removed++
	}
	return removed, nil
}

func collectOrphanCandidates(snapshotsDir string) ([]orphanCandidate, error) {
	top, err := os.ReadDir(snapshotsDir)
	if err != nil {
		return nil, err
	}
	var out []orphanCandidate
	for _, entry := range top {
		if !entry.IsDir() {
			continue // legacy top-level file; not our problem
		}
		portfolioID, perr := uuid.Parse(entry.Name())
		if perr != nil {
			continue // unrecognized name; conservative: leave alone
		}
		portDir := filepath.Join(snapshotsDir, entry.Name())
		// Whole-dir candidate; SweepOrphans decides whether to actually remove it.
		out = append(out, orphanCandidate{
			path:        portDir,
			portfolioID: portfolioID,
			wholeDir:    true,
		})
		files, ferr := os.ReadDir(portDir)
		if ferr != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			name := f.Name()
			if !strings.HasSuffix(name, ".sqlite") {
				continue // skip .sqlite.tmp and anything else
			}
			runID, rerr := uuid.Parse(strings.TrimSuffix(name, ".sqlite"))
			if rerr != nil {
				continue
			}
			out = append(out, orphanCandidate{
				path:        filepath.Join(portDir, name),
				portfolioID: portfolioID,
				runID:       runID,
			})
		}
	}
	return out, nil
}

// StartOrphanGC runs an immediate orphan sweep then schedules subsequent
// sweeps on the configured interval. interval <= 0 disables the schedule
// (the immediate sweep still runs). Returns when ctx is canceled.
func StartOrphanGC(ctx context.Context, snapshotsDir string, store OrphanGCStore, interval time.Duration) {
	runOnce := func() {
		n, err := SweepOrphans(ctx, snapshotsDir, store)
		if err != nil {
			log.Warn().Err(err).Msg("orphan sweep failed")
			return
		}
		if n > 0 {
			log.Info().Int("orphans_removed", n).Msg("orphan sweep")
		}
	}
	runOnce()
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce()
		}
	}
}
