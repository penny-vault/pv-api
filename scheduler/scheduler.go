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

// Package scheduler runs an in-process ticker that picks up due continuous
// portfolios, advances their next_run_at via tradecron, and submits each to
// the backtest dispatcher.
package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/penny-vault/pvbt/tradecron"
)

// NextRunFunc computes the next scheduled execution for a tradecron schedule.
// Returning an error causes the row to be skipped by ClaimDueContinuous.
type NextRunFunc func(schedule string, now time.Time) (time.Time, error)

// Claim is a portfolio picked up by a scheduler tick, with its newly-advanced
// next_run_at already committed in the claim tx.
type Claim struct {
	PortfolioID uuid.UUID
	Schedule    string
	NextRunAt   time.Time
}

// PortfolioStore is the subset of portfolio store operations the scheduler
// needs. Implementations claim due portfolios in a single tx that advances
// next_run_at before commit.
type PortfolioStore interface {
	ClaimDueContinuous(ctx context.Context, before time.Time, batchSize int, nextRun NextRunFunc) ([]Claim, error)
}

// Dispatcher is the subset of backtest.Dispatcher the scheduler needs.
type Dispatcher interface {
	Submit(ctx context.Context, portfolioID uuid.UUID) (runID uuid.UUID, err error)
}

// TradecronNext is the production NextRunFunc. It parses schedule via
// tradecron.New with RegularHours.
func TradecronNext(schedule string, now time.Time) (time.Time, error) {
	tc, err := tradecron.New(schedule, tradecron.RegularHours)
	if err != nil {
		return time.Time{}, fmt.Errorf("tradecron.New(%q): %w", schedule, err)
	}
	return tc.Next(now), nil
}

// Scheduler owns the tick loop that picks up due continuous portfolios.
type Scheduler struct {
	store      PortfolioStore
	dispatcher Dispatcher
	cfg        Config
	nextRun    NextRunFunc
}

// New builds a Scheduler. cfg defaults are applied.
func New(cfg Config, store PortfolioStore, dispatcher Dispatcher, nextRun NextRunFunc) *Scheduler {
	cfg.ApplyDefaults()
	return &Scheduler{
		store:      store,
		dispatcher: dispatcher,
		cfg:        cfg,
		nextRun:    nextRun,
	}
}

// Run blocks until ctx is cancelled, firing tickOnce immediately and then at
// each cfg.TickInterval. Errors in a single tick are logged but do not exit
// the loop.
func (s *Scheduler) Run(ctx context.Context) error {
	s.tickOnce(ctx)
	ticker := time.NewTicker(s.cfg.TickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			s.tickOnce(ctx)
		}
	}
}

func (s *Scheduler) tickOnce(_ context.Context) {
	// Filled in by Task 4.
}
