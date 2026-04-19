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
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// RunStore is the subset of portfolio.RunStore the dispatcher needs.
// Declared here (not imported from portfolio) to avoid the cycle
// backtest → portfolio.
type RunStore interface {
	CreateRun(ctx context.Context, portfolioID uuid.UUID, status string) (RunRow, error)
	UpdateRunRunning(ctx context.Context, runID uuid.UUID) error
	UpdateRunSuccess(ctx context.Context, runID uuid.UUID, snapshotPath string, durationMs int32) error
	UpdateRunFailed(ctx context.Context, runID uuid.UUID, errMsg string, durationMs int32) error
}

// RunRow mirrors portfolio.Run but lives here to avoid the import cycle.
type RunRow struct {
	ID          uuid.UUID
	PortfolioID uuid.UUID
	Status      string
}

// PortfolioStore is the subset of portfolio operations the backtest
// orchestrator needs. Declared here to avoid the cycle backtest → portfolio.
type PortfolioStore interface {
	GetByID(ctx context.Context, portfolioID uuid.UUID) (PortfolioRow, error)
	SetRunning(ctx context.Context, portfolioID uuid.UUID) error
	SetReady(ctx context.Context, portfolioID uuid.UUID, snapshotPath string, kpis SetKpis) error
	SetFailed(ctx context.Context, portfolioID uuid.UUID, errMsg string) error
}

// PortfolioRow carries the fields the orchestrator reads from a portfolio.
type PortfolioRow struct {
	ID           uuid.UUID
	StrategyCode string
	StrategyVer  string
	Parameters   map[string]any
	Benchmark    string
	Status       string
	SnapshotPath *string
}

// SetKpis carries the KPI values written back to the portfolios row after
// a successful backtest run.
type SetKpis struct {
	CurrentValue  float64
	YtdReturn     float64
	MaxDrawdown   float64
	Sharpe        float64
	Cagr          float64
	InceptionDate time.Time
}

type task struct {
	portfolioID uuid.UUID
	runID       uuid.UUID
}

// Dispatcher is a bounded worker-pool that funnels task submissions to
// backtest.Run invocations.
type Dispatcher struct {
	cfg     Config
	runner  Runner
	runs    RunStore
	runFn   func(ctx context.Context, portfolioID, runID uuid.UUID) error
	tasks   chan task
	wg      sync.WaitGroup
	ctx     context.Context
	cancel  context.CancelFunc
	started bool
}

// NewDispatcher builds a dispatcher. runFn is the orchestration callback;
// production passes backtest.Run (Task 15). Tests pass nil and rely on the
// direct-runner fallback for concurrency assertions.
func NewDispatcher(cfg Config, runner Runner, runs RunStore, runFn func(ctx context.Context, portfolioID, runID uuid.UUID) error) *Dispatcher {
	cfg.ApplyDefaults()
	return &Dispatcher{
		cfg:    cfg,
		runner: runner,
		runs:   runs,
		runFn:  runFn,
		tasks:  make(chan task, cfg.MaxConcurrency*4),
	}
}

func (d *Dispatcher) Start(parent context.Context) {
	if d.started {
		return
	}
	d.started = true
	d.ctx, d.cancel = context.WithCancel(parent) //nolint:gosec // G118: cancel stored in d.cancel and called by Shutdown
	for i := 0; i < d.cfg.MaxConcurrency; i++ {
		d.wg.Add(1)
		go d.worker()
	}
	log.Info().Int("workers", d.cfg.MaxConcurrency).Msg("backtest dispatcher started")
}

func (d *Dispatcher) Submit(ctx context.Context, portfolioID uuid.UUID) (uuid.UUID, error) {
	run, err := d.runs.CreateRun(ctx, portfolioID, "queued")
	if err != nil {
		return uuid.Nil, err
	}
	select {
	case d.tasks <- task{portfolioID: portfolioID, runID: run.ID}:
		return run.ID, nil
	default:
		return uuid.Nil, ErrQueueFull
	}
}

func (d *Dispatcher) Shutdown(grace time.Duration) error {
	if !d.started {
		return nil
	}
	close(d.tasks)
	done := make(chan struct{})
	go func() { d.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(grace):
	}
	d.cancel()
	return nil
}

func (d *Dispatcher) worker() {
	defer d.wg.Done()
	for t := range d.tasks {
		if d.runFn != nil {
			if err := d.runFn(d.ctx, t.portfolioID, t.runID); err != nil {
				log.Error().Err(err).Stringer("run_id", t.runID).Msg("backtest run failed")
			}
			continue
		}
		// test-only path: call runner directly so concurrency counters work
		_ = d.runner.Run(d.ctx, RunRequest{})
	}
}
