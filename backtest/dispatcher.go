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
}

// RunRow mirrors portfolio.Run but lives here to avoid the import cycle.
// Task 15 will extend this if needed.
type RunRow struct {
	ID          uuid.UUID
	PortfolioID uuid.UUID
	Status      string
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
	d.ctx, d.cancel = context.WithCancel(parent)
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
