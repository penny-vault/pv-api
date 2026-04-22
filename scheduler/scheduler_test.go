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

package scheduler_test

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/backtest"
	"github.com/penny-vault/pv-api/scheduler"
)

type stubStore struct {
	claimCalls atomic.Int64
	ids        []uuid.UUID
	err        error
}

func (s *stubStore) ClaimDue(_ context.Context, _ int) ([]uuid.UUID, error) {
	s.claimCalls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	return s.ids, nil
}

type stubDispatcher struct {
	submitCalls atomic.Int64
	err         error
}

func (d *stubDispatcher) Submit(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	d.submitCalls.Add(1)
	if d.err != nil {
		return uuid.Nil, d.err
	}
	return uuid.Must(uuid.NewV7()), nil
}

var _ = Describe("Scheduler.Run", func() {
	It("exits cleanly when context is cancelled", func() {
		store := &stubStore{}
		disp := &stubDispatcher{}
		sched := scheduler.New(scheduler.Config{TickInterval: time.Hour, BatchSize: 32}, store, disp)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := sched.Run(ctx)
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())
	})

	It("dispatches each claimed portfolio on the initial tick", func() {
		ids := []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}
		store := &stubStore{ids: ids}
		disp := &stubDispatcher{}
		sched := scheduler.New(scheduler.Config{TickInterval: time.Hour, BatchSize: 32}, store, disp)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		done := make(chan error, 1)
		go func() { done <- sched.Run(ctx) }()

		Eventually(func() int64 { return disp.submitCalls.Load() }, time.Second).Should(Equal(int64(2)))
		cancel()
		Expect(<-done).To(MatchError(context.Canceled))
		Expect(store.claimCalls.Load()).To(BeNumerically(">=", int64(1)))
	})

	It("continues past ErrQueueFull and dispatches remaining claims", func() {
		ids := []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}
		store := &stubStore{ids: ids}
		disp := &stubDispatcher{err: backtest.ErrQueueFull}
		sched := scheduler.New(scheduler.Config{TickInterval: time.Hour, BatchSize: 32}, store, disp)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- sched.Run(ctx) }()

		Eventually(func() int64 { return disp.submitCalls.Load() }, time.Second).Should(Equal(int64(2)))
		cancel()
		Expect(<-done).To(MatchError(context.Canceled))
	})

	It("continues past a generic dispatcher error", func() {
		ids := []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}
		store := &stubStore{ids: ids}
		disp := &stubDispatcher{err: errors.New("pool closed")}
		sched := scheduler.New(scheduler.Config{TickInterval: time.Hour, BatchSize: 32}, store, disp)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- sched.Run(ctx) }()

		Eventually(func() int64 { return disp.submitCalls.Load() }, time.Second).Should(Equal(int64(2)))
		cancel()
		Expect(<-done).To(MatchError(context.Canceled))
	})

	It("does not panic or exit when ClaimDue errors", func() {
		store := &stubStore{err: errors.New("db down")}
		disp := &stubDispatcher{}
		sched := scheduler.New(scheduler.Config{TickInterval: 10 * time.Millisecond, BatchSize: 32}, store, disp)

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		err := sched.Run(ctx)
		Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())
		Expect(store.claimCalls.Load()).To(BeNumerically(">=", int64(2)))
		Expect(disp.submitCalls.Load()).To(Equal(int64(0)))
	})

	It("fires the first tick immediately without waiting for TickInterval", func() {
		ids := []uuid.UUID{uuid.Must(uuid.NewV7())}
		store := &stubStore{ids: ids}
		disp := &stubDispatcher{}
		sched := scheduler.New(scheduler.Config{TickInterval: time.Hour, BatchSize: 32}, store, disp)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- sched.Run(ctx) }()

		Eventually(func() int64 { return disp.submitCalls.Load() }, 200*time.Millisecond).Should(Equal(int64(1)))
		cancel()
		<-done
	})
})
