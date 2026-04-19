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

package backtest_test

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/backtest"
)

type fakeRunner struct {
	active int64
	peak   int64
	block  chan struct{}
}

func (f *fakeRunner) Run(ctx context.Context, req backtest.RunRequest) error {
	n := atomic.AddInt64(&f.active, 1)
	for {
		p := atomic.LoadInt64(&f.peak)
		if n > p {
			if atomic.CompareAndSwapInt64(&f.peak, p, n) {
				break
			}
			continue
		}
		break
	}
	defer atomic.AddInt64(&f.active, -1)
	select {
	case <-f.block:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

type fakeRunStore struct {
	mu      sync.Mutex
	created []uuid.UUID
}

func newFakeRunStore() *fakeRunStore { return &fakeRunStore{} }

func (f *fakeRunStore) CreateRun(_ context.Context, pid uuid.UUID, status string) (backtest.RunRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	runID := uuid.New()
	f.created = append(f.created, runID)
	return backtest.RunRow{ID: runID, PortfolioID: pid, Status: status}, nil
}

func (f *fakeRunStore) UpdateRunRunning(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeRunStore) UpdateRunSuccess(_ context.Context, _ uuid.UUID, _ string, _ int32) error {
	return nil
}
func (f *fakeRunStore) UpdateRunFailed(_ context.Context, _ uuid.UUID, _ string, _ int32) error {
	return nil
}

var _ = Describe("Dispatcher", func() {
	It("caps concurrency at MaxConcurrency", func() {
		runner := &fakeRunner{block: make(chan struct{})}
		rs := newFakeRunStore()
		d := backtest.NewDispatcher(backtest.Config{
			SnapshotsDir: "/tmp", RunnerMode: "host", MaxConcurrency: 2,
		}, runner, rs, nil)
		d.Start(context.Background())
		DeferCleanup(func() { d.Shutdown(5 * time.Second) })

		for i := 0; i < 10; i++ {
			_, err := d.Submit(context.Background(), uuid.New())
			Expect(err).NotTo(HaveOccurred())
		}
		Eventually(func() int64 { return atomic.LoadInt64(&runner.peak) }).Should(Equal(int64(2)))
		close(runner.block)
	})

	It("returns ErrQueueFull when the buffer is saturated", func() {
		runner := &fakeRunner{block: make(chan struct{})}
		defer close(runner.block)
		rs := newFakeRunStore()
		d := backtest.NewDispatcher(backtest.Config{
			SnapshotsDir: "/tmp", RunnerMode: "host", MaxConcurrency: 1,
		}, runner, rs, nil)
		d.Start(context.Background())
		DeferCleanup(func() { d.Shutdown(5 * time.Second) })

		// Fill the queue: 1 in-flight + 4 buffered == 5; 6th should fail.
		for i := 0; i < 5; i++ {
			_, _ = d.Submit(context.Background(), uuid.New())
		}
		_, err := d.Submit(context.Background(), uuid.New())
		Expect(err).To(MatchError(backtest.ErrQueueFull))
	})
})
