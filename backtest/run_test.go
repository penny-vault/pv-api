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
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/backtest"
	"github.com/penny-vault/pv-api/snapshot"
)

type fakePortfolioStore struct {
	row         backtest.PortfolioRow
	setRunning  bool
	setReady    bool
	setFailed   string
	lastKpis    backtest.SetKpis
	snapshotOut string
}

func (f *fakePortfolioStore) GetByID(_ context.Context, _ uuid.UUID) (backtest.PortfolioRow, error) {
	return f.row, nil
}
func (f *fakePortfolioStore) SetRunning(_ context.Context, _ uuid.UUID) error {
	f.setRunning = true
	return nil
}
func (f *fakePortfolioStore) SetReady(_ context.Context, _ uuid.UUID, path string, k backtest.SetKpis) error {
	f.setReady = true
	f.snapshotOut = path
	f.lastKpis = k
	return nil
}
func (f *fakePortfolioStore) SetFailed(_ context.Context, _ uuid.UUID, errMsg string) error {
	f.setFailed = errMsg
	return nil
}

type fakeRunStoreFull struct {
	fakeRunStore
	updatedRunning bool
	updatedSuccess string
	updatedFailed  string
}

func (f *fakeRunStoreFull) UpdateRunRunning(_ context.Context, _ uuid.UUID) error {
	f.updatedRunning = true
	return nil
}
func (f *fakeRunStoreFull) UpdateRunSuccess(_ context.Context, _ uuid.UUID, path string, _ int32) error {
	f.updatedSuccess = path
	return nil
}
func (f *fakeRunStoreFull) UpdateRunFailed(_ context.Context, _ uuid.UUID, msg string, _ int32) error {
	f.updatedFailed = msg
	return nil
}

var _ = Describe("Run orchestration", func() {
	It("writes a fresh snapshot, renames it, and updates the portfolio row", func() {
		snapsDir := GinkgoT().TempDir()

		fixture := filepath.Join(GinkgoT().TempDir(), "fx.sqlite")
		Expect(snapshot.BuildTestSnapshot(fixture)).To(Succeed())
		Expect(os.Setenv("FAKESTRAT_FIXTURE", fixture)).To(Succeed())
		DeferCleanup(func() { os.Unsetenv("FAKESTRAT_FIXTURE") })

		ps := &fakePortfolioStore{row: backtest.PortfolioRow{
			ID: uuid.New(), StrategyCode: "fake", StrategyVer: "v0.0.0",
			Parameters: map[string]any{}, Benchmark: "SPY", Status: "queued",
		}}
		rs := &fakeRunStoreFull{}

		r := backtest.NewRunner(backtest.Config{SnapshotsDir: snapsDir, RunnerMode: "host"},
			&backtest.HostRunner{}, ps, rs,
			func(_, _ string) (string, error) { return fakeStratBin, nil })

		err := r.Run(context.Background(), ps.row.ID, uuid.New())
		Expect(err).NotTo(HaveOccurred())

		Expect(ps.setRunning).To(BeTrue())
		Expect(ps.setReady).To(BeTrue())
		Expect(ps.setFailed).To(BeEmpty())
		Expect(rs.updatedRunning).To(BeTrue())
		Expect(rs.updatedSuccess).NotTo(BeEmpty())
		Expect(ps.lastKpis.CurrentValue).To(BeNumerically("~", 103000, 0.01))

		Expect(ps.snapshotOut).To(Equal(filepath.Join(snapsDir, ps.row.ID.String()+".sqlite")))
		_, stErr := os.Stat(ps.snapshotOut)
		Expect(stErr).NotTo(HaveOccurred())
		_, stErr = os.Stat(ps.snapshotOut + ".tmp")
		Expect(os.IsNotExist(stErr)).To(BeTrue())
	})

	It("records a failure when the runner fails", func() {
		snapsDir := GinkgoT().TempDir()
		Expect(os.Setenv("FAKESTRAT_BEHAVIOR", "fail")).To(Succeed())
		DeferCleanup(func() { os.Unsetenv("FAKESTRAT_BEHAVIOR") })

		ps := &fakePortfolioStore{row: backtest.PortfolioRow{
			ID: uuid.New(), StrategyCode: "fake", StrategyVer: "v0.0.0",
			Parameters: map[string]any{}, Benchmark: "SPY", Status: "queued",
		}}
		rs := &fakeRunStoreFull{}

		r := backtest.NewRunner(backtest.Config{SnapshotsDir: snapsDir, RunnerMode: "host", Timeout: 5 * time.Second},
			&backtest.HostRunner{}, ps, rs,
			func(_, _ string) (string, error) { return fakeStratBin, nil })

		err := r.Run(context.Background(), ps.row.ID, uuid.New())
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, backtest.ErrRunnerFailed)).To(BeTrue())
		Expect(ps.setFailed).NotTo(BeEmpty())
		Expect(rs.updatedFailed).NotTo(BeEmpty())
	})
})
