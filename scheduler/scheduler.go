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

// Package scheduler runs an in-process ticker that claims open-ended portfolios
// not yet run today and submits them to the backtest dispatcher.
package scheduler

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/penny-vault/pv-api/backtest"
)

// PortfolioStore is the subset of portfolio store operations the scheduler needs.
type PortfolioStore interface {
	ClaimDue(ctx context.Context, batchSize int) ([]uuid.UUID, error)
}

// Dispatcher is the subset of backtest.Dispatcher the scheduler needs.
type Dispatcher interface {
	Submit(ctx context.Context, portfolioID uuid.UUID) (runID uuid.UUID, err error)
}

// Scheduler owns the tick loop that picks up due open-ended portfolios.
type Scheduler struct {
	store      PortfolioStore
	dispatcher Dispatcher
	cfg        Config
}

// New builds a Scheduler. cfg defaults are applied.
func New(cfg Config, store PortfolioStore, dispatcher Dispatcher) *Scheduler {
	cfg.ApplyDefaults()
	return &Scheduler{
		store:      store,
		dispatcher: dispatcher,
		cfg:        cfg,
	}
}

// Run blocks until ctx is cancelled, firing tickOnce immediately and then at
// each cfg.TickInterval.
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

func (s *Scheduler) tickOnce(ctx context.Context) {
	ids, err := s.store.ClaimDue(ctx, s.cfg.BatchSize)
	if err != nil {
		log.Error().Err(err).Msg("scheduler: claim failed")
		return
	}
	for _, id := range ids {
		runID, err := s.dispatcher.Submit(ctx, id)
		switch {
		case errors.Is(err, backtest.ErrQueueFull):
			log.Warn().Stringer("portfolio_id", id).Msg("scheduler: queue full, skipped until next tick")
		case err != nil:
			log.Error().Err(err).Stringer("portfolio_id", id).Msg("scheduler: submit failed")
		default:
			log.Info().Stringer("portfolio_id", id).Stringer("run_id", runID).Msg("scheduler: dispatched")
		}
	}
}
