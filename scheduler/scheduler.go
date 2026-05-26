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
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/penny-vault/pv-api/backtest"
	"github.com/penny-vault/pvbt/tradecron"
)

// scheduleSpec fires at 8:00 PM America/New_York on every trading day. AllHours
// lets the post-close time resolve; tradecron skips weekends and NYSE holidays
// from the calendar loaded at startup, so the run never happens on a closed day.
const scheduleSpec = "0 20 * * *"

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

// Run drives portfolio updates off a tradecron schedule: it sleeps until the
// next 8 PM ET trading-day fire, dispatches the due portfolios, and repeats.
// It blocks until ctx is cancelled. The market calendar must already be loaded
// into tradecron (see cmd startup) or the schedule will panic.
func (s *Scheduler) Run(ctx context.Context) error {
	sched, err := tradecron.New(scheduleSpec, tradecron.AllHours)
	if err != nil {
		return fmt.Errorf("scheduler: build schedule: %w", err)
	}

	// Catch up a fire we may have slept through -- e.g. a restart after 8 PM ET
	// on a trading day. dispatchDue skips portfolios that already ran today.
	now := time.Now()
	if last, ok := mostRecentFire(sched, now); ok && sameDay(last, now) {
		s.dispatchDue(ctx, sched.Next(now))
	}

	for {
		fire := sched.Next(time.Now())
		timer := time.NewTimer(time.Until(fire))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
			s.dispatchDue(ctx, sched.Next(fire))
		}
	}
}

// dispatchDue claims due portfolios and submits each for a backtest. It keeps
// claiming until none remain, retrying queue-full submissions with TickInterval
// backoff until the queue drains, ctx is cancelled, or the next fire (until)
// arrives. Successful submits create a queued run, which ClaimDue then excludes,
// so re-claiming neither double-submits nor spins.
func (s *Scheduler) dispatchDue(ctx context.Context, until time.Time) {
	for {
		ids, err := s.store.ClaimDue(ctx, s.cfg.BatchSize)
		if err != nil {
			log.Error().Err(err).Msg("scheduler: claim failed")
			return
		}
		if len(ids) == 0 {
			return
		}

		dispatched, queueFull := 0, false
		for _, id := range ids {
			runID, serr := s.dispatcher.Submit(ctx, id)
			switch {
			case errors.Is(serr, backtest.ErrQueueFull):
				queueFull = true
			case serr != nil:
				log.Error().Err(serr).Stringer("portfolio_id", id).Msg("scheduler: submit failed")
			default:
				dispatched++
				log.Info().Stringer("portfolio_id", id).Stringer("run_id", runID).Msg("scheduler: dispatched")
			}
		}

		if queueFull {
			log.Warn().Msg("scheduler: dispatch queue full, waiting to retry")
			select {
			case <-ctx.Done():
				return
			case <-time.After(s.cfg.TickInterval):
			}
			if !time.Now().Before(until) {
				return
			}
			continue
		}

		// No queue pressure: if we made no progress the remaining claims are
		// hard-erroring, so stop rather than spin.
		if dispatched == 0 {
			return
		}
	}
}

// mostRecentFire returns the latest schedule fire at or before now, if any
// occurred within the lookback window.
func mostRecentFire(sched *tradecron.TradeCron, now time.Time) (time.Time, bool) {
	probe := now.AddDate(0, 0, -10)
	var last time.Time
	found := false
	for {
		next := sched.Next(probe)
		if next.After(now) {
			break
		}
		last, found, probe = next, true, next
	}
	return last, found
}

// sameDay reports whether a and b fall on the same calendar day, compared in
// a's location.
func sameDay(a, b time.Time) bool {
	b = b.In(a.Location())
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
