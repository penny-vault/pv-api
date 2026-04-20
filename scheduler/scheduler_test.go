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

	"github.com/penny-vault/pvbt/tradecron"

	"github.com/penny-vault/pv-api/scheduler"
)

var _ = Describe("TradecronNext", func() {
	// pvbt/tradecron panics when @monthend / @monthbegin / @quarter* schedules
	// are evaluated without market-holiday data pre-loaded. Production wires
	// this at startup in cmd/server.go (Plan 6 Task 12); tests pass nil to
	// disable holiday-aware skipping.
	BeforeEach(func() {
		tradecron.SetMarketHolidays(nil)
	})

	It("returns a future time for @monthend", func() {
		now := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)
		next, err := scheduler.TradecronNext("@monthend", now)
		Expect(err).NotTo(HaveOccurred())
		Expect(next.After(now)).To(BeTrue())
	})

	It("returns an error for an unparseable schedule", func() {
		_, err := scheduler.TradecronNext("not-a-schedule", time.Now())
		Expect(err).To(HaveOccurred())
	})
})

// --- Scheduler.Run tests ---

type stubStore struct {
	claimCalls atomic.Int64
	claims     []scheduler.Claim
	err        error
}

func (s *stubStore) ClaimDueContinuous(_ context.Context, _ time.Time, _ int, _ scheduler.NextRunFunc) ([]scheduler.Claim, error) {
	s.claimCalls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	return s.claims, nil
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

func stubNextRun(_ string, now time.Time) (time.Time, error) {
	return now.Add(time.Hour), nil
}

var _ = Describe("Scheduler.Run", func() {
	It("exits cleanly when context is cancelled", func() {
		store := &stubStore{}
		disp := &stubDispatcher{}
		sched := scheduler.New(scheduler.Config{TickInterval: time.Hour, BatchSize: 32},
			store, disp, stubNextRun)

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		err := sched.Run(ctx)
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())
	})
})
