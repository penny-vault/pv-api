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

package strategy_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pv-api/snapshot"
	"github.com/penny-vault/pv-api/strategy"
)

// stubStatRunner is a strategy.StatRunner that copies the fake-stats.sqlite fixture
// into req.OutPath so the SnapshotKpisFunc can open it and read KPIs.
type stubStatRunner struct {
	err error
}

func (s *stubStatRunner) Run(_ context.Context, req strategy.StatRunRequest) error {
	if s.err != nil {
		return s.err
	}
	fixturePath, err := filepath.Abs("testdata/fake-stats.sqlite")
	if err != nil {
		return err
	}
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(req.OutPath), 0o750); err != nil {
		return err
	}
	return os.WriteFile(req.OutPath, data, 0o600)
}

// snapshotKpis is a SnapshotKpisFunc that uses snapshot.Open to read real KPIs.
// The external test package (strategy_test) may import snapshot without causing
// an import cycle because external test packages are exempt from Go's cycle rules.
func snapshotKpis(ctx context.Context, path string) (strategy.StatKpis, error) {
	reader, err := snapshot.Open(path)
	if err != nil {
		return strategy.StatKpis{}, err
	}
	defer func() { _ = reader.Close() }()
	kpis, err := reader.Kpis(ctx)
	if err != nil {
		return strategy.StatKpis{}, err
	}
	return strategy.StatKpis{
		CAGR:               kpis.Cagr,
		MaxDrawdown:        kpis.MaxDrawdown,
		Sharpe:             kpis.Sharpe,
		Sortino:            kpis.Sortino,
		UlcerIndex:         kpis.UlcerIndex,
		Beta:               kpis.Beta,
		Alpha:              kpis.Alpha,
		StdDev:             kpis.StdDev,
		TaxCostRatio:       kpis.TaxCostRatio,
		OneYearReturn:      kpis.OneYearReturn,
		YtdReturn:          kpis.YtdReturn,
		BenchmarkYtdReturn: kpis.BenchmarkYtdReturn,
	}, nil
}

func newTestRefresher(store strategy.StatsStore, runner strategy.StatRunner) *strategy.StatsRefresher {
	ref, err := strategy.NewStatsRefresher(store, runner, snapshotKpis, strategy.StatsRefresherConfig{
		StartDate:    time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC),
		RefreshTime:  "17:00",
		TickInterval: time.Minute,
	})
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return ref
}

var _ = Describe("StatsRefresher.RunOne", func() {
	var (
		store  *fakeStore
		runner *stubStatRunner
	)

	BeforeEach(func() {
		store = newFakeStore()
		runner = &stubStatRunner{}

		ref := "/tmp/fake/fake.bin"
		kind := "binary"
		store.rows["fake"] = strategy.Strategy{
			ShortCode:    "fake",
			CloneURL:     "file:///tmp/fake.git",
			IsOfficial:   true,
			ArtifactRef:  &ref,
			ArtifactKind: &kind,
			DescribeJSON: []byte(`{"shortcode":"fake","name":"Fake","benchmark":"SPY","parameters":[],"presets":[],"schedule":"@monthend"}`),
		}
	})

	It("calls UpdateStats after a successful runner run", func() {
		refresh := newTestRefresher(store, runner)
		Expect(refresh.RunOne(context.Background(), "fake")).To(Succeed())
		Expect(store.statsUpdates).To(HaveLen(1))
		Expect(store.statsUpdates[0].shortCode).To(Equal("fake"))
		Expect(store.statsErrors).To(BeEmpty())
	})

	It("calls MarkStatsError and returns error when runner fails", func() {
		runner.err = errors.New("exec failed")
		refresh := newTestRefresher(store, runner)
		err := refresh.RunOne(context.Background(), "fake")
		Expect(err).To(HaveOccurred())
		Expect(store.statsErrors).To(HaveLen(1))
		Expect(store.statsErrors[0].shortCode).To(Equal("fake"))
		Expect(store.statsErrors[0].err).To(ContainSubstring("exec failed"))
		Expect(store.statsUpdates).To(BeEmpty())
	})

	It("skips and returns nil for docker artifact_kind", func() {
		row := store.rows["fake"]
		kind := "image"
		row.ArtifactKind = &kind
		store.rows["fake"] = row

		refresh := newTestRefresher(store, runner)
		Expect(refresh.RunOne(context.Background(), "fake")).To(Succeed())
		Expect(store.statsUpdates).To(BeEmpty())
		Expect(store.statsErrors).To(BeEmpty())
	})
})

var _ = Describe("StatsRefresher time gate", func() {
	It("PastRefreshTime returns false before the configured time", func() {
		ref, err := strategy.NewStatsRefresher(newFakeStore(), &stubStatRunner{}, snapshotKpis, strategy.StatsRefresherConfig{
			RefreshTime:  "23:59",
			TickInterval: time.Minute,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(ref.PastRefreshTime()).To(BeFalse())
	})

	It("PastRefreshTime returns true after the configured time", func() {
		ref, err := strategy.NewStatsRefresher(newFakeStore(), &stubStatRunner{}, snapshotKpis, strategy.StatsRefresherConfig{
			RefreshTime:  "00:01",
			TickInterval: time.Minute,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(ref.PastRefreshTime()).To(BeTrue())
	})
})

var _ = Describe("StatsRefresher.RefreshDue", func() {
	It("skips strategies already refreshed today", func() {
		store := newFakeStore()
		runner := &stubStatRunner{}
		now := time.Now().UTC()
		artifactRef := "/tmp/fake.bin"
		kind := "binary"
		store.rows["fake"] = strategy.Strategy{
			ShortCode:    "fake",
			ArtifactRef:  &artifactRef,
			ArtifactKind: &kind,
			StatsAsOf:    &now,
		}

		refresh := newTestRefresher(store, runner)
		Expect(refresh.RefreshDue(context.Background())).To(Succeed())
		Expect(store.statsUpdates).To(BeEmpty())
	})

	It("runs stats for strategies with no StatsAsOf", func() {
		store := newFakeStore()
		runner := &stubStatRunner{}
		artifactRef := "/tmp/fake.bin"
		kind := "binary"
		store.rows["fake"] = strategy.Strategy{
			ShortCode:    "fake",
			ArtifactRef:  &artifactRef,
			ArtifactKind: &kind,
			DescribeJSON: []byte(`{"shortcode":"fake","benchmark":"SPY","parameters":[],"presets":[],"schedule":"@monthend"}`),
		}

		refresh := newTestRefresher(store, runner)
		Expect(refresh.RefreshDue(context.Background())).To(Succeed())
		Expect(store.statsUpdates).To(HaveLen(1))
	})
})
