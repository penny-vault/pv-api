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

package scheduler

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/backtest"
	"github.com/penny-vault/pvbt/tradecron"
)

// stubStore models ClaimDue: it returns up to batchSize of the still-pending
// portfolios. done() removes a portfolio (mimicking the real query excluding
// portfolios with an in-flight or completed run), so a successful submit is not
// re-claimed.
type stubStore struct {
	mu         sync.Mutex
	pending    []uuid.UUID
	claimCalls atomic.Int64
	err        error
}

func (s *stubStore) ClaimDue(_ context.Context, batchSize int) ([]uuid.UUID, error) {
	s.claimCalls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.pending)
	if batchSize < n {
		n = batchSize
	}
	return append([]uuid.UUID(nil), s.pending[:n]...), nil
}

func (s *stubStore) done(id uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.pending {
		if p == id {
			s.pending = append(s.pending[:i], s.pending[i+1:]...)
			return
		}
	}
}

func (s *stubStore) remaining() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}

// stubDispatcher returns ErrQueueFull for the first failFor submits, a generic
// error on every call when err is set, and otherwise succeeds (invoking
// onSuccess so the store can drop the portfolio).
type stubDispatcher struct {
	submitCalls atomic.Int64
	failFor     int64
	err         error
	onSuccess   func(uuid.UUID)
}

func (d *stubDispatcher) Submit(_ context.Context, id uuid.UUID) (uuid.UUID, error) {
	n := d.submitCalls.Add(1)
	if d.err != nil {
		return uuid.Nil, d.err
	}
	if n <= d.failFor {
		return uuid.Nil, backtest.ErrQueueFull
	}
	if d.onSuccess != nil {
		d.onSuccess(id)
	}
	return uuid.Must(uuid.NewV7()), nil
}

func ids(n int) []uuid.UUID {
	out := make([]uuid.UUID, n)
	for i := range out {
		out[i] = uuid.Must(uuid.NewV7())
	}
	return out
}

var _ = Describe("Scheduler", func() {
	BeforeEach(func() {
		// Install a calendar so tradecron can answer schedule queries. Memorial
		// Day 2025 lets the holiday-skip assertion below resolve.
		tradecron.SetMarketHolidays([]tradecron.MarketHoliday{
			{Date: time.Date(2025, 5, 26, 0, 0, 0, 0, time.UTC)},
		})
	})

	Describe("dispatchDue", func() {
		newSched := func(cfg Config, store *stubStore, disp *stubDispatcher) *Scheduler {
			cfg.ApplyDefaults()
			disp.onSuccess = store.done
			return New(cfg, store, disp)
		}

		It("submits every claimed portfolio exactly once", func() {
			store := &stubStore{pending: ids(3)}
			disp := &stubDispatcher{}
			s := newSched(Config{TickInterval: time.Hour, BatchSize: 32}, store, disp)

			s.dispatchDue(context.Background(), time.Now().Add(time.Hour))

			Expect(disp.submitCalls.Load()).To(Equal(int64(3)))
			Expect(store.remaining()).To(Equal(0))
		})

		It("drains more portfolios than one batch", func() {
			store := &stubStore{pending: ids(5)}
			disp := &stubDispatcher{}
			s := newSched(Config{TickInterval: time.Hour, BatchSize: 2}, store, disp)

			s.dispatchDue(context.Background(), time.Now().Add(time.Hour))

			Expect(disp.submitCalls.Load()).To(Equal(int64(5)))
			Expect(store.remaining()).To(Equal(0))
		})

		It("retries queue-full submissions until the queue drains", func() {
			store := &stubStore{pending: ids(1)}
			disp := &stubDispatcher{failFor: 2}
			s := newSched(Config{TickInterval: time.Millisecond, BatchSize: 32}, store, disp)

			s.dispatchDue(context.Background(), time.Now().Add(time.Hour))

			Expect(disp.submitCalls.Load()).To(Equal(int64(3)))
			Expect(store.remaining()).To(Equal(0))
		})

		It("stops on a generic submit error without spinning", func() {
			store := &stubStore{pending: ids(1)}
			disp := &stubDispatcher{err: errors.New("pool closed")}
			s := newSched(Config{TickInterval: time.Millisecond, BatchSize: 32}, store, disp)

			s.dispatchDue(context.Background(), time.Now().Add(time.Hour))

			Expect(disp.submitCalls.Load()).To(Equal(int64(1)))
		})

		It("gives up a persistent queue-full at the next-fire deadline", func() {
			store := &stubStore{pending: ids(1)}
			disp := &stubDispatcher{failFor: 1 << 30} // always queue full
			s := newSched(Config{TickInterval: time.Millisecond, BatchSize: 32}, store, disp)

			s.dispatchDue(context.Background(), time.Now()) // deadline already passed

			Expect(disp.submitCalls.Load()).To(Equal(int64(1)))
			Expect(store.remaining()).To(Equal(1))
		})

		It("returns without submitting when ClaimDue errors", func() {
			store := &stubStore{err: errors.New("db down")}
			disp := &stubDispatcher{}
			s := newSched(Config{TickInterval: time.Hour, BatchSize: 32}, store, disp)

			s.dispatchDue(context.Background(), time.Now().Add(time.Hour))

			Expect(disp.submitCalls.Load()).To(Equal(int64(0)))
		})
	})

	Describe("mostRecentFire", func() {
		nyc, _ := time.LoadLocation("America/New_York")
		sched, _ := tradecron.New(scheduleSpec, tradecron.AllHours)

		It("returns the prior trading day's 8 PM fire over a weekend", func() {
			now := time.Date(2025, 1, 11, 12, 0, 0, 0, nyc) // Saturday
			last, ok := mostRecentFire(sched, now)
			Expect(ok).To(BeTrue())
			Expect(last).To(Equal(time.Date(2025, 1, 10, 20, 0, 0, 0, nyc))) // Friday 8 PM
		})

		It("skips a market holiday", func() {
			now := time.Date(2025, 5, 27, 12, 0, 0, 0, nyc) // Tue after Memorial Day
			last, ok := mostRecentFire(sched, now)
			Expect(ok).To(BeTrue())
			Expect(last).To(Equal(time.Date(2025, 5, 23, 20, 0, 0, 0, nyc))) // Friday before
		})
	})

	Describe("Run", func() {
		It("exits cleanly when the context is cancelled", func() {
			store := &stubStore{}
			disp := &stubDispatcher{onSuccess: func(uuid.UUID) {}}
			s := New(Config{TickInterval: time.Hour, BatchSize: 32}, store, disp)

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			Expect(errors.Is(s.Run(ctx), context.Canceled)).To(BeTrue())
			Expect(disp.submitCalls.Load()).To(Equal(int64(0)))
		})
	})
})
