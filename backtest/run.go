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
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/penny-vault/pv-api/snapshot"
)

// Notifier is called after each run completes. alert.Checker implements this.
type Notifier interface {
	NotifyRunComplete(ctx context.Context, portfolioID, runID uuid.UUID, success bool) error
}

// ArtifactResolver resolves a strategy artifact (binary path or image ref)
// for the given cloneURL and version. The semantics of the returned
// artifactRef depends on the runner wired alongside this resolver at
// startup: path for HostRunner, image reference for DockerRunner. The
// orchestrator never interprets artifactRef — it passes it straight into
// RunRequest.Artifact paired with the runner's declared ArtifactKind.
// Callers must always call cleanup when err is nil.
type ArtifactResolver func(ctx context.Context, cloneURL, ver string) (artifactRef string, cleanup func(), err error)

// orchestrator owns a Config, all stores, the runner, and the resolver.
type orchestrator struct {
	cfg          Config
	runner       Runner
	artifactKind ArtifactKind
	ps           PortfolioStore
	rs           RunStore
	resolve      ArtifactResolver
	notifier     Notifier
}

// NewRunner builds the orchestration object that ties together the runner,
// portfolio store, run store, and artifact resolver. artifactKind tells the
// orchestrator which kind of artifact this runner+resolver pair produces; it
// is stamped onto every RunRequest.
func NewRunner(cfg Config, runner Runner, artifactKind ArtifactKind, ps PortfolioStore, rs RunStore, resolve ArtifactResolver) *orchestrator {
	cfg.ApplyDefaults()
	return &orchestrator{cfg: cfg, runner: runner, artifactKind: artifactKind, ps: ps, rs: rs, resolve: resolve}
}

// WithNotifier attaches an optional Notifier that will be called after each run completes.
func (o *orchestrator) WithNotifier(n Notifier) *orchestrator {
	o.notifier = n
	return o
}

// Run orchestrates a single backtest end-to-end for the given portfolio and
// run IDs. It:
//  1. Loads the portfolio row and guards against double-running.
//  2. Marks both the portfolio and the run as running.
//  3. Resolves the strategy binary path.
//  4. Executes the runner, writing output to a .tmp file.
//  5. fsyncs and renames the tmp file to its final path.
//  6. Opens the snapshot, reads KPIs, and writes them back to the DB.
//  7. On any failure, marks both the portfolio and run as failed.
func (o *orchestrator) Run(ctx context.Context, portfolioID, runID uuid.UUID) error {
	started := time.Now()

	row, err := o.ps.GetByID(ctx, portfolioID)
	if err != nil {
		return o.fail(ctx, portfolioID, runID, started, fmt.Errorf("load portfolio: %w", err))
	}
	if row.Status == "running" {
		_ = o.rs.UpdateRunFailed(ctx, runID, "portfolio already running",
			durationMs(time.Since(started)))
		return ErrAlreadyRunning
	}

	if err := o.ps.MarkRunningTx(ctx, portfolioID, runID); err != nil {
		return o.fail(ctx, portfolioID, runID, started, fmt.Errorf("mark running: %w", err))
	}

	artifact, cleanup, err := o.resolve(ctx, row.StrategyCloneURL, row.StrategyVer)
	if err != nil {
		return o.fail(ctx, portfolioID, runID, started, fmt.Errorf("%w: %w", ErrStrategyNotInstalled, err))
	}
	defer cleanup()

	tmp := filepath.Join(o.cfg.SnapshotsDir, portfolioID.String()+".sqlite.tmp")
	final := filepath.Join(o.cfg.SnapshotsDir, portfolioID.String()+".sqlite")
	_ = os.Remove(tmp)

	if err := o.runner.Run(ctx, RunRequest{
		RunID:        runID,
		Artifact:     artifact,
		ArtifactKind: o.artifactKind,
		Args:         BuildArgs(row.Parameters, row.Benchmark, row.StartDate, row.EndDate),
		OutPath:      tmp,
		Timeout:      o.cfg.Timeout,
	}); err != nil {
		return o.fail(ctx, portfolioID, runID, started, err)
	}

	if err := fsyncAndRename(tmp, final); err != nil {
		return o.fail(ctx, portfolioID, runID, started, err)
	}

	kp, err := readKpisFromSnapshot(ctx, final)
	if err != nil {
		return o.fail(ctx, portfolioID, runID, started, err)
	}

	if err := o.ps.MarkReadyTx(ctx, portfolioID, runID, final,
		kp.CurrentValue, kp.YtdReturn, kp.MaxDrawdown, kp.Sharpe, kp.Cagr,
		kp.InceptionDate, durationMs(time.Since(started))); err != nil {
		return o.fail(ctx, portfolioID, runID, started, fmt.Errorf("mark ready: %w", err))
	}
	if o.notifier != nil {
		if err := o.notifier.NotifyRunComplete(ctx, portfolioID, runID, true); err != nil {
			log.Warn().Err(err).Stringer("portfolio_id", portfolioID).Msg("alert notification failed")
		}
	}
	log.Info().Stringer("portfolio_id", portfolioID).Stringer("run_id", runID).Msg("backtest succeeded")
	return nil
}

// fsyncAndRename fsyncs tmp to ensure durability then atomically renames it to final.
func fsyncAndRename(tmp, final string) error {
	f, err := os.Open(tmp) //nolint:gosec // G304: tmp is constructed from validated SnapshotsDir + portfolio UUID
	if err != nil {
		return fmt.Errorf("open tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("fsync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// readKpisFromSnapshot opens the snapshot at path, reads KPIs, and closes it.
func readKpisFromSnapshot(ctx context.Context, path string) (snapshot.Kpis, error) {
	reader, err := snapshot.Open(path)
	if err != nil {
		return snapshot.Kpis{}, fmt.Errorf("open snapshot: %w", err)
	}
	kp, kpisErr := reader.Kpis(ctx)
	if closeErr := reader.Close(); closeErr != nil && kpisErr == nil {
		kpisErr = fmt.Errorf("close snapshot: %w", closeErr)
	}
	if kpisErr != nil {
		return snapshot.Kpis{}, fmt.Errorf("read kpis: %w", kpisErr)
	}
	return kp, nil
}

// durationMs converts a time.Duration to int32 milliseconds, clamping to
// math.MaxInt32 for durations exceeding ~24 days (which should never occur in
// practice but protects against G115 integer overflow).
func durationMs(d time.Duration) int32 {
	ms := d.Milliseconds()
	if ms > 2147483647 {
		return 2147483647
	}
	return int32(ms) //nolint:gosec // G115: bounded by the check above
}

// fail records the failure on both the portfolio and run rows, then returns
// an appropriate wrapped error. Context cancellation is re-wrapped as
// ErrTimedOut to give callers a consistent sentinel.
func (o *orchestrator) fail(ctx context.Context, portfolioID, runID uuid.UUID, started time.Time, err error) error {
	msg := err.Error()
	if len(msg) > 2048 {
		msg = msg[:2048]
	}
	_ = o.ps.MarkFailedTx(ctx, portfolioID, runID, msg, durationMs(time.Since(started)))
	if o.notifier != nil {
		if notifyErr := o.notifier.NotifyRunComplete(ctx, portfolioID, runID, false); notifyErr != nil {
			log.Warn().Err(notifyErr).Stringer("portfolio_id", portfolioID).Msg("alert notification failed")
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %s", ErrTimedOut, msg)
	}
	return err
}
